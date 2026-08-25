"""Replay guard: a solve run on a worker returns exactly what the same solve returns
in-process.

This is the acceptance bar for moving the solvers off the server thread. Nothing about
the objective, the search parameters or the scoring changed, so the two paths differ
only in the pickle round trip the payload makes and in the environment the worker
resolves its knobs from — and either of those going wrong is a silent quality
regression, not a crash. So the cases here carry every optional field the daemon can
send, span the instance sizes the live fleet plans over, and are compared whole rather
than by score.
"""
import random

import pytest

from generated import routing_pb2
from handlers.routing_handler import RoutingServiceHandler
from utils import solve_pool as pool_module
from utils.solve_pool import SolvePool
from utils.tour_solver import solve_tour

MODEL = {"fit_version": 1, "era": "parity",
         "impact": {"LIMITED|WEAK": {"sell_decay_per_step": 0.9,
                                     "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}
MODEL_VERSION = "1@parity"
FRESH = 9_999_999_999
GOODS = ("ORE", "FUEL", "FOOD", "ALLOY")


def _instance(stops, seed, systems=2):
    """A seeded market instance of `stops` distinct waypoints spread over `systems`.

    Each system gets a planted source/sink pair so a feasible tour always exists; the
    rest is seeded noise, which is what makes the sequencer actually choose.
    """
    rng = random.Random(seed)
    snapshot, waypoints = [], []
    for stop in range(stops):
        system = f"X1-P{stop % systems}"
        wp = f"{system}-W{stop}"
        waypoints.append(dict(symbol=wp, system_symbol=system,
                              x=rng.randint(-800, 800), y=rng.randint(-800, 800)))
        planted = stop // systems
        for good in GOODS:
            if planted == 0 and good == "ORE":
                ask, bid = rng.randint(40, 90), 0
            elif planted == 1 and good == "ORE":
                ask, bid = 0, rng.randint(260, 420)
            elif rng.random() < 0.55:
                continue
            else:
                ask, bid = rng.randint(50, 400), rng.randint(0, 350)
            snapshot.append(dict(waypoint_symbol=wp, system_symbol=system,
                                 good_symbol=good, ask=ask, bid=bid,
                                 trade_volume=rng.choice([10, 20, 40]),
                                 supply="LIMITED", activity="WEAK",
                                 observed_at_unix=FRESH))
    ship = dict(ship_symbol="PARITY-1", current_waypoint=waypoints[0]["symbol"],
                current_system=waypoints[0]["system_symbol"], hold_capacity=120,
                fuel_current=400, fuel_capacity=400, engine_speed=30, cargo=[])
    constraints = dict(max_hops=6, min_margin_per_unit=1, max_spend=2_000_000,
                       working_capital_reserve=50_000, max_snapshot_age_minutes=75,
                       allowed_systems=sorted({w["system_symbol"] for w in waypoints}),
                       max_tour_systems=4,
                       expected_model_version=MODEL_VERSION)
    return dict(snapshot=snapshot, ship=ship, constraints=constraints, model=MODEL,
                waypoints=waypoints)


def _fully_loaded_payload():
    """An instance carrying every optional request field at once — the fields most at
    risk of not surviving the trip to a worker are the ones a plain arb tour never
    populates."""
    payload = _instance(stops=14, seed=99, systems=2)
    home = payload["waypoints"][0]
    away = payload["waypoints"][1]
    payload["ship"]["cargo"] = [dict(good_symbol="FOOD", units=10)]
    payload["constraints"].update(
        closed=True,
        anchor_system=home["system_symbol"],
        inter_system_hops=[dict(from_system=home["system_symbol"],
                                to_system=away["system_symbol"], gate_hops=3)],
        gate_fees=[dict(system=home["system_symbol"], fee_credits=4200)],
        externality_weight=0.4,
        inter_system_travel_per_hop_seconds=1030,
    )
    payload["deposit_candidates"] = [dict(good_symbol="ALLOY", units_wanted=40,
                                          synthetic_bid=310,
                                          storage_waypoint=home["symbol"],
                                          storage_system=home["system_symbol"])]
    payload["absorption"] = [dict(waypoint_symbol=away["symbol"], good_symbol="ORE",
                                  side="SELL", units_planned=12, units_recovering=5)]
    payload["stock_sources"] = [dict(good_symbol="FUEL", units_available=30, unit_ask=44,
                                     storage_waypoint=home["symbol"],
                                     storage_system=home["system_symbol"])]
    return payload


@pytest.fixture(scope="module")
def pool():
    pool = SolvePool(workers=3)
    pool.warm()
    yield pool
    pool.close()


@pytest.mark.parametrize("stops", [20, 40, 100])
def test_pooled_solve_matches_in_process_across_instance_sizes(pool, stops):
    payload = _instance(stops=stops, seed=stops, systems=2)
    expected = solve_tour(**_instance(stops=stops, seed=stops, systems=2))
    assert pool.run(pool_module.solve_tour_payload, payload) == expected


def test_pooled_solve_matches_in_process_on_a_fully_loaded_request(pool):
    expected = solve_tour(**_fully_loaded_payload())
    assert pool.run(pool_module.solve_tour_payload, _fully_loaded_payload()) == expected


def test_pooled_ortools_sequencer_matches_in_process(pool):
    """The native sequencer as well as the beam: it is the path that reaches OR-Tools'
    C++ core, and the one whose result a worker could plausibly shift."""
    pytest.importorskip("ortools")
    payload = _instance(stops=10, seed=7, systems=2)
    payload["sequencer"] = "ortools"
    expected = solve_tour(**dict(payload))
    assert pool.run(pool_module.solve_tour_payload, dict(payload)) == expected


def test_concurrent_solves_do_not_perturb_each_other(pool):
    """Run the whole corpus at once, the way the fleet does. Every answer still has to
    be the one a lone solve would have produced."""
    import concurrent.futures

    payloads = [_instance(stops=size, seed=size, systems=2) for size in (12, 18, 24)]
    payloads.append(_fully_loaded_payload())
    expected = [solve_tour(**dict(p)) for p in payloads]

    with concurrent.futures.ThreadPoolExecutor(max_workers=len(payloads)) as callers:
        got = list(callers.map(
            lambda p: pool.run(pool_module.solve_tour_payload, p), payloads))
    assert got == expected


def _tour_request():
    return routing_pb2.OptimizeTradeTourRequest(
        snapshot=[routing_pb2.MarketGoodSnapshot(
            waypoint_symbol=row["waypoint_symbol"], system_symbol=row["system_symbol"],
            good_symbol=row["good_symbol"], ask=row["ask"], bid=row["bid"],
            trade_volume=row["trade_volume"], supply=row["supply"],
            activity=row["activity"], observed_at_unix=row["observed_at_unix"])
            for row in _INSTANCE["snapshot"]],
        ship=routing_pb2.TourShip(
            ship_symbol=_INSTANCE["ship"]["ship_symbol"],
            current_waypoint=_INSTANCE["ship"]["current_waypoint"],
            current_system=_INSTANCE["ship"]["current_system"],
            hold_capacity=120, fuel_current=400, fuel_capacity=400, engine_speed=30),
        constraints=routing_pb2.TourConstraints(
            max_hops=6, max_spend=2_000_000, min_margin_per_unit=1,
            working_capital_reserve=50_000,
            allowed_systems=_INSTANCE["constraints"]["allowed_systems"],
            max_snapshot_age_minutes=75, max_tour_systems=4,
            expected_model_version=MODEL_VERSION),
        waypoints=[routing_pb2.TourWaypoint(
            symbol=w["symbol"], system_symbol=w["system_symbol"], x=w["x"], y=w["y"])
            for w in _INSTANCE["waypoints"]])


_INSTANCE = _instance(stops=24, seed=24, systems=2)


def test_servicer_answers_identically_whether_or_not_it_pools(tmp_path, pool):
    """End to end through the servicer: the same request, the same response bytes,
    pooled or inline. This is the wiring the daemon actually talks to."""
    import json

    artifact = tmp_path / "market_model.json"
    artifact.write_text(json.dumps(MODEL))

    inline = RoutingServiceHandler(tour_artifact_path=str(artifact),
                                   solve_pool=SolvePool(workers=0))
    pooled = RoutingServiceHandler(tour_artifact_path=str(artifact), solve_pool=pool)

    expected = inline.OptimizeTradeTour(_tour_request(), None)
    assert expected.feasible
    assert pooled.OptimizeTradeTour(_tour_request(), None) == expected

# gobot/services/routing-service/tests/test_fleet_partition_budget.py
"""PartitionFleet's time limit must BIND, and a fallback must SAY it is one — sp-ev79y.

Two defects, both measured against the live service on 2026-08-31.

THE BOUND DID NOT BIND. optimize_fleet_tour set time_limit on the OR-Tools search
parameters and nowhere else, but the distance matrix is built BEFORE the solver
exists — one fuel-constrained Dijkstra per ORDERED PAIR of nodes, which no solver
limit can reach. A 12-hull / 53-market charting crew spent 200s in that phase and
then 30s in the solve; 89 markets never finished inside the caller's 60s bound. The
caller (parkedsensing's chartPartitionTimeout) therefore burned a full minute of the
SERIAL sensing tick per re-solve and took its angular-sector fallback anyway.

FAILURE WAS SILENT. Every first-solution strategy OR-Tools offers fails on this
model — the forced non-empty route per vehicle is not a constraint a greedy
insertion heuristic respects — so the search burned its whole limit, returned
nothing, and the engine substituted its own round-robin and reported success. Live
today at 12 ships/36 markets, 11/29 and 10/22. A permanently degraded solver was
indistinguishable from a working one.
"""
import random
import time

import pytest

from utils.routing_engine import (
    ORToolsRoutingEngine,
    PARTITION_FALLBACK_BUDGET_SPENT,
    Waypoint,
)

_FUEL_CAPACITY = 400   # a FUELLED charting hull: the case that blew the budget
_ENGINE_SPEED = 30


def _system(n_stops, seed=11):
    """A system of n_stops scattered waypoints, one in five selling fuel."""
    rng = random.Random(seed)
    graph = {}
    for i in range(n_stops):
        symbol = "X1-EV79-%03d" % i
        graph[symbol] = Waypoint(symbol=symbol, x=rng.randint(-500, 500),
                                 y=rng.randint(-500, 500), has_fuel=(i % 5 == 0))
    return graph


def _crew(graph, n_ships):
    stops = list(graph)
    return {"HULL-%02d" % i: stops[i % len(stops)] for i in range(n_ships)}


def _makespan(engine, graph, partition, crew):
    """The longest per-hull circuit — the quantity the min-makespan objective cuts."""
    worst = 0
    for ship, markets in partition.assignments.items():
        current, total = crew[ship], 0
        for nxt in markets:
            leg = engine.find_optimal_path(graph, current, nxt, _FUEL_CAPACITY,
                                           _FUEL_CAPACITY, _ENGINE_SPEED)
            total += leg["total_time"] if leg else 10 ** 6
            current = nxt
        worst = max(worst, total)
    return worst


# The live wall-time table this bead was filed on: 8 stops ~36s, 36 ~59s, 53 ~231s,
# 89 over 300s, all against a nominal 30s VRP timeout.
@pytest.mark.parametrize("n_stops,n_ships", [(8, 3), (36, 12), (53, 12), (89, 12)])
def test_partition_returns_inside_its_budget(n_stops, n_ships):
    """Whatever the stop count, the call is over when its budget says so."""
    graph = _system(n_stops)
    crew = _crew(graph, n_ships)
    budget = 3.0
    engine = ORToolsRoutingEngine(vrp_timeout=int(budget))

    started = time.monotonic()
    partition = engine.optimize_fleet_tour(
        graph=graph, markets=list(graph), ship_locations=crew,
        fuel_capacity=_FUEL_CAPACITY, engine_speed=_ENGINE_SPEED, time_limit=budget,
    )
    elapsed = time.monotonic() - started

    assert elapsed <= budget + 1.0, (
        f"{n_stops} stops over {n_ships} hulls took {elapsed:.1f}s against a "
        f"{budget:.1f}s budget"
    )
    assert set(_all(partition)) == set(graph), "every stop must still be assigned"


def _all(partition):
    return [market for markets in partition.assignments.values() for market in markets]


@pytest.mark.parametrize("n_stops,n_ships", [(22, 10), (29, 11), (36, 12), (53, 12), (89, 12)])
def test_partition_is_solved_not_silently_round_robined(n_stops, n_ships):
    """The crew sizes that were logging 'VRP returned no solution' in production."""
    graph = _system(n_stops)
    partition = ORToolsRoutingEngine(vrp_timeout=3).optimize_fleet_tour(
        graph=graph, markets=list(graph), ship_locations=_crew(graph, n_ships),
        fuel_capacity=_FUEL_CAPACITY, engine_speed=_ENGINE_SPEED, time_limit=3.0,
    )

    assert not partition.fallback, (
        f"{n_ships} hulls over {n_stops} markets fell back to round-robin: "
        f"{partition.status}"
    )
    assert sorted(_all(partition)) == sorted(graph), "every stop must be assigned once"
    loads = [len(m) for m in partition.assignments.values()]
    assert min(loads) >= 1, f"no hull may be left idle: loads={loads}"


def test_a_fallback_says_it_is_a_fallback():
    """A budget too small to price the matrix still answers — and admits how."""
    graph = _system(53)
    partition = ORToolsRoutingEngine(vrp_timeout=30).optimize_fleet_tour(
        graph=graph, markets=list(graph), ship_locations=_crew(graph, 12),
        fuel_capacity=_FUEL_CAPACITY, engine_speed=_ENGINE_SPEED, time_limit=0.0,
    )

    assert partition.fallback, "a round-robin answer must be reported as a fallback"
    assert partition.status == PARTITION_FALLBACK_BUDGET_SPENT
    assert sorted(_all(partition)) == sorted(graph), "a fallback is still a partition"
    loads = [len(m) for m in partition.assignments.values()]
    assert max(loads) - min(loads) <= 1, f"a fallback must still be balanced: {loads}"


def test_short_budget_returns_the_best_found_not_the_fallback():
    """A solver budget too short to converge yields its best answer, not a discard.

    Before the seed, a search that ran out of time had NOTHING to hand back — it
    never found a first solution — so the whole solve was thrown away and the crew
    ran on round-robin. Seeded, the search starts feasible and can only improve.
    """
    graph = _system(53)
    crew = _crew(graph, 12)
    engine = ORToolsRoutingEngine(vrp_timeout=30)

    partition = engine.optimize_fleet_tour(
        graph=graph, markets=list(graph), ship_locations=crew,
        fuel_capacity=_FUEL_CAPACITY, engine_speed=_ENGINE_SPEED, time_limit=1.2,
    )
    round_robin = engine.optimize_fleet_tour(
        graph=graph, markets=list(graph), ship_locations=crew,
        fuel_capacity=_FUEL_CAPACITY, engine_speed=_ENGINE_SPEED, time_limit=0.0,
    )

    assert not partition.fallback, f"a short budget must still solve: {partition.status}"
    assert _makespan(engine, graph, partition, crew) <= _makespan(
        engine, graph, round_robin, crew), (
        "the answer must be at least as good as the round-robin it started from")


def test_crew_parked_on_every_market_still_partitions():
    """M markets and M hulls, one per market: the forced non-empty route is
    unsatisfiable (no hull has a node left to visit), and the model used to come
    back INFEASIBLE instantly. It must still return a partition of every market."""
    graph = _system(8)
    crew = {"HULL-%02d" % i: symbol for i, symbol in enumerate(graph)}
    partition = ORToolsRoutingEngine(vrp_timeout=3).optimize_fleet_tour(
        graph=graph, markets=list(graph), ship_locations=crew,
        fuel_capacity=_FUEL_CAPACITY, engine_speed=_ENGINE_SPEED, time_limit=3.0,
    )

    assert sorted(_all(partition)) == sorted(graph)
    loads = [len(m) for m in partition.assignments.values()]
    assert min(loads) >= 1, f"each hull keeps the market it is parked on: {loads}"


@pytest.mark.parametrize("fuel_capacity", [0, 40, 120, 400])
@pytest.mark.parametrize("seed", [1, 2, 3])
def test_matrix_prices_every_arc_exactly_as_the_per_pair_pathfinder_did(seed, fuel_capacity):
    """The speed-up must not move a single arc.

    The matrix now runs one sweep per ORIGIN instead of one search per ordered
    PAIR. Both walk the same cost model — BURN/CRUISE/DRIFT, the fuel safety
    margin, free refuelling, the DRIFT last-resort penalty — so every arc must come
    out with the value find_optimal_path gives it.
    """
    engine = ORToolsRoutingEngine()
    graph = _system(9, seed=seed)
    nodes = list(graph)

    matrix = engine._build_distance_matrix_for_vrp(nodes, graph, fuel_capacity, _ENGINE_SPEED)

    for i, origin in enumerate(nodes):
        for j, target in enumerate(nodes):
            if i == j:
                assert matrix[i][j] == 0
                continue
            route = engine.find_optimal_path(graph, origin, target, fuel_capacity,
                                             fuel_capacity, _ENGINE_SPEED)
            expected = route["total_time"] if route else 1_000_000
            assert matrix[i][j] == expected, f"{origin} -> {target}"

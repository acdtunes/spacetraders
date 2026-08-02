# gobot/services/routing-service/tests/test_tour_handler.py
import json

import pytest

from generated import routing_pb2
from handlers.routing_handler import RoutingServiceHandler


def _artifact(tmp_path):
    """Fixture artifact with clean round factors — the golden test must not
    drift when the real checked-in artifact is recalibrated."""
    art = {"fit_version": 1, "era": "goldene",
           "generated_at": "2026-07-09T22:00:00Z",
           "impact": {"LIMITED|WEAK": {"sell_decay_per_step": 0.9,
                                       "buy_growth_per_step": 1.1, "n_obs": 9}},
           "recovery": {},
           "diagnostics": {"ladder_count": 9, "control_series_count": 0}}
    p = tmp_path / "market_model.json"
    p.write_text(json.dumps(art))
    return str(p)


def snap(wp, sys_, good, ask, bid, tv=40):
    return routing_pb2.MarketGoodSnapshot(
        waypoint_symbol=wp, system_symbol=sys_, good_symbol=good, ask=ask, bid=bid,
        trade_volume=tv, supply="LIMITED", activity="WEAK",
        observed_at_unix=9_999_999_999)


def request(expected_version="1@goldene", allowed=("S1", "S2", "S3")):
    # The Task 5 split-sell scenario extended with a second system:
    # A buys G; B/C are S1 sinks; D (S2, cross-gate) sells G deeper and
    # sources H; E is the S1 sink for H (hold filled both directions).
    # F (S3) posts a fat bid for H — but H only sources at D (S2), so
    # reaching F means spanning start-S1 + S2 + S3: the 2-system cap must
    # keep it out even though allowed_systems permits S3.
    return routing_pb2.OptimizeTradeTourRequest(
        snapshot=[
            snap("A", "S1", "G", ask=100, bid=90),
            snap("B", "S1", "G", ask=999, bid=200),
            snap("C", "S1", "G", ask=999, bid=195),
            snap("D", "S2", "G", ask=999, bid=320),
            snap("D", "S2", "H", ask=50, bid=40),
            snap("E", "S1", "H", ask=999, bid=160),
            snap("F", "S3", "H", ask=999, bid=990),
        ],
        ship=routing_pb2.TourShip(
            ship_symbol="HULL-1", current_waypoint="A", current_system="S1",
            hold_capacity=80, fuel_current=400, fuel_capacity=400, engine_speed=30),
        constraints=routing_pb2.TourConstraints(
            max_hops=6, max_spend=100_000, min_margin_per_unit=1,
            working_capital_reserve=0, allowed_systems=list(allowed),
            max_snapshot_age_minutes=75, expected_model_version=expected_version),
        waypoints=[
            routing_pb2.TourWaypoint(symbol="A", system_symbol="S1", x=0, y=0),
            routing_pb2.TourWaypoint(symbol="B", system_symbol="S1", x=100, y=0),
            routing_pb2.TourWaypoint(symbol="C", system_symbol="S1", x=0, y=100),
            routing_pb2.TourWaypoint(symbol="D", system_symbol="S2", x=1000, y=1000),
            routing_pb2.TourWaypoint(symbol="E", system_symbol="S1", x=50, y=50),
            routing_pb2.TourWaypoint(symbol="F", system_symbol="S3", x=2000, y=2000),
        ])


def test_missing_artifact_fails_loud(tmp_path):
    handler = RoutingServiceHandler(
        tour_artifact_path=str(tmp_path / "nope" / "market_model.json"))
    resp = handler.OptimizeTradeTour(request(), None)
    assert not resp.feasible
    assert resp.infeasible_reason == "model_artifact_missing"
    assert len(resp.legs) == 0


def test_model_version_mismatch_errors_loudly(tmp_path):
    handler = RoutingServiceHandler(tour_artifact_path=_artifact(tmp_path))
    resp = handler.OptimizeTradeTour(request(expected_version="1@some-other-era"), None)
    assert not resp.feasible
    assert resp.infeasible_reason.startswith("model_version_mismatch")
    assert resp.model_version == "1@goldene"


def test_golden_tour(tmp_path):
    handler = RoutingServiceHandler(tour_artifact_path=_artifact(tmp_path))
    resp = handler.OptimizeTradeTour(request(), None)
    assert resp.feasible
    assert resp.model_version == "1@goldene"

    legs = [(l.waypoint_symbol, l.system_symbol) for l in resp.legs]
    # Exact chosen sequence (golden): buy 80 G at A, then run the D<->E lane ONCE.
    # sp-28lw9 recalibrated the per-visit absorption cap from 1 to 2.5 trade_volumes, so both
    # tranches of each good now clear in a SINGLE visit to their dock. Under sp-2v69u's 1.0 cap
    # each dock lifted one trade_volume per visit and the second tranche rode a second D/E round
    # trip: [A, D, E, D, E]. THIS COLLAPSE IS THE FEATURE — identical total load and identical
    # per-price trade set (asserted below), carried in one round trip instead of two, which is
    # exactly the "same nav/dock overhead" throughput claim the bead is built on.
    assert legs == [("A", "S1"), ("D", "S2"), ("E", "S1")]

    # Aggregated by (waypoint, good, side, price): the total load is unchanged (80 G sold
    # cross-gate at D across two visits, 80 H sold back home at E across two visits) —
    # only the leg structure spread; the price-keyed trade set and the profit are identical.
    trades = {(l.waypoint_symbol, t.good_symbol, t.is_buy, t.expected_unit_price): t.units
              for l in resp.legs for t in l.trades}
    assert trades == {
        ("A", "G", True, 100): 40, ("A", "G", True, 110): 40,   # 80u G loaded
        ("D", "G", False, 320): 40, ("D", "G", False, 288): 40,  # sold cross-gate (two visits)
        ("D", "H", True, 50): 40, ("D", "H", True, 55): 40,      # 80u H return cargo
        ("E", "H", False, 160): 40, ("E", "H", False, 144): 40,  # sold back home (two visits)
    }
    assert resp.projected_profit == 23_880
    assert resp.projected_credits_per_hour > 0
    # sp-bc27: an empty-hold tour has no launch-liquidation revenue, so the split
    # field is 0 and projected_profit is all fresh-trade profit.
    assert resp.held_liquidation == 0
    # sp-dchv: no deposit_candidates on this request -> no deposit legs, deposit_value 0.
    assert resp.deposit_value == 0
    assert all(not t.is_deposit for l in resp.legs for t in l.trades)

    # 2-system cap held even though S3's fat bid was allowed by constraints.
    assert all(l.system_symbol in ("S1", "S2") for l in resp.legs)

    # Observability parity: up to 3 rejected alternatives, each with a reason.
    assert 1 <= len(resp.top_rejected) <= 3
    assert all(r.reason for r in resp.top_rejected)
    assert all(r.summary for r in resp.top_rejected)


def test_handler_forwards_max_tour_systems(tmp_path):
    # sp-syaz: the pb TourConstraints.max_tour_systems must be BRIDGED into the solver
    # constraints dict (the handler dropped it before this bead, so the raised cap was
    # inert on the wire). On the same S1/S2/S3 board test_golden_tour clamps to 2, a
    # request cap of 3 unlocks the S3 sink F (H bid 990, far fatter than E's 160), so
    # the winning tour spans all three systems.
    handler = RoutingServiceHandler(tour_artifact_path=_artifact(tmp_path))
    req = request()
    req.constraints.max_tour_systems = 3
    resp = handler.OptimizeTradeTour(req, None)
    assert resp.feasible
    assert {l.system_symbol for l in resp.legs} == {"S1", "S2", "S3"}, \
        [(l.waypoint_symbol, l.system_symbol) for l in resp.legs]

    # Companion (default-safe): omitting the field keeps the 2-system clamp — the pb
    # int32 defaults to 0, which the solver reads as the module default (2).
    resp_default = handler.OptimizeTradeTour(request(), None)
    assert len({l.system_symbol for l in resp_default.legs}) <= 2, \
        [(l.waypoint_symbol, l.system_symbol) for l in resp_default.legs]


def test_handler_forwards_inter_system_hops(tmp_path):
    # sp-tp5c3: the pb TourConstraints.inter_system_hops must be BRIDGED into the solver
    # constraints dict so a cross-system crossing is priced in its REAL gate-hop count.
    # sp-smbgd made that price AFFINE — 750 + 650*hops — so on the golden board an undeclared
    # S1<->S2 crossing prices at the 1-hop charge 1400, and declaring it as 2 gate hops raises
    # each crossing to 2050 (NOT 2x1400: the fixed base is paid once per crossing, not per hop),
    # WITHOUT changing the profit-primary tour shape (declaring hops changes time, not profit).
    handler = RoutingServiceHandler(tour_artifact_path=_artifact(tmp_path))

    # Baseline: no map -> every crossing defaults to 1 hop = 750 + 650 = 1400.
    base = handler.OptimizeTradeTour(request(), None)
    assert base.feasible
    # Two crossings, not four: sp-28lw9's per-visit cap of 2.5 trade_volumes lets the golden
    # board's load clear in ONE D/E round trip instead of two (see test_golden_tour).
    assert [l.travel_seconds_from_prev for l in base.legs[1:]] == [1400, 1400]

    # Armed: S1<->S2 declared as 2 gate hops -> each crossing prices at 750 + 650*2 = 2050.
    req = request()
    req.constraints.inter_system_hops.append(
        routing_pb2.InterSystemHopDistance(from_system="S1", to_system="S2", gate_hops=2))
    armed = handler.OptimizeTradeTour(req, None)
    assert armed.feasible
    assert [(l.waypoint_symbol, l.system_symbol) for l in armed.legs] == \
        [(l.waypoint_symbol, l.system_symbol) for l in base.legs]
    assert [l.travel_seconds_from_prev for l in armed.legs[1:]] == [2050, 2050]
    # The affine shape, asserted at the bridge: a 2-hop crossing costs LESS than two 1-hop ones.
    assert armed.legs[1].travel_seconds_from_prev < 2 * base.legs[1].travel_seconds_from_prev


def test_handler_forwards_closure_fields(tmp_path):
    # sp-im74: the pb closed/anchor_system fields must be BRIDGED into the solver
    # constraints dict. Golden board: the open winner is A->D->E (ends at E).
    # closed floating => a no-trade return hop to the ship's waypoint A is appended;
    # an explicit S2 anchor => the return targets S2's lexicographically-first fresh
    # market waypoint (D, the only S2 market).
    handler = RoutingServiceHandler(tour_artifact_path=_artifact(tmp_path))

    req = request()
    req.constraints.closed = True
    resp = handler.OptimizeTradeTour(req, None)
    assert resp.feasible
    last = resp.legs[-1]
    assert (last.waypoint_symbol, last.system_symbol) == ("A", "S1")
    assert len(last.trades) == 0

    req2 = request()
    req2.constraints.closed = True
    req2.constraints.anchor_system = "S2"
    resp2 = handler.OptimizeTradeTour(req2, None)
    assert resp2.feasible
    last2 = resp2.legs[-1]
    assert (last2.waypoint_symbol, last2.system_symbol) == ("D", "S2")

    # Companion (default-safe): an untouched request still plans the OPEN golden
    # tour ending at E — the absent pb fields (false/"") change nothing on the wire.
    resp_open = handler.OptimizeTradeTour(request(), None)
    assert resp_open.legs[-1].waypoint_symbol == "E"


def test_deposit_candidate_round_trips_to_deposit_leg(tmp_path):
    # sp-dchv Lane C: a request carrying a deposit candidate produces a DEPOSIT leg
    # at the storage waypoint (is_deposit=True, priced at the synthetic bid) and a
    # non-zero deposit_value — the proto plumbing the Go executor reads. Buy G cheap
    # at A, deposit into the warehouse W; no market sink, so pre-positioning is the
    # only profitable move.
    handler = RoutingServiceHandler(tour_artifact_path=_artifact(tmp_path))
    req = routing_pb2.OptimizeTradeTourRequest(
        snapshot=[snap("A", "S1", "G", ask=100, bid=90, tv=40)],
        ship=routing_pb2.TourShip(
            ship_symbol="HULL-1", current_waypoint="A", current_system="S1",
            hold_capacity=80, fuel_current=400, fuel_capacity=400, engine_speed=30),
        constraints=routing_pb2.TourConstraints(
            max_hops=4, max_spend=100_000, min_margin_per_unit=1,
            working_capital_reserve=0, allowed_systems=["S1"],
            max_snapshot_age_minutes=75, expected_model_version="1@goldene"),
        waypoints=[routing_pb2.TourWaypoint(symbol="A", system_symbol="S1", x=0, y=0)],
        deposit_candidates=[routing_pb2.DepositCandidate(
            good_symbol="G", units_wanted=40, synthetic_bid=600,
            storage_waypoint="W", storage_system="S1")])

    resp = handler.OptimizeTradeTour(req, None)
    assert resp.feasible
    deposit_trades = [(l.waypoint_symbol, t.units, t.expected_unit_price)
                      for l in resp.legs for t in l.trades if t.is_deposit]
    assert ("W", 40, 600) in deposit_trades, deposit_trades   # deposited at synthetic bid
    assert resp.deposit_value == 40 * 600
    # The deposit books no cash: projected_profit is the synthetic value minus the
    # foreign buy spend (the savings), and the fresh-cash remainder is negative.
    assert resp.projected_profit == resp.deposit_value - (40 * 100 + 0)
    assert resp.held_liquidation == 0


def test_reserve_exceeds_budget_names_the_cause_through_handler(tmp_path):
    # sp-avt4: an EXPLICIT max_spend request where working_capital_reserve consumes
    # the whole budget must round-trip a "reserve_exceeds_budget" reason through the
    # actual proto response — the Go coordinator only ever sees this field, never the
    # solve_tour() dict directly, so the wiring itself is what this test pins.
    handler = RoutingServiceHandler(tour_artifact_path=_artifact(tmp_path))
    req = routing_pb2.OptimizeTradeTourRequest(
        snapshot=[snap("A", "S1", "G", ask=100, bid=90),
                  snap("B", "S1", "G", ask=999, bid=200)],
        ship=routing_pb2.TourShip(
            ship_symbol="HULL-1", current_waypoint="A", current_system="S1",
            hold_capacity=80, fuel_current=400, fuel_capacity=400, engine_speed=30),
        constraints=routing_pb2.TourConstraints(
            max_hops=4, max_spend=50_000, min_margin_per_unit=1,
            working_capital_reserve=50_000,  # reserve == max_spend -> spend_cap 0
            allowed_systems=["S1"],
            max_snapshot_age_minutes=75, expected_model_version="1@goldene"),
        waypoints=[routing_pb2.TourWaypoint(symbol="A", system_symbol="S1", x=0, y=0),
                   routing_pb2.TourWaypoint(symbol="B", system_symbol="S1", x=100, y=0)])

    resp = handler.OptimizeTradeTour(req, None)
    assert not resp.feasible
    assert resp.infeasible_reason.startswith("reserve_exceeds_budget"), resp.infeasible_reason
    assert "max_spend 50000" in resp.infeasible_reason
    assert "reserve 50000" in resp.infeasible_reason
    assert len(resp.legs) == 0


def test_handler_forwards_per_origin_gate_fees(tmp_path):
    # sp-9idvn: the pb TourConstraints.gate_fees must be BRIDGED into the solver constraints
    # dict, or the whole per-gate table is inert on the wire — the exact failure mode that
    # made max_tour_systems dormant before sp-syaz.
    #
    # The golden board makes TWO crossings, one departing S1 and one departing S2, which is
    # what lets a single assertion prove both halves at once: pricing S1 alone must move the
    # total by exactly one crossing's difference. A handler that mirrored the entry onto both
    # directions would move it by two, and a handler that dropped the field by zero.
    handler = RoutingServiceHandler(tour_artifact_path=_artifact(tmp_path))

    base = handler.OptimizeTradeTour(request(), None)
    assert base.feasible

    req = request()
    req.constraints.gate_fees.append(routing_pb2.GateFee(system="S1", fee_credits=9_000))
    armed = handler.OptimizeTradeTour(req, None)
    assert armed.feasible

    # Same tour, only priced differently — so the delta isolates the fee.
    assert [(l.waypoint_symbol, l.system_symbol) for l in armed.legs] == \
        [(l.waypoint_symbol, l.system_symbol) for l in base.legs]
    # One crossing leaves S1, and it now costs 9,000 instead of the flat 6,000.
    assert base.projected_profit - armed.projected_profit == 3_000, (
        f"expected exactly one crossing to reprice: "
        f"base={base.projected_profit} armed={armed.projected_profit}")

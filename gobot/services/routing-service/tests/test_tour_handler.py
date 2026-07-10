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
    # Exact chosen sequence (golden): buy G at A, cross the gate to sell G
    # deep at D and load H, come back to sell H at E — hold full both ways.
    assert legs == [("A", "S1"), ("D", "S2"), ("E", "S1")]

    trades = {(l.waypoint_symbol, t.good_symbol, t.is_buy, t.expected_unit_price): t.units
              for l in resp.legs for t in l.trades}
    assert trades == {
        ("A", "G", True, 100): 40, ("A", "G", True, 110): 40,   # 80u G loaded
        ("D", "G", False, 320): 40, ("D", "G", False, 288): 40,  # sold cross-gate
        ("D", "H", True, 50): 40, ("D", "H", True, 55): 40,      # 80u H return cargo
        ("E", "H", False, 160): 40, ("E", "H", False, 144): 40,  # sold back home
    }
    assert resp.projected_profit == 23_880
    assert resp.projected_credits_per_hour > 0
    # sp-bc27: an empty-hold tour has no launch-liquidation revenue, so the split
    # field is 0 and projected_profit is all fresh-trade profit.
    assert resp.held_liquidation == 0

    # 2-system cap held even though S3's fat bid was allowed by constraints.
    assert all(l.system_symbol in ("S1", "S2") for l in resp.legs)

    # Observability parity: up to 3 rejected alternatives, each with a reason.
    assert 1 <= len(resp.top_rejected) <= 3
    assert all(r.reason for r in resp.top_rejected)
    assert all(r.summary for r in resp.top_rejected)

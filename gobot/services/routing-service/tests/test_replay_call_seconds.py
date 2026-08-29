"""Call-seconds sweep replay — verdict/wiring tests, no DB and no real solver.

Pins the plumbing the adoption gate rests on:
  * import purity — the deployed-env mirror runs only under main()
  * solve_at_cs   — ARMS the exchange rate per solve and CLEARS it afterwards (and for a
                    non-positive arm), so no arm can leak into a sibling solve
  * plan_shape    — goods/trading-leg, deep-tranche units per (market, good, side) pool,
                    crossings from the leg walk, dwell-inclusive seconds
  * loss_share    — per-case credits-per-call losses, paired by case, unpriceable skipped
"""
import importlib
import os

import pytest

pytest.importorskip("sqlalchemy", reason="model-pipeline dep; see requirements-model.txt")

import replay_call_seconds as rc
from utils import tour_solver


def test_import_does_not_mutate_solver_env(monkeypatch):
    for var in ("TOUR_SOLVER_OBJECTIVE", "TOUR_SOLVER_SEQUENCER",
                tour_solver.API_CALL_SECONDS_ENV_VAR):
        monkeypatch.delenv(var, raising=False)
    importlib.reload(rc)
    assert tour_solver.API_CALL_SECONDS_ENV_VAR not in os.environ
    assert "TOUR_SOLVER_SEQUENCER" not in os.environ


def test_solve_at_cs_arms_per_solve_and_always_clears(monkeypatch):
    seen = []

    def fake_solve_tour(*a, **kw):
        seen.append(os.environ.get(tour_solver.API_CALL_SECONDS_ENV_VAR))
        return dict(feasible=False, legs=[])

    monkeypatch.setattr(rc, "solve_tour", fake_solve_tour)
    rc.solve_at_cs(120.0, [], {}, {}, {}, [])
    assert seen[-1] == repr(120.0)
    assert tour_solver.API_CALL_SECONDS_ENV_VAR not in os.environ
    rc.solve_at_cs(0, [], {}, {}, {}, [])
    assert seen[-1] is None  # non-positive arm = the code-default solver, not a variant


def test_solve_at_cs_clears_even_when_the_solve_raises(monkeypatch):
    def boom(*a, **kw):
        raise RuntimeError("solve failed")

    monkeypatch.setattr(rc, "solve_tour", boom)
    with pytest.raises(RuntimeError):
        rc.solve_at_cs(60.0, [], {}, {}, {}, [])
    assert tour_solver.API_CALL_SECONDS_ENV_VAR not in os.environ


def _result(legs, profit=1000, calls=10.0):
    return dict(projected_profit=profit, projected_api_calls=calls, legs=legs)


def _leg(wp, system, trades, travel=100):
    return dict(waypoint_symbol=wp, system_symbol=system, trades=trades,
                travel_seconds_from_prev=travel)


def test_plan_shape_counts_goods_deep_units_and_crossings():
    legs = [
        _leg("A", "S1", [dict(good_symbol="X", units=200, is_buy=True),
                         dict(good_symbol="Y", units=50, is_buy=True)]),
        _leg("B", "S2", [dict(good_symbol="X", units=200, is_buy=False),
                         dict(good_symbol="Y", units=50, is_buy=False)]),
        _leg("C", "S1", [], travel=300),
    ]
    tv = {("A", "X"): 40, ("A", "Y"): 60, ("B", "X"): 40, ("B", "Y"): 60}
    shape = rc.plan_shape(_result(legs), tv)
    # X: 200 units against a 40-volume pool = 80 past ordinal 3, on each side.
    assert shape["deep_units"] == 160
    assert shape["units"] == 500
    assert shape["goods"] == 2.0        # the no-trade leg is not a trading leg
    assert shape["crossings"] == 2      # S1 -> S2 -> S1
    assert shape["legs"] == 3
    assert shape["seconds"] == 100 + 100 + 300 + 3 * tour_solver.DWELL_SECONDS_PER_LEG


def test_plan_shape_unreadable_volume_charges_no_deep_units():
    legs = [_leg("A", "S1", [dict(good_symbol="X", units=500, is_buy=True)])]
    shape = rc.plan_shape(_result(legs), {})
    assert shape["deep_units"] == 0


def test_loss_share_pairs_cases_and_skips_unpriceable():
    base = [dict(profit=100, calls=10.0), dict(profit=100, calls=10.0),
            dict(profit=100, calls=0.0)]
    cand = [dict(profit=50, calls=10.0), dict(profit=200, calls=10.0),
            dict(profit=999, calls=10.0)]
    assert rc.loss_share(base, cand) == 0.5
    assert rc.loss_share([], []) == 0.0


def test_arm_cases_reports_hourly_rate_and_demand_with_overhead():
    shapes = [dict(profit=3600, calls=36.0, seconds=3600 - rc.TOUR_OVERHEAD_SECONDS)]
    (case,) = rc.arm_cases(shapes)
    assert case["arm_cph"] == pytest.approx(3600.0)
    assert case["arm_demand"] == pytest.approx(36.0)

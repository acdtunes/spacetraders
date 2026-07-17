"""sp-f1yk W1 — replay-harness structure/wiring tests (seam-free, WAVE-0).

These pin the offline arming gate's PURE, DB-free plumbing that was refactored out of
replay_objective.main():
  * fleet_cph            — the single fleet-$/hr aggregator (one source of truth)
  * summarize            — delegates its per-objective cph to fleet_cph (no dual math)
  * run_case             — threads / omits the sp-syaz max_tour_systems cap key
  * arming_pass          — assembles the two-cap (baseline|candidate) x (profit|rate) matrix
  * arming_verdict       — the deterministic armed:bool gate over that matrix

All tests are seam-free: the solver is stubbed/monkeypatched, so nothing here depends on
sp-y05b (OR-Tools) or sp-z7ng (placement). The syaz cap-knob (max_tour_systems) is LIVE on
main, so RED#3 is meaningful now. The end-to-end replay gate firing on REAL OR-Tools cap-6
data is deferred — see test_arming_gate_on_real_replay_data_TODO_W4.
"""
import math
from datetime import datetime, timedelta
from types import SimpleNamespace

import pytest

import replay_objective as ro
from replay_objective import OBJECTIVE_PROFIT, OBJECTIVE_RATE


# --------------------------------------------------------------------------- helpers
def _res(profit, cph, feasible=True):
    """A minimal solver-result dict: exactly the keys plan_seconds / fleet_cph read."""
    return dict(feasible=feasible, projected_profit=profit,
                projected_credits_per_hour=float(cph))


def _case_two_obj(sample, home, hold, profit_res, rate_res):
    """A DEFAULT one_pass-shaped case: objective-keyed `results`."""
    return dict(sample=sample, home=home, hold=hold,
                results={OBJECTIVE_PROFIT: profit_res, OBJECTIVE_RATE: rate_res})


def _case_by_cell(cells):
    """An arming-pass-shaped case: (cap, objective)-keyed `results_by_cell`."""
    return dict(sample="s", home="X1-S1", hold=40, results_by_cell=cells)


# ------------------------------------------------------------- RED: fleet_cph math
@pytest.mark.parametrize("overhead,expected", [
    # two feasible plans; hours = (profit/cph*3600 + overhead)/3600.
    #   r0: 1000cr @ 2000cph -> 1800s -> 0.5h (+oh);  r1: 3000cr @ 1000cph -> 10800s -> 3.0h (+oh)
    (0,    4000.0 / 3.5),          # (1000+3000) / (0.5 + 3.0)
    (3600, 4000.0 / 5.5),          # +1h each: (1000+3000) / (1.5 + 4.0)
])
def test_fleet_cph_known_value(overhead, expected):
    results = [_res(1000, 2000), _res(3000, 1000)]
    assert math.isclose(ro.fleet_cph(results, overhead), expected, rel_tol=1e-9)


def test_fleet_cph_excludes_infeasible_and_zero_time():
    # infeasible and zero-cph rows contribute NOTHING (feasibility / positive-time filter).
    results = [_res(1000, 2000), _res(9999, 0), _res(9999, 500, feasible=False)]
    assert math.isclose(ro.fleet_cph(results, 0), 1000 / 0.5, rel_tol=1e-9)
    assert ro.fleet_cph([], 0) == 0.0
    assert ro.fleet_cph([_res(0, 0)], 0) == 0.0


# ------------------------------------------- RED#1: summarize delegates to fleet_cph
def test_summarize_delegates_to_fleet_cph_and_default_shape():
    cases = [
        _case_two_obj("t1", "H", 40, _res(1000, 2000), _res(1000, 2000)),   # identical
        _case_two_obj("t2", "H", 40, _res(2000, 1000), _res(1500, 3000)),   # diverges
        _case_two_obj("t3", "H", 40, _res(500, 500), _res(400, 800)),       # diverges
    ]
    overhead = 60
    agg, diverged, rate_wins, per_case = ro.summarize(cases, overhead)

    # SINGLE SOURCE OF TRUTH: summarize's per-objective aggregate cph is exactly what
    # fleet_cph computes over the same result lists — the human sanity-check and the
    # machine gate cannot drift (resolves the dual-math finding).
    pp, ph = agg[OBJECTIVE_PROFIT]
    rp, rh = agg[OBJECTIVE_RATE]
    assert pp / ph == ro.fleet_cph([c["results"][OBJECTIVE_PROFIT] for c in cases], overhead)
    assert rp / rh == ro.fleet_cph([c["results"][OBJECTIVE_RATE] for c in cases], overhead)

    # divergence + per-case shape preserved (default profit-vs-rate output invariant).
    assert diverged == 2
    assert rate_wins == 2   # rate choice effectively better in both diverging cases
    assert len(per_case) == 3
    assert set(per_case[0]) == {"sample", "home", "hold", "profit_choice",
                                "rate_choice", "diverged"}
    assert per_case[0]["diverged"] is False
    assert per_case[1]["diverged"] is True
    assert set(per_case[0]["profit_choice"]) == {"profit", "seconds", "cph"}


# --------------------------------------- RED#3: run_case threads/omits the cap key
@pytest.mark.parametrize("cap,expect_present", [(5, True), (2, True), (None, False)])
def test_cap_param_threads_to_constraints(monkeypatch, cap, expect_present):
    captured = {}

    def fake_solve(snapshot, ship, cons, model, waypoints=None, objective=None, **kw):
        captured["cons"] = dict(cons)
        return _res(1, 1)

    monkeypatch.setattr(ro, "solve_tour", fake_solve)
    monkeypatch.setattr(ro, "MODEL", {"fit_version": 1, "era": "e"}, raising=False)

    snapshot = [dict(waypoint_symbol="X1-S1-A", system_symbol="X1-S1", good_symbol="G",
                     ask=100, bid=90, trade_volume=40, supply="LIMITED", activity="WEAK",
                     observed_at_unix=9_999_999_999)]
    waypoints = [dict(symbol="X1-S1-A", system="X1-S1", x=0, y=0)]

    ro.run_case(snapshot, waypoints, "X1-S1", {"X1-S1"}, 40,
                1_000_000, 0, "1@e", max_tour_systems=cap)

    if expect_present:
        assert captured["cons"]["max_tour_systems"] == cap
    else:
        # None => OMIT the key entirely => byte-identical default DB run (solver
        # resolves absent/0 to MAX_TOUR_SYSTEMS=2 via _effective_tour_systems).
        assert "max_tour_systems" not in captured["cons"]


# --------------------------------- RED#2b: arming_pass assembles the two-cap matrix
def test_arming_pass_assembles_two_cap_matrix(monkeypatch):
    def fake_solve(snapshot, ship, cons, model, waypoints=None, objective=None, **kw):
        cap = cons.get("max_tour_systems")
        # encode the cap it SAW and the objective so the assembly is verifiable.
        return dict(feasible=True, projected_profit=100 * cap,
                    projected_credits_per_hour=1000.0, cap=cap, objective=objective)

    monkeypatch.setattr(ro, "solve_tour", fake_solve)
    monkeypatch.setattr(ro, "MODEL", {"fit_version": 1, "era": "e"}, raising=False)

    sample_t = datetime(2026, 7, 16, 12, 0, 0)
    rows = [
        SimpleNamespace(waypoint_symbol="X1-S1-A", good_symbol="G", purchase_price=100,
                        sell_price=90, supply="LIMITED", activity="WEAK", trade_volume=40,
                        recorded_at=sample_t),
        SimpleNamespace(waypoint_symbol="X1-S1-B", good_symbol="G", purchase_price=90,
                        sell_price=200, supply="LIMITED", activity="WEAK", trade_volume=40,
                        recorded_at=sample_t),
    ]
    coords = {"X1-S1-A": ("X1-S1", 0, 0), "X1-S1-B": ("X1-S1", 5, 5)}

    cases = ro.arming_pass([sample_t], rows, {}, coords, [40], "1@e",
                           baseline_cap=2, candidate_cap=6,
                           max_spend=1_000_000, reserve=0)

    assert len(cases) == 1
    cells = cases[0]["results_by_cell"]
    assert set(cells) == {(2, OBJECTIVE_PROFIT), (2, OBJECTIVE_RATE),
                          (6, OBJECTIVE_PROFIT), (6, OBJECTIVE_RATE)}
    for (cap, objective), res in cells.items():
        assert res["cap"] == cap and res["objective"] == objective
    # fleet_cph can read the assembled cells (positive $/hr in every cell).
    assert ro.fleet_cph([cells[(6, OBJECTIVE_RATE)]], 0) > 0


# --------------------------------- RED#2: arming_verdict -- deterministic gate
def _win_cases(n):
    # candidate (6, rate) clearly beats baseline (2, profit); (6, profit) sits between
    # so objective_delta_pct isolates the rate objective at the candidate cap.
    cells = {
        (2, OBJECTIVE_PROFIT): _res(1000, 1000),   # baseline: 1000cr @ 1h -> 1000/hr
        (2, OBJECTIVE_RATE):   _res(1100, 1100),
        (6, OBJECTIVE_PROFIT): _res(1500, 1500),   # candidate-cap profit -> 1500/hr
        (6, OBJECTIVE_RATE):   _res(2000, 2000),   # candidate: 2000/hr
    }
    return [_case_by_cell(dict(cells)) for _ in range(n)]


def test_arming_verdict_win():
    verdict = ro.arming_verdict(_win_cases(30),
                                baseline=(2, OBJECTIVE_PROFIT),
                                candidate=(6, OBJECTIVE_RATE),
                                overhead_seconds=0, min_delta_pct=5.0, min_cases=30)
    assert verdict["armed"] is True
    assert verdict["delta_pct"] > 0
    assert verdict["cases"] == 30
    assert math.isclose(verdict["baseline_cph"], 1000.0, rel_tol=1e-9)
    assert math.isclose(verdict["candidate_cph"], 2000.0, rel_tol=1e-9)
    # sp-db0n: pin the TRUE live-prod baseline the operator must read — cap=2 at the RATE
    # objective (sp-1wp8's launch-path TOUR_SOLVER_OBJECTIVE=rate default), NOT the cap=2
    # PROFIT fail-safe. Traced to the _win_cases (2, RATE)=_res(1100, 1100) cell: at zero
    # overhead that plan is 1100 cr over 1.0 h = 1100 cr/hr. This is the value now surfaced
    # in the --arm console, so ljh5 arms against the config prod ACTUALLY runs today.
    assert math.isclose(verdict["baseline_cap_rate_cph"], 1100.0, rel_tol=1e-9)
    # isolation column present and non-trivial (candidate rate vs candidate-cap profit).
    assert math.isclose(verdict["objective_delta_pct"],
                        (2000.0 - 1500.0) / 1500.0 * 100, rel_tol=1e-9)


@pytest.mark.parametrize("n,min_delta,min_cases,reason", [
    (30, 5.0, 40, "too_few_cases"),        # delta huge, but cases 30 < min 40
    (30, 200.0, 30, "delta_below_min"),    # cases ok, but delta 100% < min 200%
])
def test_arming_verdict_noop(n, min_delta, min_cases, reason):
    verdict = ro.arming_verdict(_win_cases(n),
                                baseline=(2, OBJECTIVE_PROFIT),
                                candidate=(6, OBJECTIVE_RATE),
                                overhead_seconds=0, min_delta_pct=min_delta,
                                min_cases=min_cases)
    assert verdict["armed"] is False, reason


# ------------------------------- sp-ljh5: the arm relies on the TRUE live-prod baseline
def test_arming_verdict_relies_on_true_live_prod_baseline():
    """sp-ljh5 (post sp-db0n): the arm decision must measure the fleet-$/hr win against
    the TRUE live-prod reference — cap=2 at the RATE objective (sp-1wp8's launch default,
    baseline_cap_rate_cph) — NOT the cap=2 PROFIT in-code fail-safe (a config prod does
    NOT run). A candidate that crushes the fail-safe but barely beats what prod ACTUALLY
    runs today must stay DISARMED, so the fleet is never re-objectived on a phantom win."""
    cells = {
        (2, OBJECTIVE_PROFIT): _res(1000, 1000),   # in-code fail-safe (NOT the deployed default)
        (2, OBJECTIVE_RATE):   _res(2000, 2000),   # TRUE live-prod: cap-2 RATE (sp-1wp8 launch)
        (6, OBJECTIVE_PROFIT): _res(1500, 1500),
        (6, OBJECTIVE_RATE):   _res(2050, 2050),   # candidate: +105% vs fail-safe, +2.5% vs true live
    }
    cases = [_case_by_cell(dict(cells)) for _ in range(30)]
    verdict = ro.arming_verdict(cases, baseline=(2, OBJECTIVE_PROFIT),
                                candidate=(6, OBJECTIVE_RATE),
                                overhead_seconds=0, min_delta_pct=5.0, min_cases=30)
    # The fail-safe delta is huge (+105%) — a gate that read it would have armed here.
    assert verdict["delta_pct"] > 100
    # The gating delta is measured against the true live-prod baseline (cap-2 RATE = 2000).
    assert math.isclose(verdict["baseline_cap_rate_cph"], 2000.0, rel_tol=1e-9)
    assert math.isclose(verdict["true_live_delta_pct"],
                        (2050.0 - 2000.0) / 2000.0 * 100, rel_tol=1e-9)
    assert verdict["true_live_delta_pct"] < 5.0
    # ljh5: the arm relies on baseline_cap_rate_cph, so a +2.5% true-live gain stays DISARMED.
    assert verdict["armed"] is False


# ============================================================ STUB: TODO(W4) =========
@pytest.mark.skip(reason="TODO(W4): the end-to-end fleet-$/hr replay GATE firing on real "
                         "OR-Tools cap-6 solver data requires sp-y05b (OR-Tools sequencer) "
                         "and a production-representative market_price_history replay. The "
                         "gate's pure logic (arming_pass/arming_verdict/fleet_cph) is fully "
                         "unit-tested above; W4 runs `replay_objective.py --arm` against real "
                         "data and asserts arming_verdict(...).armed on the JOINT cap+rate "
                         "package before ljh5 flips TOUR_SOLVER_OBJECTIVE=rate.")
def test_arming_gate_on_real_replay_data_TODO_W4():
    raise NotImplementedError("W4: real-data arming replay — needs sp-y05b + DB fixture")

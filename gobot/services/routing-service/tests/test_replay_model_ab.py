"""Model A/B replay — pure verdict/wiring tests, no DB and no real solver.

Pins the adoption gate's honest-comparison plumbing:
  * plan_value      — unprofitable/absent plans value at 0 (idle beats a loser)
  * classify        — win/loss/tie under the common evaluator, same-route tie
  * ab_verdict      — deterministic adopt:bool; empty evidence fails safe
  * run_ab_case     — solves BOTH models, re-scores the incumbent's route under
                      the candidate (never promise-vs-promise), skips windows
                      neither model can plan
"""
import importlib
import os

import pytest

pytest.importorskip("sqlalchemy", reason="model-pipeline dep; see requirements-model.txt")

import replay_model_ab as ab
from replay_model_ab import OBJECTIVE_RATE


def test_import_does_not_mutate_solver_env(monkeypatch):
    # The deployed-env mirror runs only under main(): a setdefault leaked at
    # import time would silently reshape every later solve in the same process
    # (the golden-tour fixtures pin the solver's in-code defaults).
    for var in ("TOUR_SOLVER_OBJECTIVE", "TOUR_SOLVER_SEQUENCER",
                "TOUR_SOLVER_MAX_PLANNED_TRANCHES", "TOUR_SOLVER_FULL_SCORE_TOP_N"):
        monkeypatch.delenv(var, raising=False)
    importlib.reload(ab)
    assert "TOUR_SOLVER_SEQUENCER" not in os.environ
    assert "TOUR_SOLVER_OBJECTIVE" not in os.environ


def _feasible(profit, cph, legs):
    return dict(feasible=True, projected_profit=profit,
                projected_credits_per_hour=float(cph),
                legs=[dict(waypoint_symbol=w) for w in legs])


INFEASIBLE = dict(feasible=False, projected_profit=0,
                  projected_credits_per_hour=0.0, legs=[])


def test_plan_value_zeroes_unprofitable_plans():
    assert ab.plan_value(None, 0, OBJECTIVE_RATE) == 0.0
    assert ab.plan_value(-50, 1000.0, OBJECTIVE_RATE) == 0.0
    assert ab.plan_value(0, 1000.0, OBJECTIVE_RATE) == 0.0
    assert ab.plan_value(500, 1200.0, OBJECTIVE_RATE) == 1200.0
    assert ab.plan_value(500, 1200.0, "profit") == 500.0


def test_classify_same_route_is_a_tie_regardless_of_value():
    assert ab.classify(900.0, 1000.0, same_route=True) == "tie"


def test_classify_win_loss_and_epsilon_tie():
    assert ab.classify(1100.0, 1000.0, same_route=False) == "win"
    assert ab.classify(900.0, 1000.0, same_route=False) == "loss"
    assert ab.classify(1000.0, 1000.0 + 1e-12, same_route=False) == "tie"


def test_route_of_reads_leg_order_and_empties_on_infeasible():
    assert ab.route_of(_feasible(10, 1.0, ["W1", "W2", "W1"])) == ("W1", "W2", "W1")
    assert ab.route_of(INFEASIBLE) == ()
    assert ab.route_of(None) == ()


def test_ab_verdict_counts_and_gates_on_loss_share():
    cases = ([dict(verdict="win")] * 6 + [dict(verdict="tie")] * 3
             + [dict(verdict="loss")])
    v = ab.ab_verdict(cases, max_loss_share=0.10)
    assert (v["cases"], v["wins"], v["losses"], v["ties"]) == (10, 6, 1, 3)
    assert v["adopt"] is True
    v2 = ab.ab_verdict(cases, max_loss_share=0.05)
    assert v2["adopt"] is False


def test_ab_verdict_fails_safe_on_no_evidence():
    assert ab.ab_verdict([], max_loss_share=0.5)["adopt"] is False


SNAPSHOT = [dict(waypoint_symbol="A1", system_symbol="X1-H", good_symbol="FUEL",
                 ask=100, bid=90, trade_volume=20, supply="HIGH", activity="WEAK",
                 observed_at_unix=0.0),
            dict(waypoint_symbol="A2", system_symbol="X1-H", good_symbol="FUEL",
                 ask=100, bid=90, trade_volume=20, supply="HIGH", activity="WEAK",
                 observed_at_unix=0.0)]
WPS = [dict(symbol="A1", system="X1-H", x=0, y=0),
       dict(symbol="A2", system="X1-H", x=5, y=0)]


def _case(monkeypatch, results_by_version, rescored):
    """Drive run_ab_case with a stubbed solver keyed on expected_model_version
    and a stubbed re-scorer, so the classification wiring is exercised alone."""
    def fake_solve(scoped, ship, cons, model, waypoints=None, objective=None):
        return results_by_version[cons["expected_model_version"]]

    monkeypatch.setattr(ab, "solve_tour", fake_solve)
    monkeypatch.setattr(ab, "rescore_route", lambda *a, **k: rescored)
    return ab.run_ab_case(SNAPSHOT, WPS, "X1-H", {"X1-H"}, 80, 1_000_000, 0,
                          {"m": "old"}, "2@old", {"m": "new"}, "2@new",
                          OBJECTIVE_RATE)


def test_run_ab_case_win_when_candidate_route_outvalues_incumbents(monkeypatch):
    case = _case(monkeypatch,
                 {"2@old": _feasible(500, 2000.0, ["A1", "A2"]),
                  "2@new": _feasible(400, 1500.0, ["A2", "A1"])},
                 rescored=dict(profit=300, cph=1200.0))
    assert case["verdict"] == "win"
    assert case["cand_value"] == 1500.0 and case["inc_value"] == 1200.0


def test_run_ab_case_loss_when_incumbent_route_outvalues_under_candidate_eval(monkeypatch):
    # The incumbent's own (inflated) promise never enters the verdict: its route
    # is re-valued under the candidate, and only that value can beat the pick.
    case = _case(monkeypatch,
                 {"2@old": _feasible(9_999_999, 9_999_999.0, ["A1", "A2"]),
                  "2@new": _feasible(400, 1500.0, ["A2", "A1"])},
                 rescored=dict(profit=300, cph=1800.0))
    assert case["verdict"] == "loss"
    assert case["inc_value"] == 1800.0


def test_run_ab_case_identical_routes_tie(monkeypatch):
    case = _case(monkeypatch,
                 {"2@old": _feasible(500, 2000.0, ["A1", "A2"]),
                  "2@new": _feasible(400, 1500.0, ["A1", "A2"])},
                 rescored=dict(profit=400, cph=1500.0))
    assert case["verdict"] == "tie"


def test_run_ab_case_candidate_refusal_of_a_losing_window_is_a_tie(monkeypatch):
    # The incumbent flies a route the candidate model prices at a loss; the
    # candidate plans nothing. Idling values 0 vs 0 — a tie, not a regression.
    case = _case(monkeypatch,
                 {"2@old": _feasible(800, 2500.0, ["A1", "A2"]),
                  "2@new": INFEASIBLE},
                 rescored=dict(profit=-120, cph=-400.0))
    assert case["verdict"] == "tie"
    assert case["cand_value"] == 0.0 and case["inc_value"] == 0.0


def test_run_ab_case_skips_windows_neither_model_can_plan(monkeypatch):
    assert _case(monkeypatch, {"2@old": INFEASIBLE, "2@new": INFEASIBLE},
                 rescored=None) is None

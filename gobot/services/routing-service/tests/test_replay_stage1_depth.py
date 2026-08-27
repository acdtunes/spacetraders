"""Stage-1 depth replay — verdict/wiring tests, no DB and no real solver.

Pins the plumbing the adoption gate rests on:
  * import purity  — the deployed-env mirror runs only under main()
  * solve_at       — ARMS the stage-1 price for the candidate and CLEARS it for the
                     incumbent, which is the only thing that makes the incumbent arm the
                     deployed solver rather than a variant of the candidate
  * crossings      — the far-away-deep-market check counts system changes from the hull's
                     own system, so a tour that leaves and returns counts both
  * classify       — win/loss/tie under the common evaluator, same-route tie
"""
import importlib
import os

import pytest

pytest.importorskip("sqlalchemy", reason="model-pipeline dep; see requirements-model.txt")

import replay_stage1_depth as rs
from utils import tour_solver


def test_import_does_not_mutate_solver_env(monkeypatch):
    for var in ("TOUR_SOLVER_OBJECTIVE", "TOUR_SOLVER_SEQUENCER",
                tour_solver.STAGE1_CALL_CREDITS_ENV_VAR):
        monkeypatch.delenv(var, raising=False)
    importlib.reload(rs)
    assert tour_solver.STAGE1_CALL_CREDITS_ENV_VAR not in os.environ
    assert "TOUR_SOLVER_SEQUENCER" not in os.environ


def test_solve_at_arms_the_price_for_the_candidate_and_clears_it_for_the_incumbent(monkeypatch):
    # A leaked price would make the INCUMBENT arm depth-charged too, and the A/B would
    # compare a variant against itself while reporting an honest-looking tie rate.
    monkeypatch.setenv(tour_solver.STAGE1_CALL_CREDITS_ENV_VAR, "999")
    seen = []

    def fake_solve_tour(*a, **kw):
        seen.append(os.environ.get(tour_solver.STAGE1_CALL_CREDITS_ENV_VAR))
        return dict(feasible=False, legs=[])

    monkeypatch.setattr(rs, "solve_tour", fake_solve_tour)
    rs.solve_at(0, [], {}, {}, {}, [])
    rs.solve_at(2500.0, [], {}, {}, {}, [])
    assert seen[0] is None
    assert float(seen[1]) == 2500.0


def test_crossings_counts_every_system_change_from_the_hull_own_system():
    plan = dict(legs=[dict(system_symbol="S1"), dict(system_symbol="S2"),
                      dict(system_symbol="S1")])
    assert rs.crossings(plan, "S1") == 2
    assert rs.crossings(dict(legs=[dict(system_symbol="S1")]), "S1") == 0
    assert rs.crossings(None, "S1") == 0


def test_classify_same_route_is_a_tie_regardless_of_value():
    assert rs.classify(900.0, 1000.0, same_route=True) == "tie"
    assert rs.classify(1100.0, 1000.0, same_route=False) == "win"
    assert rs.classify(900.0, 1000.0, same_route=False) == "loss"
    assert rs.classify(1000.0, 1000.0 + 1e-12, same_route=False) == "tie"

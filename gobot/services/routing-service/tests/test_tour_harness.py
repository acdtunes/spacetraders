"""sp-f1yk W1 — the reusable verification harness's OWN correctness tests (seam-free).

These pin the scaffold the WAVE-1 (W4) gated tests will lean on, WITHOUT depending on
sp-y05b (OR-Tools) or sp-z7ng (placement):
  * bruteforce_best  — exhaustive single-visit reference optimum (reuses the REAL
                       score_sequence + _sort_scored; differs from prod ONLY in the
                       stage-1 sequencer, so an OR-Tools `==` comparison is well-posed).
  * scenario generators — small single-visit instances (tight + random-fuzz) that are
                       well-formed and feasible.
  * latency rig      — time_solve + env-tunable ceiling/target/bench toggles.

The brute-force reference is proven correct here against HAND-KNOWN optima (not against
itself), so it is trustworthy as the W4 optimality oracle.
"""
import math

import pytest

from utils.tour_solver import solve_tour, OBJECTIVE_PROFIT, OBJECTIVE_RATE
from tests import tour_harness as th

EPS = 1e-6


# ------------------------------------------- generators produce valid scenarios
def test_single_good_two_market_is_wellformed_and_feasible():
    sc = th.single_good_two_market()
    # structural: every row carries the solver's required keys; systems within the cap.
    required = {"waypoint_symbol", "system_symbol", "good_symbol", "ask", "bid",
                "trade_volume", "supply", "activity", "observed_at_unix"}
    assert all(required <= set(row) for row in sc.snapshot)
    systems = {row["system_symbol"] for row in sc.snapshot}
    assert systems <= set(sc.cons["allowed_systems"])
    # feasible through the REAL prod solver (beam) — a valid, tradeable instance.
    out = solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model, waypoints=sc.waypoints)
    assert out["feasible"] and out["projected_profit"] > 0


@pytest.mark.parametrize("seed", [1, 2, 7, 42, 99])
def test_random_single_visit_instance_is_valid_and_feasible(seed):
    sc = th.random_single_visit_instance(seed)
    systems = {row["system_symbol"] for row in sc.snapshot}
    # single-visit space: at most the distinct-system cap the constraints declare.
    assert 1 <= len(systems) <= th.effective_cap(sc.cons)
    out = solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model, waypoints=sc.waypoints)
    # generator plants a guaranteed-profitable pair, so a feasible tour always exists.
    assert out["feasible"] and out["projected_profit"] > 0, seed


# ------------------------------------------- brute-force reference correctness
def test_bruteforce_finds_hand_known_single_visit_optimum():
    # HAND-KNOWN optimum: buy the good cheap at A (ask 100), sell dear at B (bid 200).
    # B->A and single-node tours earn nothing -> A->B is the unique global optimum.
    sc = th.single_good_two_market(source_wp="X1-S1-A", sink_wp="X1-S1-B",
                                   good="FUEL", ask=100, bid=200)
    value, best = th.bruteforce_best(sc.snapshot, sc.ship, sc.cons, sc.model,
                                     OBJECTIVE_PROFIT, waypoints=sc.waypoints)
    assert value > 0 and best is not None

    buys = [(l["waypoint_symbol"], t) for l in best["legs"] for t in l["trades"] if t["is_buy"]]
    sells = [(l["waypoint_symbol"], t) for l in best["legs"] for t in l["trades"] if not t["is_buy"]]
    assert [wp for wp, t in buys if t["good_symbol"] == "FUEL"] == ["X1-S1-A"]
    assert [wp for wp, t in sells if t["good_symbol"] == "FUEL"] == ["X1-S1-B"]
    # the reported objective value IS the value of the sequence it chose (self-consistent).
    assert math.isclose(value, best["profit"], rel_tol=EPS)


def test_bruteforce_beats_a_strictly_worse_ordering():
    # A directional-only instance: buying is possible only at the source, selling only at
    # the sink, so the reversed order (sink-first) can realize NOTHING. The reference must
    # not merely return the first enumerated sequence — it must find the profitable one.
    sc = th.single_good_two_market(ask=100, bid=200)
    value, _ = th.bruteforce_best(sc.snapshot, sc.ship, sc.cons, sc.model,
                                  OBJECTIVE_PROFIT, waypoints=sc.waypoints)
    assert value > 0


def test_bruteforce_matches_prod_beam_when_revisits_impossible():
    # At max_hops=2 no revisit sequence exists, so the exhaustive single-visit optimum
    # EQUALS the prod beam's objective — a cross-check of the reference against the real
    # planner on an instance where they provably must agree (no sp-y05b needed).
    # (At max_hops>=4 the beam legitimately BEATS single-visit via an A->B->A->B revisit
    # that unlocks a 2nd A-cap tranche — the RED#5b behavioral difference, asserted in W4.)
    sc = th.single_good_two_market(ask=100, bid=200, max_hops=2)
    brute_value, _ = th.bruteforce_best(sc.snapshot, sc.ship, sc.cons, sc.model,
                                        OBJECTIVE_PROFIT, waypoints=sc.waypoints)
    out = solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model, waypoints=sc.waypoints,
                     objective=OBJECTIVE_PROFIT)
    assert math.isclose(brute_value, out["projected_profit"], rel_tol=EPS)


def test_bruteforce_reports_rate_objective_value():
    # under OBJECTIVE_RATE the reference reports cph (what _sort_scored optimizes), so the
    # W4 optimality `==` is well-posed for BOTH objectives.
    sc = th.single_good_two_market(ask=100, bid=200)
    value, best = th.bruteforce_best(sc.snapshot, sc.ship, sc.cons, sc.model,
                                     OBJECTIVE_RATE, waypoints=sc.waypoints)
    assert value > 0 and math.isclose(value, best["cph"], rel_tol=EPS)


# ------------------------------------------- latency benchmark rig
def test_time_solve_returns_result_and_nonnegative_elapsed():
    result, elapsed = th.time_solve(lambda: sum(range(1000)))
    assert result == sum(range(1000))
    assert elapsed >= 0.0


def test_latency_thresholds_default_and_env_override(monkeypatch):
    for var in ("LATENCY_HARD_CEILING_SECONDS", "LATENCY_SOFT_TARGET_SECONDS",
                "RUN_LATENCY_BENCH"):
        monkeypatch.delenv(var, raising=False)
    # bead (d) / spec-Risk contract ceiling = routing.timeout.tsp = 60s; soft target 15s.
    assert th.latency_hard_ceiling_seconds() == 60.0
    assert th.latency_soft_target_seconds() == 15.0
    assert th.latency_bench_enabled() is False

    monkeypatch.setenv("LATENCY_HARD_CEILING_SECONDS", "42")
    monkeypatch.setenv("LATENCY_SOFT_TARGET_SECONDS", "7.5")
    monkeypatch.setenv("RUN_LATENCY_BENCH", "1")
    assert th.latency_hard_ceiling_seconds() == 42.0
    assert th.latency_soft_target_seconds() == 7.5
    assert th.latency_bench_enabled() is True

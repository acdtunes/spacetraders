"""sp-f1yk Deliverable 4 — OR-Tools cap-6 solve latency envelope.

WAVE-1 / W4 — GATED on sp-y05b (OR-Tools sequencer + time-cap / beam-fallback knobs). The
syaz cap-knob (max_tour_systems) is already live. SCAFFOLD ONLY in W1: the latency RIG
(time_solve + env-tunable ceiling/target/bench) is BUILT and proven in
tests/test_tour_harness.py; the OR-Tools-active wall-clock ASSERTIONS below are skip-marked
TODO(W4).

SEAM CHECK before removing the module skip (defer if it fails):
    grep -n "AddDisjunction|sequencer|time_cap|beam.fallback" utils/tour_solver.py

Layered assertion design (resolves 'perf gate not asserted within envelope'): a lone
`< 60s` is 12-30x looser than the 2-5s anytime envelope. Assert BOTH the HARD contract
ceiling (routing.timeout.tsp = 60s) AND a tight SOFT regression target (~15s), both
env-overridable to bound CI flake. The 15s default is recalibrated in W4 once y05b's real
cap-6 solve times on ~30-80-node instances are measured.
"""
import pytest

from utils.tour_solver import solve_tour, OBJECTIVE_RATE
from tests import tour_harness as th

pytestmark = pytest.mark.skip(reason="TODO(W4): gated on sp-y05b OR-Tools sequencer + "
                                     "time-cap / beam-fallback knobs. Rig ready in "
                                     "tests/tour_harness.py (time_solve, latency_* env).")


def test_cap6_solve_within_budget():
    """RED#9. One realistic pruned (~30-80 node) cap-6 solve, wall-clocked via the rig, TWO
    assertions: (i) HARD CEILING < latency_hard_ceiling_seconds() (60s = routing.timeout.tsp
    contract); (ii) SOFT TARGET < latency_soft_target_seconds() (~15s, ~4-30x tighter). A
    20x anytime-cap overrun FAILS (ii)."""
    sc = th.random_single_visit_instance(seed=1, n_markets=4)
    sc.cons["max_tour_systems"] = 6
    # W4 seam (force OR-Tools ON per y05b's final toggle):
    _out, elapsed = th.time_solve(
        lambda: solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model,
                           waypoints=sc.waypoints, objective=OBJECTIVE_RATE,
                           sequencer="ortools"))
    assert elapsed < th.latency_hard_ceiling_seconds()   # (i) contract ceiling
    assert elapsed < th.latency_soft_target_seconds()    # (ii) tight regression guard


def test_beam_fallback_on_timeout():
    """RED#10. Force the OR-Tools time cap ~= 0 (y05b's knob) on a large instance; assert
    (a) the plan is still feasible (fell back to beam) AND (b) wall-clock <
    sequencer_time_cap + small_margin. A deterministic branch -> a tight, meaningful guard."""
    sc = th.random_single_visit_instance(seed=2, n_markets=4)
    sc.cons["max_tour_systems"] = 6
    # W4 seam: force sequencer_time_cap ~= 0 so OR-Tools yields to the beam fallback.
    out, elapsed = th.time_solve(
        lambda: solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model,
                           waypoints=sc.waypoints, objective=OBJECTIVE_RATE,
                           sequencer="ortools", sequencer_time_cap=0.0))
    assert out["feasible"]
    assert elapsed < 0.0 + 5.0   # time_cap (~0) + small margin


def test_cap6_vs_cap2_benchmark():
    """RED#11 (env-gated, informational). RUN_LATENCY_BENCH=1 prints cap-2 vs cap-6 median;
    asserts only a LOOSE ceiling (never a tight ratio) — avoids CI flake."""
    if not th.latency_bench_enabled():
        pytest.skip("informational benchmark; set RUN_LATENCY_BENCH=1 to run")
    # W4: median cap-2 vs cap-6 solve times; assert both < latency_hard_ceiling_seconds().
    pytest.fail("W4: implement cap-2 vs cap-6 median benchmark once y05b lands")

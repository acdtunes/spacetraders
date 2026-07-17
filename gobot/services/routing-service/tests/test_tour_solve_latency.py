"""sp-f1yk Deliverable 4 — OR-Tools cap-6 solve latency envelope.

WAVE-1 / W4 — sp-y05b's OR-Tools sequencer (+ its budget / beam-fallback seams) is MERGED; the
syaz cap-knob (max_tour_systems) is live. The latency RIG (time_solve + env-tunable
ceiling/target/bench, proven in tests/test_tour_harness.py) now wall-clocks real cap-6 solves.

SEAM CHECK (verified on main before un-skipping):
    grep -nE "def ortools_sequences|SEQUENCER_ORTOOLS|TOUR_SOLVER_ORTOOLS_BUDGET_SECONDS|beam only" utils/tour_solver.py

shows y05b's sequencer, the `sequencer="ortools"` toggle, the GLOBAL wall budget
(TOUR_SOLVER_ORTOOLS_BUDGET_SECONDS, clamp [2,5]), and the "beam only" fallback log.

DELIVERED-API RECONCILIATION (real W4 finding): the W1 scaffold assumed a per-call
`sequencer_time_cap` kwarg to force the beam fallback. y05b did NOT ship that kwarg — the
finalized force-yield seam is an empty/raising ortools_sequences (its own T3/T4 fallback tests
patch it), and the wall budget is the env clamp [2,5]s. RED#10 is re-wired to that real seam.

Layered assertion design (resolves 'perf gate not asserted within envelope'): a lone
`< 60s` is 12-30x looser than the 2-5s anytime envelope. Assert BOTH the HARD contract
ceiling (routing.timeout.tsp = 60s) AND a tight SOFT regression target (~15s), both
env-overridable to bound CI flake.
"""
import pytest

from utils import tour_solver
from utils.tour_solver import solve_tour, OBJECTIVE_RATE
from tests import tour_harness as th


def test_cap6_solve_within_budget():
    """RED#9. One realistic pruned (~30-80 node) cap-6 solve, wall-clocked via the rig, TWO
    assertions: (i) HARD CEILING < latency_hard_ceiling_seconds() (60s = routing.timeout.tsp
    contract); (ii) SOFT TARGET < latency_soft_target_seconds() (~15s, ~4-30x tighter). A
    20x anytime-cap overrun FAILS (ii)."""
    pytest.importorskip("ortools")  # needs the native sequencer to time a real cap-6 solve
    sc = th.random_single_visit_instance(seed=1, n_markets=4)
    sc.cons["max_tour_systems"] = 6
    _out, elapsed = th.time_solve(
        lambda: solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model,
                           waypoints=sc.waypoints, objective=OBJECTIVE_RATE,
                           sequencer="ortools"))
    assert elapsed < th.latency_hard_ceiling_seconds()   # (i) contract ceiling
    assert elapsed < th.latency_soft_target_seconds()    # (ii) tight regression guard


def test_beam_fallback_on_timeout(monkeypatch):
    """RED#10. Force the OR-Tools stage-1 to YIELD NOTHING and assert (a) the plan is still
    feasible (the beam carried it via the UNION safety net) AND (b) the wall-clock is bounded
    (no OR-Tools work happened) — a deterministic branch → a tight, meaningful guard.

    y05b did not ship a per-call sequencer_time_cap kwarg (the W1 guess); its finalized
    force-yield seam is an empty/raising ortools_sequences (the exact seam its own T3/T4
    fallback tests patch), which is the real analog of a time cap that expires before the first
    solution. Patching it to [] drives the 'ortools produced no candidates; beam only' branch."""
    sc = th.random_single_visit_instance(seed=2, n_markets=4)
    sc.cons["max_tour_systems"] = 6
    monkeypatch.setattr(tour_solver, "ortools_sequences", lambda *a, **k: [])
    monkeypatch.setattr(tour_solver, "_logged_sequencer", set())  # reset the once-per-process log
    out, elapsed = th.time_solve(
        lambda: solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model,
                           waypoints=sc.waypoints, objective=OBJECTIVE_RATE,
                           sequencer="ortools"))
    assert out["feasible"]                             # beam carried the solve
    assert elapsed < 5.0                               # no OR-Tools work → a small, tight margin


def test_cap6_vs_cap2_benchmark():
    """RED#11 (env-gated, informational). RUN_LATENCY_BENCH=1 prints cap-2 vs cap-6 median;
    asserts only a LOOSE ceiling (never a tight ratio) — avoids CI flake."""
    if not th.latency_bench_enabled():
        pytest.skip("informational benchmark; set RUN_LATENCY_BENCH=1 to run")
    pytest.importorskip("ortools")
    import statistics

    def median_solve(cap):
        times = []
        for seed in range(1, 6):
            scenario = th.random_single_visit_instance(seed=seed, n_markets=4)
            scenario.cons["max_tour_systems"] = cap
            _out, elapsed = th.time_solve(
                lambda s=scenario: solve_tour(s.snapshot, s.ship, s.cons, s.model,
                                              waypoints=s.waypoints, objective=OBJECTIVE_RATE,
                                              sequencer="ortools"))
            times.append(elapsed)
        return statistics.median(times)

    cap2_median = median_solve(2)
    cap6_median = median_solve(6)
    print(f"\n[latency-bench] cap-2 median={cap2_median:.3f}s  cap-6 median={cap6_median:.3f}s  "
          f"hard-ceiling={th.latency_hard_ceiling_seconds():.0f}s")
    assert cap2_median < th.latency_hard_ceiling_seconds()
    assert cap6_median < th.latency_hard_ceiling_seconds()

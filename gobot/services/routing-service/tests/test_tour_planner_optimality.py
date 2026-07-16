"""sp-f1yk Deliverable 2 — OR-Tools optimality vs the brute-force reference.

WAVE-1 / W4 — GATED on sp-y05b (OR-Tools sequencer). SCAFFOLD ONLY in W1: the reusable
brute-force oracle + generators are BUILT and proven in tests/test_tour_harness.py; the
OR-Tools-vs-brute-force ASSERTIONS below are skip-marked TODO(W4) and activate once y05b
merges its sequencer toggle onto main.

SEAM CHECK before removing the module skip (run first, defer if it fails):
    grep -n "AddDisjunction\\|RoutingIndexManager\\|sequencer" utils/tour_solver.py
must show y05b's OR-Tools sequencer + a force-ON toggle / time-cap / emission mode.

The exact force-ON mechanism (a `sequencer=` kwarg vs a module/env toggle) is y05b's to
finalize per §7 y05b contract — W4 wires solve_tour's call accordingly. The brute-force
reference already enumerates the SAME single-visit space OR-Tools' AddDisjunction defines,
so `==` is well-posed.
"""
import math

import pytest

from utils.tour_solver import solve_tour, OBJECTIVE_PROFIT, OBJECTIVE_RATE
from tests import tour_harness as th

EPS = 1e-6

pytestmark = pytest.mark.skip(reason="TODO(W4): gated on sp-y05b OR-Tools sequencer "
                                     "(not yet on main). Oracle ready in tests/tour_harness.py.")


def test_ortools_matches_bruteforce_single_visit_optimum():
    """RED#4 (headline). CONSTRUCTED-tight instance (single good/market, tranche depth >=
    hold via max_hops=2 so the packing prize bound is exact). Assert OR-Tools' objective
    == the exhaustive single-visit optimum. Discriminating power: use y05b's SINGLE-EMIT
    mode so stage-2 scores only OR-Tools' one chosen sequence (no best-of-top-N rescue).
    Strict `==` if y05b confirms prize-exactness (§7 y05b-e), else `<= EPSILON`."""
    sc = th.single_good_two_market(ask=100, bid=200, max_hops=2)
    brute_value, _ = th.bruteforce_best(sc.snapshot, sc.ship, sc.cons, sc.model,
                                        OBJECTIVE_PROFIT, waypoints=sc.waypoints)
    # W4 seam (adjust to y05b's final force-ON toggle per §7):
    out = solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model, waypoints=sc.waypoints,
                     objective=OBJECTIVE_PROFIT, sequencer="ortools")
    assert math.isclose(out["projected_profit"], brute_value, rel_tol=EPS)


@pytest.mark.parametrize("seed", [1, 2, 7, 42, 99])
def test_ortools_reaches_single_visit_optimum_fuzz(seed):
    """RED#5 (replaces the FALSE domination invariant). Random small single-visit
    instances: OR-Tools objective == bruteforce single-visit optimum (or >= brute - EPS if
    the anytime search needs slack). DROP any 'OR-Tools >= beam' claim — beam searches a
    revisit-inclusive space. LOG (never assert) the OR-Tools - beam delta as a metric."""
    sc = th.random_single_visit_instance(seed)
    brute_value, _ = th.bruteforce_best(sc.snapshot, sc.ship, sc.cons, sc.model,
                                        OBJECTIVE_PROFIT, waypoints=sc.waypoints)
    out = solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model, waypoints=sc.waypoints,
                     objective=OBJECTIVE_PROFIT, sequencer="ortools")
    assert out["projected_profit"] >= brute_value - EPS


def test_beam_may_exceed_ortools_on_revisit_instance():
    """RED#5b (documents WHY the domination invariant was false). At max_hops>=4 the beam
    revisits (A->B->A->B) to unlock a 2nd A-cap tranche the single-visit OR-Tools space
    cannot reach — so beam objective > OR-Tools objective. Pins the known behavioral
    difference so it is never mistaken for a regression. (The premise is already validated
    seam-free in tests/test_tour_harness.py: prod beam 6800 > single-visit brute 4000.)"""
    sc = th.single_good_two_market(ask=100, bid=200, max_hops=4)
    beam_out = solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model, waypoints=sc.waypoints,
                          objective=OBJECTIVE_PROFIT)  # default beam sequencer
    ortools_out = solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model, waypoints=sc.waypoints,
                             objective=OBJECTIVE_PROFIT, sequencer="ortools")
    assert beam_out["projected_profit"] > ortools_out["projected_profit"]

"""sp-f1yk Deliverable 3 — closed-tour (returns-to-anchor) mode.

WAVE-1 / W4 — GATED on sp-im74 (closed/anchor_system request fields) + sp-y05b (OR-Tools).
SCAFFOLD ONLY in W1: the harness is ready; the closure ASSERTIONS below are skip-marked
TODO(W4).

SEAM CHECK before removing the module skip (defer if it fails):
    grep -nE "closed|anchor_system" pkg/proto/routing/routing.proto   # im74 request fields
    grep -n  "AddDisjunction|sequencer" utils/tour_solver.py          # y05b sequencer

VERIFIED HAZARD (resolve before finalizing the assert line): score_sequence PRUNES
no-trade hops from `legs` (tour_solver.py ~:624, `if not leg_trades[idx]: continue`). If
im74 pins the anchor as an OR-Tools end-node and the return home carries no trade, the
return hop is pruned and legs[-1].system != anchor — so a NAIVE "terminal TRADE leg ==
anchor" assertion FAILS on a CORRECT closed tour. Assert against a ROUTING property, and
pin im74's closure representation FIRST (§7 im74 contract):
  * Contract A (end-node pinned, possibly no-trade): assert the chosen node SEQUENCE's end
    system == anchor (needs im74 to expose the sequence / a closure marker); else assert
    the weaker-but-correct `anchor_system in {leg.system for leg in legs}`.
  * Contract B (appended no-trade return hop): assert the appended anchor hop exists in
    `legs` with empty trades.
The crafter finalizes RED#6's assert line only after §7 im74 is pinned.
"""
import pytest

from utils.tour_solver import solve_tour, OBJECTIVE_PROFIT
from tests import tour_harness as th

pytestmark = pytest.mark.skip(reason="TODO(W4): gated on sp-im74 (closed/anchor_system) + "
                                     "sp-y05b (OR-Tools). Harness ready in tests/tour_harness.py.")


def test_closed_tour_ends_at_anchor():
    """RED#6. closed=True; floating anchor == ship.current_system (or explicit
    anchor_system). Assert per the CONFIRMED im74 contract — default: the chosen node
    sequence's end system == anchor; documented fallback: anchor in visited systems."""
    sc = th.single_good_two_market(ask=100, bid=200, max_hops=2)
    anchor = sc.ship["current_system"]
    # W4 seam (adjust to im74's final closed/anchor request fields):
    out = solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model, waypoints=sc.waypoints,
                     objective=OBJECTIVE_PROFIT, closed=True, anchor_system=anchor)
    visited = {leg["system"] for leg in out["legs"] if "system" in leg}
    assert anchor in visited  # weakest-correct form; strengthen per §7 im74


def test_open_tour_not_pinned_to_anchor():
    """RED#7 (contrast). An instance whose OPEN optimum ends OFF-anchor must NOT be
    force-pinned home — guards a closure regression that would tie every tour to the
    anchor. Construct a two-system instance where the richest sink is away from home."""
    sc = th.single_good_two_market(ask=100, bid=200, max_hops=2)
    out = solve_tour(sc.snapshot, sc.ship, sc.cons, sc.model, waypoints=sc.waypoints,
                     objective=OBJECTIVE_PROFIT, closed=False)
    assert out["feasible"]
    # W4: assert the open tour's end is the profitable off-anchor sink, not forced home.


def test_closed_matches_bruteforce_circuit():
    """RED#8. Closed OR-Tools objective == the exhaustive single-visit optimum RESTRICTED
    to circuits whose last node's system == anchor (a brute-force over closed circuits), on
    a tight instance. Reuses tour_harness.bruteforce_best filtered to anchor-terminating
    sequences (a thin W4 wrapper)."""
    sc = th.single_good_two_market(ask=100, bid=200, max_hops=2)
    _ = th.bruteforce_best(sc.snapshot, sc.ship, sc.cons, sc.model,
                           OBJECTIVE_PROFIT, waypoints=sc.waypoints)
    # W4: brute-force over anchor-terminating circuits; assert == closed OR-Tools objective.
    pytest.fail("W4: implement closed-circuit brute-force comparison once im74+y05b land")

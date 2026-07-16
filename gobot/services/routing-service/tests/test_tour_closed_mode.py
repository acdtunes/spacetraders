"""sp-f1yk Deliverable 3 — FINALIZED by sp-im74: closed-tour (returns-to-anchor) mode.

im74's PINNED closure contract is the W1 scaffold's Contract B: closure rides the
CONSTRAINTS DICT (`closed` / `anchor_system` request fields; solve_tour resolves the
anchor once per solve into the `_anchor_return_wp`/`_anchor_system` stash), and
score_sequence APPENDS the priced no-trade return hop when the kept tour doesn't
already end at the anchor. That settles the W1 hazard (no-trade hops pruned from
`legs`): the return hop is appended AFTER pruning, so it is the only no-trade leg a
plan can carry and `legs[-1]` IS the anchor on every closed plan.
"""
import pytest

from utils.tour_solver import solve_tour, OBJECTIVE_PROFIT
from tests import tour_harness as th


def test_closed_tour_ends_at_anchor():
    """RED#6: closed=True on the tight A->B instance — the emitted tour must END at
    the floating anchor (the ship's waypoint, source A) via an appended no-trade
    return hop (Contract B)."""
    sc = th.single_good_two_market(ask=100, bid=200, max_hops=2)
    out = solve_tour(sc.snapshot, sc.ship, dict(sc.cons, closed=True), sc.model,
                     waypoints=sc.waypoints, objective=OBJECTIVE_PROFIT)
    assert out["feasible"]
    last = out["legs"][-1]
    assert last["waypoint_symbol"] == sc.ship["current_waypoint"]
    assert last["system_symbol"] == sc.ship["current_system"]
    assert last["trades"] == []
    assert last["travel_seconds_from_prev"] > 0


def test_open_tour_not_pinned_to_anchor():
    """RED#7 (contrast): the OPEN optimum ends at the off-anchor sink and closure must
    not leak into the default — guards a regression tying every tour home."""
    sc = th.single_good_two_market(ask=100, bid=200, max_hops=2)
    out = solve_tour(sc.snapshot, sc.ship, dict(sc.cons, closed=False), sc.model,
                     waypoints=sc.waypoints, objective=OBJECTIVE_PROFIT)
    assert out["feasible"]
    assert out["legs"][-1]["waypoint_symbol"] == "X1-S1-B"   # the sink, NOT the anchor


@pytest.mark.parametrize("sequencer", [None, "ortools"])
def test_closed_matches_bruteforce_circuit(sequencer):
    """RED#8: the closed pipeline (stage-1 sequencer x closure epilogue x selection)
    lands EXACTLY the exhaustive single-visit closed-circuit optimum, under both
    sequencers, on a discriminating instance: two profit-equal sinks whose returns
    differ, so an open solver — or a closure that mispriced the return — picks B
    while the true closed circuit sells at C and ends at the anchor H."""
    if sequencer == "ortools":
        pytest.importorskip("ortools")
    snapshot = [th._row("A", "X1-S1", "FUEL", ask=100, bid=0),
                th._row("B", "X1-S1", "FUEL", ask=0, bid=200),
                th._row("C", "X1-S1", "FUEL", ask=0, bid=200)]
    waypoints = [dict(symbol="H", system="X1-S1", x=0, y=0),
                 dict(symbol="A", system="X1-S1", x=50, y=0),
                 dict(symbol="B", system="X1-S1", x=100, y=0),
                 dict(symbol="C", system="X1-S1", x=50, y=50)]
    ship = th._ship("H", "X1-S1", hold=40)
    base = th._cons(["X1-S1"], max_hops=2)  # max_hops=2: brute + beam spaces coincide

    # The reference: harness brute-force scored with the SAME pre-resolved closure
    # stash solve_tour computes for a floating anchor (the ship's waypoint H).
    cons_brute = dict(base, closed=True, _anchor_return_wp="H",
                      _anchor_system="X1-S1")
    value, best = th.bruteforce_best(snapshot, ship, cons_brute, th.MODEL,
                                     OBJECTIVE_PROFIT, waypoints=waypoints)
    assert best is not None
    assert best["legs"][-1]["waypoint_symbol"] == "H"   # the reference circuit closes
    sold = [l["waypoint_symbol"] for l in best["legs"]
            for t in l["trades"] if not t["is_buy"]]
    assert sold == ["C"]                                 # near-return sink wins the tie

    out = solve_tour(snapshot, ship, dict(base, closed=True), th.MODEL,
                     waypoints=waypoints, objective=OBJECTIVE_PROFIT,
                     sequencer=sequencer)
    assert out["feasible"]
    assert out["projected_profit"] == value == best["profit"]

    def sig(legs):
        return [(l["waypoint_symbol"],
                 [(t["good_symbol"], t["units"], t["is_buy"],
                   t["expected_unit_price"]) for t in l["trades"]]) for l in legs]

    assert sig(out["legs"]) == sig(best["legs"])

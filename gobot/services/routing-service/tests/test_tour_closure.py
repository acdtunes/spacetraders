# gobot/services/routing-service/tests/test_tour_closure.py
"""sp-im74 — closed-tour mode mechanics (epic sp-fguo, wave W2).

Closure contract under test (brief §1.1/§1.2):
  * Request fields `closed` / `anchor_system` ride the constraints dict; solve_tour
    resolves the anchor ONCE per solve into the `_anchor_return_wp`/`_anchor_system`
    stash (underscore convention, like `_travel_fn`).
  * score_sequence appends the priced NO-TRADE return hop (travel + dwell charged,
    profit untouched) when the kept tour doesn't already end at the anchor — placed in
    stage 2 so beam, ortools and the fallback are all closure-correct.
  * ortools additionally prices the return on the virtual end arc (in-model steering).
  * DEFAULT-SAFE: closed unset/False + anchor_system "" change NOTHING — byte-identical
    open results on both sequencer paths.
"""
import pytest

from utils.tour_solver import (
    solve_tour, score_sequence, ortools_sequences,
    _build_markets, _make_travel_fn,
)

MODEL = {"fit_version": 1, "era": "e",
         "impact": {"LIMITED|WEAK": {"sell_decay_per_step": 0.9,
                                     "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}
FRESH = 9_999_999_999


def snap(wp, sys_, good, ask, bid, tv=40):
    return dict(waypoint_symbol=wp, system_symbol=sys_, good_symbol=good,
                ask=ask, bid=bid, trade_volume=tv, supply="LIMITED",
                activity="WEAK", observed_at_unix=FRESH)


def wpt(sym, sys_, x, y):
    return dict(symbol=sym, system_symbol=sys_, x=x, y=y)


def ship(at="A", system="S1", hold=80, cargo=None):
    return dict(ship_symbol="HULL-1", current_waypoint=at, current_system=system,
                hold_capacity=hold, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=cargo or [])


def cons(**over):
    base = dict(max_hops=4, max_spend=1_000_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    base.update(over)
    return base


def offanchor():
    """Source A (ship's rest) and one far sink B: the OPEN optimum ends OFF-anchor."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0),
                snap("B", "S1", "G", ask=0, bid=200)]
    wps = [wpt("A", "S1", 0, 0), wpt("B", "S1", 100, 0)]
    return snapshot, wps


def twosys():
    """S1 lane (rich, wins on profit) + two S2 sink markets for anchor resolution.
    S2 rows deliberately listed D-before-C: resolution must SORT, not take row order."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0),
                snap("B", "S1", "G", ask=0, bid=200),
                snap("S2-D", "S2", "G", ask=0, bid=120),
                snap("S2-C", "S2", "G", ask=0, bid=110)]
    wps = [wpt("A", "S1", 0, 0), wpt("B", "S1", 100, 0),
           wpt("S2-C", "S2", 500, 0), wpt("S2-D", "S2", 600, 0)]
    return snapshot, wps


# --------------------------------------------------------------- default safety (A0)
@pytest.mark.parametrize("sequencer", [None, "ortools"])
def test_closed_false_and_absent_are_byte_identical_open(sequencer):
    # The dormancy pin: closed=False + anchor_system="" (the proto3 zero-values an
    # old caller sends) must produce a result deep-equal to the field-absent solve,
    # on BOTH sequencer paths — nothing arms until a caller opts in.
    if sequencer == "ortools":
        pytest.importorskip("ortools")
    snapshot, wps = offanchor()
    base = solve_tour(snapshot, ship(), cons(), MODEL, waypoints=wps,
                      sequencer=sequencer)
    explicit = solve_tour(snapshot, ship(), cons(closed=False, anchor_system=""),
                          MODEL, waypoints=wps, sequencer=sequencer)
    assert base["feasible"]
    assert base["legs"][-1]["waypoint_symbol"] == "B"   # open genuinely ends off-anchor
    assert explicit == base


# ------------------------------------------------------------ closure epilogue (E1-E4)
def test_closed_floating_appends_priced_return_leg():
    # E3/E4 arithmetic: same trades as open (unique-max-profit winner unchanged),
    # plus ONE appended no-trade hop back to the floating anchor (ship's waypoint A)
    # whose travel + dwell are charged into time/cph while profit stays untouched.
    snapshot, wps = offanchor()
    open_r = solve_tour(snapshot, ship(), cons(), MODEL, waypoints=wps)
    closed_r = solve_tour(snapshot, ship(), cons(closed=True), MODEL, waypoints=wps)
    assert closed_r["feasible"]

    def strip(legs):
        return [(l["waypoint_symbol"],
                 [tuple(sorted(t.items())) for t in l["trades"]]) for l in legs]

    assert strip(closed_r["legs"][:-1]) == strip(open_r["legs"])
    ret = closed_r["legs"][-1]
    assert (ret["waypoint_symbol"], ret["system_symbol"]) == ("A", "S1")
    assert ret["trades"] == [] and ret["projected_leg_profit"] == 0
    assert ret["travel_seconds_from_prev"] == int(100 * 31 / 30)  # CRUISE B->A, 103s

    assert closed_r["projected_profit"] == open_r["projected_profit"]
    open_secs = (open_r["projected_profit"]
                 / open_r["projected_credits_per_hour"] * 3600.0)
    closed_secs = (closed_r["projected_profit"]
                   / closed_r["projected_credits_per_hour"] * 3600.0)
    # E3: the return charges its hop PLUS the per-leg dwell.
    assert round(closed_secs - open_secs) == int(100 * 31 / 30) + 60
    assert (closed_r["projected_credits_per_hour"]
            < open_r["projected_credits_per_hour"])


def test_closed_is_noop_when_tour_already_ends_at_anchor():
    # E2: ship rests at the SINK — the open optimum (buy A, sell B) already ends at
    # the floating anchor B, so closure appends nothing: deep-equal results.
    snapshot, wps = offanchor()
    at_sink = ship(at="B")
    open_r = solve_tour(snapshot, at_sink, cons(), MODEL, waypoints=wps)
    closed_r = solve_tour(snapshot, at_sink, cons(closed=True), MODEL, waypoints=wps)
    assert open_r["feasible"]
    assert open_r["legs"][-1]["waypoint_symbol"] == "B"
    assert closed_r == open_r


def test_closure_epilogue_skips_no_trade_candidates():
    # E1: a candidate that allocates NOTHING (legs=[]) must score seconds=0 without
    # raising under closed constraints. One such candidate exists in almost every
    # real pool (a bare sink seed like ("B",)); an unguarded epilogue would IndexError
    # and the handler's blanket except would fail the WHOLE solve as internal_error.
    # Keeping seconds at 0 also preserves the _sort_scored zero-time degenerate pin.
    snapshot, wps = offanchor()
    markets = _build_markets(snapshot)
    c = cons(closed=True, _anchor_return_wp="A", _anchor_system="S1")
    travel = _make_travel_fn(c, markets, ship(), wps)
    result = score_sequence(("B",), markets, ship(), c, MODEL, travel)
    assert result["legs"] == []
    assert result["seconds"] == 0 and result["profit"] == 0


# ------------------------------------------------------------ anchor resolution (R3-R6)
def test_explicit_same_system_anchor_equals_floating():
    # R3: anchor_system == the ship's current system is FLOATING semantics — return
    # to the ship's waypoint, not to some lexicographic market of the home system.
    snapshot, wps = offanchor()
    floating = solve_tour(snapshot, ship(), cons(closed=True), MODEL, waypoints=wps)
    explicit = solve_tour(snapshot, ship(),
                          cons(closed=True, anchor_system="S1"), MODEL, waypoints=wps)
    assert floating["feasible"]
    assert floating["legs"][-1]["waypoint_symbol"] == "A"
    assert explicit == floating


def test_explicit_foreign_anchor_returns_to_lexicographically_first_market():
    # R6: an explicit in-scope foreign anchor resolves to that system's
    # lexicographically-FIRST fresh market waypoint (S2-C < S2-D, despite row order).
    snapshot, wps = twosys()
    r = solve_tour(snapshot, ship(),
                   cons(allowed_systems=["S1", "S2"], closed=True,
                        anchor_system="S2"), MODEL, waypoints=wps)
    assert r["feasible"]
    last = r["legs"][-1]
    assert (last["waypoint_symbol"], last["system_symbol"]) == ("S2-C", "S2")
    assert last["trades"] == []


@pytest.mark.parametrize("anchor,keep_s2_rows,reason", [
    ("S9", True, "anchor_system_not_in_scope"),         # R4: outside allowed_systems
    ("S2", False, "anchor_system_no_return_waypoint"),  # R5: in scope, no fresh rows
])
def test_unresolvable_anchor_is_infeasible(anchor, keep_s2_rows, reason):
    snapshot, wps = twosys()
    if not keep_s2_rows:
        snapshot = [r for r in snapshot if r["system_symbol"] != "S2"]
    r = solve_tour(snapshot, ship(),
                   cons(allowed_systems=["S1", "S2"], closed=True,
                        anchor_system=anchor), MODEL, waypoints=wps)
    assert not r["feasible"]
    assert r["infeasible_reason"] == reason
    assert r["legs"] == []


# --------------------------------------------------------------- selection honesty
def test_rate_objective_honestly_charges_the_return_leg():
    # Two lanes from source A: far/rich B (bid 177, x=600) vs near/thin C (bid 120,
    # x=1). Open rate: B's lane clocks ~24.6k/hr vs C's ~21.8k/hr — B wins. Closed
    # (floating anchor A) charges the 620s return: B drops to ~12.8k/hr, C only to
    # ~14.5k/hr — the honest rate ordering flips to C. Every market also bids for
    # the held token L (A strictly best at 12) so EVERY candidate carries a real
    # sell leg => seconds>0 => the _sort_scored zero-time degrade can never silently
    # knock this fixture back to profit ordering.
    snapshot = [snap("A", "S1", "G", ask=100, bid=0),
                snap("B", "S1", "G", ask=0, bid=177),
                snap("C", "S1", "G", ask=0, bid=120),
                snap("A", "S1", "L", ask=0, bid=12),
                snap("B", "S1", "L", ask=0, bid=10),
                snap("C", "S1", "L", ask=0, bid=10)]
    wps = [wpt("A", "S1", 0, 0), wpt("B", "S1", 600, 0), wpt("C", "S1", 1, 0)]
    carrier = ship(hold=80, cargo=[dict(good_symbol="L", units=1)])

    open_r = solve_tour(snapshot, carrier, cons(max_hops=2), MODEL,
                        waypoints=wps, objective="rate")
    closed_r = solve_tour(snapshot, carrier, cons(max_hops=2, closed=True), MODEL,
                          waypoints=wps, objective="rate")
    open_stops = {l["waypoint_symbol"] for l in open_r["legs"]}
    closed_stops = {l["waypoint_symbol"] for l in closed_r["legs"]}
    assert "B" in open_stops                    # open rate: the far/rich lane wins
    assert closed_r["feasible"]
    assert "B" not in closed_stops              # closed rate: its return re-ranks it out
    assert "C" in closed_stops
    assert closed_r["legs"][-1]["waypoint_symbol"] == "A"   # and the tour ends home


def test_profit_tie_breaks_toward_the_cheaper_return():
    # Under the default profit objective closure NEVER changes profit, so the only
    # selection effect is on PROFIT-EQUAL candidates via the documented
    # (-profit, -cph, summary) order: two sinks with identical profit (4000) and
    # identical outbound time tie open (summary picks B); closed, B's 103s return vs
    # C's 73s breaks the tie toward the near-home sink C.
    snapshot = [snap("A", "S1", "G", ask=100, bid=0),
                snap("B", "S1", "G", ask=0, bid=200),
                snap("C", "S1", "G", ask=0, bid=200)]
    wps = [wpt("H", "S1", 0, 0), wpt("A", "S1", 50, 0),
           wpt("B", "S1", 100, 0), wpt("C", "S1", 50, 50)]
    rover = ship(at="H", hold=40)

    open_r = solve_tour(snapshot, rover, cons(max_hops=2), MODEL, waypoints=wps)
    closed_r = solve_tour(snapshot, rover, cons(max_hops=2, closed=True), MODEL,
                          waypoints=wps)

    def sold_at(r):
        return {l["waypoint_symbol"] for l in r["legs"]
                for t in l["trades"] if not t["is_buy"]}

    assert sold_at(open_r) == {"B"}
    assert closed_r["projected_profit"] == open_r["projected_profit"]
    assert sold_at(closed_r) == {"C"}
    ret = closed_r["legs"][-1]
    assert (ret["waypoint_symbol"], ret["trades"]) == ("H", [])


# ------------------------------------------------------------- ortools end arc (B9)
def test_ortools_end_arc_steers_the_route_home(monkeypatch):
    # Direct stage-1 check of the in-model closure arc: two equal-value sinks; OPEN
    # prefers B (outbound A->B 62s vs A->C 80s); CLOSED must flip to C because the
    # priced virtual end arc charges the return home (B->H 113s vs C->H 62s).
    pytest.importorskip("ortools")
    monkeypatch.setenv("TOUR_SOLVER_ORTOOLS_TIME_VALUE", "5")
    snapshot = [snap("A", "S1", "G", ask=100, bid=0),
                snap("B", "S1", "G", ask=0, bid=200),
                snap("C", "S1", "G", ask=0, bid=200)]
    wps = [wpt("H", "S1", 0, 0), wpt("A", "S1", 50, 0),
           wpt("B", "S1", 110, 0), wpt("C", "S1", 0, 60)]
    rover = ship(at="H", hold=40)
    markets = _build_markets(snapshot)

    c_open = cons(max_hops=3)
    travel = _make_travel_fn(c_open, markets, rover, wps)
    open_cands = ortools_sequences(markets, rover, c_open, travel)
    assert ("A", "B") in open_cands
    assert ("A", "C") not in open_cands

    # Direct call, so pass the stash solve_tour would have resolved (floating -> H).
    c_closed = cons(max_hops=3, closed=True,
                    _anchor_return_wp="H", _anchor_system="S1")
    closed_cands = ortools_sequences(markets, rover, c_closed, travel)
    assert ("A", "C") in closed_cands
    assert ("A", "B") not in closed_cands

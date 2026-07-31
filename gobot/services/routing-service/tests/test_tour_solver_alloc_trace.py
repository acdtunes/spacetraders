# gobot/services/routing-service/tests/test_tour_solver_alloc_trace.py
#
# sp-o2dzb — the allocation trace is the instrument that answers "WHICH term bounded
# planned units". A pinned value never names its constraint, so before any measurement
# taken with this trace can be believed, the trace itself has to be shown to name the
# RIGHT term. Each test below starves exactly ONE term and asserts the trace reports
# that term as the argmin while the others are held slack.
#
# It also pins the non-interference contract: _ALLOC_TRACE is None on every production
# path and the solver's output must be identical with the trace armed. The trace is
# pure observability — no branch on it may change an allocation.

import pytest

from utils import tour_solver
from utils.tour_solver import (MAX_PLANNED_TRANCHES_ENV_VAR,
                               REALIZED_SINK_TRANCHES_ENV_VAR, score_sequence,
                               solve_tour)

MODEL = {"fit_version": 1, "era": "e", "impact":
         {"LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1,
                           "n_obs": 9}},
         "recovery": {}}


@pytest.fixture(autouse=True)
def _pinned_env(monkeypatch):
    """Pin both ladders to the production run.sh values so these fixtures measure the
    term under test and not the ambient environment."""
    monkeypatch.setenv(MAX_PLANNED_TRANCHES_ENV_VAR, "3")
    monkeypatch.delenv(REALIZED_SINK_TRANCHES_ENV_VAR, raising=False)


@pytest.fixture(autouse=True)
def _trace_disarmed():
    """The trace must never leak between tests, and must be None at rest."""
    assert tour_solver._ALLOC_TRACE is None
    yield
    tour_solver._ALLOC_TRACE = None


def snap(wp, good, ask, bid, tv):
    return dict(waypoint_symbol=wp, system_symbol="S1", good_symbol=good, ask=ask,
                bid=bid, trade_volume=tv, supply="LIMITED", activity="WEAK",
                observed_at_unix=9_999_999_999)


def ship(cap):
    return dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=cap, fuel_current=4000, fuel_capacity=4000,
                engine_speed=30, cargo=[])


def cons(**over):
    base = dict(max_hops=4, max_spend=10_000_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    base.update(over)
    return base


def trace_of(seq, rows, hull, constraints):
    """Score one sequence with the trace armed and return its allocation records."""
    markets = tour_solver._build_markets(rows)
    tour_solver._ALLOC_TRACE = []
    try:
        score_sequence(seq, markets, hull, constraints, MODEL, lambda a, b: 100)
        return list(tour_solver._ALLOC_TRACE)
    finally:
        tour_solver._ALLOC_TRACE = None


def first_alloc(records):
    return next(r for r in records if r["event"] == "alloc")


# A: buys good G, B: sells it. Everything except the starved term is left slack.
def rows(buy_tv, sell_tv):
    return [snap("A", "G", ask=100, bid=1, tv=buy_tv),
            snap("B", "G", ask=0, bid=400, tv=sell_tv)]


def test_hold_space_is_named_when_the_hull_is_the_smallest_term():
    rec = first_alloc(trace_of(["A", "B"], rows(buy_tv=500, sell_tv=500), ship(30),
                               cons()))
    assert rec["units"] == 30
    assert rec["binding"] == ["hold_slack"]
    # every other term must be strictly slacker, or the fixture proves nothing
    assert rec["terms"]["buy_rem"] > 30 and rec["terms"]["sell_rem"] > 30
    assert rec["terms"]["afford"] > 30 and rec["terms"]["visit_cap"] > 30


def test_buy_depth_is_named_when_the_source_tranche_is_the_smallest_term():
    rec = first_alloc(trace_of(["A", "B"], rows(buy_tv=40, sell_tv=500), ship(400),
                               cons()))
    assert rec["units"] == 40
    assert rec["binding"] == ["buy_rem"]
    assert rec["terms"]["sell_rem"] > 40 and rec["terms"]["hold_slack"] > 40
    assert rec["terms"]["afford"] > 40 and rec["terms"]["visit_cap"] > 40


def test_sell_depth_is_named_when_the_sink_tranche_is_the_smallest_term():
    rec = first_alloc(trace_of(["A", "B"], rows(buy_tv=500, sell_tv=40), ship(400),
                               cons()))
    assert rec["units"] == 40
    assert rec["binding"] == ["sell_rem"]
    assert rec["terms"]["buy_rem"] > 40 and rec["terms"]["hold_slack"] > 40
    assert rec["terms"]["afford"] > 40


def test_spend_cap_is_named_when_the_purse_is_the_smallest_term():
    # max_spend 3,000 at ask 100 affords 30 units; everything else is slack.
    rec = first_alloc(trace_of(["A", "B"], rows(buy_tv=500, sell_tv=500), ship(400),
                               cons(max_spend=3_000)))
    assert rec["units"] == 30
    assert rec["binding"] == ["afford"]
    assert rec["terms"]["buy_rem"] > 30 and rec["terms"]["sell_rem"] > 30
    assert rec["terms"]["hold_slack"] > 30 and rec["terms"]["visit_cap"] > 30


def test_working_capital_reserve_reaches_the_same_term_as_max_spend():
    # The solver's money guard is spend_cap = max_spend - working_capital_reserve, so a
    # reserve must show up in `afford` exactly as a smaller max_spend does. This is the
    # arm that proves a 0 in the `afford` column on live plans is a real measurement of
    # the reserve, not a blind spot.
    rec = first_alloc(trace_of(["A", "B"], rows(buy_tv=500, sell_tv=500), ship(400),
                               cons(max_spend=10_000, working_capital_reserve=7_000)))
    assert rec["units"] == 30
    assert rec["binding"] == ["afford"]


def test_per_visit_sink_cap_is_named_when_it_is_the_smallest_term(monkeypatch):
    # The per-visit allowance is int(realized_sink_tranches x trade_volume). At the
    # production 2.5 it is always slacker than the sell tranche itself (2.5tv > tv), so
    # it can only be reached below 1.0 — which is exactly why it is near-invisible in
    # the live argmin distribution.
    monkeypatch.setenv(REALIZED_SINK_TRANCHES_ENV_VAR, "1.0")
    rows_ = [snap("A", "G", ask=100, bid=1, tv=500),
             snap("B", "G", ask=0, bid=400, tv=40)]
    rec = first_alloc(trace_of(["A", "B"], rows_, ship(400), cons()))
    # sell_rem (one 40-unit tranche) and visit_cap (int(1.0 x 40)) tie here by
    # construction: at 1.0 tranche the two ARE the same bound.
    assert rec["units"] == 40
    assert "visit_cap" in rec["binding"] and rec["terms"]["visit_cap"] == 40


def test_termination_census_names_a_full_hull_and_a_dead_spread():
    records = trace_of(["A", "B"], rows(buy_tv=500, sell_tv=500), ship(30), cons())
    term = [r for r in records if r["event"] == "terminated"]
    assert len(term) == 1
    assert term[0]["peak_occupancy"] == 30 and term[0]["hold_cap"] == 30
    # The hull filled, so what stops the loop is hold space — not depth, not the purse.
    assert term[0]["census"].get("hold_slack", 0) >= 1
    assert "afford" not in "".join(term[0]["census"])


def test_trace_is_pure_observability():
    """Armed or disarmed, the solve must be identical — the trace is a diagnostic, and
    a diagnostic that changes a plan is a defect, not an instrument."""
    rows_ = rows(buy_tv=90, sell_tv=90) + [snap("C", "G2", ask=50, bid=1, tv=60),
                                           snap("B", "G2", ask=0, bid=300, tv=60)]
    hull, constraints = ship(225), cons()
    waypoints = [dict(symbol=w, x=i * 40, y=0, system_symbol="S1")
                 for i, w in enumerate(["A", "B", "C"])]

    plain = solve_tour(rows_, hull, constraints, MODEL, waypoints=waypoints)
    tour_solver._ALLOC_TRACE = []
    try:
        armed = solve_tour(rows_, hull, constraints, MODEL, waypoints=waypoints)
        assert tour_solver._ALLOC_TRACE, "the trace must actually have recorded something"
    finally:
        tour_solver._ALLOC_TRACE = None

    assert armed == plain

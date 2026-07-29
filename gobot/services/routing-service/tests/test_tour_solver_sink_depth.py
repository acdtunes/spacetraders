# gobot/services/routing-service/tests/test_tour_solver_sink_depth.py
#
# sp-28lw9 — raise the single-sink per-visit absorption depth cap, and make it a calibration knob.
#
# sp-2v69u SECONDARY pinned the realized per-visit sink cap at ONE trade_volume to stop a heavy
# dumping an unrealizable two-tranche load into a shallow dock. Measured in era 5 (player 5,
# 24h, tour_leg_telemetry JOIN market_data) that calibration is far too tight:
#   * 42.4% of planned sell legs sit pinned at exactly 1.0 x trade_volume (211/498)
#   * 496 of 499 planned sell legs realized EXACTLY the planned units; ZERO stranded
#   * the tiers carrying the volume decay only ~1-2%/tranche in the live era-07-19 fit
#     (SCARCE|WEAK 0.9888 n=3189, LIMITED|WEAK 0.9783 n=944, SCARCE|GROWING 0.9886 n=718)
# so the strand risk sp-2v69u guarded against is not visible at the boundary, while the cap
# is demonstrably the binding depth constraint. The bound STAYS (a heavy still cannot dump
# unbounded depth into one dock) — only its calibration moves, and it becomes env-tunable
# so a sweep can move it without a code change, mirroring TOUR_SOLVER_MAX_PLANNED_TRANCHES.
#
# RULINGS #4: this raises a PERFORMANCE ceiling, not a money guard. The cap still only ever
# reduces planned units (it is a min()), hull capacity and the spend cap still bind ahead of
# it, and the floor is 1.0 so the cap can never be tuned to plan NO sells.

import pytest

from utils.tour_solver import (
    MAX_PLANNED_TRANCHES_ENV_VAR,
    REALIZED_SINK_TRANCHES_PER_VISIT,
    REALIZED_SINK_TRANCHES_ENV_VAR,
    REALIZED_SINK_TRANCHES_MIN,
    REALIZED_SINK_TRANCHES_MAX,
    _resolve_realized_sink_tranches,
    solve_tour,
)

# The per-(market,good,side) POOL ladder is a SECOND, independent bound on the same quantity:
# a pool holds at most max_planned_tranches x trade_volume across the whole tour, so the
# realized per-visit depth is min(realized_sink_tranches, max_planned_tranches) x tv. Production
# arms TOUR_SOLVER_MAX_PLANNED_TRANCHES=3 in run.sh, which is what makes the 2.5 default
# reachable; the CODE default is 2, which would silently clip 2.5 -> 2.0. Tests that mean to
# observe the visit cap therefore pin the pool depth explicitly rather than inheriting whatever
# the ambient environment happens to export — and test_pool_ladder_clips_a_deeper_visit_cap
# below pins the interaction itself so it cannot regress unnoticed.
PROD_POOL_TRANCHES = "3"


@pytest.fixture
def prod_pool(monkeypatch):
    monkeypatch.setenv(MAX_PLANNED_TRANCHES_ENV_VAR, PROD_POOL_TRANCHES)

MODEL = {"fit_version": 1, "era": "e", "impact":
         {"LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}


def snap(wp, sys_, good, ask, bid, tv=90, supply="LIMITED", activity="WEAK"):
    return dict(waypoint_symbol=wp, system_symbol=sys_, good_symbol=good, ask=ask,
                bid=bid, trade_volume=tv, supply=supply, activity=activity,
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


def _total_buy(out, good):
    return sum(t["units"] for l in out["legs"] for t in l["trades"]
               if t["is_buy"] and t["good_symbol"] == good)


def _sold_at(out, wp, good):
    return sum(t["units"] for l in out["legs"] if l["waypoint_symbol"] == wp
               for t in l["trades"] if not t["is_buy"] and t["good_symbol"] == good)


# --- the calibration itself -------------------------------------------------------------

def test_default_visit_depth_is_the_raised_calibration():
    """The shipped default is the raised depth, not the sp-2v69u 1.0. Pinning the NUMBER
    (not just '> 1') is the point: this constant IS the deliverable."""
    assert REALIZED_SINK_TRANCHES_PER_VISIT == 2.5


def test_heavy_hull_now_loads_the_raised_depth_into_one_sink(prod_pool):
    """THE REGRESSION TEST. A 225-cargo heavy against a single tv=90 dock, max_hops=2 so no
    revisit is possible: the reachable sink graph is that one dock. Under the sp-2v69u
    calibration this planned 90 (1 x tv). At 2.5 x tv = 225 the hull fills, which is the
    whole 3.3-4.0x throughput claim in the bead."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=90),
                snap("B", "S1", "G", ask=0, bid=300, tv=90)]
    out = solve_tour(snapshot, ship(225), cons(max_hops=2), MODEL, objective="profit")
    assert out["feasible"], out
    assert _total_buy(out, "G") == 225, out
    assert _sold_at(out, "B", "G") == 225, out
    # The sp-2v69u calibration is what we are leaving behind.
    assert _total_buy(out, "G") != 90, out


def test_visit_cap_still_binds_below_hull_capacity(prod_pool):
    """The bound STAYS a bound. A shallow tv=20 dock caps the same 225 heavy at
    int(2.5 * 20) = 50 — far below its hold — so raising the calibration did not remove
    the per-visit ceiling, it moved it. Without a live cap this would plan the hull's
    full capacity into one shallow dock, which is the sp-2v69u defect returning."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=200),
                snap("B", "S1", "G", ask=0, bid=300, tv=20)]
    out = solve_tour(snapshot, ship(225), cons(max_hops=2), MODEL, objective="profit")
    assert out["feasible"], out
    assert _sold_at(out, "B", "G") == 50, out
    assert _total_buy(out, "G") == 50, out


def test_pool_ladder_clips_a_deeper_visit_cap(monkeypatch):
    """THE INTERACTION THE BEAD DOES NOT MENTION, pinned so it cannot regress silently.
    Two independent bounds govern the same quantity and the SHALLOWER wins: with the pool
    ladder at the CODE default of 2 the same 225 heavy reaches only 2 x 90 = 180, not the
    2.5 x 90 = 225 its visit cap allows. Production reaches 225 only because run.sh arms
    TOUR_SOLVER_MAX_PLANNED_TRANCHES=3 — reverting that knob silently re-clips this one."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=90),
                snap("B", "S1", "G", ask=0, bid=300, tv=90)]
    monkeypatch.setenv(MAX_PLANNED_TRANCHES_ENV_VAR, "2")
    out = solve_tour(snapshot, ship(225), cons(max_hops=2), MODEL, objective="profit")
    assert out["feasible"], out
    assert _sold_at(out, "B", "G") == 180, out


def test_hull_capacity_still_binds_ahead_of_the_visit_cap():
    """CONTROL, and the RULINGS #4 direction: the cap only ever REDUCES. An 80-cargo hull
    against a tv=90 dock is bounded by its hold (80), not by the 225-unit visit allowance,
    exactly as before the raise — so a small hull's plan is untouched by this change."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=90),
                snap("B", "S1", "G", ask=0, bid=300, tv=90)]
    out = solve_tour(snapshot, ship(80), cons(max_hops=2), MODEL, objective="profit")
    assert out["feasible"], out
    assert _total_buy(out, "G") == 80, out


# --- the knob ---------------------------------------------------------------------------

def test_env_override_restores_the_previous_calibration_exactly(prod_pool):
    """THE REVERT PATH the bead requires: TOUR_SOLVER_REALIZED_SINK_TRANCHES=1 reproduces the
    sp-2v69u plan exactly (90 into the one dock), with no code change and no redeploy of the
    solver source. If this drifts, the documented disarm is a lie."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=90),
                snap("B", "S1", "G", ask=0, bid=300, tv=90)]
    with pytest.MonkeyPatch.context() as mp:
        mp.setenv(REALIZED_SINK_TRANCHES_ENV_VAR, "1")
        out = solve_tour(snapshot, ship(225), cons(max_hops=2), MODEL, objective="profit")
    assert out["feasible"], out
    assert _total_buy(out, "G") == 90, out
    assert _sold_at(out, "B", "G") == 90, out


def test_env_override_is_honoured_between_the_bounds(prod_pool):
    """A sweep value in range resolves as given (2.0 -> int(2.0*90) = 180)."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=90),
                snap("B", "S1", "G", ask=0, bid=300, tv=90)]
    with pytest.MonkeyPatch.context() as mp:
        mp.setenv(REALIZED_SINK_TRANCHES_ENV_VAR, "2.0")
        out = solve_tour(snapshot, ship(225), cons(max_hops=2), MODEL, objective="profit")
    assert _sold_at(out, "B", "G") == 180, out


def test_resolver_defaults_when_env_absent():
    with pytest.MonkeyPatch.context() as mp:
        mp.delenv(REALIZED_SINK_TRANCHES_ENV_VAR, raising=False)
        assert _resolve_realized_sink_tranches() == REALIZED_SINK_TRANCHES_PER_VISIT


def test_resolver_clamps_below_the_floor_and_never_reaches_zero():
    """A 0 cap would plan NO sells and silently halt trading — the same reasoning that gives
    MAX_PLANNED_TRANCHES a floor of 1. Clamped, not honoured."""
    with pytest.MonkeyPatch.context() as mp:
        mp.setenv(REALIZED_SINK_TRANCHES_ENV_VAR, "0")
        assert _resolve_realized_sink_tranches() == REALIZED_SINK_TRANCHES_MIN
        mp.setenv(REALIZED_SINK_TRANCHES_ENV_VAR, "-5")
        assert _resolve_realized_sink_tranches() == REALIZED_SINK_TRANCHES_MIN
    assert REALIZED_SINK_TRANCHES_MIN >= 1.0


def test_resolver_clamps_above_the_ceiling():
    with pytest.MonkeyPatch.context() as mp:
        mp.setenv(REALIZED_SINK_TRANCHES_ENV_VAR, "99")
        assert _resolve_realized_sink_tranches() == REALIZED_SINK_TRANCHES_MAX


def test_resolver_falls_back_to_default_on_garbage():
    """Fail-safe: an unparseable knob must not take the solver's depth to zero or explode."""
    with pytest.MonkeyPatch.context() as mp:
        mp.setenv(REALIZED_SINK_TRANCHES_ENV_VAR, "not-a-number")
        assert _resolve_realized_sink_tranches() == REALIZED_SINK_TRANCHES_PER_VISIT


def test_fractional_depth_floors_to_whole_units(prod_pool):
    """Units are integral cargo. A fractional allowance (2.5 x tv=25 -> 62.5) must floor to
    62, never round up past the modeled absorption and never leak a float into the plan."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=200),
                snap("B", "S1", "G", ask=0, bid=300, tv=25)]
    out = solve_tour(snapshot, ship(225), cons(max_hops=2), MODEL, objective="profit")
    sold = _sold_at(out, "B", "G")
    assert sold == 62, out
    assert isinstance(sold, int), out

# gobot/services/routing-service/tests/test_tour_solver_capacity_buys.py
#
# sp-2v69u SECONDARY — capacity-aware per-good buy budget (LIVE, Admiral-ruled: no flag).
#
# Root cause (bead sp-2v69u, RC1+RC2): a buy is a (buy_leg, sell_leg) pairing whose units are
# gated on the MODELED 2-tranche sink-impact depth, with no awareness of hull capacity. A
# high-capacity freighter (225 cargo) therefore loads ~180 units of one good into a SINGLE sink
# visit — two tranches the shallow WEAK/RESTRICTED market cannot realize — and the excess
# strands. The correctness bound (now the binding single-sink depth constraint, superseding the
# tranche-count MAX_PLANNED_TRANCHES per visit): a single market-sink visit realizes at most one
# trade_volume of absorption. A hull whose capacity exceeds a sink's trade_volume can never dump
# its excess into one dock. Buys are matched 1:1 to sells in the greedy allocator, so bounding
# realized per-visit sink absorption bounds the per-good BUY commitment to what the reachable sink
# graph can actually take (net of fleet-wide absorption, which nets the pools upstream).

# sp-28lw9 RECALIBRATION. The bound above is unchanged and still the point of this file; its
# NUMBER moved from 1.0 to 2.5 trade_volumes per visit, so the two tests that pinned the 1.0
# figure are re-pinned at 2.5 below. Overwriting a shipped test's expected values is normally a
# cardinal sin — it is correct here only because raising exactly this calibration IS the bead,
# and the *property* each test guards (a per-visit ceiling exists; a hull still spreads across
# the reachable sink graph rather than stranding an absorbable load) is preserved, not deleted.
#
# These tests also read a SECOND bound that they never pinned: the per-(market,good,side) pool
# ladder, max_planned_tranches x tv. They silently inherited whatever the ambient environment
# exported, so they passed at the code default of 2 and FAILED at the production run.sh value of
# 3 — a latent fragility this change exposed. The fixture below pins it to the production value
# so the file is deterministic either way.

import pytest

from utils.tour_solver import MAX_PLANNED_TRANCHES_ENV_VAR, solve_tour

MODEL = {"fit_version": 1, "era": "e", "impact":
         {"LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}


@pytest.fixture(autouse=True)
def _production_pool_depth(monkeypatch):
    """Pin the pool ladder to the production run.sh value so these fixtures measure the
    per-visit cap and not the ambient environment."""
    monkeypatch.setenv(MAX_PLANNED_TRANCHES_ENV_VAR, "3")


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


def _sells(out):
    sells = {}
    for l in out["legs"]:
        for t in l["trades"]:
            if not t["is_buy"]:
                key = (l["waypoint_symbol"], t["good_symbol"])
                sells[key] = sells.get(key, 0) + t["units"]
    return sells


def _total_buy(out, good):
    return sum(t["units"] for l in out["legs"] for t in l["trades"]
              if t["is_buy"] and t["good_symbol"] == good)


def test_heavy_hull_per_good_buy_capped_to_realized_sink_absorption():
    """A 225-cargo heavy against a single tv=90 sink is bounded by that dock's per-visit
    absorption. max_hops=2 forbids a revisit, so the whole tour's reach is that single dock and
    the per-good BUY commitment can only be what the dock realizes: int(2.5 * 90) = 225
    (sp-28lw9; this figure was 1 x 90 = 90 under the sp-2v69u calibration)."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=90),    # deep cheap source
                snap("B", "S1", "G", ask=0, bid=300, tv=90)]    # single shallow sink
    out = solve_tour(snapshot, ship(225), cons(max_hops=2), MODEL, objective="profit")
    assert out["feasible"], out
    assert _total_buy(out, "G") == 225, out
    assert _sells(out).get(("B", "G")) == 225, out


def test_heavy_hull_per_visit_ceiling_still_binds_on_a_shallow_dock():
    """The ORIGINAL sp-2v69u property, re-pinned at the new calibration: the ceiling still
    EXISTS. Against a tv=20 dock the same 225 heavy is held to int(2.5 * 20) = 50, nowhere near
    its hold — so the raise moved the bound, it did not remove it. Without a live cap this plans
    the hull's full capacity into one shallow dock, which is the sp-2v69u defect returning."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=200),
                snap("B", "S1", "G", ask=0, bid=300, tv=20)]
    out = solve_tour(snapshot, ship(225), cons(max_hops=2), MODEL, objective="profit")
    assert out["feasible"], out
    assert _total_buy(out, "G") == 50, out
    assert _sells(out).get(("B", "G")) == 50, out


def test_heavy_hull_spreads_full_load_across_reachable_sink_graph():
    """The cap is per reachable sink VISIT, not a blanket per-good ceiling: when ONE dock cannot
    absorb the hull, the load spreads across the graph instead of stranding. Two tv=40 docks each
    take int(2.5 * 40) = 100, so a 225 heavy lifts 200 — more than either dock alone. Guards
    against an over-aggressive implementation that would strand a genuinely absorbable load."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=200),
                snap("B", "S1", "G", ask=0, bid=300, tv=40),
                snap("C", "S1", "G", ask=0, bid=290, tv=40)]
    out = solve_tour(snapshot, ship(225), cons(max_hops=3), MODEL, objective="profit")
    assert out["feasible"], out
    assert _total_buy(out, "G") == 200, out            # spread across the graph, nothing stranded
    assert _sells(out).get(("B", "G")) == 100, out     # 2.5 trade_volumes per dock
    assert _sells(out).get(("C", "G")) == 100, out


def test_light_hull_against_deep_sinks_is_unchanged():
    """CONTROL (byte-identical where capacity is not the binding constraint): an 80-cargo hull
    against a deep tv=90 sink can never carry more than 80 (< tv) to a dock, so the per-visit
    absorption cap is inert. The full plan matches the pre-cap golden exactly."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=90),
                snap("B", "S1", "G", ask=0, bid=300, tv=90)]
    out = solve_tour(snapshot, ship(80), cons(max_hops=4), MODEL, objective="profit")
    assert out["feasible"], out
    assert out["projected_profit"] == 29200, out
    # Pre-cap golden leg structure (hull 80 < tv 90 -> cap never trips).
    legs = [(l["waypoint_symbol"],
             tuple((t["good_symbol"], t["units"], t["is_buy"], t["expected_unit_price"])
                   for t in l["trades"])) for l in out["legs"]]
    assert legs == [
        ("A", (("G", 80, True, 100),)),
        ("B", (("G", 80, False, 300),)),
        ("A", (("G", 10, True, 100), ("G", 70, True, 110))),
        ("B", (("G", 70, False, 270), ("G", 10, False, 300))),
    ], legs

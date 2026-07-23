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

from utils.tour_solver import solve_tour

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
    """A 225-cargo heavy against a single tv=90 sink must NOT plan the modeled 180-unit
    (two-tranche) dump. One sink visit realizes at most one trade_volume (90), so the per-good BUY
    commitment is capped to 90 — the reachable sink's realized absorption. max_hops=2 forbids a
    revisit, so the whole tour's reach is that single dock."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=90),    # deep cheap source
                snap("B", "S1", "G", ask=0, bid=300, tv=90)]    # single shallow sink
    out = solve_tour(snapshot, ship(225), cons(max_hops=2), MODEL, objective="profit")
    assert out["feasible"], out
    # The reachable sink graph is one tv=90 dock -> it can absorb 90, not 180.
    assert _total_buy(out, "G") == 90, out
    assert _sells(out).get(("B", "G")) == 90, out
    # The pre-fix defect planned the full two-tranche 180-unit dump; never again.
    assert _total_buy(out, "G") != 180, out


def test_heavy_hull_spreads_full_load_across_reachable_sink_graph():
    """The cap is per reachable sink VISIT, not a blanket per-good ceiling: a 225 heavy may still
    load 180 of a good when the reachable graph offers TWO tv=90 sinks (90 into each). Guards
    against an over-aggressive implementation that would strand a genuinely absorbable load."""
    snapshot = [snap("A", "S1", "G", ask=100, bid=0, tv=90),
                snap("B", "S1", "G", ask=0, bid=300, tv=90),
                snap("C", "S1", "G", ask=0, bid=290, tv=90)]
    out = solve_tour(snapshot, ship(225), cons(max_hops=3), MODEL, objective="profit")
    assert out["feasible"], out
    assert _total_buy(out, "G") == 180, out            # full load, absorbed by the graph
    assert _sells(out).get(("B", "G")) == 90, out      # one trade_volume per dock
    assert _sells(out).get(("C", "G")) == 90, out


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

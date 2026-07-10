# gobot/services/routing-service/tests/test_tour_solver.py
import random

from utils.tour_solver import tranche_prices, solve_tour

MODEL = {"fit_version": 1, "era": "e", "impact":
         {"LIMITED|WEAK": {"sell_decay_per_step": 0.9, "buy_growth_per_step": 1.1, "n_obs": 9}},
         "recovery": {}}

def snap(wp, sys_, good, ask, bid, tv=20, supply="LIMITED", activity="WEAK"):
    return dict(waypoint_symbol=wp, system_symbol=sys_, good_symbol=good, ask=ask,
                bid=bid, trade_volume=tv, supply=supply, activity=activity,
                observed_at_unix=9_999_999_999)

def test_tranche_prices_decay_sell_side():
    t = tranche_prices(quote=1000, trade_volume=20, tier="LIMITED|WEAK",
                       model=MODEL, is_buy=False, max_units=60)
    assert t == [(20, 1000), (20, 900), (20, 810)]

def test_solver_splits_sells_across_two_sinks():
    # 80u to sell; each sink absorbs 40u near-quote before deep decay → split beats dump
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),   # buy here
        snap("B", "S1", "G", ask=999, bid=200, tv=40),  # sink 1
        snap("C", "S1", "G", ask=999, bid=195, tv=40),  # sink 2
    ]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=80, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    cons = dict(max_hops=4, max_spend=100_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"]
    sells = [(l["waypoint_symbol"], t["units"]) for l in out["legs"]
             for t in l["trades"] if not t["is_buy"]]
    assert ("B", 40) in sells and ("C", 40) in sells  # split, not 80-dump at B

def test_solver_respects_two_system_cap():
    snapshot = [snap("A", "S1", "G", 100, 90), snap("B", "S2", "G", 999, 300),
                snap("C", "S3", "G", 999, 400)]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=40, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    cons = dict(max_hops=4, max_spend=50_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1", "S2", "S3"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    systems = {l["system_symbol"] for l in out["legs"]}
    assert len(systems) <= 2  # maxTourSystems enforced even when 3 allowed

def test_solver_prefers_nearer_equal_sink():
    # Two sinks with IDENTICAL bids: equal profit either way, so the cph
    # tiebreak must pick the nearer one — only possible with the real
    # coordinate travel matrix (harbormaster amendment). Under flat travel
    # defaults both tours tie on time and the lexicographic fallback picks
    # B-FAR, so this test pins coordinate-mode behavior.
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),
        snap("B-FAR", "S1", "G", ask=999, bid=200, tv=40),
        snap("Z-NEAR", "S1", "G", ask=999, bid=200, tv=40),
    ]
    waypoints = [
        dict(symbol="A", system_symbol="S1", x=0, y=0),
        dict(symbol="B-FAR", system_symbol="S1", x=900, y=0),
        dict(symbol="Z-NEAR", system_symbol="S1", x=30, y=0),
    ]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=40, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    # max_spend affords exactly one 40u tranche at 100 — the two tours are
    # equal-profit, so only the cph tiebreak separates them.
    cons = dict(max_hops=4, max_spend=4_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL, waypoints=waypoints)
    assert out["feasible"]
    sell_wps = {l["waypoint_symbol"] for l in out["legs"]
                for t in l["trades"] if not t["is_buy"]}
    assert sell_wps == {"Z-NEAR"}
    # CRUISE formula: 30 units of distance * 31 / speed 30 = 31 seconds.
    near_leg = [l for l in out["legs"] if l["waypoint_symbol"] == "Z-NEAR"][0]
    assert near_leg["travel_seconds_from_prev"] == 31


def test_solver_cap_reshapes_revisit_ladder():
    # The pure-profit-primary exhibit from the sp-eh9w escalation: without a
    # depth cap the optimizer revisits the deep sink (A->D->E->A->D->E) and
    # dumps G tranches 3-4 into D — the D39 ladder shape. The A-capped ruling
    # (MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE=2) must make that
    # unplannable: no (market, good, side) may exceed 2 tranches across the
    # WHOLE tour, revisits included.
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),
        snap("B", "S1", "G", ask=999, bid=200, tv=40),
        snap("C", "S1", "G", ask=999, bid=195, tv=40),
        snap("D", "S2", "G", ask=999, bid=320, tv=40),
        snap("D", "S2", "H", ask=50, bid=40, tv=40),
        snap("E", "S1", "H", ask=999, bid=160, tv=40),
    ]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=80, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    cons = dict(max_hops=6, max_spend=100_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1", "S2"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"]
    per_side = {}
    for leg in out["legs"]:
        for t in leg["trades"]:
            k = (leg["waypoint_symbol"], t["good_symbol"], t["is_buy"])
            per_side[k] = per_side.get(k, 0) + t["units"]
    assert all(units <= 2 * 40 for units in per_side.values()), per_side
    # With the ladder capped, the best plan is the hold-refilling crossing:
    # 80u G out (A->D), 80u H back (D->E) — not a third/fourth tranche at D.
    assert out["projected_profit"] == 23_880


def test_solver_held_cargo_liquidates_without_buy_leg():
    # sp-m5kv acceptance (2): a laden hull's held cargo appears as SELL legs in the
    # plan, WITHOUT a buy leg (cash recovery of pre-held inventory). This is the
    # laden-exit->manual-rescue class the continuous tour kills: even with NO fresh
    # trade affordable (max_spend 0), the held load is planned for liquidation.
    snapshot = [snap("B", "S1", "MEDICINE", ask=999, bid=1800, tv=40)]  # a sink for the held load
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=80, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[dict(good_symbol="MEDICINE", units=40)])
    cons = dict(max_hops=4, max_spend=0, min_margin_per_unit=1,  # nothing to BUY
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out
    sells = [(l["waypoint_symbol"], t["units"]) for l in out["legs"]
             for t in l["trades"] if not t["is_buy"] and t["good_symbol"] == "MEDICINE"]
    buys = [t for l in out["legs"] for t in l["trades"] if t["is_buy"]]
    assert ("B", 40) in sells, out                       # held cargo -> sell leg
    assert not buys, f"held-cargo liquidation plans no buy leg, got {buys}"


def test_solver_held_cargo_sells_ordered_before_buys():
    # Held cargo enters the manifest as sell-capable inventory and is dock-ordered
    # FIRST: a full hold of MEDICINE must be sold to free the hold before a fresh
    # FABRICS tranche is bought, and within every leg sells precede buys.
    snapshot = [
        snap("B", "S1", "MEDICINE", ask=999, bid=1800, tv=40),
        snap("B", "S1", "FABRICS", ask=100, bid=90, tv=40),
        snap("C", "S1", "FABRICS", ask=999, bid=300, tv=40),
    ]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=40, fuel_current=400, fuel_capacity=400,  # FULL of held cargo
                engine_speed=30, cargo=[dict(good_symbol="MEDICINE", units=40)])
    cons = dict(max_hops=4, max_spend=100_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out
    med_sold = any(t["good_symbol"] == "MEDICINE" and not t["is_buy"]
                   for l in out["legs"] for t in l["trades"])
    assert med_sold, out                                 # the held load is liquidated
    for l in out["legs"]:                                # dock order: sells before buys
        seen_buy = False
        for t in l["trades"]:
            if t["is_buy"]:
                seen_buy = True
            else:
                assert not seen_buy, f"sell after buy at {l['waypoint_symbol']}: {l['trades']}"


def test_solver_empty_hold_plans_no_launch_liquidation():
    # Regression (sp-m5kv): an EMPTY hull is unchanged by held-cargo support — it
    # plans a normal buy->sell arb and never fabricates a launch-liquidation sell, so
    # sold units never exceed bought units.
    snapshot = [snap("A", "S1", "G", ask=100, bid=90, tv=40),
                snap("B", "S1", "G", ask=999, bid=200, tv=40)]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=40, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])                # empty hold
    cons = dict(max_hops=4, max_spend=100_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out
    total_buys = sum(t["units"] for l in out["legs"] for t in l["trades"] if t["is_buy"])
    total_sells = sum(t["units"] for l in out["legs"] for t in l["trades"] if not t["is_buy"])
    assert total_buys > 0 and total_sells > 0, out
    assert total_sells <= total_buys, out                # nothing to liquidate from an empty hold


def test_solver_property_capacity_and_spend():
    # Randomized snapshots (seeded): the plan never overfills the hold at any
    # point of the tour and never spends past max_spend. Reconstructed from the
    # OUTPUT legs — the observable contract, not solver internals.
    rng = random.Random(424242)  # fixed seed — deterministic property trials
    goods = ["G1", "G2", "G3"]
    for trial in range(25):
        snapshot = []
        n_markets = rng.randint(3, 8)
        for m in range(n_markets):
            sys_ = "S1" if m < max(2, n_markets - 2) else "S2"
            for g in goods:
                if rng.random() < 0.3:
                    continue  # not every market lists every good
                base = rng.randint(40, 2000)
                snapshot.append(snap(f"M{m}", sys_, g,
                                     ask=base + rng.randint(0, 200),
                                     bid=base - rng.randint(0, 30),
                                     tv=rng.choice([10, 20, 40, 60])))
        hold = rng.choice([40, 80, 120, 225])
        max_spend = rng.choice([5_000, 40_000, 200_000])
        initial = [dict(good_symbol="G1", units=rng.randint(0, hold // 4))]
        ship = dict(ship_symbol="H", current_waypoint="M0", current_system="S1",
                    hold_capacity=hold, fuel_current=400, fuel_capacity=400,
                    engine_speed=30, cargo=[c for c in initial if c["units"]])
        cons = dict(max_hops=6, max_spend=max_spend, min_margin_per_unit=1,
                    working_capital_reserve=0, allowed_systems=["S1", "S2"],
                    max_snapshot_age_minutes=75, expected_model_version="1@e")
        out = solve_tour(snapshot, ship, cons, MODEL)
        if not out["feasible"]:
            continue
        held = sum(c["units"] for c in ship["cargo"])
        spend = 0
        per_good = {c["good_symbol"]: c["units"] for c in ship["cargo"]}
        for leg in out["legs"]:
            # dock order: sells first (frees hold), then buys
            for t in leg["trades"]:
                assert t["units"] > 0
                if not t["is_buy"]:
                    held -= t["units"]
                    per_good[t["good_symbol"]] = per_good.get(t["good_symbol"], 0) - t["units"]
                    assert per_good[t["good_symbol"]] >= 0, f"trial {trial}: oversold"
            for t in leg["trades"]:
                if t["is_buy"]:
                    held += t["units"]
                    per_good[t["good_symbol"]] = per_good.get(t["good_symbol"], 0) + t["units"]
                    spend += t["units"] * t["expected_unit_price"]
            assert 0 <= held <= hold, f"trial {trial}: hold {held}/{hold} breached"
        assert spend <= max_spend, f"trial {trial}: spend {spend} > cap {max_spend}"


def _peak_hold_and_goods(out):
    """Peak hold occupancy and the set of goods bought, from OUTPUT legs — the
    observable manifest, dock-ordered sells-before-buys within each leg."""
    held, peak, bought = 0, 0, set()
    for leg in out["legs"]:
        for t in leg["trades"]:
            if not t["is_buy"]:
                held -= t["units"]
        for t in leg["trades"]:
            if t["is_buy"]:
                held += t["units"]
                bought.add(t["good_symbol"])
        peak = max(peak, held)
    return peak, bought


def _loop_a_board():
    # The analyst's certified Loop-A shape plus the crowding distractor that made
    # it fail in the field. Five cluster goods whose SRC (source) and SNK (sink)
    # markets together fill a 225-hold heavy — moderate per-good spreads, vol-30
    # sinks, so NO single good fills the hold; only packing across all five does.
    # The distractor is many THIN SHIP_PARTS markets: a rich per-unit spread over
    # vol-6 sinks the OLD single-good beam bound over-valued (spread × FULL hold)
    # and crowded the scoring pool with, planning a 7%-hold single-good manifest
    # on a heavy hull. The ship starts AT a distractor (P00), so surfacing the
    # cluster is the beam's job, not a gift of the start position.
    cluster = [("PARTS", 300, 2600), ("PLATING", 250, 2700),
               ("ADV_CIRCUITRY", 500, 4200), ("CLOTHING", 200, 2300),
               ("FOODSTUFFS", 150, 1800)]
    rows = []
    for g, ask, bid in cluster:
        rows.append(snap("SRC", "S1", g, ask=ask, bid=ask - 10, tv=30))
        rows.append(snap("SNK", "S1", g, ask=bid + 400, bid=bid, tv=30))
    for i in range(18):
        side = dict(ask=100, bid=95) if i % 2 == 0 else dict(ask=9999, bid=5000)
        rows.append(snap(f"P{i:02d}", "S1", "SHIP_PARTS", tv=6, **side))
    return rows


def _plan_loop_a(hold, max_hops=6):
    ship = dict(ship_symbol="H", current_waypoint="P00", current_system="S1",
                hold_capacity=hold, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    cons = dict(max_hops=max_hops, max_spend=5_000_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    return solve_tour(_loop_a_board(), ship, cons, MODEL)


def test_solver_packs_hold_across_goods_loop_a():
    # sp-gm00 proving fixture: a 225-hold heavy on the Loop-A board must fill its
    # hold across ≥4 goods instead of planning a thin single-good SHIP_PARTS
    # manifest. (Guards the beam fix: on the old single-good bound this plans
    # 12/225 = 5% utilization.)
    out = _plan_loop_a(225)
    assert out["feasible"], out
    peak, bought = _peak_hold_and_goods(out)
    assert peak >= 0.9 * 225, f"hold {peak}/225 = {peak / 225:.0%} < 90% utilization"
    assert len(bought) >= 4, f"packed only {bought}, need ≥4 distinct goods"
    # A hold-filling multi-good manifest dwarfs the ~111k single-good class the
    # bead reports; assert the class, not an exact credit figure.
    assert out["projected_profit"] > 400_000, out["projected_profit"]


def test_solver_profit_scales_with_hull_size():
    # sp-gm00 acceptance: on the SAME board a bigger hull plans MORE profit. The
    # bead's core defect was a 225-hold heavy planning the same ~15-unit manifest
    # as an 80-hold light ("hull size barely matters"). Packing must make the
    # heavy fill its hold across goods and out-earn the light. (Guards the beam
    # fix: on the old bound both hulls plan the same thin manifest — profit and
    # peak occupancy are equal and this assertion fails.)
    light, heavy = _plan_loop_a(80), _plan_loop_a(225)
    assert light["feasible"] and heavy["feasible"], (light, heavy)
    light_peak, _ = _peak_hold_and_goods(light)
    heavy_peak, heavy_goods = _peak_hold_and_goods(heavy)
    assert heavy_peak > light_peak, (light_peak, heavy_peak)          # hull fills more
    assert len(heavy_goods) >= 4, heavy_goods                          # across goods
    assert heavy["projected_profit"] > light["projected_profit"], \
        (light["projected_profit"], heavy["projected_profit"])

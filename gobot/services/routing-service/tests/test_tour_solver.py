# gobot/services/routing-service/tests/test_tour_solver.py
import random

from utils.tour_solver import tranche_prices, solve_tour, net_absorption

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

def _cap_sweep_board():
    # sp-syaz: a cheap source in S1 and two comparably-fat sinks in S2 and S3. The
    # source fills the hold with 80u G (A-cap 2*tv=80); each sink's UNDECAYED first
    # tranche (40u) pays more than the source's second tranche pays anywhere decayed,
    # so the profit optimum SPLITS 40u/40u across B(S2) and C(S3) — which structurally
    # requires touching 3 systems. With only 2 systems reachable the tour can dump into
    # a single sink and must eat the 0.9 decay on its 2nd tranche. The raised cap is
    # therefore exactly what unlocks the strictly-higher 3-system optimum.
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),   # cheap source (80u via A-cap)
        snap("B", "S2", "G", ask=999, bid=300, tv=40),  # fat sink 1
        snap("C", "S3", "G", ask=999, bid=290, tv=40),  # fat sink 2 (comparable)
    ]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=80, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])

    def cons(**over):
        base = dict(max_hops=4, max_spend=100_000, min_margin_per_unit=1,
                    working_capital_reserve=0, allowed_systems=["S1", "S2", "S3"],
                    max_snapshot_age_minutes=75, expected_model_version="1@e")
        base.update(over)
        return base

    return snapshot, ship, cons

def test_solver_request_raises_system_cap():
    # The RED for sp-syaz: promoting MAX_TOUR_SYSTEMS to a request field lets the
    # caller RAISE the per-tour distinct-system cap. objective is pinned to profit
    # (the plan's "profit optimum REQUIRES 3 systems" framing) so the assertion is
    # deterministic regardless of the deploy-time TOUR_SOLVER_OBJECTIVE.
    snapshot, ship, cons = _cap_sweep_board()

    # Baseline: the default 2-system cap forces a single-sink dump — spans 2 systems.
    baseline = solve_tour(snapshot, ship, cons(), MODEL, objective="profit")
    assert baseline["feasible"], baseline
    assert len({l["system_symbol"] for l in baseline["legs"]}) == 2, baseline

    # Flip the field to 3: splitting across BOTH sinks now wins, spanning all 3 systems.
    raised = solve_tour(snapshot, ship, cons(max_tour_systems=3), MODEL,
                        objective="profit")
    assert raised["feasible"], raised
    assert {l["system_symbol"] for l in raised["legs"]} == {"S1", "S2", "S3"}, raised

def test_solver_max_tour_systems_zero_falls_back_to_default():
    # sp-syaz default-safety hinge (the falsy-zero path): max_tour_systems=0 — the
    # proto3 int32 default, indistinguishable from unset — must behave EXACTLY like the
    # MAX_TOUR_SYSTEMS module default (2). The SAME board the raised-cap test spans in 3
    # systems stays clamped to 2 here, because `0 or MAX_TOUR_SYSTEMS` resolves to 2.
    snapshot, ship, cons = _cap_sweep_board()
    out = solve_tour(snapshot, ship, cons(max_tour_systems=0), MODEL,
                     objective="profit")
    assert out["feasible"], out
    assert len({l["system_symbol"] for l in out["legs"]}) <= 2, out

def test_effective_tour_systems_clamps_to_sane_range():
    # sp-syaz robustness (review minor 2): the EFFECTIVE per-tour system cap is clamped
    # to [MAX_TOUR_SYSTEMS, MAX_HOPS_DEFAULT] AFTER the falsy-zero fallback, mirroring the
    # existing `max_hops = min(max_hops, MAX_HOPS_DEFAULT)` clamp. 0/absent -> the default
    # 2 (byte-identical); the degenerate 1 (a single-system, no-trade tour) FLOORS to 2;
    # an over-large request is CAPPED at the ceiling so it can't blow up the beam's
    # branching factor.
    from utils.tour_solver import _effective_tour_systems, MAX_HOPS_DEFAULT
    assert _effective_tour_systems({}) == 2                            # absent -> default
    assert _effective_tour_systems({"max_tour_systems": 0}) == 2       # falsy zero -> default
    assert _effective_tour_systems({"max_tour_systems": 1}) == 2       # degenerate 1 -> floored
    assert _effective_tour_systems({"max_tour_systems": 3}) == 3       # in-range -> passthrough
    assert _effective_tour_systems({"max_tour_systems": 10_000}) == MAX_HOPS_DEFAULT  # huge -> ceiling

def test_out_of_horizon_lane_invisible_until_sink_system_allowed():
    # sp-mtvg mechanism lock (the live-replay result, distilled): a good sourced cheap
    # in S1 with its ONLY rich sink in S2. This is NOT dropped by any good/price/volume
    # filter — it is simply absent whenever S2 is outside allowed_systems, because the
    # sink market never enters the snapshot the solver sees. The fix is observability;
    # this test PINS that the horizon guard itself is unchanged, so an accidental future
    # widening (which the flat-hop travel model would mis-price) trips here.
    snapshot = [
        snap("SRC", "S1", "LASER_RIFLES", ask=16549, bid=0, tv=30),   # cheap export source
        snap("SINK", "S2", "LASER_RIFLES", ask=61320, bid=30627, tv=6),  # rich import sink
    ]
    ship = dict(ship_symbol="H", current_waypoint="SRC", current_system="S1",
                hold_capacity=225, fuel_current=2300, fuel_capacity=2300,
                engine_speed=36, cargo=[])

    def cons(allowed):
        return dict(max_hops=6, max_spend=5_000_000, min_margin_per_unit=1,
                    working_capital_reserve=0, allowed_systems=allowed,
                    max_snapshot_age_minutes=75, expected_model_version="1@e")

    def laser_units(out):
        return sum(t["units"] for l in out["legs"] for t in l["trades"]
                   if t["good_symbol"] == "LASER_RIFLES")

    # Sink system out of the tour graph (the real ZC66-hauler horizon): lane invisible.
    only_src = solve_tour(snapshot, ship, cons(["S1"]), MODEL)
    assert laser_units(only_src) == 0

    # Sink system in scope: the SAME good, prices, and volumes now trade — proving the
    # exclusion was purely the missing sink system, never a good-level filter.
    both = solve_tour(snapshot, ship, cons(["S1", "S2"]), MODEL)
    assert both["feasible"]
    assert laser_units(both) > 0


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


def test_solver_reserve_zeroes_budget_reports_reserve_exceeds_budget():
    # sp-avt4: reserve >= max_spend zeroes spend_cap BEFORE the solver looks at the
    # market. Pre-fix this read identically to a genuinely dead market (both hit the
    # generic "no profitable allocation" reason), costing 70+ min of misdiagnosis in
    # the 2026-07-11 fleet-dark P0. The market here is a deliberately strong, real
    # arbitrage (100 -> 200) to prove the reason names the BUDGET as the cause, not
    # the market — a dead market would land on a different reason (see the next test).
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),
        snap("B", "S1", "G", ask=999, bid=200, tv=40),
    ]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=80, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    cons = dict(max_hops=4, max_spend=50_000, min_margin_per_unit=1,
                working_capital_reserve=50_000,  # reserve == max_spend -> spend_cap 0
                allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert not out["feasible"]
    assert out["infeasible_reason"].startswith("reserve_exceeds_budget"), out["infeasible_reason"]
    assert "max_spend 50000" in out["infeasible_reason"]
    assert "reserve 50000" in out["infeasible_reason"]


def test_solver_genuine_market_death_keeps_generic_reason():
    # Ample budget, but no counterpart sink exists anywhere in the snapshot for the
    # only tradeable good — genuine market infeasibility, the ORIGINAL failure class
    # this bead must keep distinguishable from a zeroed budget.
    snapshot = [snap("A", "S1", "G", ask=100, bid=90, tv=40)]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=80, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    cons = dict(max_hops=4, max_spend=50_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert not out["feasible"]
    assert out["infeasible_reason"] in ("no_candidate_tours", "no_profitable_tour"), out["infeasible_reason"]


def test_solver_held_cargo_liquidates_even_when_reserve_zeroes_budget():
    # sp-avt4: the new reserve_exceeds_budget fast-fail must NOT swallow the sp-m5kv
    # held-liquidation exemption — sells of cargo already aboard at launch have no
    # acquisition cost and are exempt from the spend_cap/afford gate in
    # score_sequence. A hull carrying stranded cargo can have a genuinely feasible
    # liquidation-only tour even though reserve has zeroed the FRESH-trade budget.
    # (Companion to test_solver_held_cargo_liquidates_without_buy_leg above, which
    # zeroes spend_cap via max_spend=0 instead of via reserve.)
    snapshot = [snap("B", "S1", "MEDICINE", ask=999, bid=1800, tv=40)]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=80, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[dict(good_symbol="MEDICINE", units=40)])
    cons = dict(max_hops=4, max_spend=50_000,
                working_capital_reserve=50_000,  # reserve == max_spend -> spend_cap 0
                min_margin_per_unit=1, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out
    sells = [(l["waypoint_symbol"], t["units"]) for l in out["legs"]
             for t in l["trades"] if not t["is_buy"] and t["good_symbol"] == "MEDICINE"]
    assert ("B", 40) in sells, out


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
    assert out["held_liquidation"] == 0, out             # sp-bc27: no held cargo -> no liquidation revenue


def test_solver_reports_held_liquidation_split():
    # sp-bc27 (Admiral ruling C): a laden-hull plan reports the held-cargo
    # liquidation REVENUE apart from fresh-trade profit, while projected_profit
    # stays the TOTAL (fresh + liquidation) that ranks selection. The hull holds
    # 40 MEDICINE (liquidates at B, no source anywhere -> no buy leg) AND flies a
    # fresh G arb (buy A -> sell B). The split must equal the MEDICINE sell
    # revenue exactly; the total must remain fresh + liquidation (unchanged).
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=40),           # fresh buy
        snap("B", "S1", "G", ask=999, bid=300, tv=40),          # fresh sink
        # MEDICINE is sink-ONLY: ask (9999) > bid (1800) so it is never a
        # profitable source, even on a revisit — every MEDICINE sell is
        # launch-liquidation of the held load, never a bought-and-resold arb.
        snap("B", "S1", "MEDICINE", ask=9999, bid=1800, tv=40),
    ]
    ship = dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=80, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[dict(good_symbol="MEDICINE", units=40)])
    cons = dict(max_hops=4, max_spend=100_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    out = solve_tour(snapshot, ship, cons, MODEL)
    assert out["feasible"], out

    # MEDICINE only ever sells (no source market) -> every MEDICINE sell is
    # launch-liquidation revenue.
    liq_rev = sum(t["units"] * t["expected_unit_price"]
                  for l in out["legs"] for t in l["trades"]
                  if not t["is_buy"] and t["good_symbol"] == "MEDICINE")
    fresh_manifest = sum(
        t["units"] * t["expected_unit_price"] * (-1 if t["is_buy"] else 1)
        for l in out["legs"] for t in l["trades"] if t["good_symbol"] != "MEDICINE")
    total_manifest = sum(
        t["units"] * t["expected_unit_price"] * (-1 if t["is_buy"] else 1)
        for l in out["legs"] for t in l["trades"])

    assert liq_rev > 0 and fresh_manifest > 0, out          # both a fresh arb and a liquidation
    assert out["held_liquidation"] == liq_rev, out          # split == liquidation-leg revenue
    assert out["projected_profit"] == total_manifest, out   # total UNCHANGED (fresh + liquidation)
    # Fresh-trade profit is the honest remainder the projection reports apart.
    assert out["projected_profit"] - out["held_liquidation"] == fresh_manifest, out


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


# --- sp-dchv Lane C: haul-to-storage deposit sinks -------------------------

def _dc_cons(**over):
    base = dict(max_hops=4, max_spend=1_000_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")
    base.update(over)
    return base


def _dc_ship(hold=80, cargo=None):
    return dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=hold, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=cargo or [])


def _deposit(good, units_wanted, synthetic_bid, wp="W", system="S1"):
    return dict(good_symbol=good, units_wanted=units_wanted,
                synthetic_bid=synthetic_bid, storage_waypoint=wp,
                storage_system=system)


def test_deposit_beats_weak_arb_sell():
    # Buy G cheap at A (ask 100). A WEAK market sink at B (bid 150, margin 50) and a
    # home warehouse DEPOSIT sink at W (synthetic bid 600, margin 500). The source
    # is scarce — tv=20 caps the foreign buy pool at 40u total (A-cap 2*tv) — so the
    # two sinks COMPETE for it, and the higher-margin deposit must win the space
    # (the emergent opportunity-cost property: hold goes to whichever earns more).
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=20),   # scarce foreign source
        snap("B", "S1", "G", ask=999, bid=150, tv=40),  # weak arb sink
    ]
    out = solve_tour(snapshot, _dc_ship(hold=40), _dc_cons(), MODEL,
                     deposit_candidates=[_deposit("G", 40, 600)])
    assert out["feasible"], out
    deposits = [(l["waypoint_symbol"], t["units"]) for l in out["legs"]
                for t in l["trades"] if t.get("is_deposit")]
    arb_sells = sum(t["units"] for l in out["legs"] for t in l["trades"]
                    if not t["is_buy"] and not t.get("is_deposit"))
    assert ("W", 40) in deposits, out           # depositing beat the weak arb sell
    assert arb_sells == 0, out                  # nothing left for the market sink
    assert out["deposit_value"] == 40 * 600, out


def test_strong_arb_beats_weak_deposit():
    # Mirror: a STRONG market sink (bid 900, margin 800) beats a WEAK deposit
    # (synthetic 150, margin 50) when the source is scarce (tv=20 → 40u total). The
    # solver's own profit-max prices the deposit against the arb sell and picks the
    # arb — the deposit is not stocked.
    snapshot = [
        snap("A", "S1", "G", ask=100, bid=90, tv=20),   # scarce foreign source
        snap("B", "S1", "G", ask=999, bid=900, tv=40),  # strong arb sink
    ]
    out = solve_tour(snapshot, _dc_ship(hold=40), _dc_cons(), MODEL,
                     deposit_candidates=[_deposit("G", 40, 150)])
    assert out["feasible"], out
    deposits = sum(t["units"] for l in out["legs"] for t in l["trades"]
                   if t.get("is_deposit"))
    arb_sells = [(l["waypoint_symbol"], t["units"]) for l in out["legs"]
                 for t in l["trades"] if not t["is_buy"] and not t.get("is_deposit")]
    assert deposits == 0, out                   # weak deposit lost to the strong arb
    assert ("B", 40) in arb_sells, out
    assert out["deposit_value"] == 0, out


def test_deposit_respects_units_wanted_cap():
    # Hold (80) and foreign supply (2*40=80 via the A-cap) allow 80u, but the sink
    # only wants 25 (the Go-side demand/space/ceiling cap). The plan deposits 25.
    snapshot = [snap("A", "S1", "G", ask=100, bid=90, tv=40)]
    out = solve_tour(snapshot, _dc_ship(hold=80), _dc_cons(), MODEL,
                     deposit_candidates=[_deposit("G", 25, 600)])
    assert out["feasible"], out
    dep = sum(t["units"] for l in out["legs"] for t in l["trades"]
              if t.get("is_deposit"))
    assert dep == 25, out                       # capped at units_wanted


def test_deposit_sink_has_no_a_cap_and_flat_price():
    # A market sink is A-capped at 2*trade_volume tranches AND decays per tranche.
    # The deposit sink has NEITHER: it absorbs units_wanted in ONE flat tranche at
    # the synthetic bid. Foreign source tv=40 (2-tranche cap 80) + hold 80 supply
    # 60u; the sink wants 60 and takes all 60 at a flat 600.
    snapshot = [snap("A", "S1", "G", ask=100, bid=90, tv=40)]
    out = solve_tour(snapshot, _dc_ship(hold=80), _dc_cons(), MODEL,
                     deposit_candidates=[_deposit("G", 60, 600)])
    assert out["feasible"], out
    dep_units = sum(t["units"] for l in out["legs"] for t in l["trades"]
                    if t.get("is_deposit"))
    dep_prices = {t["expected_unit_price"] for l in out["legs"]
                  for t in l["trades"] if t.get("is_deposit")}
    assert dep_units == 60, out                 # 60 absorbed, no A-cap
    assert dep_prices == {600}, out             # flat synthetic price, no decay


def test_launch_cargo_liquidates_at_market_never_deposited():
    # The hull launches holding 40 G with NO profitable foreign source (ask 999).
    # Even though the warehouse deposit sink pays far more (600) than the market
    # (100), launch cargo is NEVER deposited — a deposit requires a real buy leg, so
    # the held load liquidates at the market (m5kv). This keeps held-liquidation
    # accounting clean and lets bought-for-deposit cargo strand-sell if a deposit
    # fails at execution (the Go re-plan then sells it as held cargo).
    snapshot = [snap("B", "S1", "G", ask=999, bid=100, tv=40)]  # market sink only
    out = solve_tour(snapshot,
                     _dc_ship(hold=80, cargo=[dict(good_symbol="G", units=40)]),
                     _dc_cons(), MODEL,
                     deposit_candidates=[_deposit("G", 40, 600)])
    assert out["feasible"], out
    deposits = sum(t["units"] for l in out["legs"] for t in l["trades"]
                   if t.get("is_deposit"))
    market_sells = [(l["waypoint_symbol"], t["units"]) for l in out["legs"]
                    for t in l["trades"] if not t["is_buy"] and not t.get("is_deposit")]
    assert deposits == 0, out                          # launch cargo NOT deposited
    assert ("B", 40) in market_sells, out              # it liquidated at the market
    assert out["deposit_value"] == 0, out
    assert out["held_liquidation"] == 40 * 100, out    # launch liquidation, not deposit


def test_no_deposit_candidates_leaves_deposit_value_zero():
    # The pre-sp-dchv shape: no deposit_candidates -> deposit_value 0 and no trade
    # ever flagged is_deposit (existing arb planning byte-identical).
    snapshot = [snap("A", "S1", "G", 100, 90, tv=40),
                snap("B", "S1", "G", 999, 300, tv=40)]
    out = solve_tour(snapshot, _dc_ship(hold=40), _dc_cons(), MODEL)
    assert out["feasible"], out
    assert out["deposit_value"] == 0, out
    assert all(not t.get("is_deposit") for l in out["legs"] for t in l["trades"]), out


def test_deposit_value_split_and_projected_profit_total():
    # A pure pre-positioning tour: buy 40 G foreign @100, deposit @600. deposit_value
    # is the synthetic revenue (40*600); projected_profit is the TOTAL that ranks the
    # tour (synthetic value - foreign spend = the savings). Fresh cash profit
    # (projected_profit - held_liquidation - deposit_value) is the NEGATIVE foreign
    # outlay — honest: a deposit realizes no cash, only future contract-sourcing
    # savings.
    snapshot = [snap("A", "S1", "G", ask=100, bid=90, tv=40)]
    out = solve_tour(snapshot, _dc_ship(hold=40), _dc_cons(), MODEL,
                     deposit_candidates=[_deposit("G", 40, 600)])
    assert out["feasible"], out
    dep_rev = sum(t["units"] * t["expected_unit_price"] for l in out["legs"]
                  for t in l["trades"] if t.get("is_deposit"))
    spend = sum(t["units"] * t["expected_unit_price"] for l in out["legs"]
                for t in l["trades"] if t["is_buy"])
    assert out["deposit_value"] == dep_rev == 40 * 600, out
    assert out["projected_profit"] == dep_rev - spend, out      # total = synthetic - spend
    assert out["held_liquidation"] == 0, out
    fresh = out["projected_profit"] - out["held_liquidation"] - out["deposit_value"]
    assert fresh == -spend, out                                 # honest negative cash outlay


# --- sp-78ai L3: cross-container absorption netting ------------------------

def _absorb(wp, good, side="sell", planned=0, recovering=0.0):
    return dict(waypoint_symbol=wp, good_symbol=good, side=side,
                units_planned=planned, units_recovering=recovering)


def _one_sink_board():
    # buy G cheap at A (ask 100, tv 40), one sink at B (bid 1000, tv 40). The
    # LIMITED|WEAK sell-decay is 0.9, so B's A-cap-2 sell pool is [(40,1000),(40,900)]:
    # step 0 at the live bid, step 1 one decay down. The single sink isolates the
    # netting effect on that one pool.
    return [snap("A", "S1", "G", ask=100, bid=90, tv=40),
            snap("B", "S1", "G", ask=999, bid=1000, tv=40)]


def _abs_ship():
    return dict(ship_symbol="H", current_waypoint="A", current_system="S1",
                hold_capacity=80, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])


def _abs_cons():
    return dict(max_hops=4, max_spend=1_000_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e")


def _b_sells(out):
    return [(t["units"], t["expected_unit_price"]) for l in out["legs"]
            for t in l["trades"] if l["waypoint_symbol"] == "B" and not t["is_buy"]]


def test_net_absorption_unit_semantics():
    # The netting primitive in isolation: planned drops from the HEAD (price + capacity),
    # recovering from the TAIL (capacity only), both quantized UP to whole tranches.
    base = [(40, 1000), (40, 900)]
    assert net_absorption(base, 0, 0.0, 40) == base                # no-op: identity
    assert net_absorption(base, 40, 0.0, 40) == [(40, 900)]        # 1 planned: advance to step 1
    assert net_absorption(base, 0, 40.0, 40) == [(40, 1000)]       # 1 recovering: keep step 0
    assert net_absorption(base, 40, 40.0, 40) == []               # both: fully absorbed
    assert net_absorption(base, 80, 0.0, 40) == []               # 2 planned: fully absorbed
    assert net_absorption(base, 1, 0.0, 40) == [(40, 900)]        # ceil: any planned bumps a step
    assert net_absorption(base, 0, 1.0, 40) == [(40, 1000)]       # ceil: any recovering drops a tranche


def test_netting_planned_advances_price_and_capacity():
    # 1 PLANNED tranche from another container at sink B: the plan's sell there both
    # loses a tranche of CAPACITY (80 -> 40 sellable) AND prices at the ADVANCED step
    # (900, not the live 1000) — someone is taking the head tranche at 1000.
    out = solve_tour(_one_sink_board(), _abs_ship(), _abs_cons(), MODEL,
                     absorption=[_absorb("B", "G", planned=40)])
    assert out["feasible"], out
    assert _b_sells(out) == [(40, 900)], out          # one tranche, at the advanced price
    buys = sum(t["units"] for l in out["legs"] for t in l["trades"] if t["is_buy"])
    assert buys == 40, out                             # capacity netted: only 40 flows


def test_netting_recovering_consumes_capacity_only():
    # 1 RECOVERING tranche (a decayed EXECUTED shadow) at sink B: same CAPACITY loss
    # (80 -> 40 sellable) but pricing STAYS at step 0 (1000) — the live quote already
    # reflects the crush, so re-pricing would double-count it. This is the price-honesty
    # split vs the planned case above: identical capacity, different price.
    out = solve_tour(_one_sink_board(), _abs_ship(), _abs_cons(), MODEL,
                     absorption=[_absorb("B", "G", recovering=40.0)])
    assert out["feasible"], out
    assert _b_sells(out) == [(40, 1000)], out          # one tranche, at the LIVE (un-advanced) price
    buys = sum(t["units"] for l in out["legs"] for t in l["trades"] if t["is_buy"])
    assert buys == 40, out


def test_netting_zero_absorption_is_byte_identical():
    # Regression (the additive-field contract): an empty/None absorption request plans
    # EXACTLY the pre-sp-78ai tour. Compared field-by-field against the no-arg call.
    board, ship, cons = _one_sink_board(), _abs_ship(), _abs_cons()
    baseline = solve_tour(board, ship, cons, MODEL)
    for absorption in ([], None):
        out = solve_tour(board, ship, cons, MODEL, absorption=absorption)
        assert out == baseline, (absorption, out, baseline)
    # And the baseline really does take BOTH tranches (so the netting tests above are
    # cutting real depth, not a degenerate single-tranche plan). Sells emit
    # price-ascending (the executor's dock order), so step 1 (900) precedes step 0.
    assert _b_sells(baseline) == [(40, 900), (40, 1000)], baseline


def test_netting_fully_absorbed_market_reroutes():
    # Sink B fully absorbed (2 PLANNED tranches = the whole A-cap) yields NO tranche
    # there; with a clean alternative sink C the plan routes to C instead. Proves the
    # netting removes availability at the crowded sink without poisoning the tour.
    board = _one_sink_board() + [snap("C", "S1", "G", ask=999, bid=950, tv=40)]
    out = solve_tour(board, _abs_ship(), _abs_cons(), MODEL,
                     absorption=[_absorb("B", "G", planned=80)])
    assert out["feasible"], out
    assert _b_sells(out) == [], out                    # nothing sellable at the absorbed sink
    c_sells = sum(t["units"] for l in out["legs"] for t in l["trades"]
                  if l["waypoint_symbol"] == "C" and not t["is_buy"])
    assert c_sells > 0, out                            # the tour rerouted to the clean sink


def test_netting_buy_side_advances_source_price():
    # Absorption nets the BUY (ask) side too: a PLANNED buy-side reservation at source A
    # advances the source's price ladder, so the plan pays the grown step (110) and can
    # only take one buy tranche. The netting is symmetric across sides.
    out = solve_tour(_one_sink_board(), _abs_ship(), _abs_cons(), MODEL,
                     absorption=[_absorb("A", "G", side="buy", planned=40)])
    assert out["feasible"], out
    buys = [(t["units"], t["expected_unit_price"]) for l in out["legs"]
            for t in l["trades"] if l["waypoint_symbol"] == "A" and t["is_buy"]]
    assert buys == [(40, 110)], out                    # one tranche at the advanced buy step


# --- sp-1wp8: selection objective (profit default, rate switchable) ---

def _objective_fixture():
    """Two disjoint lanes from HOME: a FAST small one (A1->A2, 8k in ~4min,
    ~120k/hr) and a SLOW bigger one (B1->B2, 12k in ~2h, ~6k/hr). Profit-primary
    tours the big/combined manifest; rate-primary must take the fast lane."""
    snapshot = [
        snap("A1", "S1", "G_FAST", ask=100, bid=0, tv=40),
        snap("A2", "S1", "G_FAST", ask=999, bid=300, tv=40),
        snap("B1", "S1", "G_BIG", ask=100, bid=0, tv=40),
        snap("B2", "S1", "G_BIG", ask=999, bid=400, tv=40),
    ]
    ship = dict(ship_symbol="H", current_waypoint="HOME", current_system="S1",
                hold_capacity=40, fuel_current=400, fuel_capacity=400,
                engine_speed=30, cargo=[])
    times = {("HOME", "A1"): 60, ("A1", "A2"): 60,
             ("HOME", "B1"): 3600, ("B1", "B2"): 3600}

    def travel(a, b):
        if a == b:
            return 0
        return times.get((a, b)) or times.get((b, a)) or 3600

    cons = dict(max_hops=4, max_spend=100_000, min_margin_per_unit=1,
                working_capital_reserve=0, allowed_systems=["S1"],
                max_snapshot_age_minutes=75, expected_model_version="1@e",
                _travel_fn=travel)
    return snapshot, ship, cons


def test_default_objective_is_profit_primary_and_rate_flips_to_fast_dense():
    snapshot, ship, cons = _objective_fixture()

    # Default (no objective, no env): profit-primary — the winner books the BIG
    # sink's revenue (a B2 sell leg exists), whatever else it packs.
    out_profit = solve_tour(snapshot, ship, cons, MODEL)
    assert out_profit["feasible"]
    profit_sinks = {l["waypoint_symbol"] for l in out_profit["legs"]
                    for t in l["trades"] if not t["is_buy"]}
    assert "B2" in profit_sinks, f"profit-primary must take the bigger manifest, sold at {profit_sinks}"

    # objective="rate": cph-primary — the winner is the FAST lane alone (~120k/hr
    # beats any manifest that rides the 2h B corridor), with a strictly higher cph
    # and strictly lower absolute profit than the profit-primary choice (proof the
    # two objectives genuinely diverged on this fixture).
    out_rate = solve_tour(snapshot, ship, cons, MODEL, objective="rate")
    assert out_rate["feasible"]
    rate_stops = {l["waypoint_symbol"] for l in out_rate["legs"]}
    assert rate_stops <= {"A1", "A2"}, f"rate-primary must fly the fast lane only, got {rate_stops}"
    assert out_rate["projected_credits_per_hour"] > out_profit["projected_credits_per_hour"]
    assert out_rate["projected_profit"] < out_profit["projected_profit"]


def test_objective_env_var_selects_rate_and_explicit_argument_wins(monkeypatch):
    snapshot, ship, cons = _objective_fixture()

    # Env-selected rate mode (the production switch: no proto change).
    monkeypatch.setenv("TOUR_SOLVER_OBJECTIVE", "rate")
    out_env = solve_tour(snapshot, ship, cons, MODEL)
    stops = {l["waypoint_symbol"] for l in out_env["legs"]}
    assert stops <= {"A1", "A2"}, f"TOUR_SOLVER_OBJECTIVE=rate must select rate mode, got {stops}"

    # An explicit argument beats the env (the replay harness's lever).
    out_explicit = solve_tour(snapshot, ship, cons, MODEL, objective="profit")
    sinks = {l["waypoint_symbol"] for l in out_explicit["legs"]
             for t in l["trades"] if not t["is_buy"]}
    assert "B2" in sinks, f"explicit objective='profit' must override the env, sold at {sinks}"

    # An unrecognized env value fails toward the proven default (profit).
    monkeypatch.setenv("TOUR_SOLVER_OBJECTIVE", "bogus")
    out_bogus = solve_tour(snapshot, ship, cons, MODEL)
    bogus_sinks = {l["waypoint_symbol"] for l in out_bogus["legs"]
                   for t in l["trades"] if not t["is_buy"]}
    assert "B2" in bogus_sinks


def test_sort_scored_zero_time_falls_back_to_profit_ordering():
    # The sp-1wp8 regression pin: a seconds<=0 candidate (degenerate input) drops
    # the WHOLE selection back to profit ordering — a divide-by-zero artifact
    # (cph=0 on instant profit) must never decide selection in either direction.
    from utils.tour_solver import _sort_scored, OBJECTIVE_RATE, OBJECTIVE_PROFIT
    scored = [
        (dict(profit=50, cph=99_999.0, seconds=10), "fast"),
        (dict(profit=100, cph=0.0, seconds=0), "instant-degenerate"),
    ]
    effective = _sort_scored(scored, OBJECTIVE_RATE)
    assert effective == OBJECTIVE_PROFIT
    assert scored[0][1] == "instant-degenerate"  # profit ordering: 100 first

    # With every candidate carrying real time, rate mode orders by cph.
    scored = [
        (dict(profit=100, cph=6_000.0, seconds=60_000), "slow-big"),
        (dict(profit=50, cph=99_999.0, seconds=10), "fast"),
    ]
    effective = _sort_scored(scored, OBJECTIVE_RATE)
    assert effective == OBJECTIVE_RATE
    assert scored[0][1] == "fast"

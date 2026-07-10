"""Depth-aware multi-hop trade-tour solver (sp-1ek0 P1a).

Two-stage solve per the approved design (spec decision #4):

1. `beam_sequences` — beam search over hop sequences (width BEAM_WIDTH),
   ranked by an optimistic multi-good hold-PACKING bound (sp-gm00: the bound
   fills the hold across every good tradeable on a hop, each capped at its
   A-cap tranche depth, so a diverse cluster that fills a heavy hull out-ranks
   a thin single good a vol-6 sink could never fill). A tour touches at
   most MAX_TOUR_SYSTEMS distinct systems INCLUDING the ship's start system
   (Admiral simplification 2026-07-09: start system + one gate neighbor).
   Gate-adjacency itself is delegated to `allowed_systems` — the Go caller
   computes gate neighbors; the solver never sees the jump-gate graph.
   Crossing COUNT is not hard-capped: each crossing costs
   INTER_SYSTEM_TRAVEL_SECONDS in the $/hr objective, which prices
   ping-ponging out naturally.

2. `score_sequence` — greedy tranche allocation over the fitted impact
   curves. Greedy over sorted marginal-profit (buy-tranche, sell-tranche)
   pairings IS the LP solution here: once buy/sell tranches are enumerated
   per (good, buy-leg, sell-leg) pairing, marginal profits are fixed and
   non-increasing per pairing, and hold capacity / spend are the only
   couplings — so taking the globally best marginal pairing first is exact
   (plan Task 5 note). If a future case breaks that independence, swap in
   OR-Tools GLOP behind the same function signature.

Selection (Admiral decision 2026-07-09, amending spec §Solve step 4):
winner = max projected PROFIT; credits/hour is computed, reported in the
response, and used as the tiebreak between equal-profit tours. Rationale:
single-tour $/hr prefers concentrated dumping (it ignores the sink-crush
externality the D39 incident demonstrated); the graduation gate measures
REALIZED $/hr in the field and catches profit-primary underperformance
before autonomy. Every hop must add positive marginal profit — allocations
only exist at margin >= the min-margin gate, and hops with no allocation
are pruned from the plan.

Ladder cap (harbormaster A-capped ruling, same date): no tour plan may
schedule more than MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE tranches for
the same (market, good, side) across the WHOLE tour, revisits included —
the D39 4-tranche dump shape is unplannable, while a source can still fill
the hold (2 buy tranches) and a sink can still absorb a full hold across
its first two tranches. The cap is the interim stand-in for phase-2
recovery-externality pricing; when that lands, the cap can relax to the
economics.

Money guards (fail closed, RULINGS #4): effective spend cap is
max(0, max_spend - working_capital_reserve); a pairing must clear
max(1, min_margin_per_unit) per unit. Sells of cargo already aboard at
launch are cash recovery (no acquisition cost in this plan) and are exempt
from the margin gate but still bounded by the sink's tranche depth.

Prices: tranche 0 is quoted at the live snapshot price; each further
tradeVolume-sized tranche is decayed (sells) or grown (buys) by the fitted
per-(supply|activity)-tier factor. Missing tier/side falls back to
conservative defaults (never quote-flat) and logs `tier-missing` once per
tier per process.

Travel time: when the request carries TourWaypoint coords, intra-system
hops use the routing engine's CRUISE formula (distance x multiplier /
engine_speed — mirrors utils/routing_engine.FlightMode.CRUISE); an
inter-system leg is priced as hop-to-gate allowance + jump cooldown +
arrival-hop allowance (named consts — gate positions are not in the
request). No coords -> flat named defaults with a logged warning (degraded
mode, never silent).

Everything is pure and request-carried: no DB, no clock beyond staleness
filtering, dict shapes mirror routing.proto snake_case 1:1.
"""
import logging
import math
import time

logger = logging.getLogger(__name__)

MAX_TOUR_SYSTEMS = 2          # Admiral revision 2026-07-09: start system + 1 gate neighbor
MAX_HOPS_DEFAULT = 6          # spec: maxHops <= 6
BEAM_WIDTH = 50               # spec decision #4
FULL_SCORE_TOP_N = 20         # sequences fully tranche-scored after the beam
TOP_REJECTED_N = 3            # rejected alternatives reported (observability parity)
MAX_SNAPSHOT_AGE_MINUTES_DEFAULT = 75   # mirrors trading's maxListingAge
DEFAULT_SELL_DECAY = 0.9      # conservative fallback when tier not fitted
DEFAULT_BUY_GROWTH = 1.1
# Planned-depth ladder cap (harbormaster A-capped ruling 2026-07-09): interim
# stand-in for phase-2 recovery-externality pricing — see module docstring.
MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE = 2
CRUISE_TIME_MULTIPLIER = 31   # mirrors utils/routing_engine.FlightMode.CRUISE
GATE_HOP_ALLOWANCE_SECONDS = 450   # to-gate / from-gate hop (gate coords not carried)
JUMP_COOLDOWN_SECONDS = 900        # gate jump + cooldown
INTRA_SYSTEM_TRAVEL_SECONDS = 300   # flat fallback when no coords in the request
INTER_SYSTEM_TRAVEL_SECONDS = 1800  # flat fallback; = 2*GATE_HOP + JUMP_COOLDOWN scale
DWELL_SECONDS_PER_LEG = 60          # dock + transact allowance per market stop

_warned_tiers = set()


def tranche_prices(quote, trade_volume, tier, model, is_buy, max_units):
    """Piecewise price schedule: list of (units, unit_price) tranches.

    Tranche 0 is at the live quote; each subsequent tradeVolume-sized
    tranche is multiplied by the tier's fitted decay (sell) / growth (buy)
    factor. Missing tier or side -> conservative default, logged once.
    """
    if quote <= 0 or trade_volume <= 0 or max_units <= 0:
        return []
    entry = (model.get("impact") or {}).get(tier) or {}
    key = "buy_growth_per_step" if is_buy else "sell_decay_per_step"
    factor = entry.get(key)
    if factor is None:
        factor = DEFAULT_BUY_GROWTH if is_buy else DEFAULT_SELL_DECAY
        if (tier, key) not in _warned_tiers:
            _warned_tiers.add((tier, key))
            logger.info("tour-solver: tier-missing %s (%s) — conservative default %.2f",
                        tier, key, factor)
    tranches = []
    price = float(quote)
    left = max_units
    while left > 0:
        units = min(trade_volume, left)
        rounded = int(round(price))
        if rounded <= 0 and not is_buy:
            break  # decayed to worthless — deeper sell tranches add nothing
        tranches.append((units, rounded))
        left -= units
        price *= factor
    return tranches


class _TranchePool:
    """Consumable tranche schedule shared per (waypoint, good, side)."""

    def __init__(self, tranches):
        self.tranches = tranches
        self.idx = 0
        self.used = 0

    def head(self):
        while self.idx < len(self.tranches):
            units, price = self.tranches[self.idx]
            remaining = units - self.used
            if remaining > 0:
                return remaining, price
            self.idx += 1
            self.used = 0
        return 0, 0

    def take(self, units):
        self.used += units


def _tier_of(row):
    return f"{row.get('supply', '')}|{row.get('activity', '')}"


def _build_markets(rows):
    markets = {}
    for row in rows:
        m = markets.setdefault(row["waypoint_symbol"],
                               {"system": row["system_symbol"], "goods": {}})
        m["goods"][row["good_symbol"]] = row
    return markets


def _make_travel_fn(constraints, markets, ship, waypoints=None):
    """Travel-seconds fn(a, b). Precedence: caller-supplied `_travel_fn`
    hook > coordinate mode (CRUISE formula on request-carried TourWaypoint
    coords) > flat named defaults (degraded mode, logged warning)."""
    custom = constraints.get("_travel_fn")
    if callable(custom):
        return custom

    coords = {w["symbol"]: (w["x"], w["y"]) for w in (waypoints or [])}
    engine_speed = max(1, ship.get("engine_speed") or 1)

    def system_of(wp):
        if wp in markets:
            return markets[wp]["system"]
        if wp == ship["current_waypoint"]:
            return ship["current_system"]
        return None

    def hop(a, b):
        if a == b:
            return 0
        sys_a, sys_b = system_of(a), system_of(b)
        if sys_a and sys_b and sys_a != sys_b:
            # Gate positions are not request-carried: price the crossing as
            # named allowances around the fitted-scale jump cooldown.
            return 2 * GATE_HOP_ALLOWANCE_SECONDS + JUMP_COOLDOWN_SECONDS
        if a in coords and b in coords:
            distance = math.hypot(coords[b][0] - coords[a][0],
                                  coords[b][1] - coords[a][1])
            if distance == 0:
                return 0
            # Mirror of utils/routing_engine.FlightMode.CRUISE.travel_time.
            return max(1, int((distance * CRUISE_TIME_MULTIPLIER) / engine_speed))
        return INTRA_SYSTEM_TRAVEL_SECONDS

    if not coords:
        logger.warning(
            "tour-solver: request carries no waypoint coords — flat travel "
            "defaults in effect (degraded $/hr accuracy)")
    return hop


def score_sequence(seq, markets, ship, constraints, model, travel_fn):
    """Greedy tranche allocation over one hop sequence (the LP stage).

    Returns dict(profit, spend, seconds, cph, legs) where legs carry only
    the market stops with at least one trade (no-trade hops are pruned and
    travel re-chained). Hold accounting: a unit bought at leg i and sold at
    leg j occupies hold slots [i, j); launch cargo occupies from the start
    until its sell leg. Slot occupancy never exceeds hold_capacity, which is
    exactly the sells-then-buys dock order the executor uses.
    """
    n = len(seq)
    hold_cap = ship["hold_capacity"]
    initial = {}
    for item in ship.get("cargo") or []:
        if item["units"] > 0:
            initial[item["good_symbol"]] = initial.get(item["good_symbol"], 0) + item["units"]
    total_initial = sum(initial.values())
    spend_cap = max(0, constraints.get("max_spend", 0)
                    - constraints.get("working_capital_reserve", 0))
    min_margin = max(1, constraints.get("min_margin_per_unit", 0))
    pool_ceiling = hold_cap * n + total_initial

    buy_pools, sell_pools = {}, {}

    def pool(pools, wp, good, is_buy):
        pkey = (wp, good)
        if pkey not in pools:
            row = markets[wp]["goods"][good]
            quote = row["ask"] if is_buy else row["bid"]
            # Ladder cap: at most MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE
            # tranches per (market, good, side) across the whole tour,
            # revisits included (A-capped ruling — see module docstring).
            capped = min(pool_ceiling,
                         MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE * row["trade_volume"])
            pools[pkey] = _TranchePool(tranche_prices(
                quote, row["trade_volume"], _tier_of(row), model, is_buy, capped))
        return pools[pkey]

    # Candidate pairings. Buys and sells at repeat visits of the same market
    # share one pool per (waypoint, good) — depth is a property of the market,
    # not of the leg index.
    pairs = []  # (good, buy_leg or None for launch cargo, sell_leg)
    for j in range(n):
        for good, row in markets[seq[j]]["goods"].items():
            if row["bid"] <= 0:
                continue
            if initial.get(good):
                pairs.append((good, None, j))
            for i in range(j):
                brow = markets[seq[i]]["goods"].get(good)
                if brow and brow["ask"] > 0:
                    pairs.append((good, i, j))

    occ = [total_initial] * n   # hold occupancy per travel slot
    initial_left = dict(initial)
    spend = 0
    revenue = 0
    allocations = []            # (good, buy_leg, sell_leg, units, buy_price, sell_price)
    alive = list(pairs)

    while True:
        best = None
        for good, i, j in alive:
            sell_rem, sell_price = pool(sell_pools, seq[j], good, is_buy=False).head()
            if sell_rem <= 0:
                continue
            if i is None:
                left = initial_left.get(good, 0)
                if left <= 0 or sell_price < 1:
                    continue
                units = min(left, sell_rem)
                margin = sell_price          # cash recovery: no acquisition cost
                buy_price = 0
            else:
                buy_rem, buy_price = pool(buy_pools, seq[i], good, is_buy=True).head()
                if buy_rem <= 0 or buy_price <= 0:
                    continue
                margin = sell_price - buy_price
                if margin < min_margin:
                    continue
                slack = hold_cap - max(occ[i:j]) if j > i else 0
                afford = (spend_cap - spend) // buy_price
                units = min(buy_rem, sell_rem, slack, afford)
            if units <= 0:
                continue
            key = (margin, -j, -(i if i is not None else -1))
            if best is None or key > best[0]:
                best = (key, good, i, j, units, buy_price, sell_price)
        if best is None:
            break
        _, good, i, j, units, buy_price, sell_price = best
        pool(sell_pools, seq[j], good, is_buy=False).take(units)
        if i is None:
            initial_left[good] -= units
            for k in range(j, n):
                occ[k] -= units
        else:
            pool(buy_pools, seq[i], good, is_buy=True).take(units)
            spend += units * buy_price
            for k in range(i, j):
                occ[k] += units
        revenue += units * sell_price
        allocations.append((good, i, j, units, buy_price, sell_price))

    profit = revenue - spend
    # Held-liquidation revenue (sp-bc27, Admiral ruling C): the revenue from
    # sell tranches of cargo held at launch (buy_leg i is None — no acquisition
    # cost in this plan). It is a subset of `revenue` and thus of `profit`;
    # reported alongside the TOTAL so a projection can show fresh-trade profit
    # (profit - held_liquidation) and liquidation revenue apart. Selection still
    # ranks on total `profit`, so pure-liquidation tours stay feasible.
    held_liquidation = sum(units * sell_price
                           for _good, i, _j, units, _buy_price, sell_price in allocations
                           if i is None)

    # Assemble per-leg trades, then prune hops where nothing happens.
    leg_trades = [{} for _ in range(n)]  # (good, is_buy, price) -> units
    for good, i, j, units, buy_price, sell_price in allocations:
        if i is not None:
            k = (good, True, buy_price)
            leg_trades[i][k] = leg_trades[i].get(k, 0) + units
        k = (good, False, sell_price)
        leg_trades[j][k] = leg_trades[j].get(k, 0) + units

    legs = []
    for idx in range(n):
        if not leg_trades[idx]:
            continue
        trades = []
        entries = leg_trades[idx].items()
        for (good, is_buy, price), units in sorted(
                entries, key=lambda e: (e[0][1], e[0][0], e[0][2])):
            # sells (is_buy=False) sort first: dock order frees hold before buys
            trades.append(dict(good_symbol=good, units=units, is_buy=is_buy,
                               expected_unit_price=price))
        leg_profit = sum(t["units"] * t["expected_unit_price"] * (-1 if t["is_buy"] else 1)
                         for t in trades)
        legs.append(dict(waypoint_symbol=seq[idx],
                         system_symbol=markets[seq[idx]]["system"],
                         trades=trades,
                         projected_leg_profit=leg_profit,
                         travel_seconds_from_prev=0))

    seconds = 0
    prev = ship["current_waypoint"]
    for leg in legs:
        hop = int(travel_fn(prev, leg["waypoint_symbol"]))
        leg["travel_seconds_from_prev"] = hop
        seconds += hop + DWELL_SECONDS_PER_LEG
        prev = leg["waypoint_symbol"]

    cph = profit / (seconds / 3600.0) if seconds > 0 else 0.0
    return dict(profit=profit, spend=spend, seconds=seconds, cph=cph, legs=legs,
                held_liquidation=held_liquidation)


def beam_sequences(markets, ship, constraints, travel_fn):
    """Beam search over hop sequences; every prefix is a candidate tour.

    Ranking uses an optimistic MULTI-GOOD hold-packing bound (sp-gm00): for
    each appended hop, `pack_gain` fills the hold with every good profitably
    buyable at an earlier stop and sellable here — best undecayed spread
    first, each good capped at its A-cap tranche depth
    (MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE × trade_volume), the same
    ceiling the allocator can realize. The prior bound credited only the
    single best good over the FULL hold; that (a) over-valued a thin single
    good a shallow (e.g. vol-6) sink can never fill and (b) never surfaced a
    diverse cluster — which fills a heavy hull only by summing several goods —
    into the FULL_SCORE_TOP_N cut, so heavy hulls planned the same 7%-hold
    single-good manifests as light ones. The bound stays never-pessimistic:
    realized units per (good, buy-leg, sell-leg) can't exceed the A-cap depth,
    and the undecayed spread dominates every decayed tranche margin.

    Seeds are pruned by their best OUTGOING pack (a one-hop lookahead), not by
    their at-rest liquidation value alone: a rich cluster's SOURCE liquidates
    to nothing (empty hold, nothing to sell there) and would otherwise lose the
    width-BEAM_WIDTH seed cut to alphabetically-earlier thin markets, so the
    cluster would never be explored. The lookahead ranks the cut only — the
    stored beam score stays the real liquidation value, so a bare 1-hop seed
    never crowds the top-N scoring pool on lookahead credit it can't realize.
    Returns candidate sequences (tuples) sorted best-bound-first.
    """
    max_hops = constraints.get("max_hops") or MAX_HOPS_DEFAULT
    max_hops = min(max_hops, MAX_HOPS_DEFAULT)
    start_system = ship["current_system"]
    initial = {c["good_symbol"]: c["units"] for c in ship.get("cargo") or []}
    wps = sorted(markets)
    hold = ship["hold_capacity"]

    def liquidation_gain(wp):
        goods = markets[wp]["goods"]
        return sum(units * goods[g]["bid"] for g, units in initial.items()
                   if g in goods and goods[g]["bid"] > 0)

    def pack_gain(wp_from, wp_to):
        """Optimistic multi-good packing value for one hop (see docstring)."""
        goods_to = markets[wp_to]["goods"]
        spreads = []
        for good, brow in markets[wp_from]["goods"].items():
            srow = goods_to.get(good)
            if srow and brow["ask"] > 0 and srow["bid"] > brow["ask"]:
                depth = MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE * max(
                    1, min(brow["trade_volume"], srow["trade_volume"]))
                spreads.append((srow["bid"] - brow["ask"], depth))
        spreads.sort(reverse=True)
        gain, cap = 0, hold
        for spread, depth in spreads:
            if cap <= 0:
                break
            units = min(cap, depth)
            gain += spread * units
            cap -= units
        return gain

    def within_cap(*systems):
        return len(frozenset((start_system, *systems))) <= MAX_TOUR_SYSTEMS

    def seed_lookahead(wp):
        best = 0
        sys_from = markets[wp]["system"]
        for wp2 in wps:
            if wp2 != wp and within_cap(sys_from, markets[wp2]["system"]):
                g = pack_gain(wp, wp2)
                if g > best:
                    best = g
        return best

    beam, pool = [], []
    for wp in wps:
        if not within_cap(markets[wp]["system"]):
            continue
        beam.append(((wp,), frozenset({start_system, markets[wp]["system"]}),
                     liquidation_gain(wp)))
    beam.sort(key=lambda s: (-(s[2] + seed_lookahead(s[0][0])), s[0]))
    beam = beam[:BEAM_WIDTH]
    pool.extend(beam)

    for _ in range(1, max_hops):
        nxt = []
        for seq, systems, score in beam:
            for wp in wps:
                if wp == seq[-1]:
                    continue
                new_systems = systems | {markets[wp]["system"]}
                if len(new_systems) > MAX_TOUR_SYSTEMS:
                    continue
                gain = max(pack_gain(prev_wp, wp) for prev_wp in seq)
                nxt.append((seq + (wp,), new_systems, score + gain))
        nxt.sort(key=lambda s: (-s[2], s[0]))
        beam = nxt[:BEAM_WIDTH]
        pool.extend(beam)
        if not beam:
            break

    pool.sort(key=lambda s: (-s[2], s[0]))
    return [seq for seq, _, _ in pool]


def _infeasible(reason, model_version, top_rejected=None):
    return dict(feasible=False, infeasible_reason=reason, legs=[],
                projected_profit=0, projected_credits_per_hour=0.0,
                held_liquidation=0,
                top_rejected=top_rejected or [], model_version=model_version)


def solve_tour(snapshot, ship, constraints, model, waypoints=None):
    """Plan the best multi-hop trade tour for one hull. Pure; proto-shaped dicts.

    `waypoints` mirrors OptimizeTradeTourRequest.waypoints (coords for the
    real travel matrix); None/empty -> degraded flat travel with a warning.

    Fail-loud contract: missing artifact or version mismatch are structured
    infeasible reasons, never a silent fallback (spec error-handling table).
    """
    if not model:
        return _infeasible("model_artifact_missing", "")
    model_version = f"{model['fit_version']}@{model['era']}"
    expected = constraints.get("expected_model_version") or ""
    if not expected:
        return _infeasible("model_version_mismatch: expected_model_version not set",
                           model_version)
    if expected != model_version:
        return _infeasible(
            f"model_version_mismatch: expected {expected}, artifact {model_version}",
            model_version)

    age_cap = constraints.get("max_snapshot_age_minutes") or MAX_SNAPSHOT_AGE_MINUTES_DEFAULT
    cutoff = time.time() - age_cap * 60
    allowed = set(constraints.get("allowed_systems") or [ship["current_system"]])
    rows = [r for r in snapshot
            if r["observed_at_unix"] >= cutoff
            and r["system_symbol"] in allowed
            and (r["ask"] > 0 or r["bid"] > 0)]
    if not rows:
        return _infeasible("no_fresh_market_data", model_version)

    markets = _build_markets(rows)
    travel_fn = _make_travel_fn(constraints, markets, ship, waypoints)
    candidates = beam_sequences(markets, ship, constraints, travel_fn)
    if not candidates:
        return _infeasible("no_candidate_tours", model_version)

    scored = []
    seen = set()
    for seq in candidates[:FULL_SCORE_TOP_N]:
        result = score_sequence(seq, markets, ship, constraints, model, travel_fn)
        signature = tuple((l["waypoint_symbol"],
                           tuple((t["good_symbol"], t["units"], t["is_buy"],
                                  t["expected_unit_price"]) for t in l["trades"]))
                          for l in result["legs"])
        if signature in seen:
            continue
        seen.add(signature)
        summary = "→".join(l["waypoint_symbol"] for l in result["legs"]) or "→".join(seq)
        scored.append((result, summary))
    # Profit-primary selection, cph tiebreak (Admiral decision — docstring).
    scored.sort(key=lambda rs: (-rs[0]["profit"], -rs[0]["cph"], rs[1]))

    def rejected(entries, winner=None):
        # winner=None only on the infeasible path, where the sort invariant
        # guarantees every entry has profit <= 0 (first branch).
        out = []
        for result, summary in entries[:TOP_REJECTED_N]:
            if result["profit"] <= 0:
                reason = "no profitable allocation under tranche decay/guards"
            elif result["profit"] < winner["profit"]:
                reason = (f"profit {result['profit']:,} < winner "
                          f"{winner['profit']:,} (cph {result['cph']:,.0f}/hr)")
            else:
                reason = (f"profit tie, cph {result['cph']:,.0f}/hr <= winner "
                          f"{winner['cph']:,.0f}/hr")
            out.append(dict(summary=summary, reason=reason))
        return out

    if not scored or scored[0][0]["profit"] <= 0:
        return _infeasible("no_profitable_tour", model_version,
                           top_rejected=rejected(scored))

    best, best_summary = scored[0]
    return dict(feasible=True,
                infeasible_reason="",
                legs=best["legs"],
                projected_profit=best["profit"],
                projected_credits_per_hour=best["cph"],
                held_liquidation=best["held_liquidation"],
                top_rejected=rejected(scored[1:], winner=best),
                model_version=model_version)

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

Selection (Admiral decision 2026-07-09, amending spec §Solve step 4;
objective made switchable under sp-1wp8): the DEFAULT winner = max projected
PROFIT; credits/hour is computed, reported in the response, and used as the
tiebreak between equal-profit tours. The 2026-07-09 rationale: single-tour
$/hr prefers concentrated dumping (it ignores the sink-crush externality the
D39 incident demonstrated); the graduation gate measures REALIZED $/hr in
the field and catches profit-primary underperformance before autonomy.

sp-1wp8 (Admiral program order: the objective becomes $/HOUR) adds a
RATE-primary selection — winner = max projected cph, profit tiebreak — as an
explicit `objective` parameter / TOUR_SOLVER_OBJECTIVE env switch, default
"profit". Two things changed since the 2026-07-09 decision: (a) the
concentrated-dumping rationale is now structurally mitigated by the sp-78ai
absorption ledger (fleet-wide A-cap netting + recovery shadows bound
concentration in QUANTITY space before selection sees the candidates), and
(b) the docstring's own promise that inter-system crossings "cost … in the
$/hr objective" was dead under profit-primary selection (time only reached
the tiebreak). The DEFAULT stays profit-primary until the offline replay
(replay_objective.py) shows a clear fleet-$/hr win — the analyst's Q3 bar:
the objective of a live engine is replay-validated, never A/B-tested on a
hunch. Zero-time safety: if ANY scored candidate carries no positive time
estimate, rate mode falls back to profit ordering wholesale (a rate against
a guess is not a ranking; divide-by-zero can never decide selection).

Every hop must add positive marginal profit under EITHER objective —
allocations only exist at margin >= the min-margin gate, and hops with no
allocation are pruned from the plan.

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
import os
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

# Selection objective (sp-1wp8): "profit" = max projected profit, cph tiebreak (the
# 2026-07-09 Admiral default); "rate" = max projected cph, profit tiebreak. Resolved
# per solve: explicit `objective` argument > TOUR_SOLVER_OBJECTIVE env > profit. The
# env switch (RULINGS #5: a deploy-config knob, not a hardcode) is how the flip ships
# WITHOUT a proto change — the request and response shapes are untouched either way.
OBJECTIVE_PROFIT = "profit"
OBJECTIVE_RATE = "rate"
OBJECTIVE_ENV_VAR = "TOUR_SOLVER_OBJECTIVE"

_warned_tiers = set()
_logged_objective = set()


def _resolve_objective(objective):
    """Resolve the selection objective: explicit argument > env > profit default.
    An unrecognized value falls back to profit (fail toward the proven default,
    never toward an accidental objective flip) with a once-per-process log."""
    if objective in (OBJECTIVE_PROFIT, OBJECTIVE_RATE):
        return objective
    env = os.environ.get(OBJECTIVE_ENV_VAR, "").strip().lower()
    if env == OBJECTIVE_RATE:
        if OBJECTIVE_RATE not in _logged_objective:
            _logged_objective.add(OBJECTIVE_RATE)
            logger.info("tour-solver: selection objective RATE (cph-primary) via %s",
                        OBJECTIVE_ENV_VAR)
        return OBJECTIVE_RATE
    if env and env != OBJECTIVE_PROFIT and env not in _logged_objective:
        _logged_objective.add(env)
        logger.warning("tour-solver: unrecognized %s=%r — defaulting to profit-primary",
                       OBJECTIVE_ENV_VAR, env)
    return OBJECTIVE_PROFIT


def _sort_scored(scored, objective):
    """Order fully-scored candidates by the selection objective (sp-1wp8); returns
    the objective that ACTUALLY ordered the list.

    profit (default): (-profit, -cph, summary) — byte-identical to the 2026-07-09
    Admiral decision. rate: (-cph, -profit, summary) — fastest money first, equal
    rates break on absolute profit. Zero-time pin: rate ordering applies ONLY when
    every candidate carries a positive time estimate; any seconds<=0 candidate
    (degenerate input — a real plan always dwells >=60s/leg) drops the WHOLE
    selection back to profit ordering (and reports so), so a divide-by-zero
    artifact can never out-rank real plans."""
    if objective == OBJECTIVE_RATE and all(r["seconds"] > 0 for r, _ in scored):
        scored.sort(key=lambda rs: (-rs[0]["cph"], -rs[0]["profit"], rs[1]))
        return OBJECTIVE_RATE
    scored.sort(key=lambda rs: (-rs[0]["profit"], -rs[0]["cph"], rs[1]))
    return OBJECTIVE_PROFIT


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


def net_absorption(tranches, units_planned, units_recovering, trade_volume):
    """Net outstanding cross-container absorption out of a pool's tranche schedule
    (sp-78ai L3). Depth is quantized to whole trade_volume tranches — the model prices
    impact per tranche, so a partial planned presence still bumps the whole step (the
    conservative, D39-honest direction).

      - units_planned (in-flight PLANNED from other containers) drops ceil(planned/tv)
        tranches from the HEAD: it consumes BOTH capacity and the leading, least-decayed
        prices, so the plan's first tranche prices at the step those planned tranches
        leave behind — someone is taking them there at those prices.
      - units_recovering (the decayed EXECUTED residual) drops ceil(recovering/tv)
        tranches from the TAIL: CAPACITY ONLY. The head prices are kept at step 0 (the
        live quote already reflects the crush; re-pricing would double-count it).

    Returns the netted (units, price) tranche list. Per-tranche PRICES are never
    altered — only which tranches remain — so the D39 calibration and impact-curve math
    are untouched; ONLY availability is netted (design §3)."""
    if trade_volume <= 0 or not tranches:
        return tranches
    planned_tranches = math.ceil(units_planned / trade_volume) if units_planned > 0 else 0
    recovering_tranches = (math.ceil(units_recovering / trade_volume)
                           if units_recovering > 0 else 0)
    start = planned_tranches
    end = len(tranches) - recovering_tranches
    if end <= start:
        return []
    return tranches[start:end]


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


def _build_deposit_sinks(deposit_candidates, markets, allowed_systems):
    """Index deposit candidates as synthetic sinks and make each storage waypoint
    a routable node in `markets` (sp-dchv Lane C).

    Returns {(waypoint, good): {"bid": synthetic_bid, "units_wanted": n}}. A
    candidate with a non-positive units_wanted or bid is dropped (nothing to
    absorb / no savings value), as is one whose storage system is outside the
    tour's allowed set (the sink would be unreachable — fail closed). The storage
    waypoint is added to `markets` as an empty-goods node when it is not already a
    scanned market so the beam search can route to it; the deposit good is NOT
    written into markets[wp]["goods"] — it lives only in the returned sink map so a
    real market row and the deposit sink coexist at the same waypoint.
    """
    sinks = {}
    for c in deposit_candidates or []:
        wp = c.get("storage_waypoint")
        good = c.get("good_symbol")
        units = c.get("units_wanted", 0)
        bid = c.get("synthetic_bid", 0)
        system = c.get("storage_system", "")
        if not wp or not good or units <= 0 or bid <= 0:
            continue
        if system and system not in allowed_systems:
            continue  # sink outside the tour graph — unreachable, fail closed
        sinks[(wp, good)] = {"bid": bid, "units_wanted": units}
        markets.setdefault(wp, {"system": system, "goods": {}})
    return sinks


def _build_stock_sources(stock_sources, markets, allowed_systems):
    """Index warehouse stock as zero-ask-at-basis withdrawal SOURCES and make each
    storage waypoint a routable node in `markets` (C1, sp-64je) — the buy-side mirror
    of `_build_deposit_sinks`.

    Returns {(waypoint, good): {"ask": basis, "units_available": n}}. A source with a
    non-positive units_available or unit_ask is dropped (nothing to withdraw / no basis),
    as is one whose storage system is outside the tour's allowed set (unreachable — fail
    closed). The storage waypoint is added to `markets` as an empty-goods node when it is
    not already a scanned market so the beam search can route to it; the stock good is NOT
    written into markets[wp]["goods"] — it lives only in the returned source map so a real
    market row and the stock source coexist at the same waypoint and price independently."""
    sources = {}
    for c in stock_sources or []:
        wp = c.get("storage_waypoint")
        good = c.get("good_symbol")
        units = c.get("units_available", 0)
        ask = c.get("unit_ask", 0)
        system = c.get("storage_system", "")
        if not wp or not good or units <= 0 or ask <= 0:
            continue
        if system and system not in allowed_systems:
            continue  # source outside the tour graph — unreachable, fail closed
        sources[(wp, good)] = {"ask": ask, "units_available": units}
        markets.setdefault(wp, {"system": system, "goods": {}})
    return sources


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


def score_sequence(seq, markets, ship, constraints, model, travel_fn, deposit_sinks=None,
                   absorption_index=None, stock_sources=None):
    """Greedy tranche allocation over one hop sequence (the LP stage).

    Returns dict(profit, spend, seconds, cph, legs, held_liquidation,
    deposit_value, stock_value) where legs carry only the market stops with at least one
    trade (no-trade hops are pruned and travel re-chained). Hold accounting: a
    unit bought at leg i and sold at leg j occupies hold slots [i, j); launch
    cargo occupies from the start until its sell leg. Slot occupancy never
    exceeds hold_capacity, which is exactly the sells-then-buys dock order the
    executor uses.

    `deposit_sinks` (sp-dchv Lane C) maps (waypoint, good) -> {"bid", "units_wanted"}
    for haul-to-storage DEPOSIT sinks at the home warehouse. A deposit sink
    absorbs a foreign-bought good at a flat synthetic bid (= home_ask) with no
    depth decay and no A-cap, competing with real arb sells on margin so the
    greedy allocator hands hold space to whichever earns more.

    `absorption_index` (sp-78ai L3) maps (waypoint, good, side) -> (units_planned,
    units_recovering): outstanding cross-container depth netted out of each market
    pool at construction (see net_absorption). Empty/None -> no netting.

    `stock_sources` (C1, sp-64je) maps (waypoint, good) -> {"ask", "units_available"}
    for warehouse-stock WITHDRAWAL sources — the buy-side mirror of deposit sinks. A
    stock source supplies a good at a flat cost basis (= "ask") with no depth decay and
    no A-cap, and a withdrawal leg competes with real market buys on margin so the
    allocator draws from stock only when it is the cheaper acquisition. Empty/None -> no
    stock legs, plans against market buys unchanged.
    """
    deposit_sinks = deposit_sinks or {}
    absorption_index = absorption_index or {}
    stock_sources = stock_sources or {}
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
            tv = row["trade_volume"]
            # Ladder cap: at most MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE
            # tranches per (market, good, side) across the whole tour,
            # revisits included (A-capped ruling — see module docstring).
            capped = min(pool_ceiling,
                         MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE * tv)
            tranches = tranche_prices(quote, tv, _tier_of(row), model, is_buy, capped)
            # sp-78ai L3: net outstanding cross-container absorption on this
            # (waypoint, good, side) out of available depth. The A-cap ladder is now
            # FLEET-WIDE-outstanding, not per-plan (the bead's design goal (a)) — the
            # planned/recovering split (net_absorption) keeps per-tranche prices exact.
            side = "buy" if is_buy else "sell"
            up, ur = absorption_index.get((wp, good, side), (0, 0.0))
            if up or ur:
                tranches = net_absorption(tranches, up, ur, tv)
            pools[pkey] = _TranchePool(tranches)
        return pools[pkey]

    # Deposit sinks reachable at each leg's waypoint (sp-dchv Lane C).
    deposit_by_wp = {}
    for (wp, good), sink in deposit_sinks.items():
        deposit_by_wp.setdefault(wp, {})[good] = sink

    deposit_pools = {}

    def deposit_pool(wp, good):
        pkey = (wp, good)
        if pkey not in deposit_pools:
            sink = deposit_sinks[(wp, good)]
            # Flat single tranche: NO depth decay (an inventory transfer is not a
            # market sale — no price impact) and NO A-cap (depth = units_wanted,
            # already bounded Go-side by remaining contract demand, warehouse
            # space, and the capital ceiling). Shared per (waypoint, good) across
            # revisits, exactly like the market pools.
            deposit_pools[pkey] = _TranchePool([(sink["units_wanted"], sink["bid"])])
        return deposit_pools[pkey]

    # Stock sources reachable at each leg's waypoint (C1, sp-64je) — the buy-side
    # mirror of deposit_by_wp.
    stock_by_wp = {}
    for (wp, good), src in stock_sources.items():
        stock_by_wp.setdefault(wp, {})[good] = src

    stock_pools = {}

    def stock_pool(wp, good):
        pkey = (wp, good)
        if pkey not in stock_pools:
            src = stock_sources[(wp, good)]
            # Flat single tranche: NO depth decay (a withdrawal is not a market buy —
            # no price impact) and NO A-cap (depth = units_available, already bounded
            # Go-side by on-hand stock net of cross-tour reservations). Shared per
            # (waypoint, good) across revisits, exactly like the market pools.
            stock_pools[pkey] = _TranchePool([(src["units_available"], src["ask"])])
        return stock_pools[pkey]

    # Candidate pairings. Buys and sells at repeat visits of the same market
    # share one pool per (waypoint, good) — depth is a property of the market,
    # not of the leg index. Each pairing carries a kind: "market" (arb sell or
    # launch liquidation), "deposit" (sp-dchv haul-to-storage sink), or "stock"
    # (C1 warehouse-stock withdrawal at basis).
    pairs = []  # (good, buy_leg or None for launch cargo, sell_leg, kind)
    for j in range(n):
        for good, row in markets[seq[j]]["goods"].items():
            if row["bid"] <= 0:
                continue
            if initial.get(good):
                pairs.append((good, None, j, "market"))
            for i in range(j):
                brow = markets[seq[i]]["goods"].get(good)
                if brow and brow["ask"] > 0:
                    pairs.append((good, i, j, "market"))
        # Deposit pairings (sp-dchv): a foreign-bought depositable good pairs a
        # real buy leg i with a DEPOSIT into the home warehouse sink at leg j
        # (flat synthetic bid = home_ask). Launch cargo is NEVER deposited (no
        # (None, j) deposit pair) — a deposit always carries a real acquisition
        # cost, so held-liquidation accounting stays clean and a deposit that
        # fails at execution strand-sells as held cargo (m5kv), never at the
        # synthetic price.
        for good in deposit_by_wp.get(seq[j], ()):
            for i in range(j):
                brow = markets[seq[i]]["goods"].get(good)
                if brow and brow["ask"] > 0:
                    pairs.append((good, i, j, "deposit"))
        # Stock pairings (C1, sp-64je): a good stocked in a warehouse at an earlier leg
        # i is WITHDRAWN at basis (leg i) and sold at market leg j (kind "stock"). The
        # buy-side mirror of deposit pairings — a real acquisition drawn from the flat
        # stock pool at basis, never launch cargo, sold at the market's real bid.
        for i in range(j):
            for good in stock_by_wp.get(seq[i], ()):
                srow = markets[seq[j]]["goods"].get(good)
                if srow and srow["bid"] > 0:
                    pairs.append((good, i, j, "stock"))

    occ = [total_initial] * n   # hold occupancy per travel slot
    initial_left = dict(initial)
    spend = 0
    revenue = 0
    allocations = []            # (good, buy_leg, sell_leg, units, buy_price, sell_price, kind)
    alive = list(pairs)

    def sink_for(kind, j, good):
        # A deposit pairing draws from the flat synthetic warehouse pool; every
        # other pairing draws from the decaying, A-capped market sell pool.
        if kind == "deposit":
            return deposit_pool(seq[j], good)
        return pool(sell_pools, seq[j], good, is_buy=False)

    def source_for(kind, i, good):
        # A stock pairing WITHDRAWS from the flat warehouse stock pool at basis; every
        # other pairing (market/deposit) BUYS from the decaying, A-capped market buy pool.
        if kind == "stock":
            return stock_pool(seq[i], good)
        return pool(buy_pools, seq[i], good, is_buy=True)

    while True:
        best = None
        for good, i, j, kind in alive:
            sell_rem, sell_price = sink_for(kind, j, good).head()
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
                buy_rem, buy_price = source_for(kind, i, good).head()
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
                best = (key, good, i, j, units, buy_price, sell_price, kind)
        if best is None:
            break
        _, good, i, j, units, buy_price, sell_price, kind = best
        sink_for(kind, j, good).take(units)
        if i is None:
            initial_left[good] -= units
            for k in range(j, n):
                occ[k] -= units
        else:
            source_for(kind, i, good).take(units)
            spend += units * buy_price
            for k in range(i, j):
                occ[k] += units
        revenue += units * sell_price
        allocations.append((good, i, j, units, buy_price, sell_price, kind))

    profit = revenue - spend
    # Held-liquidation revenue (sp-bc27, Admiral ruling C): the revenue from
    # sell tranches of cargo held at launch (buy_leg i is None — no acquisition
    # cost in this plan). It is a subset of `revenue` and thus of `profit`;
    # reported alongside the TOTAL so a projection can show fresh-trade profit
    # (profit - held_liquidation) and liquidation revenue apart. Selection still
    # ranks on total `profit`, so pure-liquidation tours stay feasible.
    held_liquidation = sum(units * sell_price
                           for _good, i, _j, units, _buy_price, sell_price, _kind in allocations
                           if i is None)
    # Deposit value (sp-dchv Lane C): synthetic savings from haul-to-storage
    # deposit legs (units*synthetic_bid, synthetic_bid = home_ask). It is a subset
    # of `revenue`/`profit` — the sink priced each deposit at home_ask so the
    # solver ranks it against real arb sells — but it is NOT cash: the executor
    # books zero revenue and realizes the value later when a contract sources the
    # good from inventory. Reported apart (like held_liquidation) so a projection
    # can show fresh cash profit and pre-positioning value separately. Deposits
    # never have buy_leg=None, so they are disjoint from held_liquidation.
    deposit_value = sum(units * sell_price
                        for _good, _i, _j, units, _buy_price, sell_price, kind in allocations
                        if kind == "deposit")
    # Stock value (C1, sp-64je): the basis-value of factory output WITHDRAWN from
    # warehouse stock (units*basis) — the acquisition the tour drew at basis instead of
    # buying at the laddered market ask. Reported apart (like deposit_value) so a
    # projection can show how much output realization moved to withdrawal-at-basis.
    # Stock draws always have a real buy_leg, so they are disjoint from held_liquidation.
    stock_value = sum(units * buy_price
                      for _good, _i, _j, units, buy_price, _sell_price, kind in allocations
                      if kind == "stock")

    # Assemble per-leg trades, then prune hops where nothing happens.
    leg_trades = [{} for _ in range(n)]  # (good, is_buy, is_deposit, is_stock, price) -> units
    for good, i, j, units, buy_price, sell_price, kind in allocations:
        if i is not None:
            # A stock pairing's BUY leg is a warehouse WITHDRAWAL at basis (is_stock);
            # market/deposit buys are ordinary market purchases.
            k = (good, True, False, kind == "stock", buy_price)
            leg_trades[i][k] = leg_trades[i].get(k, 0) + units
        k = (good, False, kind == "deposit", False, sell_price)
        leg_trades[j][k] = leg_trades[j].get(k, 0) + units

    legs = []
    for idx in range(n):
        if not leg_trades[idx]:
            continue
        trades = []
        entries = leg_trades[idx].items()
        for (good, is_buy, is_deposit, is_stock, price), units in sorted(
                entries, key=lambda e: (e[0][1], e[0][0], e[0][2], e[0][3], e[0][4])):
            # sells (is_buy=False) sort first: dock order frees hold before buys
            trades.append(dict(good_symbol=good, units=units, is_buy=is_buy,
                               is_deposit=is_deposit, is_stock=is_stock,
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
                held_liquidation=held_liquidation, deposit_value=deposit_value,
                stock_value=stock_value)


def beam_sequences(markets, ship, constraints, travel_fn, deposit_sinks=None,
                   stock_sources=None):
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
    deposit_sinks = deposit_sinks or {}
    stock_sources = stock_sources or {}
    stock_by_wp = {}
    for (wp, good), src in stock_sources.items():
        stock_by_wp.setdefault(wp, {})[good] = src
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
            # Deposit sink at wp_to (sp-dchv): a synthetic sink priced at home_ask
            # absorbs up to units_wanted with no depth decay. Credit it in the
            # packing bound so the beam explores sequences that reach the warehouse
            # to deposit cheap foreign buys — otherwise a rich foreign source whose
            # only profitable sink is the warehouse never survives the beam cut.
            dsink = deposit_sinks.get((wp_to, good))
            if dsink and brow["ask"] > 0 and dsink["bid"] > brow["ask"]:
                spreads.append((dsink["bid"] - brow["ask"], dsink["units_wanted"]))
        # Stock source at wp_from (C1, sp-64je): a good stocked in the warehouse here is
        # WITHDRAWN at basis and sold at wp_to's market bid. Credit it in the packing
        # bound (source-side mirror of the deposit sink) so the beam explores sequences
        # that reach the warehouse to draw cheap stock — otherwise a stock waypoint whose
        # market goods are thin could lose the beam cut and its stock never be planned.
        for good, ssrc in stock_by_wp.get(wp_from, {}).items():
            srow = goods_to.get(good)
            if srow and srow["bid"] > ssrc["ask"]:
                spreads.append((srow["bid"] - ssrc["ask"], ssrc["units_available"]))
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
                held_liquidation=0, deposit_value=0, stock_value=0,
                top_rejected=top_rejected or [], model_version=model_version)


def _index_absorption(absorption):
    """Index MarketAbsorption rows as {(waypoint, good, side): (units_planned,
    units_recovering)} for O(1) pool netting (sp-78ai L3). Duplicate keys are summed
    (the Go assembler emits one row per key, but summing is the safe fold). None/empty
    -> {} -> no netting anywhere (pre-sp-78ai plans byte-identical)."""
    index = {}
    for a in absorption or []:
        key = (a.get("waypoint_symbol"), a.get("good_symbol"), a.get("side"))
        if not all(key):
            continue
        up, ur = index.get(key, (0, 0.0))
        index[key] = (up + int(a.get("units_planned", 0)),
                      ur + float(a.get("units_recovering", 0.0)))
    return index


def solve_tour(snapshot, ship, constraints, model, waypoints=None,
               deposit_candidates=None, absorption=None, objective=None,
               stock_sources=None):
    """Plan the best multi-hop trade tour for one hull. Pure; proto-shaped dicts.

    `waypoints` mirrors OptimizeTradeTourRequest.waypoints (coords for the
    real travel matrix); None/empty -> degraded flat travel with a warning.

    `deposit_candidates` mirrors OptimizeTradeTourRequest.deposit_candidates
    (sp-dchv Lane C): each is a haul-to-storage sink offer the Go daemon assembled
    and capped. None/empty -> no deposit legs, pure-arb planning unchanged.

    `absorption` mirrors OptimizeTradeTourRequest.absorption (sp-78ai L3): outstanding
    cross-container depth per (waypoint, good, side) the Go daemon assembled from the
    absorption ledger, decaying EXECUTED shadows Go-side. It NETS available tranche
    depth (net_absorption) without touching per-tranche prices. None/empty -> no
    netting, plans against full depth byte-identical to pre-sp-78ai.

    `objective` (sp-1wp8): OBJECTIVE_PROFIT (default) or OBJECTIVE_RATE — see the
    module docstring's Selection section. None resolves via TOUR_SOLVER_OBJECTIVE,
    falling back to profit. Selection-only: candidate generation, tranche pricing,
    guards, and the response shape are identical under both.

    Fail-loud contract: missing artifact or version mismatch are structured
    infeasible reasons, never a silent fallback (spec error-handling table).
    """
    objective = _resolve_objective(objective)
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

    # sp-avt4: a reserve >= max_spend zeroes spend_cap BEFORE the market is ever
    # looked at (score_sequence's own guard, mirrored here). Pre-fix this read
    # identically to a genuinely dead market — both fell through to the same generic
    # "no_profitable_tour"/"no_candidate_tours" reason, costing 70+ min of
    # misdiagnosis in the 2026-07-11 fleet-dark P0 (a zeroed budget is a solvency
    # problem, not a market problem). Gated on held cargo too: a sell of cargo
    # already aboard at launch has no acquisition cost and is EXEMPT from spend_cap
    # in score_sequence's allocation loop (sp-m5kv) — a laden hull can have a
    # genuinely feasible liquidation-only tour even at spend_cap 0, so this fast-fail
    # must not shadow that case.
    #
    # Deliberately NOT a "cheapest-ask" min-viable-unit heuristic: a small-but-nonzero
    # spend_cap that affords a unit but can't clear min_margin is genuine market
    # infeasibility, not a budget-class failure — guessing a threshold here would
    # reintroduce a subtler version of the same misdiagnosis this fix exists to kill.
    #
    # Also deliberately NOT silently clamping the reserve down to fit max_spend (e.g.
    # reserve = min(reserve, max_spend)) so a tour proceeds on whatever headroom is
    # left. For an EXPLICIT --max-spend run, max_spend is an operator-set ceiling and
    # reserve is an operator-set floor; eroding the floor to keep a tour alive on an
    # ambiguous overlap is exactly the silent auto-proceed RULINGS #4 forbids for
    # money-guard code. A zeroed/negative spend_cap fails loud with a named cause
    # instead — the caller (or the operator) decides whether to relax max_spend or the
    # reserve, the solver never decides for them.
    max_spend = constraints.get("max_spend", 0)
    reserve = constraints.get("working_capital_reserve", 0)
    spend_cap = max(0, max_spend - reserve)
    has_initial_cargo = any(c["units"] > 0 for c in ship.get("cargo") or [])
    if spend_cap <= 0 and not has_initial_cargo:
        return _infeasible(
            f"reserve_exceeds_budget (spend_cap=0: max_spend {max_spend} - "
            f"reserve {reserve})",
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
    # Deposit sinks (sp-dchv Lane C): index the candidates and make each storage
    # waypoint a routable node in `markets` (as an empty-goods node when it is not
    # itself a scanned market). The deposit goods live in the sink map, NOT in
    # markets[wp]["goods"], so real market rows and the deposit sink coexist at the
    # same waypoint without collision and are priced independently.
    deposit_sinks = _build_deposit_sinks(deposit_candidates, markets, allowed)
    # Stock sources (C1, sp-64je): index warehouse stock as zero-ask-at-basis
    # withdrawal sources and make each storage waypoint routable — the buy-side mirror
    # of the deposit sinks, priced independently from any real market row at the same
    # waypoint. Absent -> {} -> the tour plans against market buys unchanged.
    stock_source_idx = _build_stock_sources(stock_sources, markets, allowed)
    absorption_index = _index_absorption(absorption)
    travel_fn = _make_travel_fn(constraints, markets, ship, waypoints)
    candidates = beam_sequences(markets, ship, constraints, travel_fn, deposit_sinks,
                                stock_source_idx)
    if not candidates:
        return _infeasible("no_candidate_tours", model_version)

    scored = []
    seen = set()
    for seq in candidates[:FULL_SCORE_TOP_N]:
        result = score_sequence(seq, markets, ship, constraints, model, travel_fn,
                                deposit_sinks, absorption_index, stock_source_idx)
        signature = tuple((l["waypoint_symbol"],
                           tuple((t["good_symbol"], t["units"], t["is_buy"],
                                  t["is_deposit"], t["is_stock"], t["expected_unit_price"])
                                 for t in l["trades"]))
                          for l in result["legs"])
        if signature in seen:
            continue
        seen.add(signature)
        summary = "→".join(l["waypoint_symbol"] for l in result["legs"]) or "→".join(seq)
        scored.append((result, summary))
    # Objective-ordered selection (sp-1wp8): profit-primary by default (the
    # 2026-07-09 Admiral decision), cph-primary under OBJECTIVE_RATE. `effective`
    # is what actually ordered the list (rate falls back to profit on any
    # zero-time candidate), so the rejection reasons below can never claim a
    # comparison the sort didn't make.
    effective = _sort_scored(scored, objective)

    def rejected(entries, winner=None):
        # winner=None only on the infeasible path, where the sort invariant
        # guarantees every entry has profit <= 0 (first branch — under BOTH
        # objectives an all-nonpositive pool sorts a nonpositive candidate first).
        out = []
        for result, summary in entries[:TOP_REJECTED_N]:
            if result["profit"] <= 0:
                reason = "no profitable allocation under tranche decay/guards"
            elif effective == OBJECTIVE_RATE:
                # Rate-primary honesty: name the cph comparison that decided it.
                if result["cph"] < winner["cph"]:
                    reason = (f"cph {result['cph']:,.0f}/hr < winner "
                              f"{winner['cph']:,.0f}/hr (profit {result['profit']:,})")
                else:
                    reason = (f"cph tie, profit {result['profit']:,} <= winner "
                              f"{winner['profit']:,}")
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
                deposit_value=best["deposit_value"],
                stock_value=best["stock_value"],
                top_rejected=rejected(scored[1:], winner=best),
                model_version=model_version)

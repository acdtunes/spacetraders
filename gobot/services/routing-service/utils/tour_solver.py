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
import itertools
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

# Stage-1 sequencer selection (sp-y05b): "beam" = the proven beam search
# (default, byte-identical); "ortools" = the OR-Tools prize-collecting
# sequencer UNIONED with beam candidates (ortools can only ADD candidates,
# never hide beam's — stage 2 stays the arbiter). Resolved per solve:
# explicit `sequencer` argument > TOUR_SOLVER_SEQUENCER env > beam. Ships
# dormant; arming is a separate replay-gated run.sh export commit, exactly
# like TOUR_SOLVER_OBJECTIVE.
SEQUENCER_ENV_VAR = "TOUR_SOLVER_SEQUENCER"
SEQUENCER_BEAM = "beam"
SEQUENCER_ORTOOLS = "ortools"
ORTOOLS_TIME_BUDGET_SECONDS = 3        # GLOBAL per-call wall budget, env TOUR_SOLVER_ORTOOLS_BUDGET_SECONDS, clamp [2, 5]
ORTOOLS_MIN_MODEL_MS = 250             # floor per subset model
ORTOOLS_MAX_SUBSETS = 8                # max subset models solved per call, env TOUR_SOLVER_ORTOOLS_MAX_SUBSETS, clamp [1, 32]
ORTOOLS_MAX_NODES = 80                 # per-model node cap after pruning
# λ (credits per second of travel+dwell) shapes IN-MODEL visit/skip and
# ordering only — candidate ranking and stage-2 pricing never see it. Default
# 10.0 is a documented PLACEHOLDER pending the pre-arming replay sweep
# (λ ∈ {0, 1, 10, 30, 100} via TOUR_SOLVER_ORTOOLS_TIME_VALUE): it sits
# strictly BELOW the fleet's realized 28-280 cr/s band so no genuinely
# profitable lane is stage-1-skipped, but above 0 so time orders/skips
# junk-margin detours. NOT fleet-median cph/3600: with disjunctions, any tour
# whose gain/time falls below λ is strictly dominated by visiting nothing, so
# a median-priced λ would stage-1-skip every below-median lane.
ORTOOLS_TIME_VALUE_CREDITS_PER_SECOND = 10.0  # env TOUR_SOLVER_ORTOOLS_TIME_VALUE, clamp [0, 1000]
COST_SCALE = 100                       # integer scaling, same trick as routing_engine.py int(distance*100)

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
_logged_sequencer = set()


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


def _log_once_sequencer(key, msg, *args):
    """Once-per-process sequencer-path log (mirror of the _logged_objective
    pattern) — fallback diagnostics must never spam a solve-per-tick fleet."""
    if key in _logged_sequencer:
        return
    _logged_sequencer.add(key)
    logger.warning(msg, *args)


def _resolve_sequencer(sequencer):
    """Resolve the stage-1 sequencer: explicit argument > env > beam default
    (sp-y05b). An unrecognized value falls back to beam (fail toward the
    proven default, never toward an accidental solver flip) with a
    once-per-process log — the structural clone of _resolve_objective."""
    if sequencer in (SEQUENCER_BEAM, SEQUENCER_ORTOOLS):
        return sequencer
    env = os.environ.get(SEQUENCER_ENV_VAR, "").strip().lower()
    if env == SEQUENCER_ORTOOLS:
        if SEQUENCER_ORTOOLS not in _logged_sequencer:
            _logged_sequencer.add(SEQUENCER_ORTOOLS)
            logger.info("tour-solver: stage-1 sequencer ORTOOLS (union with beam) via %s",
                        SEQUENCER_ENV_VAR)
        return SEQUENCER_ORTOOLS
    if env and env != SEQUENCER_BEAM and env not in _logged_sequencer:
        _logged_sequencer.add(env)
        logger.warning("tour-solver: unrecognized %s=%r — defaulting to beam sequencer",
                       SEQUENCER_ENV_VAR, env)
    return SEQUENCER_BEAM


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


def _effective_tour_systems(constraints):
    """Resolve the per-tour DISTINCT-system cap (sp-syaz), clamped to a sane range.

    Falsy-zero/absent -> the MAX_TOUR_SYSTEMS module default (2): the default-safety
    hinge, byte-identical to the pre-sp-syaz clamp. The result is then clamped to
    [MAX_TOUR_SYSTEMS, MAX_HOPS_DEFAULT] — the floor turns the degenerate 1 (a
    single-system, no-trade tour) back into the tradable default, and the ceiling stops
    an over-large request from exploding the beam's branching factor. Mirrors the
    `max_hops = min(max_hops, MAX_HOPS_DEFAULT)` clamp already in beam_sequences.
    """
    requested = constraints.get("max_tour_systems") or MAX_TOUR_SYSTEMS
    return max(MAX_TOUR_SYSTEMS, min(requested, MAX_HOPS_DEFAULT))


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


def _held_liquidation_value(wp, markets, initial_cargo):
    """Value of liquidating the ship's held cargo at wp's market bids.

    Mirror of beam_sequences.liquidation_gain — keep in sync (sp-y05b).
    beam stays byte-untouched (default-safety + sibling merge pressure);
    the T6/T7 brute-force equality tests catch semantic drift."""
    goods = markets[wp]["goods"]
    return sum(units * goods[g]["bid"] for g, units in initial_cargo.items()
               if g in goods and goods[g]["bid"] > 0)


def _pair_gain(wp_from, wp_to, markets, hold, deposit_sinks, stock_by_wp):
    """Optimistic multi-good hold-packing value of the DIRECTED pair (buy at
    wp_from, sell/deposit at wp_to) — directional: gain(a,b) != gain(b,a), so
    buy-before-sell precedence is priced into the ortools arc costs.

    Mirror of beam_sequences.pack_gain — keep in sync (sp-y05b). Module-level
    TRANSCRIPTION on explicit args; beam's closure stays byte-untouched."""
    goods_to = markets[wp_to]["goods"]
    spreads = []
    for good, brow in markets[wp_from]["goods"].items():
        srow = goods_to.get(good)
        if srow and brow["ask"] > 0 and srow["bid"] > brow["ask"]:
            depth = MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE * max(
                1, min(brow["trade_volume"], srow["trade_volume"]))
            spreads.append((srow["bid"] - brow["ask"], depth))
        dsink = deposit_sinks.get((wp_to, good))
        if dsink and brow["ask"] > 0 and dsink["bid"] > brow["ask"]:
            spreads.append((dsink["bid"] - brow["ask"], dsink["units_wanted"]))
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


def _prune_nodes(markets, ship, constraints, deposit_sinks, stock_by_wp):
    """Two-phase node pruning for the ortools subset models (sp-y05b).

    Phase 1 — cheap prefilter on per-good global max_bid/min_ask with the
    SAME margin floor score_sequence applies (max(1, min_margin_per_unit)):
    a positive directed pair (a, b) on good g at the floor implies
    a.ask <= max_bid[g] - floor and b.bid >= min_ask[g] + floor, so no pair
    participant is ever dropped (strict superset of the pair criterion).
    Also kept: held-cargo liquidation sinks (beam's liquidation-seed parity),
    deposit-sink/stock-source hosts, and the ship's current waypoint.

    Phase 2 — if still over ORTOOLS_MAX_NODES, rank by max incident
    _pair_gain + liquidation value and truncate, with start/deposit/stock/
    liquidation-positive nodes EXEMPT from truncation."""
    deposit_sinks = deposit_sinks or {}
    stock_by_wp = stock_by_wp or {}
    initial = {c["good_symbol"]: c["units"] for c in ship.get("cargo") or []}
    floor = max(1, constraints.get("min_margin_per_unit", 0))  # == score_sequence's floor
    max_bid, min_ask = {}, {}
    for wp in markets:
        for good, row in markets[wp]["goods"].items():
            if row["bid"] > 0 and row["bid"] > max_bid.get(good, 0):
                max_bid[good] = row["bid"]
            if row["ask"] > 0 and (good not in min_ask or row["ask"] < min_ask[good]):
                min_ask[good] = row["ask"]
    deposit_wps = {wp for wp, _ in deposit_sinks}
    stock_wps = set(stock_by_wp)
    start = ship["current_waypoint"]

    def keep(wp):
        if wp == start or wp in deposit_wps or wp in stock_wps:
            return True
        if _held_liquidation_value(wp, markets, initial) > 0:
            return True
        for good, row in markets[wp]["goods"].items():
            if row["ask"] > 0 and row["ask"] <= max_bid.get(good, 0) - floor:
                return True   # buy-side potential
            if good in min_ask and row["bid"] >= min_ask[good] + floor:
                return True   # sell-side potential
        return False

    kept = [wp for wp in sorted(markets) if keep(wp)]
    if len(kept) <= ORTOOLS_MAX_NODES:
        return kept

    exempt = {wp for wp in kept
              if wp == start or wp in deposit_wps or wp in stock_wps
              or _held_liquidation_value(wp, markets, initial) > 0}
    hold = ship["hold_capacity"]

    def node_potential(wp):
        best = 0
        for other in kept:
            if other == wp:
                continue
            g = max(_pair_gain(wp, other, markets, hold, deposit_sinks, stock_by_wp),
                    _pair_gain(other, wp, markets, hold, deposit_sinks, stock_by_wp))
            if g > best:
                best = g
        return best + _held_liquidation_value(wp, markets, initial)

    ranked = sorted((wp for wp in kept if wp not in exempt),
                    key=lambda wp: (-node_potential(wp), wp))
    room = max(0, ORTOOLS_MAX_NODES - len(exempt))
    survivors = set(ranked[:room]) | exempt
    return [wp for wp in kept if wp in survivors]


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
    # sp-syaz: the per-tour distinct-system cap is now request-carried, resolved +
    # clamped to [MAX_TOUR_SYSTEMS, MAX_HOPS_DEFAULT] by _effective_tour_systems. The
    # falsy-zero fallback is the default-safety hinge — a missing key OR an unset proto3
    # int32 (0) resolves to the module default (2), byte-identical to the pre-sp-syaz
    # clamp; a positive request value sweeps tour length (bounded) with no redeploy.
    max_tour_systems = _effective_tour_systems(constraints)
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
        return len(frozenset((start_system, *systems))) <= max_tour_systems

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
                if len(new_systems) > max_tour_systems:
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


def _sequencer_env_scalar(name, default, lo, hi, cast):
    """Env override for an ortools knob, clamped to [lo, hi]; invalid values
    fall back to the default with a once-per-process warning."""
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        val = cast(raw)
    except ValueError:
        _log_once_sequencer("env:" + name,
                            "tour-solver: invalid %s=%r — using default %s",
                            name, raw, default)
        return default
    clamped = max(lo, min(val, hi))
    if clamped != val:
        _log_once_sequencer("envclamp:" + name,
                            "tour-solver: %s=%r clamped to %s", name, raw, clamped)
    return clamped


def ortools_sequences(markets, ship, constraints, travel_fn, deposit_sinks=None,
                      stock_sources=None, stats_out=None):
    """OR-Tools prize-collecting stage-1 sequencer (sp-y05b). Same contract as
    beam_sequences: list of waypoint-symbol tuples, best-first. Returning []
    is the no-solution surface; the solve_tour seam catches any exception and
    the union with beam candidates means this can only ADD candidates.

    Encoding (one open-path routing model per selected system subset):
    - Pair values fold into ARC costs: real->real arc costs
      int((travel + dwell) * lam * COST_SCALE) + OFFSET - gain[a][b], where
      gain is the DIRECTED _pair_gain — a pure buy source earns its value on
      the arc that LEAVES it toward its sink, and gain(a,b) != gain(b,a)
      prices buy-before-sell ordering. Held-cargo liquidation is the only
      node-intrinsic prize (disjunction penalty OFFSET + liq[v]).
    - OFFSET wash: a route visiting k non-start nodes collects k*OFFSET on
      arcs and pays m*OFFSET for the m skipped; k + m = N is constant, so
      minimizing cost == maximizing sum(consecutive-arc gains) + sum(visited
      liq) - lam*time.
    - HONESTY NOTE (relaxation vs beam): beam's per-hop bound is the max of
      pack_gain over the WHOLE prefix (non-consecutive pairs credited); the
      arc encoding credits CONSECUTIVE pairs only, so source->detour->sink
      orderings are under-credited in-model. Mitigations: stage 2 exactly
      prices ALL i<j pairs on every emitted prefix; the solve_tour UNION
      keeps every beam candidate in the pool; the emission re-ranking below
      uses beam's own max-over-prefix bound so cross-subset ordering stays
      commensurate with beam's semantics.
    - Stop cap lives IN the model (AddConstantDimension), not in post-hoc
      truncation. A single GLOBAL wall budget spans all subset models.
    """
    # Lazy import: the beam default path never calls this function, so a
    # broken ortools wheel cannot affect default mode.
    from ortools.constraint_solver import pywrapcp, routing_enums_pb2

    started = time.monotonic()
    deposit_sinks = deposit_sinks or {}
    stock_sources = stock_sources or {}
    stock_by_wp = {}
    for (wp, good), src in stock_sources.items():
        stock_by_wp.setdefault(wp, {})[good] = src

    lam = _sequencer_env_scalar("TOUR_SOLVER_ORTOOLS_TIME_VALUE",
                                ORTOOLS_TIME_VALUE_CREDITS_PER_SECOND,
                                0.0, 1000.0, float)
    budget_ms = _sequencer_env_scalar("TOUR_SOLVER_ORTOOLS_BUDGET_SECONDS",
                                      ORTOOLS_TIME_BUDGET_SECONDS, 2, 5, int) * 1000
    max_subsets = _sequencer_env_scalar("TOUR_SOLVER_ORTOOLS_MAX_SUBSETS",
                                        ORTOOLS_MAX_SUBSETS, 1, 32, int)

    pruned = _prune_nodes(markets, ship, constraints, deposit_sinks, stock_by_wp)
    start = ship["current_waypoint"]
    start_system = ship["current_system"]
    start_is_market = start in markets
    initial = {c["good_symbol"]: c["units"] for c in ship.get("cargo") or []}
    hold = ship["hold_capacity"]
    max_hops = constraints.get("max_hops") or MAX_HOPS_DEFAULT
    max_hops = min(max_hops, MAX_HOPS_DEFAULT)
    # cap read — same accessor as beam_sequences (sp-syaz)
    cap = _effective_tour_systems(constraints)

    # Precompute directed pair gains + liquidation prizes over the pruned set
    # (start included iff it is itself a market node; arcs INTO the start are
    # never taken so gains into it are irrelevant).
    sys_of = {wp: markets[wp]["system"] for wp in pruned}
    pair, liq = {}, {}
    syspair_gain, sys_liq = {}, {}
    for a in pruned:
        v = _held_liquidation_value(a, markets, initial)
        if v > 0:
            liq[a] = v
            sys_liq[sys_of[a]] = sys_liq.get(sys_of[a], 0) + v
    for a in pruned:
        for b in pruned:
            if a == b:
                continue
            g = _pair_gain(a, b, markets, hold, deposit_sinks, stock_by_wp)
            if g > 0:
                pair[(a, b)] = g
                key = (sys_of[a], sys_of[b])
                syspair_gain[key] = syspair_gain.get(key, 0) + g

    # Subset enumeration: S subseteq systems with start_system in S,
    # 1 <= |S| <= cap, ranked by aggregated potential. Enumeration is cheap
    # (C(12,5)=792 x O(cap^2)); only SOLVES are bounded by max_subsets.
    other_systems = sorted({s for s in sys_of.values() if s != start_system})
    subsets = []
    for r in range(0, min(cap - 1, len(other_systems)) + 1):
        for combo in itertools.combinations(other_systems, r):
            in_s = frozenset((start_system,) + combo)
            potential = sum(v for (sa, sb), v in syspair_gain.items()
                            if sa in in_s and sb in in_s)
            potential += sum(v for s, v in sys_liq.items() if s in in_s)
            if potential > 0:
                subsets.append((potential, tuple(sorted(in_s))))
    subsets.sort(key=lambda t: (-t[0], t[1]))
    eligible = len(subsets)
    selected = subsets[:max_subsets]

    emitted, seen = [], set()

    def emit(seq):
        if seq and seq not in seen:
            seen.add(seq)
            emitted.append(seq)

    solved = 0
    if selected:
        per_model_ms = max(ORTOOLS_MIN_MODEL_MS, budget_ms // len(selected))
        for _, subset in selected:
            # Global wall budget (F3/F7): GLS is anytime and burns its whole
            # per-model limit, so the aggregate tracks budget by construction;
            # this hard short-circuit covers model-build overhead too.
            if (time.monotonic() - started) * 1000 >= budget_ms:
                break
            in_s = set(subset)
            real = [wp for wp in pruned if sys_of[wp] in in_s]
            if start not in real:
                real = [start] + real  # ship position routable but prize-less
            if len(real) < 2:
                continue  # nothing to sequence beyond the start
            n_real = len(real)
            start_idx = real.index(start)
            virtual_node = n_real  # open-path terminal (sp-im74 flips end=start)

            gain = [[0] * n_real for _ in range(n_real)]
            liq_scaled = [0] * n_real
            top = 0
            for i, a in enumerate(real):
                liq_scaled[i] = COST_SCALE * liq.get(a, 0)
                top = max(top, liq_scaled[i])
                for j, b in enumerate(real):
                    if i != j:
                        gain[i][j] = COST_SCALE * pair.get((a, b), 0)
                        top = max(top, gain[i][j])
            offset = top + 1
            arc_cost = [[0] * n_real for _ in range(n_real)]
            for i, a in enumerate(real):
                for j, b in enumerate(real):
                    if i == j:
                        continue
                    t = travel_fn(a, b)  # the SAME _make_travel_fn product as beam/stage 2
                    arc_cost[i][j] = (int((t + DWELL_SECONDS_PER_LEG) * lam * COST_SCALE)
                                      + offset - gain[i][j])

            manager = pywrapcp.RoutingIndexManager(n_real + 1, 1,
                                                   [start_idx], [virtual_node])
            routing = pywrapcp.RoutingModel(manager)

            def transit(from_index, to_index, _m=manager, _c=arc_cost,
                        _v=virtual_node):
                to_node = _m.IndexToNode(to_index)
                if to_node == _v:
                    return 0  # F10: BEFORE any travel lookup; virtual arc has no dwell
                from_node = _m.IndexToNode(from_index)
                if from_node == _v:
                    return 0
                return _c[from_node][to_node]

            transit_idx = routing.RegisterTransitCallback(transit)
            routing.SetArcCostEvaluatorOfAllVehicles(transit_idx)
            for i in range(n_real):
                if i != start_idx:
                    routing.AddDisjunction([manager.NodeToIndex(i)],
                                           offset + liq_scaled[i])
            # In-model stop cap (F5): route start->v1->..->vk->virtual has k+1
            # arcs => end cumul k+1; emitted length is k+1 when the start is
            # itself a market (start included in the seq) else k.
            stop_cap = max_hops if start_is_market else max_hops + 1
            routing.AddConstantDimension(1, stop_cap, True, "Stops")

            params = pywrapcp.DefaultRoutingSearchParameters()
            params.first_solution_strategy = (
                routing_enums_pb2.FirstSolutionStrategy.PATH_CHEAPEST_ARC)
            params.local_search_metaheuristic = (
                routing_enums_pb2.LocalSearchMetaheuristic.GUIDED_LOCAL_SEARCH)
            try:
                params.time_limit.FromMilliseconds(per_model_ms)
            except AttributeError:
                params.time_limit.seconds = per_model_ms // 1000
                params.time_limit.nanos = (per_model_ms % 1000) * 1_000_000
            solution = routing.SolveWithParameters(params)
            solved += 1
            if solution is None:
                continue

            order = []
            index = routing.Start(0)
            while not routing.IsEnd(index):
                node = manager.IndexToNode(index)
                if node < n_real:
                    order.append(real[node])
                index = solution.Value(routing.NextVar(index))
            # Emission: ordered real market nodes (start included only if it
            # is a market); every prefix is a candidate, and prefixes anchored
            # on the start market also emit the head-dropped variant (beam
            # sequences are not anchored to the ship's waypoint).
            seq_nodes = [wp for wp in order if wp in markets]
            for k in range(1, len(seq_nodes) + 1):
                prefix = tuple(seq_nodes[:k])
                emit(prefix)
                if start_is_market and k > 1 and prefix[0] == start:
                    emit(prefix[1:])

    # Beam's liquidation-seed parity: the bare start-market candidate.
    if start_is_market and liq.get(start, 0) > 0:
        emit((start,))

    # Rank by BEAM'S OWN transcribed bound (liquidation seed + max-over-prefix
    # pack accumulation) with beam's (-score, seq) tiebreak — lam shapes
    # IN-MODEL selection only, never this ranking, so union ordering stays
    # commensurate with beam's semantics.
    def beam_bound(seq):
        score = _held_liquidation_value(seq[0], markets, initial)
        for k in range(1, len(seq)):
            score += max(pair.get((seq[j], seq[k]), 0) for j in range(k))
        return score

    emitted.sort(key=lambda s: (-beam_bound(s), s))

    if stats_out is not None:
        stats_out.update(subsets_eligible=eligible, subsets_solved=solved,
                         wall_ms=int((time.monotonic() - started) * 1000),
                         nodes=len(pruned))
        logger.info("tour-solver: ortools stage-1 solved %d/%d subsets "
                    "(%d nodes, %d candidates, %d ms)",
                    solved, eligible, len(pruned), len(emitted),
                    stats_out["wall_ms"])
    return emitted


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
               stock_sources=None, sequencer=None):
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

    `sequencer` (sp-y05b): SEQUENCER_BEAM (default) or SEQUENCER_ORTOOLS. None
    resolves via TOUR_SOLVER_SEQUENCER, falling back to beam. Stage-1-only: in
    ortools mode the candidate pool is the UNION of ortools and beam candidates
    (deduped, ortools first) — stage 2 scoring, selection, guards, and every
    reason string are identical under both; beam mode is byte-identical to
    the pre-sp-y05b solver.

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
    sequencer = _resolve_sequencer(sequencer)
    beam_cands = beam_sequences(markets, ship, constraints, travel_fn, deposit_sinks,
                                stock_source_idx)
    if sequencer == SEQUENCER_ORTOOLS:
        # F9: pass the BUILT indices positionally, byte-mirroring the beam call.
        try:
            ortools_cands = ortools_sequences(markets, ship, constraints, travel_fn,
                                              deposit_sinks, stock_source_idx)
        except Exception:
            # The servicer never dies on the new path — beam carries the solve.
            # Once-per-process with traceback (a broken wheel fails identically
            # every call; per-solve tracebacks would spam the fleet log).
            if "ortools_error" not in _logged_sequencer:
                _logged_sequencer.add("ortools_error")
                logger.exception("tour-solver: ortools sequencer failed — beam only")
            ortools_cands = []
        if not ortools_cands:
            _log_once_sequencer(
                "ortools_empty",
                "tour-solver: ortools sequencer produced no candidates; beam only")
        # UNION (F1/F2 safety net): a degenerate non-empty ortools pool must
        # never hide beam's candidates — ortools can only ADD, stage 2 arbitrates.
        pool = list(ortools_cands[:FULL_SCORE_TOP_N])
        seen_seqs = set(pool)
        pool += [s for s in beam_cands[:FULL_SCORE_TOP_N] if s not in seen_seqs]
    else:
        pool = beam_cands[:FULL_SCORE_TOP_N]
    if not pool:
        # Union-empty ⇒ beam-empty ⇒ today's reason string, byte-identical.
        return _infeasible("no_candidate_tours", model_version)

    scored = []
    seen = set()
    for seq in pool:
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

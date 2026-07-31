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
   INTER_SYSTEM_TRAVEL_BASE_SECONDS + gate_hops x
   INTER_SYSTEM_TRAVEL_PER_HOP_SECONDS in the $/hr objective, which prices
   ping-ponging out naturally — the fixed base is what makes a second
   crossing dearer than one deeper crossing (sp-smbgd; both terms
   env-tunable, see the constants for the fit).

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

sp-ljh5 (epic sp-fguo item #4) arms RATE as the DEFAULT for LONGER tours, but
default-OFF and replay-gated. A "long" tour is one whose per-tour distinct-system
cap was raised above MAX_TOUR_SYSTEMS (arming thus implies rate for ALL caps > 2).
The armed-long tier (see _resolve_objective, "Option C") sits ABOVE the
TOUR_SOLVER_OBJECTIVE env block: for a long tour the arm flag is the SOLE governor
of the objective, deliberately superseding the launcher's fleet-wide env=rate
default (the per-solve `objective` arg still wins over all). It is (a) default-OFF;
(b) gated on replay_objective.py --arm showing a fleet-$/hr win at the higher cap
measured against the TRUE live-prod baseline (cap-2 RATE, baseline_cap_rate_cph —
NOT the cap-2 profit fail-safe) AND p99 solve latency within the anytime cap at
that cap (a conjunction — never the $/hr delta alone); (c) selection-only —
candidate generation, tranche pricing, guards, and the response shape are identical
under both objectives (the sp-1wp8 invariant); (d) armed by exporting
TOUR_SOLVER_RATE_ARMED_LONG=1 in the Go manager's / process-manager's environment
(inherited via os.Environ()), reversible without a redeploy.

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
# sp-7q5t/sp-fguo widening unlock: env-overridable so the widened candidate set
# (candidate_hop_depth=2) actually survives to full stage-2 scoring — the distant rich
# sinks were being cut here before scoring. Resolved ONCE per solve_tour
# (_resolve_full_score_top_n) and used at every cut site, mirroring
# TOUR_SOLVER_MAX_PLANNED_TRANCHES. DEFAULT-SAFE: absent/unset/invalid env -> 20 ->
# byte-identical to the pre-widening hardcode (the governance gate).
FULL_SCORE_TOP_N = 20         # sequences fully tranche-scored after the beam; env TOUR_SOLVER_FULL_SCORE_TOP_N, clamp [10, 100]
FULL_SCORE_TOP_N_ENV_VAR = "TOUR_SOLVER_FULL_SCORE_TOP_N"
FULL_SCORE_TOP_N_MIN = 10     # floor: a tiny top-N starves stage-2 of candidates; NEVER 0 (0 scores nothing)
FULL_SCORE_TOP_N_MAX = 150    # ceiling: bounds stage-2 scoring cost
# RAISED 100 -> 150, 2026-07-31, on a latency sweep rather than a $/hr grid alone. The old
# ceiling was CENSORING its own measurement: 100 was simultaneously the best cell and the
# boundary, so the grid could not say whether more was better. Swept tn 100/150/200 with
# wall-time: p50 solve 6,056 -> 6,065 ms (the anytime budget dominates, so shortlist size
# barely moves it), tn=150 +1.34% 6W/0L, tn=200 +1.09%. 150 is an INTERIOR optimum now, not
# a boundary — 200 is measurably worse, so the ceiling is no longer hiding the answer.
TOP_REJECTED_N = 3            # rejected alternatives reported (observability parity)
MAX_SNAPSHOT_AGE_MINUTES_DEFAULT = 75   # mirrors trading's maxListingAge
DEFAULT_SELL_DECAY = 0.9      # conservative fallback when tier not fitted
DEFAULT_BUY_GROWTH = 1.1
# Recovery-externality pricing. The reference window normalises a
# fitted half-life into a dimensionless multiplier: WEAK is the best-sampled mid tier
# (n_series 416 in the era-07-19 fit), so a WEAK sink prices near 1.0x and every other
# tier scales relative to it. A CONSTANT rather than a re-read of the artifact on
# purpose — a reference that moved with each re-fit would silently re-scale what
# externality_weight means. It is pinned to the era-07-19 WEAK half-life; a future
# re-fit that moves WEAK far from this re-scales the weight and should be repinned
# together with it.
EXTERNALITY_REFERENCE_MINUTES = 1599.2
# Below this many fitted control series a tier's half-life is a false prior
# (PLAYBOOK §12) and prices on the pooled untagged fit instead.
EXTERNALITY_MIN_FITTED_SERIES = 5
# Planned-depth ladder cap (harbormaster A-capped ruling 2026-07-09): interim
# stand-in for phase-2 recovery-externality pricing — see module docstring.
# sp-acb8 Tune 1: the DEFAULT for the now env-overridable throughput knob. It caps
# how deep a hull loads per (market, good, side) — the economy-analyst's dominant
# $/hr lever — so the replay can sweep it {2,3,4} and run.sh arm the winner later,
# exactly like TOUR_SOLVER_ORTOOLS_TIME_VALUE. Resolved ONCE per solve_tour call
# (_resolve_max_planned_tranches) and threaded to every read site so a single solve
# is internally consistent. DEFAULT-SAFE: absent/unset/invalid env -> 2 -> byte-
# identical to the pre-sp-acb8 hardcode (the governance gate — nothing moves until
# TOUR_SOLVER_MAX_PLANNED_TRANCHES is explicitly set).
MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE = 2  # env TOUR_SOLVER_MAX_PLANNED_TRANCHES, clamp [1, 6]
MAX_PLANNED_TRANCHES_ENV_VAR = "TOUR_SOLVER_MAX_PLANNED_TRANCHES"
MAX_PLANNED_TRANCHES_MIN = 1   # floor: 1 tranche still trades; 0 would plan no loads
MAX_PLANNED_TRANCHES_MAX = 6   # ceiling: well above the analyst's {2,3,4} sweep
# Realized per-visit market-sink absorption cap (sp-2v69u SECONDARY — capacity-aware buy budget).
# THE binding single-sink depth constraint, LIVE on deploy (Admiral ruling: no flag, no arm-seam;
# absorption-based depth SUPERSEDES the tranche-count MAX_PLANNED_TRANCHES where they conflict —
# the more-principled model). MAX_PLANNED_TRANCHES still bounds the pool depth per (market, good,
# side) across the WHOLE tour, but it is capacity-BLIND: it would let a 225-cargo freighter dump
# its full 2*trade_volume load into a SINGLE sink dock — tranches a shallow WEAK/RESTRICTED market
# cannot realize, so the excess strands (the bead's RC1/RC2). This bound is capacity-AWARE: a
# single sink VISIT realizes at most REALIZED_SINK_TRANCHES_PER_VISIT trade_volume tranches at the
# live quote; deeper same-visit tranches are the optimistic-decay bet that does not clear the
# execution sell floor under real (fleet-contended, shallow) depth. Because the greedy allocator
# matches each buy 1:1 to a sell, bounding realized per-visit sink absorption bounds the per-good
# BUY commitment to what the reachable sink graph can actually take — net of fleet-wide absorption,
# which net_absorption already nets out of each pool upstream. Revisits and multiple sinks are
# unaffected: each dock still absorbs its tranche, so a diverse or repeat-visit tour (with real
# travel-time recovery between visits) realizes its full spread. A named const (RULINGS #5); caps
# PLANNED units downward only, never weakening a spend guard (RULINGS #4).
#
# sp-28lw9 RECALIBRATION 1 -> 2.5, and the knob. sp-2v69u set this to ONE trade_volume to stop a
# heavy dumping an unrealizable load into a shallow dock. That bound is right; the NUMBER was far
# too tight, and it is now THE binding depth constraint. Measured in era 5 (player 5, 24h,
# tour_leg_telemetry JOIN market_data):
#   * 42.4% of planned sell legs sit pinned at exactly 1.0 x trade_volume (211/498)
#   * 496 of 499 planned sell legs realized EXACTLY the planned units — ZERO stranded, so the
#     strand sp-2v69u guarded against is not observable at the boundary
#   * avg realized depth 0.668 tranches/visit against 225-cargo heavies => ~23% hull utilization
# and the tiers that carry the volume decay only ~1-2% per tranche in the live era-07-19 fit
# (SCARCE|WEAK 0.9888 n_obs=3189, LIMITED|WEAK 0.9783 n=944, SCARCE|GROWING 0.9886 n=718), so a
# second and third tranche keep almost all of their spread. Depth is bounded ELSEWHERE too and
# those bounds are untouched: hull capacity (`slack`), the spend cap (`afford`), the per-(market,
# good,side) pool ladder (MAX_PLANNED_TRANCHES, armed at 3 in run.sh), and the fleet-wide
# absorption netting that shrinks each pool upstream.
# Env-overridable so a sweep — or a revert — needs no code change, mirroring
# TOUR_SOLVER_MAX_PLANNED_TRANCHES. Resolved ONCE per solve_tour
# (_resolve_realized_sink_tranches) and threaded to the read site. Disarm to the sp-2v69u
# behaviour exactly: TOUR_SOLVER_REALIZED_SINK_TRANCHES=1 + restart routing. FLOOR IS 1.0, NEVER
# 0: a 0 cap plans no sells at all and would silently halt trading (the MAX_PLANNED_TRANCHES
# floor reasoning). Fractional allowances floor to whole units — cargo is integral.
REALIZED_SINK_TRANCHES_PER_VISIT = 2.5  # env TOUR_SOLVER_REALIZED_SINK_TRANCHES, clamp [1.0, 6.0]
REALIZED_SINK_TRANCHES_ENV_VAR = "TOUR_SOLVER_REALIZED_SINK_TRANCHES"
REALIZED_SINK_TRANCHES_MIN = 1.0   # floor: one tranche still trades; 0 would plan no sells
REALIZED_SINK_TRANCHES_MAX = 6.0   # ceiling: matches MAX_PLANNED_TRANCHES_MAX (the pool bound)
CRUISE_TIME_MULTIPLIER = 31   # mirrors utils/routing_engine.FlightMode.CRUISE
GATE_HOP_ALLOWANCE_SECONDS = 450   # to-gate / from-gate hop (gate coords not carried)
JUMP_COOLDOWN_SECONDS = 900        # gate jump + cooldown
INTRA_SYSTEM_TRAVEL_SECONDS = 300   # flat fallback when no coords in the request
# AFFINE inter-system crossing charge (sp-smbgd): total(hops) = BASE + PER_HOP x hops.
#
# The two named allowances above already describe an affine cost, and the flat model
# multiplied BOTH by the hop count — so every extra gate hop re-charged the to-gate and
# from-gate legs that are paid exactly ONCE per crossing. That is the whole defect: a
# crossing's endpoint legs are FIXED and amortize over depth, while only the gate-to-gate
# jump+cooldown is marginal. Hence base ~ 2*GATE_HOP_ALLOWANCE (the endpoint legs) and
# per_hop ~ JUMP_COOLDOWN (the jump itself), which is what the constants meant all along.
#
# Constants are FITTED, not theoretical, and they are the ACTIVE defaults — this model is
# armed on deploy, with no flat-equivalent default path (Admiral standing order). Refit
# 2026-07-30 over tour_leg_telemetry: consecutive realized legs whose system changes, dt in
# [120s, 3h], hop depth by BFS over the SAME graph the Go caller feeds the solver from
# (open-era gate_edges, marker rows excluded, under-construction edges NOT traversable —
# gategraph.storedDistances). Median crossing seconds by depth, last 24h (n=567):
#   hops   1      2      3      4      5      6
#   med  1376   2105   2732   3571   3786   4161
# Weighted OLS on those medians => total(h) = 749 + 661*h; the fit is stable across
# 12h/24h/48h/72h windows (base 642-833, per_hop 632-723), so 750 + 650 is the fitted
# level rounded, and it lands within 6% of every measured median at depth 1-5.
#
# What this changes against the previously live flat 1200/hop: 1 hop 1200 -> 1400 (the flat
# charge was 13% UNDER-priced there, so near-lane ping-ponging gets DEARER, not cheaper),
# 2 hop 2400 -> 2050, 3 hop 3600 -> 2700, 4 hop 4800 -> 3350, 5 hop 6000 -> 4000. Deep
# crossings stop being over-priced ~30-60% and the long lanes the sensing expansion made
# visible can finally win on $/hr.
#
# Both terms stay env-tunable for operational retuning. The env var is deliberately RENAMED
# away from TOUR_SOLVER_INTER_SYSTEM_TRAVEL_SECONDS: that name meant "whole-crossing flat
# charge", and reusing it for the MARGINAL term would let a stale `=1200` export outside
# this repo silently price total(h) = 750 + 1200*h — worse than the model it replaced at
# every depth. Under the new names a stale old-name export is inert.
INTER_SYSTEM_TRAVEL_BASE_SECONDS = 750       # fixed per-crossing cost (~ the 2 endpoint legs)
INTER_SYSTEM_TRAVEL_PER_HOP_SECONDS = 650    # marginal cost of each gate hop (~ jump + cooldown)
INTER_SYSTEM_TRAVEL_BASE_ENV_VAR = "TOUR_SOLVER_INTER_SYSTEM_TRAVEL_BASE_SECONDS"
INTER_SYSTEM_TRAVEL_PER_HOP_ENV_VAR = "TOUR_SOLVER_INTER_SYSTEM_TRAVEL_PER_HOP_SECONDS"
# One clamp for both terms. The floor is POSITIVE on purpose, twice over: a crossing's
# endpoint legs and its jump are both real costs that physically exist, and a 0 floor on the
# base would let an operator silently restore the flat model this fit exists to remove. The
# bounds are chosen so the 1-hop TOTAL still spans exactly [600, 3600] — the same ping-pong
# floor and ceiling the old single-term clamp guaranteed.
INTER_SYSTEM_TRAVEL_TERM_MIN = 300
INTER_SYSTEM_TRAVEL_TERM_MAX = 1800

# Jump-gate FEES (sp-wtc47). CREDITS, not seconds. The affine model above prices a
# crossing's TIME; the gate also charges MONEY, and until now nothing subtracted it — so
# every cross-system candidate's projected profit was overstated by ~fee x hops. At the
# measured fleet rate that is ~570k/hr, ~15% of trading margin, and it biased BOTH
# objectives toward crossing. Rate doubly so: a fee adds cost without adding time, so it
# lowers the numerator of profit/seconds while leaving the denominator untouched.
#
# Fitted from realized rows (transactions type=JUMP, category=TRAVEL_COSTS, player 5,
# n=2,355 over 24h, total 14.11M):
#   min 4,386 | p25 5,204 | p50 5,619 | MEAN 5,992 | p75 6,342 | p90 7,435 | max 19,409
#   stddev 1,296
#
# THE MEAN, NOT THE MEDIAN — this choice is load-bearing, not cosmetic. The charge is
# SUMMED across hops, so the quantity we need is E[fee]: expected total = hops x E[fee].
# The distribution is right-skewed (mean 5,992 > median 5,619), so pricing at the median
# under-charges expected total by 6.2%. The median would be the right estimator only if we
# were predicting a SINGLE jump's typical cost, which is not what this term does.
#
# WHAT THE SINGLE CONSTANT COSTS, stated rather than hidden: the real fee is
# distance-scaled and spans 4,386-19,409, so one flat per-hop charge is unbiased in
# aggregate but wrong on any individual crossing — it under-prices long jumps and
# over-prices short ones. A per-pair lookup would fix that, and _build_inter_system_hop_index
# is the natural place to carry it since it already keys on (system, system). This is the
# deliberate median-first simplification the bead specified, not an oversight.
INTER_SYSTEM_JUMP_FEE_PER_HOP = 6000    # fitted MEAN 5,992, rounded (cf. 749->750, 661->650)
INTER_SYSTEM_JUMP_FEE_ENV_VAR = "TOUR_SOLVER_INTER_SYSTEM_JUMP_FEE_PER_HOP"
# The floor is POSITIVE for the same reason the travel terms' floor is, and one more: a 0
# floor would let an operator silently switch fee pricing OFF, which is exactly the
# default-off seam this must not have. The fitted default is ACTIVE — absent env means fees
# are charged, not that pricing is disabled. Bounds span the observed distribution with
# headroom for a retune.
INTER_SYSTEM_JUMP_FEE_MIN = 500
INTER_SYSTEM_JUMP_FEE_MAX = 25000
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
ORTOOLS_MAX_NODES = 80                 # per-model node cap after pruning; env TOUR_SOLVER_ORTOOLS_MAX_NODES, clamp [40, 400]
ORTOOLS_MAX_NODES_ENV_VAR = "TOUR_SOLVER_ORTOOLS_MAX_NODES"  # sp-7q5t/sp-fguo widening unlock
ORTOOLS_MAX_NODES_MIN = 40             # floor: below this a rich distant cluster is over-pruned before it can compete
ORTOOLS_MAX_NODES_MAX = 400            # ceiling: bounds per-model routing cost / p99 solve latency
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

# Longer-tour RATE arm (sp-ljh5, epic sp-fguo item #4). A governed, default-OFF runtime
# switch: for a LONGER-than-default tour (per-tour distinct-system cap raised above
# MAX_TOUR_SYSTEMS) the arm is the SOLE governor of the objective — see _resolve_objective's
# tier-2 (Option C). Ships DORMANT; the governed ACT of arming is (a) an operator raising the
# [trade_fleet].max_tour_systems cap knob > 2 AND (b) exporting TOUR_SOLVER_RATE_ARMED_LONG=1,
# and only after replay_objective.py --arm shows a fleet-$/hr win against the TRUE live-prod
# baseline (cap-2 RATE, baseline_cap_rate_cph) AND p99 solve latency within the anytime cap.
OBJECTIVE_LONG_TOUR_ARM_ENV_VAR = "TOUR_SOLVER_RATE_ARMED_LONG"
_ARMED_LONG_LOG_KEY = "armed_long"   # DISTINCT once-log key so the tier-2 and tier-3 (env)
                                     # RATE logs never suppress each other
_ARM_TRUTHY = frozenset({"1", "true", "yes", "on"})

_warned_tiers = set()
_logged_objective = set()
_logged_sequencer = set()


def _rate_armed_long():
    """Governed runtime arm for rate-on-long-tours (sp-ljh5, default OFF). Truthy set
    only; unset/''/'0'/'false'/'off' -> False (fail toward the proven profit default)."""
    return os.environ.get(OBJECTIVE_LONG_TOUR_ARM_ENV_VAR, "").strip().lower() in _ARM_TRUTHY


def _resolve_objective(objective, long_tour=False):
    """Resolve the selection objective. Precedence, top wins:
      (1) explicit per-solve `objective` argument in {profit, rate}  [sp-1wp8, unchanged]
      (2) LONG-TOUR governance (sp-ljh5): for a longer-than-default tour the arm flag is
          the SOLE governor of objective, DELIBERATELY superseding the global
          TOUR_SOLVER_OBJECTIVE launch default (main.go / run.sh set it =rate fleet-wide as
          a cap-2-validated default; at this layer that env is indistinguishable from an
          operator override). armed -> RATE; unarmed -> PROFIT. The epic requires long tours
          to default profit until replay_objective.py --arm shows a fleet-$/hr win against
          the TRUE live-prod baseline (cap-2 RATE) AND p99 latency within the anytime cap;
          only then is TOUR_SOLVER_RATE_ARMED_LONG set. Short tours never reach here.
      (3) explicit env TOUR_SOLVER_OBJECTIVE=rate (governs SHORT tours)  [sp-1wp8, unchanged]
      (4) profit fail-safe (unrecognized env warns once)                 [sp-1wp8, unchanged]
    `long_tour` is a pre-solve, deterministic boolean (the cap raised above the default);
    it is fixed before _sort_scored runs, so it can never depend on realized tour length."""
    if objective in (OBJECTIVE_PROFIT, OBJECTIVE_RATE):
        return objective
    if long_tour:
        if _rate_armed_long():
            if _ARMED_LONG_LOG_KEY not in _logged_objective:
                _logged_objective.add(_ARMED_LONG_LOG_KEY)
                logger.info("tour-solver: long-tour selection objective RATE (armed) via %s",
                            OBJECTIVE_LONG_TOUR_ARM_ENV_VAR)
            return OBJECTIVE_RATE
        return OBJECTIVE_PROFIT
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
    rates break on absolute profit.

    Degenerate (seconds<=0) candidates are QUARANTINED, not vetoed. score_sequence
    already pins cph to 0.0 whenever seconds<=0 — that IS the divide-by-zero guard —
    so under rate ordering a degenerate candidate sorts BELOW every profitable real
    plan for free, which is the safety property this function ever needed.

    sp-97ine falsifier (real reconstructed snapshots, 2026-07-31): the previous
    `all(seconds > 0)` guard was a WHOLE-POOL VETO — ONE degenerate candidate
    anywhere demoted the ENTIRE selection back to profit ordering. Every real
    stage-1 pool contains one: beam seeds each market as a single-waypoint
    sequence, a lone market cannot arb, so it scores 0 profit / 0 seconds (the
    "bare sink seed" test_tour_closure already documents as present in almost
    every real pool). Two consequences, both observed:
      1. OBJECTIVE_RATE was inoperative on live snapshots — every solve silently
         selected profit-primary — while still working on hand-built fixtures,
         which only engage cph ordering because they carry ballast cargo giving
         every candidate a leg (see test_tour_solver's _deep_lane_board note).
      2. Selection was NON-MONOTONIC in pool size: adding a candidate could change
         the ordering RULE for every other candidate. That silently voided the
         can-only-ADD contract the sp-97ine home-scoped union and the sp-y05b
         ortools union both rely on — the union could contribute exactly the right
         candidate, at an identical score, and the wider arm would still lose the
         $/hr comparison because it was never ranking on $/hr.
    Ordering now depends only on the objective, never on pool composition, which
    is what restores the strict-superset property (wide >= home-only on every
    case). An all-nonpositive pool still sorts a nonpositive candidate first under
    both objectives, so the no_profitable_tour guard below is unchanged."""
    if objective == OBJECTIVE_RATE:
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


def _resolve_anchor(constraints, ship, rows, allowed):
    """Resolve the sp-im74 closure anchor ONCE per solve into the constraints stash
    (underscore convention, like `_travel_fn`): `_anchor_return_wp` is the waypoint a
    CLOSED tour must end at, `_anchor_system` its system. Returns None on success or
    the structured infeasible reason when the anchor cannot be resolved.

    R1 closed falsey         -> no-op, nothing stashed (open tours never consult it).
    R2 anchor_system ""      -> FLOATING: return to the ship's current waypoint.
    R3 anchor == ship's own  -> floating semantics (R2), not a lexicographic pick.
    R4 anchor not in scope   -> "anchor_system_not_in_scope" (allowed_systems gate).
    R5 no fresh market rows  -> "anchor_system_no_return_waypoint".
    R6 in-scope foreign      -> that system's lexicographically-FIRST fresh market
                                waypoint, computed from the filtered market rows
                                BEFORE deposit/stock synthesis adds non-market nodes.
    """
    if not constraints.get("closed"):
        return None
    anchor = (constraints.get("anchor_system") or "").strip()
    if not anchor or anchor == ship["current_system"]:
        constraints["_anchor_return_wp"] = ship["current_waypoint"]
        constraints["_anchor_system"] = ship["current_system"]
        return None
    if anchor not in allowed:
        return "anchor_system_not_in_scope"
    wps = sorted(r["waypoint_symbol"] for r in rows if r["system_symbol"] == anchor)
    if not wps:
        return "anchor_system_no_return_waypoint"
    constraints["_anchor_return_wp"] = wps[0]
    constraints["_anchor_system"] = anchor
    return None


def impact_factor(model, tier, is_buy):
    """The ONE resolution of a market's fitted per-tranche price factor.

    Both the tranche ladder and the recovery-externality charge must read the
    SAME number for the same market: two decay values for one market is the
    market is a second table free to drift. Missing tier or side -> conservative default,
    logged once per (tier, side) per process.
    """
    entry = (model.get("impact") or {}).get(tier) or {}
    key = "buy_growth_per_step" if is_buy else "sell_decay_per_step"
    factor = entry.get(key)
    if factor is None:
        factor = DEFAULT_BUY_GROWTH if is_buy else DEFAULT_SELL_DECAY
        if (tier, key) not in _warned_tiers:
            _warned_tiers.add((tier, key))
            logger.info("tour-solver: tier-missing %s (%s) — conservative default %.2f",
                        tier, key, factor)
    return factor


def tranche_prices(quote, trade_volume, tier, model, is_buy, max_units):
    """Piecewise price schedule: list of (units, unit_price) tranches.

    Tranche 0 is at the live quote; each subsequent tradeVolume-sized
    tranche is multiplied by the tier's fitted decay (sell) / growth (buy)
    factor.
    """
    if quote <= 0 or trade_volume <= 0 or max_units <= 0:
        return []
    factor = impact_factor(model, tier, is_buy)
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


def externality_cost_per_unit(activity, units, trade_volume, sell_price,
                              weight, recovery_tbl, sell_decay=None):
    """Per-unit charge for the FUTURE recovery burden a sell tranche imposes on
    the rest of the fleet.

    Three disjoint accounts, so this cannot double-count:
      - PAST crush    -> already in the live quote
      - PLANNED depth -> netted as CAPACITY by net_absorption()
      - FUTURE crush  -> this term, and nothing else prices it

    Per UNIT so it is commensurable with `margin` (also per unit); charging a
    per-tranche total against a per-unit margin would bias against big tranches.

    Fails OPEN (0.0 = today's ordering) on any unreadable input. This is an
    OBJECTIVE term, not a spend guard: RULINGS #4 governs guards, and degrading
    to the measured baseline is the safe direction for a price.
    """
    if not recovery_tbl or weight <= 0 or trade_volume <= 0 or units <= 0:
        return 0.0

    half_life = _recovery_half_life(recovery_tbl, activity)
    if half_life <= 0:
        return 0.0

    # Crush per tranche = 1 - the fitted sell-decay the tranche builder already
    # applies as `price *= factor`. The caller threads the SAME resolved factor
    # (impact_factor); DEFAULT_SELL_DECAY is only this module's unfitted fallback.
    decay = DEFAULT_SELL_DECAY if sell_decay is None else sell_decay
    crush_per_tranche = max(0.0, 1.0 - decay)

    tranches = units / float(trade_volume)
    recovery_multiple = half_life / EXTERNALITY_REFERENCE_MINUTES
    return weight * tranches * recovery_multiple * sell_price * crush_per_tranche


def _recovery_half_life(recovery_tbl, activity):
    """Fitted recovery half-life for an activity, in minutes, or 0.0 when the
    table can price nothing.

    A tier fitted on fewer than EXTERNALITY_MIN_FITTED_SERIES control series is
    not a trustworthy prior (PLAYBOOK §12), so it prices on the pooled untagged
    fit instead of its own thin one — as does an activity the table never saw.
    The pool itself is the fallback of last resort and is used whatever its n.
    """
    tier = recovery_tbl.get(activity) or {}
    if (tier.get("n_series") or 0) < EXTERNALITY_MIN_FITTED_SERIES:
        tier = recovery_tbl.get("") or {}
    return tier.get("half_life_minutes") or 0.0


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


def _build_inter_system_hop_index(constraints):
    """Build a SYMMETRIC {(from_system, to_system): gate_hops} lookup from the request-carried
    `inter_system_hops` list (sp-tp5c3). The Go caller computes these gate-hop distances over
    the SAME gate graph the reposition/candidate walk uses; the solver never sees the jump-gate
    graph itself, so this fed distance is the ONE multi-hop route cost (no duplicated path-cost
    logic here). Gate-hop distance is symmetric, so both directions are stored. A non-int /
    non-positive / partial entry is DROPPED — the crossing then defaults to 1 hop (the flat
    charge), so a malformed or incomplete feed can never zero-price or under-price BELOW today's
    baseline. Empty / absent -> {} -> every crossing defaults to 1 hop, byte-identical to the
    pre-multi-hop flat model (the un-widened / degraded path)."""
    index = {}
    for entry in constraints.get("inter_system_hops") or []:
        from_system = entry.get("from_system")
        to_system = entry.get("to_system")
        gate_hops = entry.get("gate_hops")
        if (not from_system or not to_system
                or not isinstance(gate_hops, int) or gate_hops <= 0):
            continue
        index[(from_system, to_system)] = gate_hops
        index[(to_system, from_system)] = gate_hops
    return index


def _make_gate_fee_fn(constraints, markets, ship):
    """Gate-FEE fn(a, b) -> credits (sp-wtc47). The money sibling of _make_travel_fn.

    Deliberately mirrors _make_travel_fn's crossing test rather than reimplementing it: the
    same system_of resolution and the same inter_system_hops lookup with the same 1-hop
    default for an absent pair. If the two ever disagreed about what constitutes a crossing,
    a tour could be charged time without money or the reverse, which is worse than either
    charge being wrong.

    Returns 0 for an intra-system move — no gate, no fee — so an all-local tour is priced
    exactly as before this change.

    NOT hooked to `_travel_fn`. A caller-supplied travel hook overrides TIME only; it
    carries no fee model, and honouring it here would mean any test or caller passing a
    custom travel_fn silently reverted to unpriced crossings. Fees are charged on the real
    system topology regardless of how travel time is computed."""
    fee_per_hop = _resolve_inter_system_jump_fee_per_hop()
    inter_system_hops = _build_inter_system_hop_index(constraints)

    def system_of(wp):
        if wp in markets:
            return markets[wp]["system"]
        if wp == ship["current_waypoint"]:
            return ship["current_system"]
        return None

    def fee(a, b):
        if a == b:
            return 0
        sys_a, sys_b = system_of(a), system_of(b)
        if sys_a and sys_b and sys_a != sys_b:
            gate_hops = inter_system_hops.get((sys_a, sys_b), 1)
            return gate_hops * fee_per_hop
        return 0

    return fee


def _make_travel_fn(constraints, markets, ship, waypoints=None):
    """Travel-seconds fn(a, b). Precedence: caller-supplied `_travel_fn`
    hook > coordinate mode (CRUISE formula on request-carried TourWaypoint
    coords) > flat named defaults (degraded mode, logged warning)."""
    custom = constraints.get("_travel_fn")
    if callable(custom):
        return custom

    coords = {w["symbol"]: (w["x"], w["y"]) for w in (waypoints or [])}
    engine_speed = max(1, ship.get("engine_speed") or 1)
    # sp-smbgd: resolve BOTH affine terms ONCE per build so every crossing in this solve is
    # priced by the same model — the discipline the single flat charge already followed.
    inter_system_base = _resolve_inter_system_travel_base_seconds()
    inter_system_per_hop = _resolve_inter_system_travel_per_hop_seconds()
    # sp-tp5c3: the per-pair gate-hop distance map. Empty (un-widened default / no feed) -> {} ->
    # every crossing prices at 1 hop = the flat charge, byte-identical to today.
    inter_system_hops = _build_inter_system_hop_index(constraints)

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
            # Gate positions are not request-carried: price the crossing AFFINELY in the REAL
            # gate-hop count (sp-smbgd) — a fixed base for the to-gate/from-gate endpoint legs,
            # paid once per crossing, plus the marginal jump+cooldown per hop. The flat
            # gate_hops x charge model this replaces re-charged the endpoint legs on every hop,
            # over-pricing a 3-hop crossing ~32% and a 5-hop one ~58% against realized telemetry.
            # gate_hops comes from the fed distance map, DEFAULTING to 1 hop when the pair is
            # absent — the shallowest (cheapest) crossing, so an incomplete feed under-prices
            # rather than refuses, exactly as before.
            gate_hops = inter_system_hops.get((sys_a, sys_b), 1)
            return inter_system_base + gate_hops * inter_system_per_hop
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


# --- Allocation trace (sp-o2dzb diagnosis) -----------------------------------
#
# PURE OBSERVABILITY. `_ALLOC_TRACE` is None in every production path; the solver
# never reads it and no branch on it can change an allocation. Set it to a list
# from a replay harness and each committed allocation appends the value of EVERY
# term that could have bounded it, plus the argmin — which is the only honest way
# to answer "which term binds", since a pinned value never names its constraint.
#
# The terms are RECOMPUTED for the winning pairing after `best` is chosen and
# before any `take()`, so the pools, `occ`, `spend` and `sold_this_visit` are all
# in exactly the state the scan saw. That keeps the inner candidate loop — the hot
# path — byte-identical, at the cost of one extra head() per commit while tracing.
_ALLOC_TRACE = None


def _trace_allocation(best, alive, seq, markets, hold_cap, occ, spend, spend_cap,
                      min_margin, initial_left, sold_this_visit, realized_sink_tranches,
                      sink_for, source_for):
    """Append one trace record for `best`, or a termination census when best is None."""
    if best is None:
        # The loop is about to stop. Classify every pairing by the FIRST gate that
        # zeroed it, in the same order the scan applies them — this is what actually
        # ends a plan, and it is invisible from the leg output.
        census = {}
        for good, i, j, kind in alive:
            sell_rem, sell_price = sink_for(kind, j, good).head()
            if sell_rem <= 0:
                reason = "sell_pool_exhausted"
            elif i is None:
                reason = ("launch_cargo_exhausted"
                          if initial_left.get(good, 0) <= 0 or sell_price < 1
                          else "residual_launch_cargo")
            else:
                buy_rem, buy_price = source_for(kind, i, good).head()
                if buy_rem <= 0 or buy_price <= 0:
                    reason = "buy_pool_exhausted"
                elif sell_price - buy_price < min_margin:
                    # Distinguish a pairing THIS plan traded until the spread closed
                    # (an economically correct stop) from one that was never profitable
                    # at the live quotes (structural — no cap change can reach it).
                    src, snk = source_for(kind, i, good), sink_for(kind, j, good)
                    touched = src.idx or src.used or snk.idx or snk.used
                    reason = ("margin_closed_by_our_own_trading" if touched
                              else "margin_never_positive")
                else:
                    slack = hold_cap - max(occ[i:j]) if j > i else 0
                    afford = (spend_cap - spend) // buy_price
                    visit_rem = (int(realized_sink_tranches
                                     * markets[seq[j]]["goods"][good]["trade_volume"])
                                 - sold_this_visit.get((j, good), 0))
                    zeroed = [n for n, v in (("hold_slack", slack), ("afford", afford),
                                             ("visit_cap", visit_rem)) if v <= 0]
                    reason = "+".join(zeroed) if zeroed else "unclassified"
            census[reason] = census.get(reason, 0) + 1
        _ALLOC_TRACE.append(dict(event="terminated", census=census,
                                 peak_occupancy=max(occ) if occ else 0,
                                 hold_cap=hold_cap, spend=spend, spend_cap=spend_cap))
        return

    _, good, i, j, units, _buy_price, _sell_price, kind = best
    sell_rem, _sp = sink_for(kind, j, good).head()
    if i is None:
        terms = {"launch_cargo_left": initial_left.get(good, 0), "sell_rem": sell_rem}
    else:
        buy_rem, buy_price = source_for(kind, i, good).head()
        terms = {"buy_rem": buy_rem, "sell_rem": sell_rem,
                 "hold_slack": hold_cap - max(occ[i:j]) if j > i else 0,
                 "afford": (spend_cap - spend) // buy_price}
    if kind != "deposit":
        terms["visit_cap"] = (int(realized_sink_tranches
                                  * markets[seq[j]]["goods"][good]["trade_volume"])
                              - sold_this_visit.get((j, good), 0))
    _ALLOC_TRACE.append(dict(event="alloc", good=good, buy_leg=i, sell_leg=j, kind=kind,
                             units=units, terms=terms,
                             binding=sorted(n for n, v in terms.items() if v == units)))


def score_sequence(seq, markets, ship, constraints, model, travel_fn, deposit_sinks=None,
                   absorption_index=None, stock_sources=None, max_planned_tranches=None,
                   realized_sink_tranches=None, gate_fee_fn=None):
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

    Closure (sp-im74): when constraints carry closed=True and the solve_tour-resolved
    `_anchor_return_wp` stash, a priced NO-TRADE return hop is appended after the
    prune unless the tour already ends at the anchor — travel + dwell charged into
    seconds/cph, profit untouched. Absent/False -> byte-identical open scoring.
    """
    deposit_sinks = deposit_sinks or {}
    absorption_index = absorption_index or {}
    stock_sources = stock_sources or {}
    if max_planned_tranches is None:   # sp-acb8: env-resolve for direct callers; solve_tour threads it
        max_planned_tranches = _resolve_max_planned_tranches()
    if realized_sink_tranches is None:  # sp-28lw9: same contract as max_planned_tranches
        realized_sink_tranches = _resolve_realized_sink_tranches()
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
    # Recovery-externality pricing, resolved ONCE per scoring so one solve is
    # internally consistent. The recovery table is read straight off the artifact
    # already threaded here — the SINGLE fitted table, never a Python redeclaration.
    # 0 weight / no table -> every charge is 0.0 -> byte-identical to today.
    externality_weight = constraints.get("externality_weight") or 0.0
    recovery_tbl = (model or {}).get("recovery")
    externality_priced = externality_weight > 0 and bool(recovery_tbl)

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
                         max_planned_tranches * tv)
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
    # sp-2v69u SECONDARY: units already sold at each market-sink VISIT keyed by (sell_leg, good).
    # Bounds one dock's realized absorption to REALIZED_SINK_TRANCHES_PER_VISIT * trade_volume so a
    # heavy hull cannot dump its excess capacity into a single sink (a repeat visit or another sink
    # each get their own tranches). DEPOSIT sinks (synthetic transfers, no crush) are exempt.
    sold_this_visit = {}

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
            # sp-2v69u SECONDARY (LIVE): a single market-sink visit realizes at most
            # REALIZED_SINK_TRANCHES_PER_VISIT trade_volume tranches; cap this pairing's units at
            # the dock's remaining per-visit absorption so a heavy never over-concentrates its
            # capacity into one sink (buys are matched to sells, so this bounds the per-good BUY
            # commitment). DEPOSIT sinks are synthetic transfers with no market crush — exempt.
            # The pool head is already fleet-absorption-netted; this is the binding single-sink
            # depth constraint, superseding MAX_PLANNED_TRANCHES per visit where they conflict.
            if kind != "deposit":
                # sp-28lw9: the allowance is a FRACTION of a trade_volume, floored to whole
                # units — cargo is integral, and flooring keeps the cap from ever rounding UP
                # past the modeled absorption.
                visit_rem = (int(realized_sink_tranches
                                 * markets[seq[j]]["goods"][good]["trade_volume"])
                             - sold_this_visit.get((j, good), 0))
                if units > visit_rem:
                    units = visit_rem
            if units <= 0:
                continue
            # Rank on the externality-adjusted margin: at equal spread a hull now
            # prefers the sink the fleet is not still recovering. Deposits are
            # synthetic inventory transfers with no market crush — exempt, exactly
            # like the per-visit absorption cap above.
            #
            # NOTE the min_margin gate above tests the RAW margin, deliberately: this
            # term reorders PREFERENCE, it does not decide ELIGIBILITY. Gating on the
            # adjusted margin would silently tighten a spend guard, which RULINGS #4
            # forbids as a side effect.
            eff_margin = margin
            if externality_priced and kind != "deposit":
                srow = markets[seq[j]]["goods"][good]
                eff_margin -= externality_cost_per_unit(
                    srow.get("activity"), units, srow["trade_volume"], sell_price,
                    externality_weight, recovery_tbl,
                    sell_decay=impact_factor(model, _tier_of(srow), is_buy=False))
            key = (eff_margin, -j, -(i if i is not None else -1))
            if best is None or key > best[0]:
                best = (key, good, i, j, units, buy_price, sell_price, kind)
        if _ALLOC_TRACE is not None:
            _trace_allocation(best, alive, seq, markets, hold_cap, occ, spend, spend_cap,
                              min_margin, initial_left, sold_this_visit,
                              realized_sink_tranches, sink_for, source_for)
        if best is None:
            break
        _, good, i, j, units, buy_price, sell_price, kind = best
        sink_for(kind, j, good).take(units)
        if kind != "deposit":   # sp-2v69u SECONDARY: bank this dock's realized absorption
            sold_this_visit[(j, good)] = sold_this_visit.get((j, good), 0) + units
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

    # sp-wtc47: gate fees are CREDITS and accumulate alongside seconds, on the same walk
    # over the same pairs, so a crossing can never be charged time without money.
    #
    # The fn is a PARAMETER with a build-if-absent fallback, not a memo stashed on
    # constraints. Two reasons, both learned the hard way: stashing it would MUTATE the
    # caller's own dict (unlike `_travel_fn`, which the caller supplies, this is something
    # the solver writes), and a caller that reused or copied that dict across solves would
    # then carry a stale fee resolved under different env. The fallback is what keeps the
    # charge unconditional — there is no path to scoring that skips it — while solve_tour
    # passes a prebuilt fn so the hot path never rebuilds the hop index per candidate.
    if gate_fee_fn is None:
        gate_fee_fn = _make_gate_fee_fn(constraints, markets, ship)
    gate_fees = 0

    seconds = 0
    prev = ship["current_waypoint"]
    for leg in legs:
        hop = int(travel_fn(prev, leg["waypoint_symbol"]))
        leg["travel_seconds_from_prev"] = hop
        seconds += hop + DWELL_SECONDS_PER_LEG
        gate_fees += int(gate_fee_fn(prev, leg["waypoint_symbol"]))
        prev = leg["waypoint_symbol"]

    # sp-im74 closure epilogue: a CLOSED tour ends at the anchor solve_tour resolved
    # into the `_anchor_return_wp` stash — append the priced NO-TRADE return hop and
    # charge its travel + dwell into time/cph, never profit. Living here (after the
    # no-trade prune, before cph) makes EVERY stage-1 sequencer closure-correct.
    # Guards: E1 empty legs stay a 0-second degenerate (the _sort_scored zero-time
    # pin, and a bare-seed candidate must never crash the pool); E2 a tour already
    # ending at the anchor appends nothing (open-equal by construction).
    return_wp = constraints.get("_anchor_return_wp")
    if constraints.get("closed") and return_wp and legs \
            and legs[-1]["waypoint_symbol"] != return_wp:
        hop = int(travel_fn(prev, return_wp))
        legs.append(dict(waypoint_symbol=return_wp,
                         system_symbol=constraints["_anchor_system"],
                         trades=[], projected_leg_profit=0,
                         travel_seconds_from_prev=hop))
        seconds += hop + DWELL_SECONDS_PER_LEG
        # The return hop carries no TRADE, so it contributes no profit — but if it crosses a
        # gate it still costs real credits. Time and money part company here: the epilogue's
        # travel is charged to the clock only, its fee to the purse.
        gate_fees += int(gate_fee_fn(prev, return_wp))

    # sp-wtc47: fees are subtracted from projected profit, which is what makes them bite in
    # BOTH objectives — profit ordering sees the lower number directly, and cph sees it
    # through the numerator while the denominator is untouched (a fee costs money, not time).
    # That asymmetry is the point: it is why an unpriced fee biased rate ordering harder than
    # profit ordering.
    profit -= gate_fees

    cph = profit / (seconds / 3600.0) if seconds > 0 else 0.0
    return dict(profit=profit, spend=spend, seconds=seconds, cph=cph, legs=legs,
                held_liquidation=held_liquidation, deposit_value=deposit_value,
                stock_value=stock_value, gate_fees=gate_fees)


def _held_liquidation_value(wp, markets, initial_cargo):
    """Value of liquidating the ship's held cargo at wp's market bids.

    Mirror of beam_sequences.liquidation_gain — keep in sync (sp-y05b).
    beam stays byte-untouched (default-safety + sibling merge pressure);
    the T6/T7 brute-force equality tests catch semantic drift."""
    goods = markets[wp]["goods"]
    return sum(units * goods[g]["bid"] for g, units in initial_cargo.items()
               if g in goods and goods[g]["bid"] > 0)


def _pair_gain(wp_from, wp_to, markets, hold, deposit_sinks, stock_by_wp,
               max_planned_tranches=MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE):
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
            depth = max_planned_tranches * max(
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


def _prune_nodes(markets, ship, constraints, deposit_sinks, stock_by_wp,
                 max_planned_tranches=MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE,
                 ortools_max_nodes=ORTOOLS_MAX_NODES):
    """Two-phase node pruning for the ortools subset models (sp-y05b).

    Phase 1 — cheap prefilter on per-good global max_bid/min_ask with the
    SAME margin floor score_sequence applies (max(1, min_margin_per_unit)):
    a positive directed pair (a, b) on good g at the floor implies
    a.ask <= max_bid[g] - floor and b.bid >= min_ask[g] + floor, so no pair
    participant is ever dropped (strict superset of the pair criterion).
    Also kept: held-cargo liquidation sinks (beam's liquidation-seed parity),
    deposit-sink/stock-source hosts, and the ship's current waypoint.

    Phase 2 — if still over the node cap ortools_max_nodes, rank by max incident
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
    if len(kept) <= ortools_max_nodes:
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
            g = max(_pair_gain(wp, other, markets, hold, deposit_sinks, stock_by_wp,
                               max_planned_tranches),
                    _pair_gain(other, wp, markets, hold, deposit_sinks, stock_by_wp,
                               max_planned_tranches))
            if g > best:
                best = g
        return best + _held_liquidation_value(wp, markets, initial)

    ranked = sorted((wp for wp in kept if wp not in exempt),
                    key=lambda wp: (-node_potential(wp), wp))
    room = max(0, ortools_max_nodes - len(exempt))
    survivors = set(ranked[:room]) | exempt
    return [wp for wp in kept if wp in survivors]


def beam_sequences(markets, ship, constraints, travel_fn, deposit_sinks=None,
                   stock_sources=None, max_planned_tranches=None):
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
    if max_planned_tranches is None:   # sp-acb8: env-resolve for direct callers; solve_tour threads it
        max_planned_tranches = _resolve_max_planned_tranches()
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
                depth = max_planned_tranches * max(
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


def _resolve_max_planned_tranches():
    """Per-solve env override for the planned-tranche ladder cap
    (TOUR_SOLVER_MAX_PLANNED_TRANCHES, sp-acb8 Tune 1). Delegates to
    _sequencer_env_scalar, so it clamps to [MAX_PLANNED_TRANCHES_MIN,
    MAX_PLANNED_TRANCHES_MAX] and falls back to the
    MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE default (2) on absent/unset/non-int
    — byte-identical to the pre-sp-acb8 hardcode when the env is not set. The floor
    is 1 (NEVER 0: a 0 cap plans no loads and would silently halt trading). Resolved
    ONCE at the top of solve_tour and threaded to every read site (score_sequence,
    beam_sequences, ortools_sequences -> _prune_nodes/_pair_gain) so a single solve
    is internally consistent — the same discipline as the ORTOOLS_TIME_VALUE lambda."""
    return _sequencer_env_scalar(MAX_PLANNED_TRANCHES_ENV_VAR,
                                 MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE,
                                 MAX_PLANNED_TRANCHES_MIN,
                                 MAX_PLANNED_TRANCHES_MAX, int)


def _resolve_realized_sink_tranches():
    """Per-solve env override for the realized per-visit sink depth cap
    (TOUR_SOLVER_REALIZED_SINK_TRANCHES, sp-28lw9). Delegates to _sequencer_env_scalar, so it
    clamps to [REALIZED_SINK_TRANCHES_MIN, REALIZED_SINK_TRANCHES_MAX] and falls back to the
    REALIZED_SINK_TRANCHES_PER_VISIT default (2.5) on absent/unset/non-float. Cast is FLOAT (not
    int, unlike the tranche-count knobs): the calibration is a fraction of a trade_volume, and
    the read site floors the product to whole units. The floor is 1.0 (NEVER 0: a 0 cap plans no
    sells and would silently halt trading). Setting it to 1 restores the sp-2v69u calibration
    exactly — the documented disarm. Resolved ONCE at the top of solve_tour and threaded to
    score_sequence so a single solve is internally consistent, the same discipline as
    _resolve_max_planned_tranches."""
    return _sequencer_env_scalar(REALIZED_SINK_TRANCHES_ENV_VAR,
                                 REALIZED_SINK_TRANCHES_PER_VISIT,
                                 REALIZED_SINK_TRANCHES_MIN,
                                 REALIZED_SINK_TRANCHES_MAX, float)


def _resolve_full_score_top_n():
    """Per-solve env override for the stage-2 full-scoring cut
    (TOUR_SOLVER_FULL_SCORE_TOP_N, sp-7q5t/sp-fguo widening unlock). Delegates to
    _sequencer_env_scalar, so it clamps to [FULL_SCORE_TOP_N_MIN, FULL_SCORE_TOP_N_MAX]
    and falls back to the FULL_SCORE_TOP_N default (20) on absent/unset/non-int —
    byte-identical to the pre-widening hardcode when the env is not set. The floor is
    10 (NEVER 0: a 0/negative top-N would admit no sequence to full scoring and return
    no tour). Resolved ONCE at the top of solve_tour and used at every cut site — the
    same discipline as _resolve_max_planned_tranches."""
    return _sequencer_env_scalar(FULL_SCORE_TOP_N_ENV_VAR,
                                 FULL_SCORE_TOP_N,
                                 FULL_SCORE_TOP_N_MIN,
                                 FULL_SCORE_TOP_N_MAX, int)


def _resolve_ortools_max_nodes():
    """Per-solve env override for the OR-Tools per-model node cap
    (TOUR_SOLVER_ORTOOLS_MAX_NODES, sp-7q5t/sp-fguo widening unlock). Delegates to
    _sequencer_env_scalar, so it clamps to [ORTOOLS_MAX_NODES_MIN, ORTOOLS_MAX_NODES_MAX]
    and falls back to the ORTOOLS_MAX_NODES default (80) on absent/unset/non-int —
    byte-identical when the env is not set. Resolved ONCE inside ortools_sequences
    (alongside the lam/budget/max_subsets OR-Tools knobs, since the node cap is only
    reached on the ortools path) and threaded to _prune_nodes so both node-cap read
    sites use one consistent value — the same discipline as TOUR_SOLVER_ORTOOLS_TIME_VALUE."""
    return _sequencer_env_scalar(ORTOOLS_MAX_NODES_ENV_VAR,
                                 ORTOOLS_MAX_NODES,
                                 ORTOOLS_MAX_NODES_MIN,
                                 ORTOOLS_MAX_NODES_MAX, int)


def _resolve_inter_system_travel_base_seconds():
    """Per-solve env override for the FIXED term of the affine crossing charge
    (TOUR_SOLVER_INTER_SYSTEM_TRAVEL_BASE_SECONDS, sp-smbgd). Delegates to
    _sequencer_env_scalar, so it clamps to [INTER_SYSTEM_TRAVEL_TERM_MIN,
    INTER_SYSTEM_TRAVEL_TERM_MAX] and falls back to the FITTED default
    INTER_SYSTEM_TRAVEL_BASE_SECONDS (750) on absent/unset/non-int. The fitted value is
    the ACTIVE default: absent env is the armed affine model, not a flat-equivalent
    fallback. Resolved ONCE per _make_travel_fn build alongside the per-hop term so every
    crossing in a single solve is priced by one model — the same discipline as
    _resolve_max_planned_tranches."""
    return _sequencer_env_scalar(INTER_SYSTEM_TRAVEL_BASE_ENV_VAR,
                                 INTER_SYSTEM_TRAVEL_BASE_SECONDS,
                                 INTER_SYSTEM_TRAVEL_TERM_MIN,
                                 INTER_SYSTEM_TRAVEL_TERM_MAX, int)


def _resolve_inter_system_travel_per_hop_seconds():
    """Per-solve env override for the MARGINAL per-gate-hop term of the affine crossing
    charge (TOUR_SOLVER_INTER_SYSTEM_TRAVEL_PER_HOP_SECONDS, sp-smbgd). Same clamp and
    fallback discipline as _resolve_inter_system_travel_base_seconds; fitted default
    INTER_SYSTEM_TRAVEL_PER_HOP_SECONDS (650). NOT the old
    TOUR_SOLVER_INTER_SYSTEM_TRAVEL_SECONDS under a new meaning — that name is retired
    precisely so a stale export of it cannot be read as this term."""
    return _sequencer_env_scalar(INTER_SYSTEM_TRAVEL_PER_HOP_ENV_VAR,
                                 INTER_SYSTEM_TRAVEL_PER_HOP_SECONDS,
                                 INTER_SYSTEM_TRAVEL_TERM_MIN,
                                 INTER_SYSTEM_TRAVEL_TERM_MAX, int)


def _resolve_inter_system_jump_fee_per_hop():
    """Per-solve env override for the per-gate-hop CREDIT fee (sp-wtc47,
    TOUR_SOLVER_INTER_SYSTEM_JUMP_FEE_PER_HOP). Same clamp-and-fallback discipline as the
    two travel-seconds resolvers; fitted default INTER_SYSTEM_JUMP_FEE_PER_HOP (6000, the
    realized MEAN rounded).

    The env var exists to RE-FIT the number as gate pricing moves, NOT to switch fee
    pricing off: the clamp floor is positive, so no export can zero this term, and an
    absent env resolves to the fitted default rather than to unpriced. Charging fees is the
    armed behaviour."""
    return _sequencer_env_scalar(INTER_SYSTEM_JUMP_FEE_ENV_VAR,
                                 INTER_SYSTEM_JUMP_FEE_PER_HOP,
                                 INTER_SYSTEM_JUMP_FEE_MIN,
                                 INTER_SYSTEM_JUMP_FEE_MAX, int)


def ortools_sequences(markets, ship, constraints, travel_fn, deposit_sinks=None,
                      stock_sources=None, stats_out=None, max_planned_tranches=None):
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
    if max_planned_tranches is None:   # sp-acb8: env-resolve for direct callers; solve_tour threads it
        max_planned_tranches = _resolve_max_planned_tranches()
    # sp-7q5t/sp-fguo: resolve the per-model node cap alongside the sibling OR-Tools
    # knobs (lam/budget/max_subsets) — the node cap is only reached on this path — and
    # thread it to _prune_nodes so both node-cap read sites use one consistent value.
    ortools_max_nodes = _resolve_ortools_max_nodes()

    pruned = _prune_nodes(markets, ship, constraints, deposit_sinks, stock_by_wp,
                          max_planned_tranches, ortools_max_nodes=ortools_max_nodes)
    # sp-im74: CLOSED mode prices the return-to-anchor on the virtual END arc, so the
    # in-model route optimum is a closed circuit. Open (closed falsey, or a direct
    # caller without the solve_tour stash) keeps all end arcs at 0 — byte-identical.
    closed_return_wp = (constraints.get("_anchor_return_wp")
                        if constraints.get("closed") else None)
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
            g = _pair_gain(a, b, markets, hold, deposit_sinks, stock_by_wp,
                           max_planned_tranches)
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
            virtual_node = n_real  # terminal; sp-im74 CLOSED prices its inbound arc

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

            # sp-im74 end arcs: closed -> each node's priced hop home (0 at the
            # anchor itself, mirroring the stage-2 no-op); open -> all zeros, so the
            # transit values below stay byte-identical to the free F10 terminal.
            end_cost = [0] * n_real
            if closed_return_wp:
                for i, a in enumerate(real):
                    if a != closed_return_wp:
                        t = travel_fn(a, closed_return_wp)
                        end_cost[i] = int((t + DWELL_SECONDS_PER_LEG)
                                          * lam * COST_SCALE)

            manager = pywrapcp.RoutingIndexManager(n_real + 1, 1,
                                                   [start_idx], [virtual_node])
            routing = pywrapcp.RoutingModel(manager)

            def transit(from_index, to_index, _m=manager, _c=arc_cost,
                        _v=virtual_node, _e=end_cost):
                to_node = _m.IndexToNode(to_index)
                if to_node == _v:
                    # F10: BEFORE any travel lookup. Open: free terminal (0).
                    # Closed (sp-im74): the priced return-to-anchor arc.
                    from_node = _m.IndexToNode(from_index)
                    return _e[from_node] if from_node < _v else 0
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
                gate_fees=0,
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

    Closure (sp-im74): constraints `closed` / `anchor_system` make the planned tour
    END at an anchor. The anchor is resolved once per solve (see _resolve_anchor,
    R1-R6) into the `_anchor_return_wp`/`_anchor_system` stash; score_sequence
    appends the priced no-trade return hop (every sequencer closure-correct) and the
    ortools model additionally prices the return on its virtual end arc. Unresolvable
    anchors fail structured: "anchor_system_not_in_scope" /
    "anchor_system_no_return_waypoint". closed unset/False + anchor_system "" (the
    proto3 zero-values) are a strict no-op — byte-identical open planning.

    Fail-loud contract: missing artifact or version mismatch are structured
    infeasible reasons, never a silent fallback (spec error-handling table).
    """
    # sp-ljh5: "long" == the request opted the per-tour distinct-system cap ABOVE the
    # default (consume syaz's canonical _effective_tour_systems — the single read, already
    # falsy-zero/clamp safe). At the epic default (cap absent/0/2 -> 2) long_tour is False,
    # so tier-2 is skipped, _rate_armed_long() is never called, and resolution is
    # byte-identical to pre-ljh5. This must be fixed pre-solve — objective feeds _sort_scored.
    long_tour = _effective_tour_systems(constraints) > MAX_TOUR_SYSTEMS
    objective = _resolve_objective(objective, long_tour=long_tour)
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
    in_scope = [r for r in snapshot
                if r["system_symbol"] in allowed
                and (r["ask"] > 0 or r["bid"] > 0)]
    rows = [r for r in in_scope if r["observed_at_unix"] >= cutoff]
    # This cap is a BACKSTOP behind the caller's own per-activity freshness pass, so every
    # row it drops is one the caller judged rankable. Report the count: an unmetered second
    # filter can void the upstream model wholesale and, unreported, only ever surfaces in
    # the total-wipeout case below.
    stale_dropped = len(in_scope) - len(rows)
    if stale_dropped:
        logger.info("tour-solver: staleness backstop dropped %d of %d in-scope rows "
                    "(cap %d min)", stale_dropped, len(in_scope), age_cap)
    if not rows:
        return _infeasible("no_fresh_market_data", model_version)

    markets = _build_markets(rows)
    # sp-im74: resolve the closure anchor ONCE per solve, from the fresh in-scope
    # MARKET rows (before deposit/stock synthesis adds non-market storage nodes).
    # Open requests (closed falsey) are a strict no-op here — default safety.
    anchor_error = _resolve_anchor(constraints, ship, rows, allowed)
    if anchor_error:
        return _infeasible(anchor_error, model_version)
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
    # sp-wtc47: built ONCE per solve beside travel_fn, for the same reason that one does —
    # every crossing in a single solve must be priced by one model, and the hop index is
    # rebuilt per call otherwise.
    gate_fee_fn = _make_gate_fee_fn(constraints, markets, ship)
    sequencer = _resolve_sequencer(sequencer)
    # sp-acb8 Tune 1: resolve the planned-tranche ladder cap ONCE per solve (env
    # TOUR_SOLVER_MAX_PLANNED_TRANCHES, default 2 == byte-identical) and thread the
    # single value to every stage so this solve is internally consistent.
    max_planned_tranches = _resolve_max_planned_tranches()
    # sp-28lw9: resolve the realized per-visit sink depth cap ONCE per solve (env
    # TOUR_SOLVER_REALIZED_SINK_TRANCHES, default 2.5; =1 restores the sp-2v69u calibration)
    # and thread the single value to score_sequence, so one solve is internally consistent.
    realized_sink_tranches = _resolve_realized_sink_tranches()
    # sp-7q5t/sp-fguo widening unlock: resolve the stage-2 full-scoring cut ONCE per
    # solve (env TOUR_SOLVER_FULL_SCORE_TOP_N, default 20 == byte-identical) so the
    # widened beam candidates can actually survive to full scoring.
    full_score_top_n = _resolve_full_score_top_n()
    beam_cands = beam_sequences(markets, ship, constraints, travel_fn, deposit_sinks,
                                stock_source_idx,
                                max_planned_tranches=max_planned_tranches)
    if sequencer == SEQUENCER_ORTOOLS:
        # F9: pass the BUILT indices positionally, byte-mirroring the beam call.
        try:
            ortools_cands = ortools_sequences(markets, ship, constraints, travel_fn,
                                              deposit_sinks, stock_source_idx,
                                              max_planned_tranches=max_planned_tranches)
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
        pool = list(ortools_cands[:full_score_top_n])
        seen_seqs = set(pool)
        pool += [s for s in beam_cands[:full_score_top_n] if s not in seen_seqs]
    else:
        pool = beam_cands[:full_score_top_n]

    # HOME-SCOPED UNION (sp-97ine) — mirrors the ortools-unions-beam pattern above.
    #
    # Stage 1 is a TRUNCATED search (BEAM_WIDTH per level, then the full_score_top_n
    # cut), so a WIDE candidate set can crowd out the in-system tours a home-only
    # solve finds — even though the wide solve's solution space strictly CONTAINS
    # the home-only one. Economy-analyst A/B, 2026-07-31, 51 joint cases (hull 220,
    # live cap-2 / candidate-hop-depth 3 vs allowed_systems={home}): the intra-only
    # solve BEAT live on 14 of 51, concentrated in the richest systems where the
    # in-system tours are monsters. Losing to a SUBSET of your own search space is a
    # dilution defect, not a strategy result — the third confirmed instance of this
    # mechanism (cap-4 -26%, top_n-100 -21/-29%, now this).
    #
    # Fix: re-run stage 1 on the home-system market subset and UNION its candidates
    # into the pool. Same can-only-ADD contract as the ortools union above — stage 2
    # stays the sole arbiter — which restores the strict-superset property
    # (live >= intra on every case) WITHOUT banning profitable crossings.
    #
    # Deliberately a re-SEARCH, not a filter of the wide candidates: a home-only
    # sequence can be pruned out of the wide beam mid-search (its prefix loses a
    # BEAM_WIDTH cut to foreign competitors), so filtering the wide output would
    # recover only the dilution at the final cut, not the dilution inside the search.
    #
    # Scoping predicate: keep a node in the ship's home system, PLUS any node
    # carrying no system at all. The latter are only ever the synthetic storage
    # nodes `_build_deposit_sinks`/`_build_stock_sources` setdefault in when a
    # candidate's storage_system is "" — which an allowed_systems={home} solve also
    # keeps (its `if system and system not in allowed_systems` guard tolerates the
    # empty string), so dropping them here would leave a gap in the very superset
    # property this exists to restore. Real market nodes always carry their row's
    # system_symbol, so no foreign market can enter through the falsy branch.
    home_markets = {wp: m for wp, m in markets.items()
                    if not m["system"] or m["system"] == ship["current_system"]}
    # Skipped when the home subset IS the whole market set — the ordinary
    # single-system solve, where the re-run would reproduce the wide candidates
    # exactly and add nothing. Costs nothing on the common path; the extra stage-1
    # is paid only by genuinely multi-system solves.
    if home_markets and len(home_markets) < len(markets):
        home_deposit_sinks = {k: v for k, v in deposit_sinks.items()
                              if k[0] in home_markets}
        home_stock_sources = {k: v for k, v in stock_source_idx.items()
                              if k[0] in home_markets}
        # travel_fn is REUSED, not rebuilt: it resolves a waypoint's system out of
        # the full `markets`, and home_markets is a strict subset, so every home hop
        # prices identically here, in the wide stage 1, and in stage 2 — one travel
        # model per solve, which is the discipline the rest of solve_tour follows.
        home_beam = beam_sequences(home_markets, ship, constraints, travel_fn,
                                   home_deposit_sinks, home_stock_sources,
                                   max_planned_tranches=max_planned_tranches)
        if sequencer == SEQUENCER_ORTOOLS:
            try:
                home_ortools = ortools_sequences(home_markets, ship, constraints,
                                                 travel_fn, home_deposit_sinks,
                                                 home_stock_sources,
                                                 max_planned_tranches=max_planned_tranches)
            except Exception:
                # Same never-die contract as the wide call. DISTINCT once-log key so
                # a home failure can never suppress the wide traceback, or vice versa.
                if "ortools_error_home" not in _logged_sequencer:
                    _logged_sequencer.add("ortools_error_home")
                    logger.exception("tour-solver: ortools home-scoped stage-1 "
                                     "failed — home beam only")
                home_ortools = []
            home_pool = list(home_ortools[:full_score_top_n])
            home_seen = set(home_pool)
            home_pool += [s for s in home_beam[:full_score_top_n]
                          if s not in home_seen]
        else:
            home_pool = home_beam[:full_score_top_n]
        # APPENDED last, and de-duplicated against the wide pool: a home candidate
        # has to win stage 2 outright to change the answer, and when the union adds
        # nothing the pool — and therefore the whole response — is unchanged.
        already = set(pool)
        added = [s for s in home_pool if s not in already]
        pool += added
        if added:
            # The mechanism has to be measurable or a union that never contributes
            # looks exactly like one that is working.
            logger.info("tour-solver: home-scoped stage-1 added %d candidate(s) "
                        "(%d home of %d markets)",
                        len(added), len(home_markets), len(markets))
    if not pool:
        # Union-empty ⇒ beam-empty ⇒ today's reason string, byte-identical.
        return _infeasible("no_candidate_tours", model_version)

    scored = []
    seen = set()
    for seq in pool:
        result = score_sequence(seq, markets, ship, constraints, model, travel_fn,
                                deposit_sinks, absorption_index, stock_source_idx,
                                max_planned_tranches=max_planned_tranches,
                                realized_sink_tranches=realized_sink_tranches,
                                gate_fee_fn=gate_fee_fn)
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
                # sp-wtc47: reported SEPARATELY as well as netted out of projected_profit.
                # projected_profit is already fee-inclusive, so this is not a second charge —
                # it is the term that makes the bead's realized check possible at all:
                # compare this against the tour's actual JUMP/TRAVEL_COSTS rows and the
                # per-hop constant can be re-fitted from the gap instead of re-derived.
                gate_fees=best["gate_fees"],
                top_rejected=rejected(scored[1:], winner=best),
                model_version=model_version)

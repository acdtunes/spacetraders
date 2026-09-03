package commands

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// RunTourCoordinatorCommand is a captain-directed, guarded multi-hop trade-tour run:
// plan a depth-aware tour for THIS hull, fly it leg by leg with prices re-verified
// live at every dock, re-plan at most ReplanLimit times when reality drifts past
// tolerance. The route is dynamically planned, so honest completion is a response
// VETO (not a Go error) — a re-run cannot resume a planner-chosen route.
//
// Iterations makes it a CONTINUOUS engine: on manifest completion it re-plans from
// the hull's CURRENT position + live market and flies the next tour with no captain
// in the loop, turning capital velocity from captain-cadence into engine-cadence. See
// Iterations for the loop semantics.
type RunTourCoordinatorCommand struct {
	ShipSymbol string
	PlayerID   int
	MaxHops    int // 0 → maxTourHops
	// MaxTourSystems caps the DISTINCT systems one tour may touch (start + gate
	// neighbors). 0 → the solver's MAX_TOUR_SYSTEMS default (2), byte-identical to
	// today; a positive value sweeps tour length without a redeploy.
	MaxTourSystems int
	// ClosedTours + AnchorSystem opt this run into closed-tour mode: every planned
	// tour ENDS at the anchor via an appended, honestly-priced no-trade return leg.
	// AnchorSystem "" floats the anchor to the ship's waypoint at plan time; an
	// explicit system symbol pins the return to that system's first fresh market.
	// Zero-values = open tours, byte-identical to today. Deliberately no CLI flag
	// yet — arming is governance-owned.
	ClosedTours       bool
	AnchorSystem      string
	MaxSpend          int64 // 0 → the capital budget (re-resolved per tour when Iterations != 0/1)
	MinMargin         int
	LookbackMinMargin int // the look-back manifest's own floor; 0 → lookbackMinMarginDefault
	// LookbackSourceCallCredits is what a FURTHER source waypoint must add to a look-back
	// manifest to earn the navigate/dock/orbit bundle it costs, in credits of that manifest's
	// own gross margin and scaled by how hard the request budget is binding. 0 →
	// lookbackSourceCallCreditsDefault (the armed price); negative is the operator's disarm,
	// sourcing every waypoint that adds value.
	LookbackSourceCallCredits int
	// LookbackItemCallCredits: what ONE MORE GOOD must earn for its buy+sell request pair.
	// 0 → lookbackItemCallCreditsDefault; negative is the operator's disarm.
	LookbackItemCallCredits int
	ReplanLimit             int // 0 → tourMaxReplansDefault (PER TOUR)
	// Iterations is the tour count, unifying the container iteration semantics
	// (registry invariant 3): -1 = CONTINUOUS (tour, re-plan from the new position,
	// tour again — until margins die/starvation/stop), N>0 = exactly N tours, 0 =
	// the one-tour default (byte-for-byte unchanged from the original one-shot
	// behavior). The coordinator owns this loop internally
	// (CoordinatorOwnsIterations); the container runs Handle() once.
	Iterations            int
	AgentSymbol           string
	ContainerID           string // the tour id; groups this run's telemetry legs
	WorkingCapitalReserve int64  // 0 → defaultWorkingCapitalReserve (flat, sp-05glh — no proportional shrink)
	// ModelArtifactPath overrides defaultModelArtifactPath (tests point it at a temp
	// artifact); empty → the default repo-relative path.
	ModelArtifactPath string

	// --- Reposition-on-margins-death ---
	// When a CONTINUOUS (--iterations -1) tour's margins die (tourStarvationLimit
	// consecutive no-plans after >=1 productive tour), the coordinator RANKS
	// jump-reachable systems by expected tour margin and JUMPS to the best one before
	// exiting — a hull stranded on its own freshly-sold-out ground rotates to a fresh
	// renewable one instead of dying on it and burning a captain relaunch. Bounded to
	// ONE reposition per margins-death episode (no infinite hop-scotching).

	// RepositionDisabled is the kill-switch. false (the zero value / absent config,
	// and the default) → reposition is ON for continuous runs; true disables it and
	// a margins-died tour exits without rotating.
	RepositionDisabled bool
	// RepositionMinMargin is the fresh-profit floor (RULINGS #5) a candidate's planned
	// tour must clear to justify the jump: a jump costs antimatter + fuel + a one-way
	// hop the hull spends not trading, so a marginal destination isn't worth relocating
	// for. 0 → repositionMinMarginDefault.
	RepositionMinMargin int
	// RepositionMaxCandidates bounds the solver fan-out: at most K pre-ranked candidate
	// systems get a real planner call per margins-death episode. 0 →
	// repositionMaxCandidatesDefault.
	RepositionMaxCandidates int
	// RepositionJumpBound is the jump bound the reposition flight resolves its cross-system
	// leg over the PERSISTED stored adjacency (RepositionPath) with, routing PAST an
	// unreadable frontier gate rather than fail-closing on it via the strict Path — a tour
	// reposition is a MOVEMENT of the hull, not a money commitment, so it shares the scout
	// reposition's relaxation. 0/absent → repositionJumpBoundDefault (12, the scout
	// frontier depth); a positive value is the captain's [trade_fleet].reposition_jump_bound
	// override. Always resolves > 0, so the reposition never degrades to the strict resolver,
	// which cannot route a heavy off an unreadable-gate origin. The buy-side (arb pre-buy,
	// trade-route lane commits, cargo delivery) keeps strict Path — money-commitment vs
	// hull-movement is the guard line.
	RepositionJumpBound int
	// RepositionInProgress / RepositionTargetSystem / RepositionTargetWaypoint are the
	// restart-resume state (RULINGS #2): persisted into the container config the instant
	// a reposition jump is committed and cleared once it lands, so a daemon restart
	// mid-jump resumes toward the SAME ground through the shared cooldown-riding travel
	// machinery rather than re-planning at whatever intermediate hop it was re-adopted
	// on. Set by the recovery rebuild from the persisted config; a fresh launch leaves
	// them zero.
	RepositionInProgress     bool
	RepositionTargetSystem   string
	RepositionTargetWaypoint string

	// --- Reposition reach (always-broaden discovery + deadhead-decay + anti-herd) ---
	// A hull whose origin has ANY fresh-market 1-hop neighbour (even a money-losing one) never
	// sees richer systems 2-4 gate hops away, because buildRepositionCandidates broadens to the
	// multi-hop scan ONLY when the 1-hop set is EMPTY (the off-circuit gate).

	// RepositionReachEnabled arms the reach improvement. false (the zero value / absent config) →
	// the legacy 1-hop-first + broaden-on-empty path runs byte-for-byte unchanged (the governance
	// gate). true → buildRepositionCandidates ALWAYS runs BOTH the 1-hop and the multi-hop scan,
	// merges+dedups them (1-hop precedence on ties), RE-RANKS by a hop-decayed score so a rich
	// distant ground can outrank a mediocre near one without a marginally-better distant one
	// overflying, and EXCLUDES candidate systems already saturated with active trade hulls.
	RepositionReachEnabled bool
	// RepositionReachHopDecayPct is the per-hop ranking decay (an int percent): the pre-rank score
	// is adjusted to score·(pct/100)^hops so distant candidates pay for the extra deadhead travel.
	// 0/absent → repositionReachHopDecayPctDefault (85 ⇒ 0.85/hop). Only read when
	// RepositionReachEnabled is true.
	RepositionReachHopDecayPct int
	// RepositionReachMaxHullsPerSystem is the anti-herd cap: a candidate system already served by
	// >= this many active trade hulls is excluded, so simultaneously-margin-dead hulls do not all
	// pile onto the same top system and re-drain it. 0/absent →
	// repositionReachMaxHullsPerSystemDefault (5). Only read when RepositionReachEnabled is true.
	RepositionReachMaxHullsPerSystem int

	// --- Own-trade recency de-ranking (ARMED by default) ---
	// The anti-herd cap above counts hulls RESIDENT in a system at one instant, so a queue of
	// hulls each staying minutes and leaving never trips it while the system is drained
	// continuously. These charge the pre-rank for ground the fleet ITSELF worked recently.

	// OwnTradePenaltyPct is the largest share of its pre-rank score a candidate can lose for
	// having been traded moments ago, decaying to nothing as the ground rests. 0/absent →
	// ownTradePenaltyPctDefault; >100 is clamped so the multiplier can never go negative and
	// invert the ranking. It re-orders candidates only: nothing is excluded and the money floor
	// is untouched.
	OwnTradePenaltyPct int
	// OwnTradeColdMinutes is the age at which ground counts as rested and the penalty reaches
	// zero. 0/absent → ownTradeColdMinutesDefault, clamped to ownTradeColdMinutesMax because
	// this horizon is also the ledger scan's lookback.
	OwnTradeColdMinutes int
	// OwnTradePenaltyDisabled is the kill switch. false (the zero value / absent config) leaves
	// the de-ranking ARMED; true restores the crowding-blind pre-rank.
	OwnTradePenaltyDisabled bool

	// --- Rate-floor early-reposition (always-relocate chronic under-earners) ---
	// The margins-death reposition only fires when a continuous tour's margins DIE. A hull earning
	// mediocre-but-profitable local arb (say 80k/hr while frontier pays 360-480k/hr) never
	// margin-dies, so it never relocates. This trigger, evaluated AFTER a PRODUCTIVE continuous tour,
	// relocates a hull whose realized rate is far below the fleet median to a meaningfully better
	// reachable ground via part-1's reach discovery. DEFAULT-OFF and heavily gated (thrash is the
	// failure mode); the whole trigger lives inside RepositionRateFloorEnabled.

	// RepositionRateFloorEnabled is the master gate. false (the zero value / absent config) → the
	// trigger never runs and the productive-tour path is byte-identical to today; true → after a
	// productive continuous tour the coordinator evaluates the rate-floor relocation (still subject
	// to the shared RepositionDisabled kill-switch, the fail-closed median, the improvement gate,
	// anti-herd, and the dwell window). Governance-owned arming (config + restart).
	RepositionRateFloorEnabled bool
	// RepositionRateFloorPct is the under-earner threshold as a percent of the fleet-median realized
	// tour $/hr: a hull earning < this % of the median is a relocation candidate. 0/absent →
	// repositionRateFloorPctDefault (40). Only read when RepositionRateFloorEnabled is true.
	RepositionRateFloorPct int
	// RepositionRateFloorImprovementPct is how much better the best reach candidate's PROJECTED rate
	// must be than the hull's CURRENT realized rate to justify the jump (the anti-thrash cushion):
	// relocate only if candidate_projected >= this % of current_realized (and strictly better).
	// 0/absent → repositionRateFloorImprovementPctDefault (200, i.e. 2x). Only read when
	// RepositionRateFloorEnabled is true.
	RepositionRateFloorImprovementPct int
	// RepositionRateFloorDwellMinutes is the per-hull cooldown after a rate-floor relocation: a hull
	// that relocated within this window is never a rate-floor candidate again, so it cannot
	// hop-scotch across successive productive tours. 0/absent → repositionRateFloorDwellMinutesDefault
	// (15). Only read when RepositionRateFloorEnabled is true.
	RepositionRateFloorDwellMinutes int

	// --- Placement/relocation scoring loop ---
	// The margins-death rescue evolves into the spec's score(x)=E_x−β·D_x placement loop:
	// argmax over reachable systems (INCLUDING staying put) on the deadhead-charged score,
	// with a φ·β park floor. DEFAULT-OFF and byte-identical to the legacy static-floor
	// reposition when unarmed; the shared RepositionDisabled kill-switch and one-per-episode
	// budget still win (they sit ABOVE the placement dispatch). Governance-owned arming.

	// PlacementDisabled is the placement loop's kill switch. false (the zero value / absent config)
	// → ARMED: candidates are scored on score(x)=E_x·(H−D_x)/H with the current system competing as
	// a stay at D=0. true → the legacy static-floor reposition. An unreadable β also falls back.
	PlacementDisabled bool
	// PlacementBetaWindowMinutes is the trailing window for the fleet rolling-median realized
	// tour $/hr (β). 0 → placementBetaWindowMinutesDefault (60).
	PlacementBetaWindowMinutes int
	// PlacementParkFloorPct is φ×100: a candidate's deadhead-charged score must clear φ·β to be
	// worth the jump, else the hull parks. 0 → placementParkFloorPctDefault (30, spec φ=0.3).
	PlacementParkFloorPct int
	// PlacementShortlistTopN is the same-budget shortlist N: armed mode prices top-(N−1) foreign
	// candidates + 1 current-system E_s = N planner calls per episode, identical to legacy's K.
	// 0 → the resolved RepositionMaxCandidates (default 3), so arming never grows the solver herd.
	PlacementShortlistTopN int
	// PlacementHorizonMinutes is H: a candidate keeps only the (H−D_x)/H its crossing leaves.
	// 0 → placementHorizonMinutesDefault (60). Lower H holds hulls harder.
	PlacementHorizonMinutes int

	// StrandedConsecutiveThreshold is the stranded-hull detector threshold: how many
	// CONSECUTIVE origin-level empty reposition discoveries (no durable adjacency + gate
	// inaccessible) a hull must accrue before the coordinator pages the watch with a
	// WARN + the fleet_hull_stranded_total counter. 0/absent →
	// strandedConsecutiveThresholdDefault (3). Config-driven from [trade_fleet]
	// (RULINGS #5), threaded through the container config so a captain retunes it by
	// editing config.yaml + restarting the daemon.
	StrandedConsecutiveThreshold int

	// CandidateHopDepth is the gate-hop radius for the tour candidate set. 0/absent →
	// candidateHopDepthDefault (1 = today's exact behavior: home + live 1-gate-hop
	// neighbors). Clamped to [1, maxCandidateHopDepth=3] (spec "2-3 gate hops"). EFFECT
	// is arming-gated: a value > 1 is a NO-OP unless the solver clamp is lifted
	// (cmd.MaxTourSystems > 2), so a lone live-config edit can never feed a
	// non-gate-adjacent set to the flat-pricing solver.
	CandidateHopDepth int
	// CandidateShortlistTopN bounds how many ≥2-hop systems the profitable-edge shortlist
	// ADDS on top of the always-present 1-hop floor. 0/absent → candidateShortlistTopNDefault (6).
	CandidateShortlistTopN int

	// MVT trade loop (spec docs/superpowers/specs/2026-09-02-mvt-trade-loop-design.md).
	// MVTLoop is set from the hull's fleet tag (trade-mvt) at launch; the knobs below are
	// resolved by the command builder from trade_fleet.* with the spec defaults.
	MVTLoop          bool
	YieldWindowSells int
	YieldMinSells    int
	ClaimReachHops   int
	// ClaimReachMaxHops caps how far a CLAIM widens its reach when the ranking at
	// ClaimReachHops offers the hull nothing; equal to ClaimReachHops means no escalation.
	ClaimReachMaxHops        int
	SpecialistCadenceMinutes int
	// YieldRateSpanFloorMinutes is how long a visit must have run before the hull's own
	// earning rate outranks the fleet's in the travel-cost term. 0/absent → no floor.
	YieldRateSpanFloorMinutes int

	// TourNeighborsDurableFirst serves the tour graph's 1-hop neighbor scan from the persisted
	// gate adjacency. false/absent → the live jump-gate query, exactly as today.
	TourNeighborsDurableFirst bool

	// ExternalityWeight prices the recovery burden a planned sell tranche imposes on the
	// rest of the fleet, so hulls stop converging on the same sinks.
	// Config-driven from [trade_fleet] (RULINGS #5) and threaded through the container
	// config, so a captain arms and retunes it by editing config.yaml + restarting.
	// 0/absent → no charge → the solver ranks on raw margin exactly as today, which is
	// also the documented revert.
	ExternalityWeight float64
	// --- sp-e8d92 FIRST REFUSAL knobs. Every one is 0/absent -> a documented default (RULINGS #5). ---

	// RelocationOfferWindowSeconds is how long the tour waits for the relocator at its boundary.
	// 0/absent -> defaultRelocationOfferWindowSeconds (150s), which is longer than the relocator's own
	// tick (an offer shorter than that can lapse between two observations and never be seen) and shorter
	// than the measured 224s median inter-tour gap.
	RelocationOfferWindowSeconds int
	// RelocationOfferMinHulls is the herd gate: offer only a hull whose system already holds at least
	// this many trade hulls. 0/absent -> defaultRelocationOfferMinHullsInSystem (2).
	RelocationOfferMinHulls int
	// RelocationOfferBackoffMinutes is how long a hull whose offer LAPSED waits before being offered
	// again. 0/absent -> defaultRelocationOfferBackoffMinutes (30).
	RelocationOfferBackoffMinutes int
	// RelocationOfferUntil is the restart-durable OFFER deadline, reloaded from the container config so a
	// restart mid-offer honours the same deadline instead of opening a fresh one.
	RelocationOfferUntil time.Time
	// RelocationOfferBackoffUntil is the restart-durable backoff instant, reloaded from the container
	// config so a restart does not forget that this hull's last offer went unclaimed and immediately pay
	// another window (RULINGS #2).
	RelocationOfferBackoffUntil time.Time

	// TourLegWaypoint / TourLegGoods are the in-flight SELL leg reloaded from the container
	// config: the sink the hull was flying to and the goods it was carrying there. Written the
	// instant the leg is chosen and cleared once it is finished, so a re-adopted hull discharges
	// cargo it already paid for where it was always going instead of idling through a full
	// re-plan first (RULINGS #2). Both empty on a fresh launch, and on any hull whose leg was
	// already flown — the resume then declines and the run plans exactly as before. See
	// run_tour_coordinator_legresume.go for why the buy side is deliberately absent.
	TourLegWaypoint string
	TourLegGoods    string
}

// MVT trade loop defaults (spec §5). Fitted from the replay before ship; none encodes fleet size.
const (
	DefaultYieldWindowSells          = 8
	DefaultYieldMinSells             = 3
	DefaultClaimReachHops            = 2
	DefaultClaimReachMaxHops         = 4
	DefaultSpecialistFractionPct     = 10  // specialist_fraction 0.10
	DefaultFatLaneMultiplePct        = 200 // fat_lane_multiple 2.0
	DefaultSpecialistCadenceMinutes  = 60  // specialist_cadence 1h
	DefaultYieldRateSpanFloorMinutes = 0
)

// RunTourCoordinatorResponse reports the realised tour economics and — via
// CompletionOutcome — whether the run honestly completed. Three terminal shapes:
// a completed tour (Completed), a fail-open no-op (TourUnavailable, a clean
// completion — planner down/infeasible or model artifact unreadable, single-lane
// fallback stands), and a stranded-cargo veto (CargoStranded → the runner
// terminalizes FAILED via the honest-completion contract).
type RunTourCoordinatorResponse struct {
	ShipSymbol   string
	TourID       string
	LegsPlanned  int
	LegsExecuted int
	Replans      int
	TotalSpent   int64
	TotalRevenue int64
	NetProfit    int64
	ModelVersion string
	Completed    bool

	// ToursCompleted counts how many tours flew >=1 trade this run. 1 for
	// the one-shot default; >1 for a continuous (--iterations) run. TradesExecuted is
	// the run's total executed buy+sell tranches (the per-tour progress signal the
	// starvation guard reads). ExitReason (a tourExit* constant) explains why a
	// continuous loop stopped; empty on the one-shot path.
	ToursCompleted int
	TradesExecuted int
	ExitReason     string

	// Repositions counts how many times this run rotated the hull to a fresh ground on
	// margins-death. ExitDetail is the human-readable exit explanation the
	// ExitReason constant abbreviates — on a reposition-then-death it NAMES BOTH the
	// origin and the destination system ("repositioned X -> Y ... margins died there
	// too"), so a captain reading a completed continuous tour sees the full rotation
	// story, not just the machine-readable "starvation".
	Repositions int
	ExitDetail  string

	// RetirementStandDown records that the operator's retirement mark ended this run. It is the
	// falsifier for the stand-down — a run that stopped for any other reason leaves it false —
	// and it is how a plan that stopped itself between legs tells the loop not to choose another
	// ground for a hull leaving service. ExitReason separates the two shapes: tourExitRetired
	// (drained, ready to scrap) from tourExitRetiredHolding (the ladder ran out of rungs).
	RetirementStandDown bool

	// RetirementDischarging records that a plan in flight was put into SELL-ONLY discharge by a
	// mark set mid-tour: its queued buys were dropped and the hull is still laden. It hands the
	// run straight back to the disposal ladder at the boundary instead of the rank-and-reposition
	// machinery, and is cleared there — it describes one plan, not the run.
	RetirementDischarging bool

	// RetirementDisposalSales counts the sell-only disposal sales the retirement ladder made.
	// It is the falsifier for the drain: a marked hull that emptied for any other reason (its
	// plan's own sells, the exit sweep) leaves this at zero, so a regression that stops
	// disposing shows up as a flat counter rather than a slower drain nobody notices.
	RetirementDisposalSales int

	// RetirementResidualUnits reports the sellable units still aboard when a marked hull stood
	// down UNDRAINED — cargo no reachable market bids for. Non-zero means a hull that cannot be
	// scrapped until the load is cleared by hand.
	RetirementResidualUnits int

	// DistressLiquidations counts how many stuck-laden episodes this run resolved by the
	// sp-2v69u TERTIARY last resort: a laden hull with no profitable fresh tour AND no
	// reachable sink (the fresh-arb reposition and the held-cargo offload both declined)
	// sold its held cargo at the best AVAILABLE local bid — below the profit floor,
	// sunk-cost cash recovery — so it re-enters planning EMPTY instead of churning
	// relaunch-after-relaunch full. Zero on every run that never strands a hull laden.
	// It is the >50%-laden, one-per-episode rung ABOVE the sp-b9alf pre-release drain, which
	// covers the same local dump for any hold. Declared overlap (RULINGS #21, 2026-08-29): the
	// two never fire on one episode, and sp-gzh7q folds this rung into the ladder.
	DistressLiquidations int

	// StrandDisposalSales counts the sales the sp-b9alf pre-release drain made on a hull the
	// margins-death rescues could not clear. It is the falsifier for that ladder: a hold emptied
	// any other way leaves it at zero, so a regression shows up as a flat counter beside a rising
	// stranded-cargo veto rate rather than as a silent return of the old behaviour.
	StrandDisposalSales int

	// ExitHoldLiquidations counts the goods the exit sweep cleared out of the hold on
	// the way to release: cargo that had a live local bid and would otherwise have been marooned
	// on an idle hull the instant releaseShipAssignments ran. It is the falsifier for that sweep
	// — a run whose hold emptied for any OTHER reason leaves this at zero — so a regression that
	// stops consulting the invariant shows up as a flat counter, not just a changed row.
	ExitHoldLiquidations int

	// ResumedLegs counts the in-flight sell legs this run finished on re-adoption instead of
	// abandoning to a fresh plan. It is the falsifier for the restart resume — a run that was
	// launched rather than recovered, or one whose persisted sink no longer bid, leaves it at
	// zero — so a regression that stops resuming shows up as a flat counter across a bounce.
	ResumedLegs int

	// CapitalDeniedBuys counts buys a MONEY GUARD refused: the working-capital floor, or a
	// fail-closed unreadable balance. A tour that flew zero trades while this rose was
	// DENIED CAPITAL — the planner found the margin, the treasury could not fund it — which
	// is the opposite of margin death and must never feed the margins-death breaker.
	CapitalDeniedBuys int

	// TourUnavailable marks a fail-open exit: no trading happened, the single-lane
	// fallback remains. A CLEAN completion (not a failure), never a phantom trade.
	TourUnavailable       bool
	TourUnavailableReason string

	// CargoStranded is the honest-completion veto (invariant: a tour ending with
	// unsold bought cargo is never a clean completion). Threaded through
	// CompletionOutcome (nil Go error), NOT arb's non-nil-error shape — a
	// dynamically-planned tour cannot be resumed by a re-run, which would trade
	// AROUND the strand.
	CargoStranded       bool
	CargoStrandedReason string

	// PlannerInternalError is the honest-completion veto for a planner OUTAGE: the
	// routing-service caught an exception and returned a STRUCTURED feasible=false
	// with an "internal_error:" reason (not a gRPC transport error). That is a real
	// planner failure, NOT a legitimate "no profitable tour" — routing it to the clean
	// TourUnavailable fail-open would mask a live outage as container success=true.
	// Surfaced through CompletionOutcome (nil Go error, like CargoStranded) so the
	// container terminalizes FAILED and the outage is loud.
	PlannerInternalError       bool
	PlannerInternalErrorReason string

	Error string
}

// CompletionOutcome implements common.CompletionReporter: a stranded tour vetoes
// the runner's success=true (terminalized FAILED with the strand as its signature).
// A planner internal_error vetoes the same way — a real routing-service outage is a
// FAILURE, never masked as a clean completion. A fail-open "tour unavailable" is an
// honest clean completion (nothing half-done).
func (r *RunTourCoordinatorResponse) CompletionOutcome() (bool, string) {
	if r.CargoStranded {
		return false, r.CargoStrandedReason
	}
	if r.PlannerInternalError {
		return false, r.PlannerInternalErrorReason
	}
	return true, ""
}

// Compile-time pin: the tour response participates in the honest-completion contract.
var _ common.CompletionReporter = (*RunTourCoordinatorResponse)(nil)

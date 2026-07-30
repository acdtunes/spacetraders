package commands

// run_opportunity_relocator.go — the standing OPPORTUNITY RELOCATOR (sp-zvywu Part 2).
//
// WHAT IT IS. The same actuator family as the rate-floor relocation
// (run_tour_coordinator_rate_floor.go) with the TRIGGER INVERTED. That one RESCUES an under-earner:
// it fires on a hull failing a fraction of the fleet median, and its whole job is to stop a hull
// rotting. Nothing in the fleet chases distant UPSIDE — a hull earning perfectly respectable money
// on a mediocre ground never trips any rescue, so it stays there forever while better ground goes
// unworked.
//
// WHY IT PAYS. The tradable envelope moves WITH the hull: a cap-2 tour is planned from wherever the
// hull is standing, so relocating a hull is what makes a distant region tradable at all. Measured
// 2026-07-30, only 116 of 1,183 charted systems carry any market data — roughly 90% of charted space
// is unpriced, so the reachable-but-unworked ground is not a rounding error, it is most of the map.
//
// SHAPE: a standing reconciler on a ~15 minute tick. It is a reconciler and not a per-tour hook
// because both of its decisions are FLEET-WIDE: it relocates the TOP-NPV hull, which requires
// ranking every (hull, region) pair against every other, and it honours a fleet-wide
// max_concurrent_relocations. A per-hull hook cannot see either.
//
// WHAT IT NEVER DOES:
//
//   - IT NEVER SPENDS. Hulls move through the existing occupancy/reposition primitives
//     (RelocatorActuator, backed by the same ClaimShip-guarded travel machinery the rate-floor
//     relocation uses). No money guard is read, touched, or relaxed (RULINGS #4).
//   - IT NEVER TOUCHES A PROTECTED HULL. RULINGS #7 — "the ownership model is law. Pinned/dedicated
//     hulls are never poached (l7h2 P1-P2.5, atomic ClaimShip); the command frigate hauls only as
//     last resort (sp-4a4e)." The command frigate and any pinned hull are dropped at OBSERVATION, so
//     no scoring path can reach them.
//   - IT NEVER MOVES A HULL MID-TOUR. Only a hull at honest tour release is a candidate. A hull
//     currently touring is skipped this tick and reconsidered on a later one.
//   - IT NEVER DECIDES A RELOCATION TWICE. See the restart contract on reconcileInFlight.
//
// ARMED ON DEPLOY. There is no enable flag and no arming seam: the knobs below are operational
// thresholds and caps only. The one stop is the shared RepositionDisabled kill-switch, so a single
// operator stand-down halts ALL auto-relocation including this.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	// defaultRelocatorTickSeconds is the reconcile cadence: ~15 minutes. Slower than a tour, so a
	// hull is normally evaluated at a genuine release rather than mid-flight; fast enough that a
	// region newly priced by the sensing surge is scored within a tour of appearing.
	defaultRelocatorTickSeconds = 900

	// defaultRelocatorNPVThresholdCredits is the credit floor a relocation's NPV must clear. A heavy
	// freighter baselines ~300k/hr, so a move onto genuinely better ground over a 24h horizon prices
	// in the millions; 500k is therefore a NOISE screen rather than an economic bar — it discards
	// moves whose entire value is inside the projection's own error, without standing in the way of a
	// real one.
	defaultRelocatorNPVThresholdCredits = 500_000

	// defaultRelocatorUpliftBarPct is the multiplicative bar a candidate region's projected rate must
	// clear against the hull's realized rate: 150 (1.5x), the bead's calibrated starting point.
	defaultRelocatorUpliftBarPct = 150

	// relocatorUpliftBarPctMin is the HARD FLOOR the uplift bar clamps to. It equals the default, so
	// the bar can only ever be raised. Same reasoning as repositionRateFloorImprovementPctMin: the
	// multiplicative bar is the real per-relocation anti-thrash limiter (the cooldown spans about two
	// tours, so on a quiet fleet the bar is what stops churn), and a safety-critical ratchet must not
	// be silently tunable downward. An operator who wants less relocation raises it.
	relocatorUpliftBarPctMin = 150

	// defaultRelocatorMaxConcurrentRelocations caps how many hulls may be relocating at once: 2, the
	// bead's figure. It bounds how much of the fleet is in transit — and therefore earning nothing —
	// on any single reading of the map.
	defaultRelocatorMaxConcurrentRelocations = 2

	// defaultRelocatorCooldownMinutes is the per-hull cooldown after a relocation: 90 minutes,
	// roughly two tours. Deliberately LONGER than the rate-floor rescue's 45-minute dwell: a rescue
	// is trying to get a rotting hull moving and wants to retry soon, whereas chasing upside must
	// give the new ground time to actually produce the tours the EWMA will judge it on. Relocating
	// again before then would score the new ground on the old ground's rate.
	defaultRelocatorCooldownMinutes = 90

	// defaultRelocatorHorizonHours caps the productive window a relocation may be valued over: 24h.
	// Past a day, a projection off today's snapshot is not evidence about anything.
	defaultRelocatorHorizonHours = 24

	// defaultRelocatorRiskMarginTourMinutes sets the risk margin at one tour of the hull's OWN
	// earnings (risk_margin = current_rate x this), the bead's calibration. 60 minutes is the top of
	// the measured 30-60 minute productive-tour range — the conservative end, since a larger margin
	// refuses more moves.
	defaultRelocatorRiskMarginTourMinutes = 60

	// defaultRelocatorRegionHopRadius is how far a region's neighbourhood reaches: 2 gate hops, the
	// bead's "system + 2-hop neighbours".
	defaultRelocatorRegionHopRadius = 2

	// defaultRelocatorRateWindowMinutes is the telemetry window the per-hull EWMA reads: 4 hours.
	// Productive tours run 30-60 minutes, so this holds roughly 4-8 completed tours per hull — enough
	// for the EWMA to be an average rather than an echo of the newest tour, and short enough that it
	// describes the ground the hull is on NOW.
	defaultRelocatorRateWindowMinutes = 240
)

// RunOpportunityRelocatorCommand launches the standing relocator. Every field past the identity is
// an OPERATIONAL threshold or cap (RULINGS #5); there is deliberately no enable flag.
type RunOpportunityRelocatorCommand struct {
	PlayerID    int
	ContainerID string

	// TickSeconds is the reconcile cadence. 0/absent -> defaultRelocatorTickSeconds.
	TickSeconds int
	// NPVThresholdCredits is the credit floor a relocation's NPV must strictly exceed.
	// 0/absent -> defaultRelocatorNPVThresholdCredits.
	NPVThresholdCredits int64
	// UpliftBarPct is the multiplicative bar as a percent (150 = 1.5x), clamped up to
	// relocatorUpliftBarPctMin. 0/absent -> defaultRelocatorUpliftBarPct.
	UpliftBarPct int
	// MaxConcurrentRelocations caps hulls in transit. 0/absent ->
	// defaultRelocatorMaxConcurrentRelocations.
	MaxConcurrentRelocations int
	// CooldownMinutes is the per-hull cooldown. 0/absent -> defaultRelocatorCooldownMinutes.
	CooldownMinutes int
	// HorizonHours caps the productive window. 0/absent -> defaultRelocatorHorizonHours.
	HorizonHours int
	// RiskMarginTourMinutes expresses the risk margin as that many minutes of the hull's own
	// earnings. 0/absent -> defaultRelocatorRiskMarginTourMinutes.
	RiskMarginTourMinutes int
	// RegionHopRadius is the region neighbourhood radius in gate hops. 0/absent ->
	// defaultRelocatorRegionHopRadius.
	RegionHopRadius int
	// RateWindowMinutes is the telemetry window for the per-hull EWMA. 0/absent ->
	// defaultRelocatorRateWindowMinutes.
	RateWindowMinutes int
	// RepositionReachMaxHullsPerSystem is the anti-herd cap, THE SAME KNOB the reposition-reach
	// discovery uses (reposition_reach_max_hulls_per_system) resolved through the SAME
	// resolveRepositionReachMaxHulls, so the fleet-spread policy has one definition across both
	// relocation triggers. 0/absent -> repositionReachMaxHullsPerSystemDefault (5).
	RepositionReachMaxHullsPerSystem int
	// JumpBound bounds the stored-adjacency jump resolver, as for the reposition paths.
	// 0/absent -> resolveRepositionJumpBound's default.
	JumpBound int
	// RepositionDisabled is the SHARED operator kill-switch: one stand-down halts ALL auto-relocation
	// including this reconciler. Not an arming seam — the armed state is false.
	RepositionDisabled bool
}

// RelocatorHull is one live trade hull as the relocator observes it.
type RelocatorHull struct {
	ShipSymbol    string
	CurrentSystem string
	// IsCommandFrigate marks the command frigate. RULINGS #7 protects it; it is dropped at
	// observation and can never be scored.
	IsCommandFrigate bool
	// Pinned marks a hull under a fleet pin or exclusive dedication. RULINGS #7 — never poached.
	Pinned bool
	// OnTour is true while the hull is executing a tour. Only a hull at honest release is a
	// candidate; a touring hull is skipped and reconsidered on a later tick.
	OnTour bool
}

// RelocatorRegion is one candidate region: an anchor system plus the neighbourhood a tour planned
// from its landing waypoint can reach, carrying the rate the planner projects there.
type RelocatorRegion struct {
	AnchorSystem string
	// LandingWaypoint is where the hull lands and the planner prices the candidate tour from.
	LandingWaypoint string
	// GateHops is the gate-hop distance from the hull's current system, priced through the
	// TravelHopModel.
	GateHops int
	// ProjectedRate is the planner-projected credits/hour for this hull on the region's snapshot.
	ProjectedRate float64
	// RateReadable is false when the region carries no usable projection. Such a region is EXCLUDED,
	// never scored at an assumed rate (fail closed).
	RateReadable bool
	// SnapshotAge is how old the region's market snapshot is, measured against the per-activity
	// RankerAgeCaps cap for Activity.
	SnapshotAge time.Duration
	// Activity is the region's market activity level, selecting its freshness cap.
	Activity string
}

// RelocationIntent is the durable record of one relocation decision. ONE record per hull, rewritten
// in place, and it serves two restart duties at once (RULINGS #2): while Completed is false it is an
// in-flight move a restart must finish rather than re-decide, and once Completed it is the per-hull
// COOLDOWN clock, which therefore survives a restart instead of resetting to "never relocated".
type RelocationIntent struct {
	ShipSymbol     string
	FromSystem     string
	TargetSystem   string
	TargetWaypoint string
	// DecidedAt is when the relocation was decided — the cooldown clock's origin.
	DecidedAt time.Time
	// Completed marks the move as landed.
	Completed bool
}

// RelocatorFleetObserver lists the live trade hulls with the position and protection facts the
// relocator filters on.
type RelocatorFleetObserver interface {
	ObserveTradeHulls(ctx context.Context, playerID int) ([]RelocatorHull, error)
}

// RelocatorRegionObserver produces the candidate regions reachable from originSystem within
// hopRadius gate hops, each with a FRESH snapshot and the rate a planner projects on it.
type RelocatorRegionObserver interface {
	ObserveRegions(ctx context.Context, playerID int, originSystem string, hopRadius int) ([]RelocatorRegion, error)
}

// RelocatorTelemetryObserver reads the per-leg tour telemetry the per-hull EWMA rate is computed
// from — realized TRANSACTIONS, which is what makes the rate per-hull rather than per-lane.
type RelocatorTelemetryObserver interface {
	ObserveTourTelemetry(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error)
}

// RelocatorEraHorizon reports how much era is left, and whether that is a real reading. An
// unreadable horizon bounds the valuation to the horizon knob rather than silencing the reconciler
// (see trading.ValueRelocation).
type RelocatorEraHorizon interface {
	RemainingEraHours(ctx context.Context, playerID int) (float64, bool)
}

// RelocatorActuator moves a hull through the existing occupancy/reposition primitives. NO SPEND: it
// is travel and a claim, never a purchase.
type RelocatorActuator interface {
	RelocateHull(ctx context.Context, playerID int, shipSymbol, targetWaypoint string, jumpBound int) error
}

// RelocationIntentStore persists relocation intents so a restart finishes rather than re-decides,
// and so the per-hull cooldown survives a restart (RULINGS #2).
type RelocationIntentStore interface {
	// LoadRelocationIntents returns every persisted intent for the container, completed and not.
	LoadRelocationIntents(ctx context.Context, containerID string, playerID int) ([]RelocationIntent, error)
	// RecordRelocationIntent writes (or rewrites) the single record for intent.ShipSymbol.
	RecordRelocationIntent(ctx context.Context, containerID string, playerID int, intent RelocationIntent) error
}

// RunOpportunityRelocatorHandler is the reconciler. Its collaborators are all ports; the scoring is
// pure domain (trading.ValueRelocation, trading.EwmaHullTourRate, trading.TravelHopModel).
type RunOpportunityRelocatorHandler struct {
	fleet     RelocatorFleetObserver
	regions   RelocatorRegionObserver
	telemetry RelocatorTelemetryObserver
	era       RelocatorEraHorizon
	actuator  RelocatorActuator
	intents   RelocationIntentStore
	travel    trading.TravelHopModel
	ageCaps   trading.RankerAgeCaps
	clock     shared.Clock
}

// NewRunOpportunityRelocatorHandler builds the reconciler. travel may be nil, in which case the
// ARMED fitted affine model is used; passing a different TravelHopModel is how a refit is adopted
// without touching any scoring code.
func NewRunOpportunityRelocatorHandler(
	fleet RelocatorFleetObserver,
	regions RelocatorRegionObserver,
	telemetry RelocatorTelemetryObserver,
	era RelocatorEraHorizon,
	actuator RelocatorActuator,
	intents RelocationIntentStore,
	travel trading.TravelHopModel,
	clock shared.Clock,
) *RunOpportunityRelocatorHandler {
	if travel == nil {
		travel = trading.DefaultAffineHopModel()
	}
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunOpportunityRelocatorHandler{
		fleet:     fleet,
		regions:   regions,
		telemetry: telemetry,
		era:       era,
		actuator:  actuator,
		intents:   intents,
		travel:    travel,
		clock:     clock,
	}
}

// SetRankerAgeCaps wires the activity-conditioned freshness table stale regions are excluded
// against, mirroring the tour coordinator's setter of the same name. The zero value is SAFE:
// RankerAgeCaps.For falls back to the fitted armed caps per activity.
func (h *RunOpportunityRelocatorHandler) SetRankerAgeCaps(caps trading.RankerAgeCaps) {
	h.ageCaps = caps
}

// SetTravelHopModel swaps the travel-time model at runtime — THE REFIT SEAM. A cap-2 re-measurement
// becomes trading.FitAffineHopModel over the new medians followed by this call; no NPV or reconcile
// code changes, and a model of a different shape satisfies the same interface.
func (h *RunOpportunityRelocatorHandler) SetTravelHopModel(model trading.TravelHopModel) {
	if model == nil {
		return
	}
	h.travel = model
}

// RunOpportunityRelocatorResponse is the standing loop's running tally.
type RunOpportunityRelocatorResponse struct {
	Ticks       int
	Relocations int
	Resumptions int
	Errors      []string
}

// Handle runs the reconciler as a standing container: reconcile, wait a tick, repeat, until the
// context is cancelled. It owns its whole loop inside one Handle() like the fleet autosizer and the
// capacity reconciler, so it is NOT re-dispatched per tick.
//
// A failing tick is LOGGED AND COUNTED, never fatal: the reconciler is a standing sensor whose next
// tick re-derives everything from live state anyway (RULINGS #2), so a transient repository blip must
// not take the container down and strand the fleet with no relocation at all.
func (h *RunOpportunityRelocatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunOpportunityRelocatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type for the opportunity relocator")
	}
	logger := common.LoggerFromContext(ctx)
	tick := time.Duration(resolveRelocatorTickSeconds(cmd.TickSeconds)) * time.Second
	logger.Log("INFO", fmt.Sprintf("Opportunity relocator starting (tick %s, NPV threshold %d cr, uplift bar %d%%, max %d concurrent)", tick, resolveRelocatorNPVThreshold(cmd.NPVThresholdCredits), resolveRelocatorUpliftBarPct(cmd.UpliftBarPct), resolveRelocatorMaxConcurrent(cmd.MaxConcurrentRelocations)), map[string]interface{}{
		"action": "opportunity_relocator_start", "container_id": cmd.ContainerID,
	})

	response := &RunOpportunityRelocatorResponse{Errors: []string{}}
	for {
		if err := ctx.Err(); err != nil {
			return response, err
		}
		result, err := h.Reconcile(ctx, cmd)
		if err != nil {
			response.Errors = append(response.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Opportunity relocator tick failed: %v", err), nil)
		}
		if result != nil {
			response.Relocations += len(result.Relocated)
			response.Resumptions += len(result.Resumed)
		}
		response.Ticks++

		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return response, ctx.Err()
		}
	}
}

// RelocatorTickResult is one tick's observable outcome.
type RelocatorTickResult struct {
	// Relocated names the hulls moved this tick, in the order they were committed.
	Relocated []string
	// Resumed names the hulls whose interrupted in-flight move was finished rather than re-decided.
	Resumed []string
	// Evaluated is how many hulls reached the scoring stage.
	Evaluated int
	// Skipped counts hulls dropped before scoring, keyed by reason, for the heartbeat.
	Skipped map[string]int
}

func newRelocatorTickResult() *RelocatorTickResult {
	return &RelocatorTickResult{Skipped: map[string]int{}}
}

func (r *RelocatorTickResult) skip(reason string) { r.Skipped[reason]++ }

// Reconcile runs ONE tick: re-derive from live state, score, act. It is the driving port — every
// test enters here.
//
// RULINGS #2: the tick begins by RE-DERIVING everything from live state. Nothing is carried in
// memory between ticks except what the intent store holds, so a restart mid-tick loses no decision
// and repeats none.
func (h *RunOpportunityRelocatorHandler) Reconcile(ctx context.Context, cmd *RunOpportunityRelocatorCommand) (*RelocatorTickResult, error) {
	result := newRelocatorTickResult()
	if cmd.RepositionDisabled {
		result.skip("reposition_disabled")
		return result, nil
	}

	hulls, err := h.fleet.ObserveTradeHulls(ctx, cmd.PlayerID)
	if err != nil {
		return result, fmt.Errorf("opportunity relocator could not observe the trade fleet: %w", err)
	}
	intents, err := h.intents.LoadRelocationIntents(ctx, cmd.ContainerID, cmd.PlayerID)
	if err != nil {
		return result, fmt.Errorf("opportunity relocator could not load persisted relocation intents: %w", err)
	}

	state := h.reconcileInFlight(ctx, cmd, hulls, intents, result)
	budget := resolveRelocatorMaxConcurrent(cmd.MaxConcurrentRelocations) - state.inFlight
	if budget <= 0 {
		result.skip("at_concurrency_cap")
		return result, nil
	}

	candidates := h.scoreCandidates(ctx, cmd, hulls, state, result)
	h.commitTopCandidates(ctx, cmd, candidates, state, budget, result)
	return result, nil
}

// relocatorState is the live-derived picture one tick decides against: who is where, who is already
// moving, and when each hull last relocated.
type relocatorState struct {
	// hullsBySystem counts landed active trade hulls per system, plus every in-flight relocation
	// TARGET — the anti-herd baseline, mirroring excludeHerdedSystems' landed+pending sum.
	hullsBySystem map[string]int
	// movingOrSettling names hulls not eligible this tick because a move of theirs is in flight or
	// was just resumed.
	movingOrSettling map[string]bool
	// lastRelocation is each hull's cooldown clock, read from the persisted intents so it survives a
	// restart.
	lastRelocation map[string]time.Time
	inFlight       int
}

// reconcileInFlight is THE RESTART CONTRACT, and the reason a restart cannot double-relocate.
//
// A persisted intent is never re-DECIDED — only finished. For each not-yet-completed intent the
// hull's LIVE position decides which:
//
//   - already AT the target system: the move landed (possibly moments before the crash, before the
//     completion could be written). Mark it completed. Its DecidedAt becomes the hull's cooldown
//     clock, so the hull is not immediately re-scored either.
//   - NOT at the target: the move was interrupted in flight. RESUME it toward the SAME target
//     through the SAME actuator, rather than re-scoring the hull against a map that has moved since.
//     Re-deciding here is exactly the double-relocation bug: the hull would be sent somewhere new
//     from an intermediate position it was never evaluated at.
//
// Either way the hull is excluded from this tick's scoring and its target counts against both the
// anti-herd cap and the concurrency budget — so a restart resumes work already authorized instead of
// authorizing more.
func (h *RunOpportunityRelocatorHandler) reconcileInFlight(
	ctx context.Context,
	cmd *RunOpportunityRelocatorCommand,
	hulls []RelocatorHull,
	intents []RelocationIntent,
	result *RelocatorTickResult,
) *relocatorState {
	state := &relocatorState{
		hullsBySystem:    countHullsBySystem(hulls),
		movingOrSettling: map[string]bool{},
		lastRelocation:   map[string]time.Time{},
	}
	positions := hullPositions(hulls)
	for _, intent := range intents {
		if intent.DecidedAt.After(state.lastRelocation[intent.ShipSymbol]) {
			state.lastRelocation[intent.ShipSymbol] = intent.DecidedAt
		}
		if intent.Completed {
			continue
		}
		state.movingOrSettling[intent.ShipSymbol] = true
		state.hullsBySystem[intent.TargetSystem]++
		state.inFlight++
		h.finishInterruptedMove(ctx, cmd, intent, positions, result)
	}
	return state
}

// finishInterruptedMove completes or resumes ONE interrupted intent (see reconcileInFlight).
func (h *RunOpportunityRelocatorHandler) finishInterruptedMove(
	ctx context.Context,
	cmd *RunOpportunityRelocatorCommand,
	intent RelocationIntent,
	positions map[string]string,
	result *RelocatorTickResult,
) {
	logger := common.LoggerFromContext(ctx)
	if positions[intent.ShipSymbol] == intent.TargetSystem {
		h.markCompleted(ctx, cmd, intent)
		logger.Log("INFO", fmt.Sprintf("Opportunity relocator: %s already landed at %s - completing the persisted intent, not re-deciding it", intent.ShipSymbol, intent.TargetSystem), map[string]interface{}{
			"ship_symbol": intent.ShipSymbol, "target_system": intent.TargetSystem, "reason": "intent_landed",
		})
		return
	}
	logger.Log("INFO", fmt.Sprintf("Opportunity relocator: resuming %s's interrupted relocation to %s (%s) rather than re-scoring it from an intermediate position", intent.ShipSymbol, intent.TargetSystem, intent.TargetWaypoint), map[string]interface{}{
		"ship_symbol": intent.ShipSymbol, "target_system": intent.TargetSystem, "target_waypoint": intent.TargetWaypoint, "reason": "intent_resumed",
	})
	if err := h.actuator.RelocateHull(ctx, cmd.PlayerID, intent.ShipSymbol, intent.TargetWaypoint, resolveRepositionJumpBound(cmd.JumpBound)); err != nil {
		// The intent stays UNcompleted so the next tick resumes again — the same resumable contract
		// the rate-floor relocation keeps on a travel error.
		logger.Log("WARN", fmt.Sprintf("Opportunity relocator: resuming %s's relocation to %s failed, intent left in flight for the next tick: %v", intent.ShipSymbol, intent.TargetWaypoint, err), map[string]interface{}{
			"ship_symbol": intent.ShipSymbol, "target_waypoint": intent.TargetWaypoint, "reason": "resume_failed",
		})
		return
	}
	h.markCompleted(ctx, cmd, intent)
	result.Resumed = append(result.Resumed, intent.ShipSymbol)
}

// markCompleted rewrites the hull's record as landed, preserving DecidedAt as its cooldown clock.
func (h *RunOpportunityRelocatorHandler) markCompleted(ctx context.Context, cmd *RunOpportunityRelocatorCommand, intent RelocationIntent) {
	intent.Completed = true
	if err := h.intents.RecordRelocationIntent(ctx, cmd.ContainerID, cmd.PlayerID, intent); err != nil {
		common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf("Opportunity relocator: could not record %s's completed relocation intent: %v", intent.ShipSymbol, err), map[string]interface{}{
			"ship_symbol": intent.ShipSymbol, "reason": "intent_persist_failed",
		})
	}
}

// relocationCandidate is one priced (hull, region) pair eligible to be committed.
type relocationCandidate struct {
	hull      RelocatorHull
	region    RelocatorRegion
	valuation trading.RelocationValuation
}

// scoreCandidates prices every eligible (hull, region) pair and returns the licensed ones, best NPV
// first. Every exclusion is counted by reason for the heartbeat.
func (h *RunOpportunityRelocatorHandler) scoreCandidates(
	ctx context.Context,
	cmd *RunOpportunityRelocatorCommand,
	hulls []RelocatorHull,
	state *relocatorState,
	result *RelocatorTickResult,
) []relocationCandidate {
	telemetry := h.readTelemetry(ctx, cmd)
	remainingEra, eraKnown := h.readEraHorizon(ctx, cmd)
	cooldown := time.Duration(resolveRelocatorCooldownMinutes(cmd.CooldownMinutes)) * time.Minute

	var candidates []relocationCandidate
	for _, hull := range hulls {
		if reason, eligible := h.hullEligibility(hull, state, cooldown); !eligible {
			result.skip(reason)
			continue
		}
		currentRate, rateReadable := trading.EwmaHullTourRate(telemetry, hull.ShipSymbol, trading.DefaultHullRateSmoothing)
		if !rateReadable {
			result.skip("hull_rate_unreadable") // fail closed: never move a hull off a guessed rate
			continue
		}
		result.Evaluated++
		candidates = append(candidates, h.scoreHull(ctx, cmd, hull, currentRate, remainingEra, eraKnown, state, result)...)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].valuation.NPV > candidates[j].valuation.NPV
	})
	return candidates
}

// hullEligibility applies every non-economic exclusion, in order of how cheap it is to check. The
// reason string is what the heartbeat counts.
func (h *RunOpportunityRelocatorHandler) hullEligibility(hull RelocatorHull, state *relocatorState, cooldown time.Duration) (string, bool) {
	if hull.IsCommandFrigate {
		return "command_frigate_protected", false // RULINGS #7
	}
	if hull.Pinned {
		return "pinned_hull_protected", false // RULINGS #7
	}
	if hull.OnTour {
		return "mid_tour", false // only at honest tour release
	}
	if state.movingOrSettling[hull.ShipSymbol] {
		return "already_relocating", false
	}
	if last, seen := state.lastRelocation[hull.ShipSymbol]; seen && h.clock.Now().Sub(last) < cooldown {
		return "within_cooldown", false
	}
	return "", true
}

// scoreHull prices one hull against every region it could reach.
func (h *RunOpportunityRelocatorHandler) scoreHull(
	ctx context.Context,
	cmd *RunOpportunityRelocatorCommand,
	hull RelocatorHull,
	currentRate, remainingEra float64,
	eraKnown bool,
	state *relocatorState,
	result *RelocatorTickResult,
) []relocationCandidate {
	logger := common.LoggerFromContext(ctx)
	regions, err := h.regions.ObserveRegions(ctx, cmd.PlayerID, hull.CurrentSystem, resolveRelocatorRegionHopRadius(cmd.RegionHopRadius))
	if err != nil {
		// Fail closed: an unreadable region set scores NOTHING for this hull.
		result.skip("regions_unreadable")
		logger.Log("WARN", fmt.Sprintf("Opportunity relocator: regions around %s unreadable for %s, scoring nothing there: %v", hull.CurrentSystem, hull.ShipSymbol, err), map[string]interface{}{
			"ship_symbol": hull.ShipSymbol, "origin_system": hull.CurrentSystem, "reason": "regions_unreadable",
		})
		return nil
	}
	var scored []relocationCandidate
	for _, region := range regions {
		if reason, usable := h.regionUsable(region, hull); !usable {
			result.skip(reason)
			continue
		}
		valuation := trading.ValueRelocation(h.inputsFor(cmd, currentRate, region, remainingEra, eraKnown))
		if !valuation.Licensed() {
			result.skip(string(valuation.Verdict))
			continue
		}
		scored = append(scored, relocationCandidate{hull: hull, region: region, valuation: valuation})
	}
	return scored
}

// regionUsable applies the region-side exclusions: FAIL CLOSED on an unreadable projection or a
// snapshot older than its activity's freshness cap.
//
// A STALE REGION IS EXCLUDED, NOT SCORED OPTIMISTICALLY. That is the whole point of reusing
// RankerAgeCaps here: an 8-hour-old quote on a WEAK market is still rankable, while a 40-minute-old
// one on a STRONG market is not, and a single flat cap gets one of those wrong. Scoring a stale
// region at its last-seen prices is how a hull gets sent to ground that no longer exists.
//
// THE ANTI-HERD CAP IS DELIBERATELY NOT CHECKED HERE. It lives at ONE site, in commitTopCandidates,
// because that is where the per-system count actually MUTATES: committing one hull to a region is
// what fills it for the next. A copy of the check here would be indistinguishable from the commit
// check in every outcome — a mutation probe removing it killed no test — which makes it dead surface
// that can silently drift out of step with the real enforcement point.
func (h *RunOpportunityRelocatorHandler) regionUsable(region RelocatorRegion, hull RelocatorHull) (string, bool) {
	if region.AnchorSystem == hull.CurrentSystem {
		return "region_is_current_system", false
	}
	if !region.RateReadable {
		return "region_rate_unreadable", false
	}
	if region.SnapshotAge > h.ageCaps.For(region.Activity) {
		return "region_snapshot_stale", false
	}
	return "", true
}

// inputsFor assembles the pure valuation's inputs. travel_h comes from the swappable TravelHopModel;
// the risk margin is one configured tour of THIS hull's own earnings.
func (h *RunOpportunityRelocatorHandler) inputsFor(
	cmd *RunOpportunityRelocatorCommand,
	currentRate float64,
	region RelocatorRegion,
	remainingEra float64,
	eraKnown bool,
) trading.RelocationInputs {
	riskMarginHours := float64(resolveRelocatorRiskMarginTourMinutes(cmd.RiskMarginTourMinutes)) / 60
	return trading.RelocationInputs{
		CurrentRate:       currentRate,
		ProjectedRate:     region.ProjectedRate,
		TravelHours:       h.travel.CrossingHours(region.GateHops),
		RemainingEraHours: remainingEra,
		EraHorizonKnown:   eraKnown,
		HorizonHours:      float64(resolveRelocatorHorizonHours(cmd.HorizonHours)),
		RiskMargin:        currentRate * riskMarginHours,
		UpliftBar:         float64(resolveRelocatorUpliftBarPct(cmd.UpliftBarPct)) / 100,
		NPVThreshold:      float64(resolveRelocatorNPVThreshold(cmd.NPVThresholdCredits)),
	}
}

// commitTopCandidates relocates the best-NPV candidates until a cap stops it.
//
// The caps are re-checked AT COMMIT, not only at scoring, because committing one candidate changes
// what the next one is allowed to do: two hulls whose best ground is the same system must not both
// go there if that would breach the anti-herd cap. Ranking alone cannot express that.
func (h *RunOpportunityRelocatorHandler) commitTopCandidates(
	ctx context.Context,
	cmd *RunOpportunityRelocatorCommand,
	candidates []relocationCandidate,
	state *relocatorState,
	budget int,
	result *RelocatorTickResult,
) {
	maxHulls := resolveRepositionReachMaxHulls(cmd.RepositionReachMaxHullsPerSystem)
	moved := map[string]bool{}
	for _, candidate := range candidates {
		if budget <= 0 {
			result.skip("at_concurrency_cap")
			return
		}
		if moved[candidate.hull.ShipSymbol] {
			continue // one relocation per hull per tick
		}
		if state.hullsBySystem[candidate.region.AnchorSystem] >= maxHulls {
			result.skip("region_herded")
			continue
		}
		if !h.commitRelocation(ctx, cmd, candidate) {
			continue
		}
		moved[candidate.hull.ShipSymbol] = true
		state.hullsBySystem[candidate.region.AnchorSystem]++
		budget--
		result.Relocated = append(result.Relocated, candidate.hull.ShipSymbol)
	}
}

// commitRelocation persists the intent BEFORE moving (so a crash mid-flight is resumed, never
// re-decided), moves the hull, then records the intent as completed — which also starts the
// restart-durable cooldown clock. Returns whether the hull actually moved.
func (h *RunOpportunityRelocatorHandler) commitRelocation(ctx context.Context, cmd *RunOpportunityRelocatorCommand, candidate relocationCandidate) bool {
	logger := common.LoggerFromContext(ctx)
	intent := RelocationIntent{
		ShipSymbol:     candidate.hull.ShipSymbol,
		FromSystem:     candidate.hull.CurrentSystem,
		TargetSystem:   candidate.region.AnchorSystem,
		TargetWaypoint: candidate.region.LandingWaypoint,
		DecidedAt:      h.clock.Now(),
	}
	if err := h.intents.RecordRelocationIntent(ctx, cmd.ContainerID, cmd.PlayerID, intent); err != nil {
		// Fail closed: without a durable intent a crash mid-flight would leave an unattributed hull
		// in transit and the next tick would re-decide it. Do not move.
		logger.Log("WARN", fmt.Sprintf("Opportunity relocator: could not persist %s's relocation intent, not moving it: %v", intent.ShipSymbol, err), map[string]interface{}{
			"ship_symbol": intent.ShipSymbol, "reason": "intent_persist_failed",
		})
		return false
	}
	logger.Log("INFO", fmt.Sprintf("Opportunity relocator: relocating %s from %s to %s (%s), %d gate hops - NPV %.0f cr (uplift %.0f/hr over %.1f h, payback %.2f h)", intent.ShipSymbol, intent.FromSystem, intent.TargetSystem, intent.TargetWaypoint, candidate.region.GateHops, candidate.valuation.NPV, candidate.valuation.UpliftPerHour, candidate.valuation.ProductiveWindowHours, candidate.valuation.PaybackHours), map[string]interface{}{
		"ship_symbol": intent.ShipSymbol, "from_system": intent.FromSystem, "to_system": intent.TargetSystem,
		"to_waypoint": intent.TargetWaypoint, "gate_hops": candidate.region.GateHops,
		"npv": candidate.valuation.NPV, "uplift_per_hour": candidate.valuation.UpliftPerHour,
		"productive_window_hours": candidate.valuation.ProductiveWindowHours,
		"payback_hours":           candidate.valuation.PaybackHours, "trigger": "opportunity_relocator",
	})
	if err := h.actuator.RelocateHull(ctx, cmd.PlayerID, intent.ShipSymbol, intent.TargetWaypoint, resolveRepositionJumpBound(cmd.JumpBound)); err != nil {
		// The intent stays UNcompleted, so the next tick RESUMES this move instead of re-deciding it.
		logger.Log("WARN", fmt.Sprintf("Opportunity relocator: relocating %s to %s failed, intent left in flight for the next tick: %v", intent.ShipSymbol, intent.TargetWaypoint, err), map[string]interface{}{
			"ship_symbol": intent.ShipSymbol, "target_waypoint": intent.TargetWaypoint, "reason": "relocate_failed",
		})
		return false
	}
	h.markCompleted(ctx, cmd, intent)
	return true
}

// readTelemetry reads the EWMA window. An unreadable repository yields no rows, which makes every
// hull rate unreadable and therefore relocates nothing — fail closed by construction.
func (h *RunOpportunityRelocatorHandler) readTelemetry(ctx context.Context, cmd *RunOpportunityRelocatorCommand) []trading.TourLegTelemetry {
	window := time.Duration(resolveRelocatorRateWindowMinutes(cmd.RateWindowMinutes)) * time.Minute
	rows, err := h.telemetry.ObserveTourTelemetry(ctx, cmd.PlayerID, h.clock.Now().Add(-window))
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf("Opportunity relocator: tour telemetry unreadable, no hull has a provable rate this tick: %v", err), map[string]interface{}{
			"reason": "telemetry_unreadable",
		})
		return nil
	}
	return rows
}

// readEraHorizon reads the remaining era. Unknown is honest and bounded, not fatal — see
// trading.ValueRelocation on an unknown horizon.
func (h *RunOpportunityRelocatorHandler) readEraHorizon(ctx context.Context, cmd *RunOpportunityRelocatorCommand) (float64, bool) {
	if h.era == nil {
		return 0, false
	}
	return h.era.RemainingEraHours(ctx, cmd.PlayerID)
}

// countHullsBySystem tallies landed trade hulls per system — the anti-herd baseline, taken from the
// SAME observation the candidates come from so there is no second, separately-failing fleet read.
func countHullsBySystem(hulls []RelocatorHull) map[string]int {
	counts := make(map[string]int, len(hulls))
	for _, hull := range hulls {
		counts[hull.CurrentSystem]++
	}
	return counts
}

// hullPositions maps each observed hull to the system it is actually in — the live state the restart
// contract compares a persisted intent against.
func hullPositions(hulls []RelocatorHull) map[string]string {
	positions := make(map[string]string, len(hulls))
	for _, hull := range hulls {
		positions[hull.ShipSymbol] = hull.CurrentSystem
	}
	return positions
}

// ── knob resolution (RULINGS #5: the 0/absent -> documented default idiom) ───────────────────────

func resolveRelocatorTickSeconds(configured int) int {
	if configured <= 0 {
		return defaultRelocatorTickSeconds
	}
	return configured
}

func resolveRelocatorNPVThreshold(configured int64) int64 {
	if configured <= 0 {
		return defaultRelocatorNPVThresholdCredits
	}
	return configured
}

// resolveRelocatorUpliftBarPct applies the 0/absent -> 150 rule AND clamps UP to
// relocatorUpliftBarPctMin: the anti-thrash ratchet can be raised, never weakened.
func resolveRelocatorUpliftBarPct(configured int) int {
	if configured <= 0 {
		return defaultRelocatorUpliftBarPct
	}
	if configured < relocatorUpliftBarPctMin {
		return relocatorUpliftBarPctMin
	}
	return configured
}

func resolveRelocatorMaxConcurrent(configured int) int {
	if configured <= 0 {
		return defaultRelocatorMaxConcurrentRelocations
	}
	return configured
}

func resolveRelocatorCooldownMinutes(configured int) int {
	if configured <= 0 {
		return defaultRelocatorCooldownMinutes
	}
	return configured
}

func resolveRelocatorHorizonHours(configured int) int {
	if configured <= 0 {
		return defaultRelocatorHorizonHours
	}
	return configured
}

func resolveRelocatorRiskMarginTourMinutes(configured int) int {
	if configured <= 0 {
		return defaultRelocatorRiskMarginTourMinutes
	}
	return configured
}

func resolveRelocatorRegionHopRadius(configured int) int {
	if configured <= 0 {
		return defaultRelocatorRegionHopRadius
	}
	return configured
}

func resolveRelocatorRateWindowMinutes(configured int) int {
	if configured <= 0 {
		return defaultRelocatorRateWindowMinutes
	}
	return configured
}

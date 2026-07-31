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
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	// defaultRelocatorTickSeconds is the reconcile cadence: 2 minutes. It is sized to LAND INSIDE
	// the idle window between one tour ending and the same hull's next tour starting, because that
	// window is the only moment a hull can be relocated at all.
	//
	// IT WAS 900 (~15 min), on the reasoning that a cadence slower than a tour would catch hulls at
	// a genuine release rather than mid-flight. The second half of that is right and unchanged — the
	// mid_tour guard is what enforces it, not the cadence — but the first half had the arithmetic
	// backwards: a tick SLOWER than the window sleeps through it. Measured over 12h of tour
	// containers, 195 samples of lead(started_at) - stopped_at per hull:
	//
	//	under 3 min      0      min     183s
	//	3-15 min       184      p05     185s
	//	over 15 min     11      median  206s
	//
	// No hull is ever idle for less than three minutes, and at 900s the relocator slept through
	// ~86% of those windows: a live tick read `1 relocated, 0 resumed, 1 evaluated,
	// skipped[mid_tour=32]` — one eligible hull out of 33, because the tick landed mid-tour for the
	// other 32 rather than because they were ineligible.
	//
	// WHY 120 AND NOT LOWER. A periodic tick of period T lands inside a window of length W whenever
	// T <= W, so 183 would already catch every window observed. 120 leaves a 1.5x margin under the
	// narrowest one for windows shorter than any yet measured, and going below that buys no capture
	// at all — only more ticks.
	//
	// COST. Cadence does not drive API cost; ELIGIBLE HULLS do. ~29 hulls come free per hour, so
	// there are ~29 evaluations to perform however often we look, and the cadence only decides
	// whether we catch them. A tick with nothing eligible issues ZERO API calls: hullEligibility
	// returns mid_tour and `continue`s before scoreHull, which is the only path that reaches
	// ObserveRegions or the planner. What such a tick does cost is three DB reads — the fleet
	// observation, the persisted intents, and a 4h telemetry window (968 rows, 0.79ms on
	// idx_tour_leg_telemetry_player) — plus a no-op era read. 7x the rate of that is nothing, and
	// the per-hull pre-flight stays separately bounded by relocationRegionCandidateBudget so a
	// burst of simultaneously-free hulls cannot spike either.
	//
	// The anti-thrash bounds are cadence-INDEPENDENT and unaffected: the per-hull cooldown is
	// wall-clock against DecidedAt (see hullEligibility) rather than a count of ticks, and
	// concurrency is capped by max_concurrent_relocations minus what is already in flight. Nor can
	// a faster tick re-enter a running relocation — Handle runs one tick to completion and only
	// then starts the timer, so the period is tick duration PLUS this cadence, never less.
	defaultRelocatorTickSeconds = 120

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
	// Offered marks a hull whose tour has reached a boundary and has DURABLY offered it for
	// relocation until a deadline (sp-e8d92 first refusal). Its tour container is still RUNNING — it is
	// waiting — so the hull reads OnTour and would otherwise be excluded as mid_tour, which is exactly
	// the 38..40 exclusions the fleet reports while occupying 23 of 373 tradeable systems.
	//
	// AN OFFER IS PERMISSION, NOT OWNERSHIP. It says "this hull's tour will wait for you until T", never
	// "ownership is waived": a protected hull stays protected, and an offer that lapses before the move
	// commits is abandoned through the counted actuation re-check like any other lost hull.
	Offered bool
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
	// ObserveHull re-reads ONE hull's live protection facts, for the actuation-time re-check
	// (sp-x2jr6 slice 1). It must derive Pinned/OnTour/IsCommandFrigate exactly as ObserveTradeHulls
	// does, or the commit gate and the scoring gate will disagree about what a protected hull is.
	// An error means the hull's ownership is UNPROVABLE, which fails the move closed.
	ObserveHull(ctx context.Context, playerID int, shipSymbol string) (RelocatorHull, error)
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
	// stall is the WRITE-ONLY stall-escalation seam (health.StallObserver): each tick's verdict is
	// reported once, so a relocator that refuses every decision escalates instead of looking idle
	// (sp-j1i49). Optional and nil-safe — observability never gates a tick. Its method returns
	// nothing, so no decision path can read the streak.
	stall health.StallObserver
	// metrics is the WRITE-ONLY counter seam (sp-j1i49): per-tick verdict, per-hull decision, and the
	// per-reason skip counts, so the relocator's behaviour is a RATE and not an anecdote reconstructed
	// from a log/table join. Optional and nil-safe.
	metrics RelocatorMetricsSink
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
		result, err := h.tick(ctx, cmd)
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

// tick runs ONE reconcile and REPORTS it: the unit Handle repeats, and the unit every observability
// test drives.
//
// Reporting lives here rather than inside Reconcile so the decision path stays free of it: Reconcile
// decides, tick observes. That separation is what lets every pre-existing test drive Reconcile directly
// and still prove what it always proved, and it keeps the once-per-tick contract the escalator depends
// on at exactly one call site (see observeTickStall).
func (h *RunOpportunityRelocatorHandler) tick(ctx context.Context, cmd *RunOpportunityRelocatorCommand) (*RelocatorTickResult, error) {
	result, err := h.Reconcile(ctx, cmd)
	h.logTickHeartbeat(ctx, cmd, result)
	h.observeTickStall(ctx, cmd, result, err)
	return result, err
}

// Reconcile runs ONE tick: re-derive from live state, score, act. It is the driving port — every
// test enters here.
//
// RULINGS #2: the tick begins by RE-DERIVING everything from live state. Nothing is carried in
// memory between ticks except what the intent store holds, so a restart mid-tick loses no decision
// and repeats none.
func (h *RunOpportunityRelocatorHandler) Reconcile(ctx context.Context, cmd *RunOpportunityRelocatorCommand) (*RelocatorTickResult, error) {
	result := newRelocatorTickResult()
	if cmd.RepositionDisabled {
		result.skip(skipReasonRepositionDisabled)
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
	// A RESUME calls the same actuator, so it can poach exactly as a fresh commit can (sp-x2jr6 slice
	// 1). The two failure modes are handled DIFFERENTLY, and the difference is load-bearing:
	//
	//   - DEFINITELY TAKEN → ABANDON, marking the intent completed. It must not be left in flight:
	//     reconcileInFlight counts every uncompleted intent against max_concurrent_relocations, so a
	//     permanently-unresumable intent would consume a slot forever and two of them would deadlock
	//     the reconciler at its cap — it would stop relocating anything at all. Completing preserves
	//     DecidedAt, so the hull's cooldown still runs from the original decision and it is re-scored
	//     afresh later rather than immediately.
	//   - UNREADABLE → leave it IN FLIGHT and retry next tick. A transient blip must not abandon a
	//     relocation that is probably still valid; failing closed here means refusing the MOVE, not
	//     discarding the record.
	switch verdict := h.actuationVerdict(ctx, cmd, intent.ShipSymbol); verdict {
	case actuationTaken:
		h.markCompleted(ctx, cmd, intent)
		result.skip(string(verdict))
		return
	case actuationUnreadable:
		result.skip(string(verdict))
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

// relocationActuationVerdict is what an actuation-time re-read of ONE hull concluded. The string value
// IS the heartbeat skip reason, so the branch taken and the counter incremented have one definition.
type relocationActuationVerdict string

const (
	// actuationClear means the hull is still unowned and the move may proceed.
	actuationClear relocationActuationVerdict = ""
	// actuationTaken means someone has claimed or reserved the hull since it was scored. The
	// relocation is abandoned. Counted apart from the scoring-time protections on purpose: it measures
	// the RACE (how much throughput the non-atomic window is costing), which a hull that was never
	// eligible does not.
	actuationTaken relocationActuationVerdict = "claimed_at_actuation"
	// actuationUnreadable means the re-read failed, so ownership is unprovable. Also refuses the move,
	// but is a DIFFERENT outcome: it is transient, and the resume path treats the two differently.
	actuationUnreadable relocationActuationVerdict = "actuation_recheck_unreadable"
)

// actuationVerdict re-reads ONE hull's ownership immediately before it would be moved — the sp-x2jr6
// slice-1 narrowing.
//
// WHY IT IS NEEDED AT ALL. Observation and actuation are not atomic and the actuator holds no claim
// (RepositionToWaypointWithinJumps trusts a claim its caller already holds; this reconciler holds none).
// Between the fleet snapshot and the commit sits the bounded region pre-flight — up to
// relocationRegionCandidateBudget planner calls per hull — which measured ~14 seconds live. In that
// window a tour container can be created and claim the hull, and 3 of the relocator's first 4 live
// decisions were lost exactly so.
//
// WHAT IT IS NOT. This does NOT make observe-and-act atomic; a claim landing between this read and the
// first hop still wins. It shrinks the window from ~14 seconds to the round-trip of one read, which on
// the live numbers turns the dominant failure mode into a rare one — and it introduces NO claim, so it
// cannot leak one and cannot strand a hull, which a real ClaimShip could. Closing the window properly
// needs that claim plus an airtight release (sp-x2jr6 slice 3); this bead stays OPEN.
//
// FAIL CLOSED, asymmetrically. An unreadable re-read refuses the move: a lost relocation costs one tick
// of throughput, while moving a hull another operation is driving costs the hull.
func (h *RunOpportunityRelocatorHandler) actuationVerdict(ctx context.Context, cmd *RunOpportunityRelocatorCommand, shipSymbol string) relocationActuationVerdict {
	hull, err := h.fleet.ObserveHull(ctx, cmd.PlayerID, shipSymbol)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf("Opportunity relocator: could not re-read %s's ownership immediately before moving it, so the move is refused (fail closed): %v", shipSymbol, err), map[string]interface{}{
			"ship_symbol": shipSymbol, "reason": string(actuationUnreadable),
		})
		return actuationUnreadable
	}
	if reason, protected := hullProtected(hull); protected {
		common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Opportunity relocator: %s was claimed between scoring and actuation (%s) - abandoning the relocation rather than flying a hull another operation now holds", shipSymbol, reason), map[string]interface{}{
			"ship_symbol": shipSymbol, "protection": reason, "reason": string(actuationTaken),
		})
		return actuationTaken
	}
	return actuationClear
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
	// OFFERED FIRST, then by NPV. An offered hull's tour is STALLED and its window is closing; an
	// un-offered idle hull is under no clock and will still be here next tick. Ranking purely by NPV
	// would spend a scarce concurrency budget on the unhurried hull and let the offer lapse — wasting
	// the very window the offer exists to create, and paying the tour's idle time for nothing.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].hull.Offered != candidates[j].hull.Offered {
			return candidates[i].hull.Offered
		}
		return candidates[i].valuation.NPV > candidates[j].valuation.NPV
	})
	return candidates
}

// hullProtected reports whether a hull's OWNERSHIP facts forbid relocating it, and names which one.
//
// It is the ONE definition of "this hull belongs to someone else", read at BOTH gates: at scoring
// (hullEligibility) and again at commit (actuationVerdict, sp-x2jr6 slice 1). Sharing it is the point —
// two separately-written copies of the RULINGS #7 protections would drift the moment a fourth
// protection fact is added, and the copy that got missed would be the one guarding the actual move.
//
// It deliberately covers ONLY ownership. The relocator's own bookkeeping — the per-hull cooldown, an
// already-in-flight move — is not ownership and is checked once, at scoring, where it belongs: applying
// "already_relocating" at commit would refuse every resumed move, which is the one move a restart MUST
// finish.
func hullProtected(hull RelocatorHull) (string, bool) {
	switch {
	case hull.IsCommandFrigate:
		return "command_frigate_protected", true // RULINGS #7
	case hull.Pinned:
		return "pinned_hull_protected", true // RULINGS #7
	case hull.OnTour && !hull.Offered:
		// Only at honest tour release — OR at a boundary its tour has explicitly offered. The offer is
		// the ONE exemption here, and it is deliberately narrow: it lifts the mid-tour rule and nothing
		// else, because a stalled tour is a hull nobody is using, whereas the two cases above are
		// ownership and are never waived.
		return "mid_tour", true
	}
	return "", false
}

// hullEligibility applies every non-economic exclusion, in order of how cheap it is to check. The
// reason string is what the heartbeat counts.
func (h *RunOpportunityRelocatorHandler) hullEligibility(hull RelocatorHull, state *relocatorState, cooldown time.Duration) (string, bool) {
	if reason, protected := hullProtected(hull); protected {
		return reason, false
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
		result.skip(skipReasonRegionsUnreadable)
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
		// sp-e8d92/relocator: staleness no longer vetoes (see regionUsable), so a LICENSED candidate
		// may be resting on cold ground. Say so at the moment it is admitted, not afterwards — this is
		// the only place that knows both the age and that the economics said yes. A licensed-on-stale
		// candidate is the deliberate trade the Admiral authorised; an unexplained one later is not.
		if h.regionSnapshotStale(region) {
			logger.Log("INFO", fmt.Sprintf(
				"Opportunity relocator: licensing %s -> %s on a STALE snapshot (%s old, cap %s for activity %q) - accepted deliberately, the alternative was leaving the hull in a shared system",
				hull.ShipSymbol, region.AnchorSystem, region.SnapshotAge.Round(time.Minute),
				h.ageCaps.For(region.Activity).Round(time.Minute), region.Activity),
				map[string]interface{}{
					"ship_symbol": hull.ShipSymbol, "origin_system": hull.CurrentSystem,
					"target_system": region.AnchorSystem, "snapshot_age_seconds": int(region.SnapshotAge.Seconds()),
					"activity_cap_seconds": int(h.ageCaps.For(region.Activity).Seconds()),
					"activity":             region.Activity, "trigger": "relocator_stale_region_licensed",
				})
		}
		scored = append(scored, relocationCandidate{hull: hull, region: region, valuation: valuation})
	}
	return scored
}

// regionUsable applies the region-side exclusions: FAIL CLOSED on an unreadable projection.
//
// STALENESS NO LONGER EXCLUDES A REGION (Admiral, 2026-07-31). The previous rule refused any region
// whose snapshot was older than its activity's freshness cap, and it was the single largest blocker
// of actual spreading: over the measured window the relocator was offered hulls at a healthy rate,
// exempted them from the mid-tour rule correctly, and then refused 27 of ~48 eligible pairings as
// region_snapshot_stale — against 1 relocation. Meanwhile the fleet held market data for 516 systems
// and traded in 39.
//
// The rule was locally correct and globally wrong. Its own reasoning still stands as written: an
// 8-hour-old quote on a WEAK market is rankable, a 40-minute-old one on a STRONG market is not, and
// scoring a stale region at last-seen prices can send a hull to ground that no longer exists. What
// it missed is the alternative. Refusing the move does not hold the hull somewhere known-good; it
// holds it in a system it already shares with ~26 other trade hulls, grinding a contended sink. A
// stale estimate of somewhere else beats a fresh estimate of the crush.
//
// WHAT STILL PROTECTS THE MOVE, because this removes ONE gate and not the economics:
//   - region_rate_unreadable stays. An unreadable projection is still fail-closed: we will act on an
//     OLD number, never on NO number.
//   - ValueRelocation still has to LICENSE the move (no_uplift / below_npv_threshold at :767). The
//     destination must still beat the hull's current rate by the NPV threshold, on whatever data
//     exists.
//   - the anti-herd cap in commitTopCandidates still stops a stampede into one region.
//   - the concurrency cap, the per-hull cooldown, and the actuation-time ownership re-check are all
//     downstream of here and untouched.
//
// So the accepted risk is bounded and RECOVERABLE: a hull can be sent somewhere that turns out no
// better, and is then relocated again from there. That is strictly preferable to the status quo, in
// which it is never sent anywhere at all.
//
// SnapshotAge is deliberately still carried on the region and is reported by the caller, so the cost
// of this decision stays measurable — see the stale-accepted counting at the call site. If
// stale-sourced relocations systematically underperform fresh ones, that shows up as data rather
// than as an argument.
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
	return "", true
}

// regionSnapshotStale reports whether a region's snapshot is older than its activity's freshness cap.
//
// This is the predicate that used to VETO the region. It is retained, and still consulted, purely as
// an OBSERVATION: the relocator now accepts stale ground deliberately, and the fleet needs to be able
// to tell afterwards how much of its spreading was bought on cold data. Deleting it would have made
// that unanswerable, which is the same mistake as shipping the offer path with no refusal counter.
func (h *RunOpportunityRelocatorHandler) regionSnapshotStale(region RelocatorRegion) bool {
	return region.SnapshotAge > h.ageCaps.For(region.Activity)
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
		if !h.commitRelocation(ctx, cmd, candidate, result) {
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
func (h *RunOpportunityRelocatorHandler) commitRelocation(ctx context.Context, cmd *RunOpportunityRelocatorCommand, candidate relocationCandidate, result *RelocatorTickResult) bool {
	logger := common.LoggerFromContext(ctx)
	// RE-READ THE HULL'S OWNERSHIP FIRST, before anything is persisted (sp-x2jr6 slice 1). The order
	// matters: an intent written for a move this check then refuses would be RESUMED on the next tick,
	// which is how a refused relocation happens anyway one tick later — and the resume path would be
	// carrying it out against the very hull the check rejected. Nothing is written until the hull is
	// proven still unowned.
	if verdict := h.actuationVerdict(ctx, cmd, candidate.hull.ShipSymbol); verdict != actuationClear {
		result.skip(string(verdict))
		return false
	}
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
			"ship_symbol": intent.ShipSymbol, "reason": skipReasonIntentPersistFailed,
		})
		// Counted, not just logged: a licensed relocation that did not happen is what the stall
		// verdict escalates on, and before sp-j1i49 this path left no trace in the tick result at all.
		result.skip(skipReasonIntentPersistFailed)
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
			"ship_symbol": intent.ShipSymbol, "target_waypoint": intent.TargetWaypoint, "reason": skipReasonRelocateFailed,
		})
		result.skip(skipReasonRelocateFailed) // see the persist branch above
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

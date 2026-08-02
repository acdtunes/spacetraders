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
//     last resort." The command frigate and any pinned hull are dropped at OBSERVATION, so
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
	// metrics is the WRITE-ONLY counter seam: per-tick verdict, per-hull decision, and the
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
	tick := cmd.tickInterval()
	logger.Log("INFO", fmt.Sprintf("Opportunity relocator starting (tick %s, NPV threshold %d cr, uplift bar %d%%, max %d concurrent)", tick, cmd.npvThreshold(), cmd.upliftBarPct(), cmd.maxConcurrent()), map[string]interface{}{
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
	budget := cmd.maxConcurrent() - state.inFlight
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

package commands

// run_tour_coordinator_rate_floor.go — rate-floor early-reposition (epic sp-fguo, Part 2 of the
// reposition-reach work; part 1 = sp-uf64 widened DISCOVERY). The margins-death reposition
// (run_tour_coordinator_reposition.go) only fires when a continuous tour's margins DIE (3-strike).
// A hull earning MEDIOCRE-BUT-PROFITABLE local arb (say 80k/hr while frontier systems pay
// 360-480k/hr) never margin-dies, so it never relocates — it sits on weak arb forever. That
// describes most of the fleet (~180k/hull blended vs 360-480k on the frontier).
//
// This trigger is a SEPARATE path evaluated AFTER a hull completes a PRODUCTIVE continuous tour
// (alongside — not replacing — the margins-death reposition): if the hull's realized tour rate is
// far below the fleet-median realized rate, relocate it via part-1's reach discovery to a
// MEANINGFULLY better system. It is DEFAULT-OFF (the whole trigger lives inside
// cmd.RepositionRateFloorEnabled) and heavily gated so it can NEVER churn the fleet — thrash is the
// failure mode, so every gate is conservative and fail-closed:
//
//   - FAIL-CLOSED on a bad median: an unreadable or non-positive fleet median does NOTHING (mirrors
//     the sp-z7ng placement senseBeta — a relocation is never decided off a fabricated rate).
//   - UNDER-EARNER predicate: only a hull below rate_floor_pct% of the fleet median is a candidate.
//   - IMPROVEMENT gate: the best reach candidate's projected rate (net of deadhead, the SAME
//     repositionCandidateRate the margins-death path ranks on) must clear improvement_pct% of the
//     hull's CURRENT realized rate AND strictly beat staying — never a worse/marginal target.
//   - ANTI-HERD: the candidate must survive excludeHerdedSystems (the sp-uf64 per-system hull cap),
//     the primary fleet-spread mechanism so a whole cohort cannot stampede onto one ground.
//   - DWELL: never relocate a hull that relocated within rate_floor_dwell_minutes — the per-hull
//     cadence cap (the episode budget resets on every productive tour, which is exactly when this
//     fires, so dwell — not episode.repositioned — is the durable per-hull rate limiter).
//   - KILL-SWITCH: the shared RepositionDisabled stop silences this too (one operator stop halts
//     ALL auto-relocation).
//
// When in doubt it STAYS. A relocation reuses the EXACT proven jump machinery the margins-death path
// uses (persist-before-jump → look-back load → bounded stored-adjacency resolver → clear → metrics),
// so it inherits the restart-resume durability (RULINGS #2) and the deadhead look-back loading.

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	// repositionRateFloorPctDefault is the under-earner threshold as a percent of the fleet-median
	// realized tour $/hr, when the captain leaves reposition_rate_floor_pct at 0: a hull earning
	// < 40% of the fleet median is a relocation CANDIDATE. Deliberately well below 100% so only a
	// CHRONIC under-earner (not merely a below-average one) is ever a candidate — the bar to even
	// look at candidates is conservative.
	repositionRateFloorPctDefault = 40

	// repositionRateFloorImprovementPctDefault is how much better the best reach candidate's
	// PROJECTED rate must be than the hull's CURRENT realized rate to justify the jump, when the
	// captain leaves reposition_rate_floor_improvement_pct at 0: >= 200% (2x). The 2x bar is
	// deliberately generous — it is the anti-thrash cushion that absorbs both the projected-vs-
	// realized optimism (the candidate rate is a pre-flight projection; the hull rate is booked)
	// and any deadhead the netted candidate rate did not fully capture. A relocation that is not a
	// clear 2x win does not happen.
	//
	// This ratchet is SAFETY-CRITICAL: dwell (below) is < a typical 30-60min tour, so it rarely
	// bites across consecutive tours — the improvement ratchet is the REAL per-relocation limiter.
	// So it is clamped to a hard floor (repositionRateFloorImprovementPctMin) that a config value
	// cannot tune away.
	repositionRateFloorImprovementPctDefault = 200

	// repositionRateFloorImprovementPctMin is the HARD FLOOR the improvement ratchet is clamped to:
	// a configured reposition_rate_floor_improvement_pct below this is treated as this. 150 (1.5x)
	// keeps a meaningful anti-thrash margin even if an operator sets the knob too low, so the
	// safety-critical ratchet can never be silently tuned away.
	repositionRateFloorImprovementPctMin = 150

	// repositionRateFloorDwellMinutesDefault is the per-hull cooldown after a rate-floor relocation,
	// when the captain leaves reposition_rate_floor_dwell_minutes at 0: 45 minutes. Raised from an
	// earlier 15 so it BITES across consecutive tours — a typical productive tour runs 30-60min, so
	// a 15min dwell would already have elapsed by the next tour boundary and never gate. At 45min a
	// hull that relocated cannot rate-floor-relocate again for roughly one more tour, the durable
	// per-hull rate limiter that stops hop-scotching (the one-reposition-per-episode budget cannot
	// serve here: it resets on every productive tour, which is exactly when this trigger evaluates).
	repositionRateFloorDwellMinutesDefault = 45
)

// resolveRateFloorPct applies the 0/absent -> 40 rule (RULINGS #5), so the default lives in ONE
// place, mirroring resolveRepositionReachHopDecay / resolvePlacementParkFloorPct.
func resolveRateFloorPct(configured int) int {
	if configured <= 0 {
		return repositionRateFloorPctDefault
	}
	return configured
}

// resolveRateFloorImprovementPct applies the 0/absent -> 200 rule (RULINGS #5) AND clamps to the
// hard floor repositionRateFloorImprovementPctMin (150): a configured value below the floor is
// treated as the floor, so the safety-critical anti-thrash ratchet can never be tuned away (an
// operator can raise it, never weaken it below 1.5x).
func resolveRateFloorImprovementPct(configured int) int {
	if configured <= 0 {
		return repositionRateFloorImprovementPctDefault
	}
	if configured < repositionRateFloorImprovementPctMin {
		return repositionRateFloorImprovementPctMin
	}
	return configured
}

// resolveRateFloorDwellMinutes applies the 0/absent -> 15 rule (RULINGS #5).
func resolveRateFloorDwellMinutes(configured int) int {
	if configured <= 0 {
		return repositionRateFloorDwellMinutesDefault
	}
	return configured
}

// maybeRepositionRateFloor is the rate-floor early-reposition trigger, evaluated after a PRODUCTIVE
// continuous tour ONLY when cmd.RepositionRateFloorEnabled. It relocates a chronic under-earner to a
// meaningfully better reachable ground, or STAYS (returns nil) whenever any conservative gate is not
// cleared. A non-nil error is an OPERATIONAL travel failure the runner should retry (resumable — the
// persisted in-flight destination is left set so a restart resumes the jump), never a "no relocate"
// verdict; every stay path returns nil so the continuous loop simply keeps touring the current
// ground.
func (h *RunTourCoordinatorHandler) maybeRepositionRateFloor(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	maxHops int,
	maxSpend, reserve int64,
	modelVersion string,
) error {
	logger := common.LoggerFromContext(ctx)

	// The shared kill-switch halts ALL auto-relocation — an armed rate-floor still honours a
	// captain's reposition_disabled stop, mirroring maybeReposition's first guard.
	if cmd.RepositionDisabled {
		return nil
	}

	// SENSE: the hull's realized rate + the fleet-median realized rate over the trailing window
	// (the SAME telemetry seam senseBeta uses). Fail-closed — an unreadable/non-positive median
	// NEVER triggers a relocation.
	hullRate, fleetMedian, ok := h.senseRateFloor(ctx, cmd)
	if !ok {
		return nil
	}

	// UNDER-EARNER predicate: only a hull below rate_floor_pct% of the fleet median is a candidate.
	floor := float64(resolveRateFloorPct(cmd.RepositionRateFloorPct)) / 100 * fleetMedian
	if hullRate >= floor {
		return nil // earning its keep — no change, keep touring here
	}

	// DWELL (cheap, before any candidate pre-flight): never relocate a hull that relocated recently.
	dwell := time.Duration(resolveRateFloorDwellMinutes(cmd.RepositionRateFloorDwellMinutes)) * time.Minute
	if h.withinRateFloorDwell(cmd.ShipSymbol, dwell) {
		logger.Log("INFO", fmt.Sprintf("Reposition rate-floor: %s realized %.0f/hr < floor %.0f/hr (%d%% of fleet median %.0f) but relocated within the %s dwell window - staying", cmd.ShipSymbol, hullRate, floor, resolveRateFloorPct(cmd.RepositionRateFloorPct), fleetMedian, dwell), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "hull_rate": hullRate, "floor": floor, "fleet_median": fleetMedian, "reason": "dwell",
		})
		return nil
	}

	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return err
	}
	currentSystem := ship.CurrentLocation().SystemSymbol

	// DISCOVERY: part-1 reach candidate set (buildRepositionCandidates), then the sp-uf64 anti-herd
	// exclusion (gate b). excludeHerdedSystems is applied HERE regardless of RepositionReachEnabled,
	// so the anti-herd spread mechanism always guards a rate-floor relocation; when reach is also
	// armed it already ran inside buildRepositionCandidates and this second pass is a harmless
	// idempotent re-check.
	candidates := h.buildRepositionCandidates(ctx, cmd, currentSystem)
	candidates, herdExcluded := h.excludeHerdedSystems(ctx, cmd, candidates)
	if len(candidates) == 0 {
		logger.Log("INFO", fmt.Sprintf("Reposition rate-floor: %s realized %.0f/hr < floor %.0f/hr but no reachable non-herded candidate from %s (%d herd-excluded) - staying", cmd.ShipSymbol, hullRate, floor, currentSystem, herdExcluded), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "current_system": currentSystem, "herd_excluded": herdExcluded, "reason": "no_candidate",
		})
		return nil
	}

	// The best reach candidate (buildRepositionCandidates already ranked best-first). Price its
	// projected rate with the SAME planAtCandidate pre-flight the margins-death path uses, then the
	// SAME deadhead-netted repositionCandidateRate it ranks on — a rate the jump+re-plan overhead is
	// already priced into. Evaluating only the single best is the cheapest (one pre-flight) and most
	// conservative choice: if the top ground is not a clear win we STAY, never fishing for a #2.
	best := candidates[0]
	plan, perr := h.planAtCandidate(ctx, ship, best, maxHops, maxSpend, reserve, cmd, modelVersion)
	if perr != nil || plan == nil || !plan.Feasible {
		logger.Log("INFO", fmt.Sprintf("Reposition rate-floor: %s best candidate %s is infeasible (%s) - staying", cmd.ShipSymbol, best.system, repositionCandidateReason(plan, perr)), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "candidate": best.system, "reason": "infeasible",
		})
		return nil
	}
	freshProfit := plan.ProjectedProfit - plan.HeldLiquidation - plan.DepositValue
	candidateRate, rateOK := repositionCandidateRate(freshProfit, plan, best.hops)
	if !rateOK {
		// No usable time estimate on the pre-flight plan (cph<=0): fail-closed, we cannot prove the
		// jump is worth it, so we STAY rather than relocate off a rate we had to guess.
		logger.Log("INFO", fmt.Sprintf("Reposition rate-floor: %s best candidate %s carries no rate estimate - staying (fail-closed)", cmd.ShipSymbol, best.system), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "candidate": best.system, "reason": "no_rate_estimate",
		})
		return nil
	}

	// IMPROVEMENT gate: the candidate must be a MEANINGFUL win, not a marginal one.
	improvementFactor := float64(resolveRateFloorImprovementPct(cmd.RepositionRateFloorImprovementPct)) / 100
	if !rateFloorImprovementClears(candidateRate, hullRate, floor, improvementFactor) {
		logger.Log("INFO", fmt.Sprintf("Reposition rate-floor: %s realized %.0f/hr, best candidate %s projects %.0f/hr - not a >=%d%% improvement, staying", cmd.ShipSymbol, hullRate, best.system, candidateRate, resolveRateFloorImprovementPct(cmd.RepositionRateFloorImprovementPct)), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "hull_rate": hullRate, "candidate": best.system, "candidate_rate": candidateRate, "reason": "below_improvement",
		})
		return nil
	}

	// ALL gates cleared — relocate through the shared jump machinery.
	return h.commitRateFloorRelocation(ctx, cmd, response, netBought, currentSystem, best, hullRate, candidateRate, maxSpend, reserve)
}

// rateFloorImprovementClears reports whether a candidate's deadhead-netted projected rate is a
// MEANINGFUL improvement worth the jump. Two hard requirements:
//   - STRICTLY beats staying (candidateRate > hullRate): a relocation is NEVER to a worse-or-equal
//     ground, whatever improvementFactor a captain configures (a misconfigured <100% factor can
//     therefore never cause a downgrade relocation).
//   - clears the multiplicative bar. When the hull's realized rate is POSITIVE, that is
//     candidateRate >= improvementFactor * hullRate (>= 2x by default). When the hull's realized
//     rate is NON-POSITIVE (a hull LOSING money — a real chronic under-earner) the multiplicative
//     bar is degenerate (2x a negative number is more negative, i.e. trivially cleared), so it
//     falls back to requiring the candidate to clear the SAME under-earner floor the hull failed
//     (floorFraction * fleetMedian): a money-loser relocates only to a ground projected to lift it
//     OUT of under-earner status, never to another still-below-floor ground.
func rateFloorImprovementClears(candidateRate, hullRate, floor, improvementFactor float64) bool {
	if candidateRate <= hullRate {
		return false // never relocate to a worse-or-equal ground
	}
	if hullRate > 0 {
		return candidateRate >= improvementFactor*hullRate
	}
	return candidateRate >= floor
}

// senseRateFloor reads the hull's realized tour rate AND the fleet rolling-median realized tour
// $/hr over the trailing window, via the SAME telemetry seam the sp-z7ng placement senseBeta uses
// (ListByPlayer with the window as its `since` bound + trading.MedianTourRate). It reuses the
// placement β window knob (placement_beta_window_minutes, default 60) rather than inventing a fifth
// rate-floor knob — one trailing-window concept for both rate-based relocation paths.
//
// sp-461l (epic sp-g9td) cash-true audit: BOTH rates STAY on telemetry. The under-earner predicate is
// a RATIO (hullRate < rate_floor_pct% × fleetMedian) of two PER-HULL/PER-TOUR medians, which the
// transactions ledger (no ship/tour column) cannot reproduce, and both sides must share one basis for
// the ratio to be meaningful. sp-rd21's write-path fix (dropped buy legs now recorded) removed the
// per-hull UNEVEN bias — a hull whose buys were dropped no longer looks like a star — so this trigger
// now relocates the genuinely-under-earning hulls on the true netted rate.
//
// The hull rate is MedianTourRate over the hull's OWN telemetry rows (filtered by ship symbol) —
// symmetric with the fleet median and reusing the exact same rate math (no reinvention). ok=false
// (fail-closed) whenever the telemetry repo is nil/errors, the fleet median is unreadable or
// NON-POSITIVE, or the hull has no computable realized rate — mirroring senseBeta so a relocation is
// never decided off a fabricated or degenerate median.
func (h *RunTourCoordinatorHandler) senseRateFloor(ctx context.Context, cmd *RunTourCoordinatorCommand) (hullRate, fleetMedian float64, ok bool) {
	if h.telemetry == nil {
		return 0, 0, false
	}
	window := time.Duration(resolvePlacementBetaWindowMinutes(cmd.PlacementBetaWindowMinutes)) * time.Minute
	since := h.clock.Now().Add(-window)
	rows, err := h.telemetry.ListByPlayer(ctx, cmd.PlayerID, since)
	if err != nil {
		return 0, 0, false
	}
	median, medianOK := trading.MedianTourRate(rows)
	if !medianOK || median <= 0 {
		return 0, 0, false // fail-closed: a bad/non-positive median never triggers a relocation
	}
	hullRows := make([]trading.TourLegTelemetry, 0, len(rows))
	for _, row := range rows {
		if row.ShipSymbol == cmd.ShipSymbol {
			hullRows = append(hullRows, row)
		}
	}
	hull, hullOK := trading.MedianTourRate(hullRows)
	if !hullOK {
		return 0, 0, false // no computable hull rate → cannot prove under-earning → stay
	}
	return hull, median, true
}

// withinRateFloorDwell reports whether the hull relocated (via rate-floor) within the dwell window.
// In-memory per-hull state on the shared handler singleton, so it holds across a hull's successive
// tours within a daemon lifetime; a restart resets it (acceptable — dwell is a soft anti-thrash
// timer, not a correctness invariant). Guarded by rateFloorMu because the handler is dispatched
// concurrently for every touring hull (the strandedStreak/depositParked discipline).
func (h *RunTourCoordinatorHandler) withinRateFloorDwell(shipSymbol string, window time.Duration) bool {
	h.rateFloorMu.Lock()
	last, seen := h.rateFloorLastRelocation[shipSymbol]
	h.rateFloorMu.Unlock()
	if !seen {
		return false
	}
	return h.clock.Now().Sub(last) < window
}

// noteRateFloorRelocation starts the dwell clock for a hull the instant a rate-floor relocation
// commits, so a later productive tour within the window is dwell-locked.
func (h *RunTourCoordinatorHandler) noteRateFloorRelocation(shipSymbol string) {
	h.rateFloorMu.Lock()
	h.rateFloorLastRelocation[shipSymbol] = h.clock.Now()
	h.rateFloorMu.Unlock()
}

// incrementPendingRelocation registers a rate-floor relocation IN FLIGHT toward system at its
// commit-decision (just before the jump), so a concurrent evaluator's herd check counts it against
// the per-system cap while it is still mid-jump. Paired with a deferred decrementPendingRelocation.
func (h *RunTourCoordinatorHandler) incrementPendingRelocation(system string) {
	h.pendingMu.Lock()
	h.pendingRelocationsBySystem[system]++
	h.pendingMu.Unlock()
}

// decrementPendingRelocation releases the in-flight claim on system once the jump returns (landing
// or error). Never drops below zero, and prunes the key at zero so the snapshot stays tight.
func (h *RunTourCoordinatorHandler) decrementPendingRelocation(system string) {
	h.pendingMu.Lock()
	if h.pendingRelocationsBySystem[system] > 1 {
		h.pendingRelocationsBySystem[system]--
	} else {
		delete(h.pendingRelocationsBySystem, system)
	}
	h.pendingMu.Unlock()
}

// snapshotPendingRelocations returns a copy of the in-flight counts for the herd check to add to the
// landed count. A copy (not the live map) so the caller never holds pendingMu across the cap loop.
func (h *RunTourCoordinatorHandler) snapshotPendingRelocations() map[string]int {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	if len(h.pendingRelocationsBySystem) == 0 {
		return nil
	}
	out := make(map[string]int, len(h.pendingRelocationsBySystem))
	for system, count := range h.pendingRelocationsBySystem {
		out[system] = count
	}
	return out
}

// commitRateFloorRelocation relocates the hull to the chosen ground through the EXACT proven jump
// machinery the margins-death reposition uses, verbatim order (RULINGS #2 persist-before-jump →
// look-back load → bounded stored-adjacency resolver → clear persisted flag → metrics). A travel
// error leaves the persisted destination SET so a restart resumes the jump — identical to the
// margins-death contract. The dwell clock starts only on a successful landing.
func (h *RunTourCoordinatorHandler) commitRateFloorRelocation(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	response *RunTourCoordinatorResponse,
	netBought map[string]int,
	currentSystem string,
	best repositionCandidate,
	hullRate, candidateRate float64,
	maxSpend, reserve int64,
) error {
	logger := common.LoggerFromContext(ctx)
	// Register the in-flight mover at the commit-decision (BEFORE the jump) and release it on landing
	// (defer, after the synchronous RepositionToWaypointWithinJumps returns — success OR error). This
	// is what a concurrent evaluator's herd check reads to respect the cap while this hull is mid-jump
	// (the atomic anti-herd fix). The jump blocks until arrival, so the pending claim is held for the
	// whole flight — exactly the window the landed count cannot yet see.
	h.incrementPendingRelocation(best.system)
	defer h.decrementPendingRelocation(best.system)
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: true, TargetSystem: best.system, TargetWaypoint: best.waypoint})
	jumpBound := resolveRepositionJumpBound(cmd.RepositionJumpBound)
	logger.Log("INFO", fmt.Sprintf("Reposition rate-floor: %s under-earning (realized %.0f/hr) - relocating from %s to %s (%s) within %d stored-adjacency jumps, best candidate projects %.0f/hr, then re-planning there", cmd.ShipSymbol, hullRate, currentSystem, best.system, best.waypoint, jumpBound, candidateRate), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "from_system": currentSystem, "to_system": best.system, "to_waypoint": best.waypoint,
		"hull_rate": hullRate, "candidate_rate": candidateRate, "reposition_jump_bound": jumpBound, "trigger": "rate_floor",
	})

	loadedUnits := h.loadLookbackManifest(ctx, cmd, response, netBought, currentSystem, best.system, maxSpend, reserve)

	if terr := h.legs.RepositionToWaypointWithinJumps(ctx, cmd.ShipSymbol, best.waypoint, cmd.PlayerID, jumpBound); terr != nil {
		metrics.RecordTourReposition(cmd.PlayerID, "failed")
		return fmt.Errorf("rate-floor reposition jump of %s to %s failed: %w", cmd.ShipSymbol, best.waypoint, terr)
	}
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: false})
	metrics.RecordTourJumpLoaded(cmd.PlayerID, loadedUnits > 0)
	h.noteRateFloorRelocation(cmd.ShipSymbol)
	response.Repositions++
	metrics.RecordTourReposition(cmd.PlayerID, "success")
	return nil
}

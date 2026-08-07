package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

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

// reconcileInFlight is THE RESTART CONTRACT, and the reason a restart cannot double-relocate.
//
// A persisted intent is never re-DECIDED — only finished. For each not-yet-completed intent the
// hull's LIVE position decides which:
//
//   - already AT the target system: the move landed, possibly moments before the crash. Mark it
//     completed; DecidedAt stays the cooldown clock, so the hull is not immediately re-scored either.
//   - still IN TRANSIT: the move is under way and needs nothing from this tick. Leave it in flight.
//   - stationary and NOT at the target: the move was interrupted. RESUME it toward the SAME target
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
	live := hullsBySymbol(hulls)
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
		h.finishInterruptedMove(ctx, cmd, intent, live, result)
	}
	return state
}

// finishInterruptedMove completes, waits out, or resumes ONE unfinished intent (see reconcileInFlight).
func (h *RunOpportunityRelocatorHandler) finishInterruptedMove(
	ctx context.Context,
	cmd *RunOpportunityRelocatorCommand,
	intent RelocationIntent,
	live map[string]RelocatorHull,
	result *RelocatorTickResult,
) {
	logger := common.LoggerFromContext(ctx)
	hull := live[intent.ShipSymbol]
	if hull.CurrentSystem == intent.TargetSystem {
		h.markCompleted(ctx, cmd, intent)
		logger.Log("INFO", fmt.Sprintf("Opportunity relocator: %s already landed at %s - completing the persisted intent, not re-deciding it", intent.ShipSymbol, intent.TargetSystem), map[string]interface{}{
			"ship_symbol": intent.ShipSymbol, "target_system": intent.TargetSystem, "reason": "intent_landed",
		})
		return
	}
	// STILL FLYING — leave it alone, and do not ask the ownership gate about it. A hull in transit
	// reads OnTour, so actuationVerdict would call it "mid_tour" and ABANDON the move mid-journey,
	// stranding a multi-leg one at a gate. Nothing moves here, so there is nothing for a gate to guard.
	if hull.InTransit {
		result.skip(skipReasonStillInFlight)
		logger.Log("INFO", fmt.Sprintf("Opportunity relocator: %s is still flying to %s - leaving the intent in flight rather than re-issuing or abandoning it", intent.ShipSymbol, intent.TargetSystem), map[string]interface{}{
			"ship_symbol": intent.ShipSymbol, "target_system": intent.TargetSystem, "reason": skipReasonStillInFlight,
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

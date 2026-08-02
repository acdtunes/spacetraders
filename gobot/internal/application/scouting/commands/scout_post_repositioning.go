package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// reconcileRepositioningSlots runs pass 1.5 over one post's slots: reclaim any ended relay
// and clear its reference so pass 2 re-evaluates the slot. The relay's TERMINAL
// disposition decides what happens to the post's dispatch cooldown:
//   - FAILED (the unroutable verdict): arm the LONG failure cooldown and count the attempt,
//     so the coordinator stops respawning the same corpse every few minutes and the freed
//     probe rotates to the next candidate this tick.
//   - COMPLETED (the probe arrived): reset the streak and clear any stale cooldown — pass 2a
//     mans it in-system, and a post that finally succeeded starts clean.
//   - neither (restart-interrupted / fast opaque exit): keep only the short dispatch floor
//     armed at dispatch — not a routing failure, so it never arms the long cooldown.
func (h *RunScoutPostCoordinatorHandler) reconcileRepositioningSlots(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	states containerStates,
) {
	logger := common.LoggerFromContext(ctx)
	for _, slot := range post.Slots() {
		relayID := slot.RepositionContainerID()
		if slot.AssignedHull() != "" || relayID == "" {
			continue
		}
		if states.running[relayID] {
			continue // relay airborne — leave it; pass 2 skips this slot
		}

		h.reclaimHullFromContainer(ctx, cmd, relayID, "scout_reposition_ended")
		key := backoffKey(cmd.PlayerID.Value(), post.SystemSymbol, slot.Index())
		switch {
		case states.failed[relayID]:
			cooldown := repositionFailureCooldown(cmd)
			attempt := h.noteRepositionFailure(key, cooldown)
			logger.Log("WARNING", fmt.Sprintf("Scout reposition relay for post %s FAILED (attempt %d, container %s) — cooling down %s, freeing the probe to the next candidate", post.SystemSymbol, attempt, relayID, cooldown), map[string]interface{}{
				"action":        "scout_reposition_failed",
				"system_symbol": post.SystemSymbol,
				"attempt":       attempt,
				"relay":         relayID,
				"cooldown_secs": int(cooldown.Seconds()),
			})
		case states.completed[relayID]:
			h.resetRepositionFailures(key)
			logger.Log("INFO", fmt.Sprintf("Scout reposition relay for post %s arrived (container %s) — hull idle in-system, re-manning locally", post.SystemSymbol, relayID), map[string]interface{}{
				"action":        "scout_reposition_arrived",
				"system_symbol": post.SystemSymbol,
				"relay":         relayID,
			})
		default:
			logger.Log("INFO", fmt.Sprintf("Scout reposition relay for post %s ended (container %s not running) — re-evaluating next tick", post.SystemSymbol, relayID), map[string]interface{}{
				"action":        "scout_reposition_relay_ended",
				"system_symbol": post.SystemSymbol,
			})
		}

		slot.SetRepositionContainerID("")
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to clear ended reposition relay on post %s: %v", post.SystemSymbol, err), nil)
		}
	}
}

// repositionUnmannedSlot jump-routes the fleet-wide nearest idle satellite to a slot
// with no in-system hull. It FAILS CLOSED at every gap — no gate graph, no idle
// satellite, an active backoff, an unserviceable/undiscoverable virgin system, or no
// jump-routable satellite — by parking the slot honest and returning. On success it claims
// the satellite to a new reposition container, records the relay on the slot, and arms the
// per-slot dispatch backoff. idleSats is a pointer so a dispatched satellite is removed from
// the shared pool for the rest of this tick.
func (h *RunScoutPostCoordinatorHandler) repositionUnmannedSlot(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	slot domainScouting.ScoutSlotRef,
	idleSats *[]*navigation.Ship,
	sourcePosts []*domainScouting.ScoutPost,
	relayCfg scoutRelayConfig,
) {
	logger := common.LoggerFromContext(ctx)
	key := backoffKey(cmd.PlayerID.Value(), post.SystemSymbol, slot.Index())

	// No gate graph wired, or no idle satellite left this tick → cannot idle-reposition.
	// With a gate graph but NO idle probe, try a CROSS-SYSTEM reuse relay from an
	// over-covered system's surplus BEFORE parking. maybeRelaySurplusProbe returns
	// false immediately when the relay is disabled (flag off or no demand reader
	// wired), so a disabled coordinator parks honest with the in-system reason and a
	// greppable message. A nil gate graph short-circuits before the relay (nothing to
	// route over).
	if h.gateGraph == nil || len(*idleSats) == 0 {
		if h.gateGraph != nil && len(*idleSats) == 0 &&
			h.maybeRelaySurplusProbe(ctx, cmd, post, slot, key, sourcePosts, relayCfg) {
			return
		}
		h.parkNoInSystemSatellite(ctx, post)
		return
	}

	// A recent relay for this slot failed — don't hot-loop re-dispatch. Announce the
	// skip ONCE per cooldown episode: noteRepositionBackoffLogged keys the announcement
	// on the exact backoff deadline, so a new failure (a later deadline) re-announces
	// once and a steady cooldown stays quiet.
	if h.repositionBackedOff(key) {
		if h.noteRepositionBackoffLogged(key) {
			logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: reposition backing off after a recent relay — retrying shortly", post.SystemSymbol), map[string]interface{}{
				"action":        "scout_reposition_backoff",
				"system_symbol": post.SystemSymbol,
			})
		}
		return
	}

	// travel() needs a concrete destination waypoint in the target system; any market
	// serves (the relay just lands the hull there and the next in-system tick's tour rotates
	// to start from wherever it sits). Use the whole system's markets, not the slot's
	// partition, so the destination logic is byte-identical to s232 for a single-hull post.
	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to discover markets for reposition target %s: %v", post.SystemSymbol, err), nil)
		return
	}
	if len(markets) == 0 {
		// Virgin frontier system: discover its waypoints presence-free, then retry.
		markets = h.discoverVirginMarkets(ctx, cmd, post, key)
		if len(markets) == 0 {
			return // parked honest by discoverVirginMarkets
		}
	}
	destWaypoint := pickRepositionDestination(markets)

	// Fleet-wide nearest idle satellite by jump-hop count over the stored adjacency,
	// bounded to the expendable-probe reposition reach. Fail-closed on unroutable.
	maxJumps := resolveMaxRepositionJumps(cmd)
	idx, hops, ok := h.selectNearestSatelliteByHops(ctx, *idleSats, post.SystemSymbol, maxJumps)
	if !ok {
		logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: no satellite within %d reposition jumps of the fleet — parked (fail-closed)", post.SystemSymbol, maxJumps), map[string]interface{}{
			"action":        "scout_reposition_unroutable",
			"system_symbol": post.SystemSymbol,
			"max_jumps":     maxJumps,
		})
		return
	}

	sat := (*idleSats)[idx]
	*idleSats = append((*idleSats)[:idx], (*idleSats)[idx+1:]...)
	shipSymbol := sat.ShipSymbol()

	// A manning reposition never charts the gate (ChartGateOnArrival=false) — it only
	// moves the hull into the post's system; the gate-reconcile sweep owns gate charting.
	relayID, err := h.spawnReposition(ctx, cmd, shipSymbol, destWaypoint, maxJumps, false)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to dispatch reposition of %s to post %s: %v", shipSymbol, post.SystemSymbol, err), nil)
		return
	}

	// Arm the backoff BEFORE persisting the relay reference: if the Upsert below fails,
	// the backoff still prevents an immediate second relay to this slot next tick.
	h.noteRepositionDispatch(key)
	slot.SetRepositionContainerID(relayID)
	if err := h.postRepo.Upsert(ctx, post); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Dispatched reposition for post %s but failed to persist relay reference: %v", post.SystemSymbol, err), nil)
	}

	logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: repositioning %s (%d jump(s) over stored adjacency, ≤%d bound, relay %s) → %s", post.SystemSymbol, shipSymbol, hops, maxJumps, relayID, destWaypoint), map[string]interface{}{
		"action":        "scout_reposition_dispatch",
		"system_symbol": post.SystemSymbol,
		"ship_symbol":   shipSymbol,
		"jumps":         hops,
		"max_jumps":     maxJumps,
		"destination":   destWaypoint,
		"relay":         relayID,
	})
}

// discoverVirginMarkets resolves the bootstrap chicken-and-egg for a post whose system
// has ZERO known market waypoints: it DISCOVERS the system's waypoints presence-free
// via the graph provider's cache-first GetGraph, persisting them era-scoped, then
// re-reads. markets found -> returns them (the caller repositions this tick); none ->
// parks UNSERVICEABLE (charted but barren); API error -> parks fail-closed. It arms the
// per-slot dispatch backoff (key) BEFORE the API call, so a marketless or API-erroring
// system is probed at most ONCE per window. With no graph provider wired it logs the
// plain "no markets, cannot reposition" park.
func (h *RunScoutPostCoordinatorHandler) discoverVirginMarkets(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	key string,
) []string {
	logger := common.LoggerFromContext(ctx)

	if h.graphProvider == nil {
		logger.Log("INFO", fmt.Sprintf("No known marketplace waypoints in %s yet — cannot reposition (nothing to scan), post parks", post.SystemSymbol), map[string]interface{}{
			"action":        "scout_reposition_no_markets",
			"system_symbol": post.SystemSymbol,
		})
		return nil
	}

	// Arm the backoff BEFORE the API call: whether discovery finds markets, finds none, or
	// errors, this system is not probed again until the window elapses.
	h.noteRepositionDispatch(key)

	if _, err := h.graphProvider.GetGraph(ctx, post.SystemSymbol, false, cmd.PlayerID.Value()); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Virgin-system waypoint discovery for reposition target %s failed: %v — post parks (fail-closed), retrying after backoff", post.SystemSymbol, err), map[string]interface{}{
			"action":        "scout_reposition_discovery_failed",
			"system_symbol": post.SystemSymbol,
		})
		return nil
	}

	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to re-read markets for %s after discovery: %v", post.SystemSymbol, err), nil)
		return nil
	}
	if len(markets) == 0 {
		logger.Log("INFO", fmt.Sprintf("%s has no marketplaces — post is unserviceable; consider removing", post.SystemSymbol), map[string]interface{}{
			"action":        "scout_post_unserviceable",
			"system_symbol": post.SystemSymbol,
		})
		return nil
	}

	logger.Log("INFO", fmt.Sprintf("Discovered %d market waypoint(s) in virgin system %s — repositioning now", len(markets), post.SystemSymbol), map[string]interface{}{
		"action":        "scout_reposition_virgin_discovered",
		"system_symbol": post.SystemSymbol,
		"markets":       len(markets),
	})
	return markets
}

// selectNearestSatelliteByHops returns the index (into idleSats) of the satellite
// FEWEST jump hops from postSystem, its hop count, and ok=false when none can be
// jump-routed there. Distance is the RepositionPath BFS length over the PERSISTED
// stored adjacency bounded to maxJumps — the expendable-probe resolver that routes
// PAST unreadable frontier gates and reaches the multi-jump posts the strict
// MaxJumpPath=5 rejects. A satellite whose route errors is skipped (fail-closed). idleSats
// is pre-sorted by symbol, and the comparison is strict (< bestHops), so the lowest-symbol
// satellite wins an equal-hops tie.
func (h *RunScoutPostCoordinatorHandler) selectNearestSatelliteByHops(
	ctx context.Context,
	idleSats []*navigation.Ship,
	postSystem string,
	maxJumps int,
) (idx int, hops int, ok bool) {
	logger := common.LoggerFromContext(ctx)
	bestIdx, bestHops := -1, 0
	for i, sat := range idleSats {
		loc := sat.CurrentLocation()
		if loc == nil {
			continue // unknown location — cannot route
		}
		path, err := h.gateGraph.RepositionPath(ctx, loc.SystemSymbol, postSystem, maxJumps)
		if err != nil {
			logger.Log("INFO", fmt.Sprintf("Reposition candidate %s → %s unroutable this tick (stored-adjacency, ≤%d jumps): %v", loc.SystemSymbol, postSystem, maxJumps, err), map[string]interface{}{
				"action":    "scout_reposition_candidate_unroutable",
				"from":      loc.SystemSymbol,
				"to":        postSystem,
				"max_jumps": maxJumps,
			})
			continue
		}
		candidateHops := len(path) - 1
		if bestIdx == -1 || candidateHops < bestHops {
			bestIdx, bestHops = i, candidateHops
		}
	}
	if bestIdx == -1 {
		return -1, 0, false
	}
	return bestIdx, bestHops, true
}

// spawnReposition persists a coordinator-managed scout_reposition worker for
// hullSymbol, atomically claims the hull to it (operation scoutPostFleet — the same
// poach guard the tour claim uses, RULINGS #7), and starts it. Mirrors spawnTour
// exactly (persist → claim → start, with rollback on each failure) so the reposition
// worker inherits the same restart-recovery semantics.
func (h *RunScoutPostCoordinatorHandler) spawnReposition(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	hullSymbol string,
	destinationWaypoint string,
	maxJumps int,
	chartGateOnArrival bool,
) (string, error) {
	workerID := utils.GenerateContainerID("scout_reposition", hullSymbol)
	repoCmd := &ScoutRepositionCommand{
		PlayerID:            cmd.PlayerID,
		ShipSymbol:          hullSymbol,
		DestinationWaypoint: destinationWaypoint,
		CoordinatorID:       cmd.ContainerID,
		MaxRepositionJumps:  maxJumps,
		// A gate-reconcile 0-hop dispatch charts the target gate on arrival; a plain
		// manning reposition (false) never detours to the gate.
		ChartGateOnArrival: chartGateOnArrival,
	}

	if err := h.daemonClient.PersistContainer(ctx, daemon.ContainerKindScoutReposition, workerID, uint(cmd.PlayerID.Value()), repoCmd); err != nil {
		return "", fmt.Errorf("failed to persist scout reposition worker: %w", err)
	}

	if err := h.shipRepo.ClaimShip(ctx, hullSymbol, workerID, cmd.PlayerID, scoutPostFleet); err != nil {
		_ = h.daemonClient.StopContainer(ctx, workerID)
		return "", fmt.Errorf("failed to claim satellite %s for reposition: %w", hullSymbol, err)
	}

	if err := h.daemonClient.StartContainer(ctx, daemon.ContainerKindScoutReposition, workerID); err != nil {
		h.releaseHull(ctx, cmd, hullSymbol, "scout_reposition_start_failed")
		_ = h.daemonClient.StopContainer(ctx, workerID)
		return "", fmt.Errorf("failed to start scout reposition worker: %w", err)
	}

	return workerID, nil
}

// parkNoInSystemSatellite logs the honest, system-scoped park reason for an unmanned
// slot that has no in-system satellite and cannot be repositioned (no gate graph, or no
// idle satellite left this tick). The message text is stable so `container logs` greps
// and park assertions keep matching.
func (h *RunScoutPostCoordinatorHandler) parkNoInSystemSatellite(ctx context.Context, post *domainScouting.ScoutPost) {
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Scout post %s unmanned: no in-system satellite — reposition one or wait", post.SystemSymbol), map[string]interface{}{
		"action":        "scout_post_unmanned_no_in_system_satellite",
		"system_symbol": post.SystemSymbol,
	})
}

// pickRepositionDestination chooses the reposition target waypoint from a post's
// discovered markets — the lexicographically smallest, so the destination (and thus the
// dispatch log and the tests) is deterministic. Any market in the system serves. The
// caller has already ensured markets is non-empty.
func pickRepositionDestination(markets []string) string {
	best := markets[0]
	for _, m := range markets[1:] {
		if m < best {
			best = m
		}
	}
	return best
}

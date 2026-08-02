package commands

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// ensurePartitions materializes a multi-probe post's N DISJOINT market partitions.
// It is a no-op for a single-hull post (HullBudget 1 -> no partition, the primary
// tours all markets) and for a multi-probe post ALREADY partitioned at its current
// budget AND not yet drifted enough to re-cut — so it re-partitions on a hull-budget
// change (unconditional) or on a DEBOUNCED market-set drift (virgin discovery adds
// markets to a system after a post's tours are already cut, and a market discovered
// post-cut belongs to NO partition — it goes permanently stale even though the post
// reads fully manned; removals fold into the same check), never on every tick. On any
// re-cut of a running post it stops the existing tours/relays (their markets change),
// reclaims their hulls, and rebuilds the slots with fresh partitions; pass 2 re-mans
// them. Fails closed: no routing client, no markets, or a VRP error leaves an
// UNPARTITIONED post un-partitioned (it parks) and retries next tick — symmetrically,
// an ALREADY-stable-and-partitioned post hitting one of those same conditions just
// keeps touring its existing (possibly stale) partition rather than being torn down
// over a transient discovery hiccup or a missing routing client.
//
// API-BUDGET INVARIANT: partitioning changes WHERE probes scan, not HOW MUCH. Total
// scans/hour ~= markets / freshness-target regardless of N — N smaller partitions
// each paced to the freshness target (circuitPaceInterval, scout_tour.go) sum to one
// scan per market per freshness window. More probes buy fresher data, NOT more API calls.
func (h *RunScoutPostCoordinatorHandler) ensurePartitions(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost) {
	logger := common.LoggerFromContext(ctx)

	key := driftKey(cmd.PlayerID.Value(), post.SystemSymbol)
	budget := h.debouncedHullBudget(ctx, cmd, post, key)

	if budget <= 1 {
		h.revertToSingleHull(ctx, cmd, post, budget, key)
		return
	}

	// stableAtBudget: already partitioned at this budget. A budget CHANGE always
	// re-cuts unconditionally (below); a stable budget only re-cuts if the market SET
	// has drifted enough to debounce-trigger — checked once the current market list is known.
	stableAtBudget := len(post.ExtraSlots) == budget-1

	if h.routingClient == nil {
		if stableAtBudget {
			return // can't check for drift without a routing client — the existing partition stands.
		}
		logger.Log("WARNING", fmt.Sprintf("Scout post %s wants %d hulls but no routing client is wired — cannot partition; parking", post.SystemSymbol, budget), map[string]interface{}{
			"action":        "scout_post_partition_unavailable",
			"system_symbol": post.SystemSymbol,
		})
		return
	}

	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to discover markets to partition post %s: %v", post.SystemSymbol, err), nil)
		return
	}
	if len(markets) == 0 {
		if stableAtBudget {
			return // an already-partitioned post keeps touring its existing partition.
		}
		logger.Log("INFO", fmt.Sprintf("No known marketplace waypoints in %s yet — cannot partition, post parks", post.SystemSymbol), map[string]interface{}{
			"action":        "scout_post_partition_no_markets",
			"system_symbol": post.SystemSymbol,
		})
		return
	}

	driftTrigger := ""
	if stableAtBudget {
		trigger, recut := h.marketDriftRecut(ctx, cmd, post, key, markets)
		if !recut {
			return
		}
		driftTrigger = trigger
	}

	partitions, err := h.partitionMarkets(ctx, cmd, post.SystemSymbol, markets, budget)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("VRP partition of post %s into %d tours failed: %v — parking, retry next tick", post.SystemSymbol, budget, err), map[string]interface{}{
			"action":        "scout_post_partition_failed",
			"system_symbol": post.SystemSymbol,
		})
		return
	}

	// Re-partition: stop any existing tours/relays (their markets change) and reclaim their
	// hulls before overwriting the slots. On first partition (no slots yet) this is a no-op.
	repartition := len(post.ExtraSlots) > 0 || post.AssignedHull != "" || post.RepositionContainerID != ""
	h.tearDownSlots(ctx, cmd, post)

	post.PrimaryPartition = partitions[0]
	post.ExtraSlots = make([]domainScouting.ScoutPostSlot, budget-1)
	for i := 1; i < budget; i++ {
		post.ExtraSlots[i-1] = domainScouting.ScoutPostSlot{Partition: partitions[i]}
	}
	if err := h.postRepo.Upsert(ctx, post); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Partitioned post %s but failed to persist: %v", post.SystemSymbol, err), nil)
		return
	}
	h.clearDriftPending(key)

	action, verb := partitionLogVerb(driftTrigger, repartition)
	logger.Log("INFO", fmt.Sprintf("%s scout post %s into %d disjoint tours over %d markets", verb, post.SystemSymbol, budget, len(markets)), map[string]interface{}{
		"action":        action,
		"system_symbol": post.SystemSymbol,
		"hulls":         budget,
		"markets":       len(markets),
	})
}

func (h *RunScoutPostCoordinatorHandler) debouncedHullBudget(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost, key string) int {
	// The freshness sizer's per-post budget can oscillate ±1 on normal demand-noise;
	// re-partitioning on every swing stops the post's tours and RE-SCANS its markets — the
	// API burn. Absorb a transient swing: on an already-MATERIALIZED post (one with live
	// slots/tours a re-cut would disrupt) act on a budget that differs from the currently-cut
	// partition only once the SAME new budget has PERSISTED the debounce window; until then
	// hold the current cut, so the downstream logic keeps touring the existing partition and
	// still honors an independent market-set drift. A FIRST partition (a fresh,
	// un-materialized post) is never debounced — its budget lands immediately. A genuine
	// PERSISTENT change re-partitions once the window closes; a swing that reverts clears the
	// pending count and never re-cuts.
	budget := post.HullBudget()
	physicalBudget := len(post.ExtraSlots) + 1
	materialized := len(post.ExtraSlots) > 0 || post.AssignedHull != "" || post.TourContainerID != "" || post.RepositionContainerID != ""
	switch {
	case budget == physicalBudget:
		h.clearBudgetChangePending(key) // stable, or a swing reverted — forget any pending change.
	case materialized:
		debounceCycles := cmd.BudgetChangeDebounceCycles
		if debounceCycles <= 0 {
			debounceCycles = defaultBudgetChangeDebounceCycles
		}
		if cycles := h.noteBudgetChangePending(key, budget); cycles < debounceCycles {
			common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Scout post %s hull budget %d→%d — below re-partition debounce (%d/%d cycles), holding current partition", post.SystemSymbol, physicalBudget, budget, cycles, debounceCycles), map[string]interface{}{
				"action":         "scout_post_budget_change_pending",
				"system_symbol":  post.SystemSymbol,
				"current_budget": physicalBudget,
				"pending_budget": budget,
				"pending_cycles": cycles,
			})
			return physicalBudget // hold this tick: keep touring the existing cut.
		}
		h.clearBudgetChangePending(key) // persisted — act on the change and reset the debounce.
	}
	return budget
}

func (h *RunScoutPostCoordinatorHandler) revertToSingleHull(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost, budget int, key string) {
	// A genuine single-hull post carries no partition state and this is a no-op. But a post
	// REDUCED from multi-probe (hulls lowered to 1, or converted to sweep-once) still carries
	// stale extra slots / partition — tear them down so it reverts to the single-slot shape,
	// freeing the surplus probes to the pool. Pass 2 then re-mans the primary over ALL markets.
	h.clearDriftPending(key) // no longer partitioned — any pending drift episode is moot.
	if len(post.ExtraSlots) == 0 && len(post.PrimaryPartition) == 0 {
		return
	}
	logger := common.LoggerFromContext(ctx)
	h.tearDownSlots(ctx, cmd, post)
	if err := h.postRepo.Upsert(ctx, post); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to revert post %s to single-hull: %v", post.SystemSymbol, err), nil)
		return
	}
	logger.Log("INFO", fmt.Sprintf("Scout post %s hull budget reduced to %d — reverted to single-hull, surplus probes freed", post.SystemSymbol, budget), map[string]interface{}{
		"action":        "scout_post_reverted_single_hull",
		"system_symbol": post.SystemSymbol,
	})
}

// marketDriftRecut reports whether an already-partitioned post's market set has drifted far
// enough to justify re-cutting it, and which trigger fired.
func (h *RunScoutPostCoordinatorHandler) marketDriftRecut(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost, key string, markets []string) (string, bool) {
	logger := common.LoggerFromContext(ctx)
	drifted, oldMarketCount := marketSetDrift(post, markets)
	if len(drifted) == 0 {
		h.clearDriftPending(key)
		return "", false // stable: same hulls, same markets — no re-cut.
	}

	threshold := cmd.MarketDriftThreshold
	if threshold <= 0 {
		threshold = defaultMarketDriftThreshold
	}
	maxAge := resolveMarketDriftMaxAge(cmd, h.liveConfigSnapshot(ctx, cmd))
	age := h.noteDriftPending(key)

	driftTrigger := ""
	switch {
	case len(drifted) >= threshold:
		driftTrigger = "threshold"
	case age >= maxAge:
		driftTrigger = "age"
	default:
		// Below both triggers — debounce. Keep touring the existing (now slightly
		// stale) partition a while longer rather than thrash the fleet on every
		// single new/removed market.
		logger.Log("INFO", fmt.Sprintf("Scout post %s market set drifted (%d markets) — below re-cut threshold, waiting", post.SystemSymbol, len(drifted)), map[string]interface{}{
			"action":         "scout_post_market_drift_pending",
			"system_symbol":  post.SystemSymbol,
			"drifted":        len(drifted),
			"drift_age_secs": int(age.Seconds()),
		})
		return "", false
	}

	logger.Log("INFO", fmt.Sprintf("Scout post %s market set drifted (%d markets, trigger=%s) — re-cutting partitions", post.SystemSymbol, len(drifted), driftTrigger), map[string]interface{}{
		"action":           "scout_post_market_drift_detected",
		"system_symbol":    post.SystemSymbol,
		"trigger":          driftTrigger,
		"drifted_markets":  len(drifted),
		"old_market_count": oldMarketCount,
		"new_market_count": len(markets),
		"drift_age_secs":   int(age.Seconds()),
	})
	return driftTrigger, true
}

func partitionLogVerb(driftTrigger string, repartition bool) (action, verb string) {
	switch {
	case driftTrigger != "":
		return "scout_post_repartitioned", fmt.Sprintf("Re-cut (market-set drift, trigger=%s)", driftTrigger)
	case repartition:
		return "scout_post_repartitioned", "Re-partitioned (hull budget changed)"
	}
	return "scout_post_partitioned", "Partitioned"
}

// ensureSingleHullFreshness is the single-hull mirror of ensurePartitions' market-set
// drift re-cut: a single-hull standing post's tour is spawned once with the system's
// market list AT THAT MOMENT (spawnTour<-slotMarkets<-discoverMarkets) and never
// re-reads it afterward — executeMultiMarketTour only re-derives markets at a respawn,
// and executeStationaryScout (chosen when the system had exactly one known market at
// spawn) has no circuit-boundary hook at all. A market discovered after spawn is
// therefore never toured until the post re-mans for an unrelated reason. This closes
// that gap the same way as the partitioned case: tear the post down (which pass 2
// immediately re-mans, and slotMarkets/discoverMarkets gives the new tour a FRESH
// market list) once the live discovered set has drifted from the snapshot taken at
// last manning by at least MarketDriftThreshold markets, or the drift has been pending
// at least MarketDriftMaxAgeSecs — reusing the same thresholds/config fields and
// diffMarketSets' set-diff semantics as the partitioned path rather than inventing new ones.
//
// Scoped to standing, single-hull, CURRENTLY MANNED posts only:
//   - multi-hull posts are ensurePartitions' job (skip: HullBudget() > 1);
//   - sweep-once posts are a one-shot frontier sweep that auto-retires on completion,
//     not a freshness target (skip: Kind != PostKindStanding);
//   - an unmanned/repositioning post has no live tour to go stale — pass 2a gives it a
//     fresh market list the moment it mans it (skip: AssignedHull/TourContainerID
//     empty).
func (h *RunScoutPostCoordinatorHandler) ensureSingleHullFreshness(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost) {
	if post.HullBudget() > 1 || post.Kind != domainScouting.PostKindStanding {
		return
	}
	if post.AssignedHull == "" || post.TourContainerID == "" {
		return
	}

	logger := common.LoggerFromContext(ctx)
	key := driftKey(cmd.PlayerID.Value(), post.SystemSymbol)

	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to discover markets to check freshness of post %s: %v", post.SystemSymbol, err), nil)
		return
	}
	if len(markets) == 0 {
		return // transient discovery hiccup — leave the tour running, don't touch state.
	}

	snapshot, ok := h.singleHullSnapshot(key)
	if !ok {
		// No baseline yet — a fresh handler (daemon restart lost the in-memory map).
		// Adopt the CURRENT markets as the new baseline without respawning: an
		// already-healthy tour surviving a restart is not maximal drift, and treating
		// it as such would respawn every standing post fleet-wide on every restart
		// (mirrors driftPendingSince's own restart-safety rationale).
		h.setSingleHullSnapshot(key, markets)
		return
	}

	drifted, oldMarketCount := diffMarketSets(snapshot, markets)
	if len(drifted) == 0 {
		h.clearSingleHullDriftPending(key)
		return // stable: same markets — no respawn.
	}

	threshold := cmd.MarketDriftThreshold
	if threshold <= 0 {
		threshold = defaultMarketDriftThreshold
	}
	maxAge := resolveMarketDriftMaxAge(cmd, h.liveConfigSnapshot(ctx, cmd))
	age := h.noteSingleHullDriftPending(key)

	driftTrigger := ""
	switch {
	case len(drifted) >= threshold:
		driftTrigger = "threshold"
	case age >= maxAge:
		driftTrigger = "age"
	default:
		// Below both triggers — debounce, exactly like ensurePartitions: keep touring
		// the existing (now slightly stale) market list a while longer rather than
		// thrash the fleet on every single new/removed market.
		logger.Log("INFO", fmt.Sprintf("Scout post %s market set drifted (%d markets) — below re-cut threshold, waiting", post.SystemSymbol, len(drifted)), map[string]interface{}{
			"action":         "scout_post_single_hull_drift_pending",
			"system_symbol":  post.SystemSymbol,
			"drifted":        len(drifted),
			"drift_age_secs": int(age.Seconds()),
		})
		return
	}

	logger.Log("INFO", fmt.Sprintf("Scout post %s market set drifted (%d markets, trigger=%s) — respawning single-hull tour", post.SystemSymbol, len(drifted), driftTrigger), map[string]interface{}{
		"action":           "scout_post_single_hull_market_drift_detected",
		"system_symbol":    post.SystemSymbol,
		"trigger":          driftTrigger,
		"drifted_markets":  len(drifted),
		"old_market_count": oldMarketCount,
		"new_market_count": len(markets),
		"drift_age_secs":   int(age.Seconds()),
	})

	h.tearDownSlots(ctx, cmd, post)
	if err := h.postRepo.Upsert(ctx, post); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to persist single-hull freshness teardown for post %s: %v", post.SystemSymbol, err), nil)
	}
	h.clearSingleHullDriftPending(key)
	h.clearSingleHullSnapshot(key)
}

// marketSetDrift returns the symbols where a partitioned post's CURRENT discovered
// market set differs from the union of its persisted partitions, plus the size of
// that union (the "old market count" for the re-cut's observability log). A market
// discovered after the post was last cut belongs to no partition (an addition); a
// market still assigned to a partition but no longer discovered (a removal) is
// included identically — both fold into ONE set so additions and removals debounce
// the same way, with no special-casing. Sorted for a deterministic re-cut log and
// test assertions.
func marketSetDrift(post *domainScouting.ScoutPost, currentMarkets []string) (drifted []string, unionSize int) {
	union := make([]string, 0, len(post.PrimaryPartition))
	union = append(union, post.PrimaryPartition...)
	for _, slot := range post.ExtraSlots {
		union = append(union, slot.Partition...)
	}
	return diffMarketSets(union, currentMarkets)
}

// diffMarketSets is marketSetDrift's set-diff core, factored out so a SINGLE-hull
// post's freshness check (ensureSingleHullFreshness) can reuse the identical
// symmetric-difference semantics against its own snapshot baseline instead of a
// partitioned post's PrimaryPartition/ExtraSlots union — a single-hull post never
// carries a partition (see ScoutPost.PrimaryPartition's doc comment), so it has no
// union to read here. oldMarkets is the baseline (a partition union, or a prior
// discovered-markets snapshot); currentMarkets is the live discovered set. Returns the
// symbols present in one set but not the other — additions AND removals, no
// special-casing — sorted for a deterministic log/test assertions, plus the
// deduplicated size of oldMarkets for the caller's "old market count" observability
// log.
func diffMarketSets(oldMarkets, currentMarkets []string) (drifted []string, unionSize int) {
	old := make(map[string]bool, len(oldMarkets))
	for _, m := range oldMarkets {
		old[m] = true
	}
	current := make(map[string]bool, len(currentMarkets))
	for _, m := range currentMarkets {
		current[m] = true
	}

	for _, m := range currentMarkets {
		if !old[m] {
			drifted = append(drifted, m) // discovered, but not in the baseline yet
		}
	}
	for m := range old {
		if !current[m] {
			drifted = append(drifted, m) // in the baseline, but no longer discovered
		}
	}
	sort.Strings(drifted)
	return drifted, len(old)
}

// tearDownSlots stops every slot's tour/relay container and reclaims its hull ahead of
// a re-partition, then clears the assignments in memory. Best-effort: a hull the
// coordinator fails to reclaim here is reclaimed by pass 1 on a later tick.
func (h *RunScoutPostCoordinatorHandler) tearDownSlots(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost) {
	for _, slot := range post.Slots() {
		if tourID := slot.TourContainerID(); tourID != "" {
			_ = h.daemonClient.StopContainer(ctx, tourID)
			h.reclaimHullFromContainer(ctx, cmd, tourID, "scout_post_repartition")
		}
		if relayID := slot.RepositionContainerID(); relayID != "" {
			_ = h.daemonClient.StopContainer(ctx, relayID)
			h.reclaimHullFromContainer(ctx, cmd, relayID, "scout_post_repartition")
		}
	}
	post.AssignedHull = ""
	post.TourContainerID = ""
	post.RepositionContainerID = ""
	post.PrimaryPartition = nil
	post.ExtraSlots = nil
	// A market-set re-cut is a genuine state change — the post's tour will fly a
	// different market list, so its consecutive-respawn streak (which measured failures
	// under the old state) no longer applies. Clear it and lift any respawn park so the
	// re-cut post gets a fresh chance.
	post.RespawnAttempts = 0
	post.RespawnParkedUntil = time.Time{}
}

// partitionMarkets solves the VRP that splits markets into n DISJOINT per-probe tours,
// reusing the SAME PartitionFleet the scout-markets verb uses. The N probes are
// synthetic slots anchored at a common waypoint (the lexicographically-smallest market),
// so the partition depends only on (n, markets, geometry) and is STABLE across which real
// probes are present; the caller freezes and persists the result. It guarantees complete,
// disjoint coverage: any market the VRP fails to place (e.g. the fallback mock's 1-per-ship
// stub) is appended to slot 0, so no market is silently dropped.
func (h *RunScoutPostCoordinatorHandler) partitionMarkets(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, systemSymbol string, markets []string, n int) ([][]string, error) {
	if h.routingClient == nil {
		return nil, fmt.Errorf("no routing client wired")
	}

	anchor := markets[0]
	for _, m := range markets[1:] {
		if m < anchor {
			anchor = m
		}
	}

	slotIDs := make([]string, n)
	configs := make(map[string]*routing.ShipConfigData, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-slot-%d", systemSymbol, i)
		slotIDs[i] = id
		configs[id] = &routing.ShipConfigData{
			CurrentLocation: anchor,
			FuelCapacity:    partitionAnchorFuelCapacity,
			EngineSpeed:     partitionAnchorEngineSpeed,
		}
	}

	var waypointData []*system.WaypointData
	if h.graphProvider != nil {
		if graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, cmd.PlayerID.Value()); err == nil {
			waypointData, _ = extractWaypointData(graphResult.Graph)
		}
	}

	resp, err := h.routingClient.PartitionFleet(ctx, &routing.VRPRequest{
		SystemSymbol:    systemSymbol,
		ShipSymbols:     slotIDs,
		MarketWaypoints: markets,
		ShipConfigs:     configs,
		AllWaypoints:    waypointData,
	})
	if err != nil {
		return nil, err
	}

	partitions := make([][]string, n)
	assigned := make(map[string]bool, len(markets))
	for i, id := range slotIDs {
		if tour, ok := resp.Assignments[id]; ok {
			for _, wp := range tour.Waypoints {
				if assigned[wp] {
					continue // keep partitions strictly disjoint
				}
				assigned[wp] = true
				partitions[i] = append(partitions[i], wp)
			}
		}
	}
	// Complete coverage: any market the VRP left unplaced goes to slot 0, so a partition
	// never silently drops a market (defense against a degraded/stub partitioner).
	for _, m := range markets {
		if !assigned[m] {
			partitions[0] = append(partitions[0], m)
			assigned[m] = true
		}
	}
	return partitions, nil
}

// slotMarkets returns the waypoints a slot's tour should scan: ALL the system's
// markets for a single-hull post (discovered fresh), or the slot's frozen partition
// for a multi-probe post.
func (h *RunScoutPostCoordinatorHandler) slotMarkets(ctx context.Context, post *domainScouting.ScoutPost, slot domainScouting.ScoutSlotRef) ([]string, error) {
	if post.HullBudget() <= 1 {
		return h.discoverMarkets(ctx, post.SystemSymbol)
	}
	return slot.Partition(), nil
}

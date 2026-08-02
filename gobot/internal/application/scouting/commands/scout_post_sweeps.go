package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipqueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// releaseReasonScoutOrphanSwept marks a scout hull freed by the coordinator's orphan
// sweep: an active probe whose owning container is orphaned and which no post slot
// manages, returned to the idle pool for in-system re-seat. Distinct from
// stale_claim_reconciled (refresh-time) so the audit trail names WHICH reconciler
// freed the hull.
const releaseReasonScoutOrphanSwept = "scout_orphan_swept"

// releaseReasonScoutZombieSwept marks a hull reclaimed from a RUNNING
// coordinator-spawned worker no post slot references — a removed post's tour or
// relay, stopped by the zombie sweep.
const releaseReasonScoutZombieSwept = "scout_zombie_swept"

// postReferencedContainers collects every container ID some post slot owns — its
// tour or its in-flight reposition relay. Both Pass 0 sweeps treat these as slot
// territory: Pass 1 / Pass 1.5 reclaim them against their post, so a sweep must
// never touch them.
func postReferencedContainers(posts []*domainScouting.ScoutPost) map[string]bool {
	referenced := make(map[string]bool)
	for _, post := range posts {
		for _, slot := range post.Slots() {
			if t := slot.TourContainerID(); t != "" {
				referenced[t] = true
			}
			if r := slot.RepositionContainerID(); r != "" {
				referenced[r] = true
			}
		}
	}
	return referenced
}

// sweepZombieScoutWorkers stops every RUNNING coordinator-spawned scout worker —
// tour or reposition relay, discriminated by a NON-EMPTY coordinator_id in its
// persisted config — that no post slot references, and reclaims its hull. ANY
// non-empty id qualifies (a prior coordinator instance's tours count); an empty
// id is a manually-launched tour and is never the reconciler's to stop. Pure
// best-effort: a list error skips the sweep this tick, never aborting the pass —
// and never consuming a worker list returned alongside an error.
func (h *RunScoutPostCoordinatorHandler) sweepZombieScoutWorkers(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, posts []*domainScouting.ScoutPost) {
	logger := common.LoggerFromContext(ctx)

	workers, err := h.containerQuery.ListRunningScoutWorkers(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Scout zombie sweep skipped: failed to list running scout workers: %v", err), nil)
		return
	}
	if len(workers) == 0 {
		return
	}

	referenced := postReferencedContainers(posts)
	for _, worker := range workers {
		if worker.CoordinatorID == "" {
			continue // manual tour — an operator's hull, never swept
		}
		if referenced[worker.ID] {
			continue // a post slot manages this worker — Pass 1 / Pass 1.5 territory
		}
		_ = h.daemonClient.StopContainer(ctx, worker.ID)
		h.reclaimHullFromContainer(ctx, cmd, worker.ID, releaseReasonScoutZombieSwept)
		logger.Log("INFO", fmt.Sprintf("Scout zombie swept: stopped running worker %s (no post references it) — hull reclaimed to the idle pool", worker.ID), map[string]interface{}{
			"action":       "scout_zombie_swept",
			"container_id": worker.ID,
			"coordinator":  worker.CoordinatorID,
		})
	}
}

// sweepOrphanedScoutHulls frees scout hulls stranded active on an orphaned container
// that NO post slot references — see the Pass 0 comment in reconcileOnce. It reuses
// refresh_ship's IsClaimOrphaned verdict so the sweep and refresh-time reconciliation
// can never disagree on which claims are safe to reap, and it is pure best-effort: any
// read error is logged and skipped, never aborting the reconcile pass.
func (h *RunScoutPostCoordinatorHandler) sweepOrphanedScoutHulls(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, posts []*domainScouting.ScoutPost) {
	logger := common.LoggerFromContext(ctx)

	// A hull claimed through a slot-owned container is Pass 1 / Pass 1.5 territory
	// (they reclaim it against the post), so the sweep skips it regardless of that
	// container's state: the sweep touches ONLY fleet orphans whose post is gone,
	// and is a strict no-op for every post-referenced hull.
	postContainers := postReferencedContainers(posts)

	actives, err := h.shipRepo.FindActiveByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Scout orphan sweep skipped: failed to list active hulls: %v", err), nil)
		return
	}

	for _, ship := range actives {
		// Only expendable scout hulls; only real container claims. A captain reservation
		// is an active assignment with NO container — nothing to go stale — so it must
		// be excluded before the container lookup, exactly as refresh_ship's reconciler
		// does, or it would be reaped as "container gone".
		if !ship.IsScoutType() || !ship.IsAssigned() || ship.IsReservedByCaptain() {
			continue
		}
		containerID := ship.ContainerID()
		if postContainers[containerID] {
			continue // a post slot manages this claim — not the sweep's job
		}

		status, found, err := h.containerQuery.ContainerStatus(ctx, containerID, cmd.PlayerID)
		if err != nil {
			// Can't determine the owner's state — keep the claim (no positive orphan
			// evidence), mirroring refresh_ship's conservative stance. Next tick retries.
			continue
		}
		if !shipqueries.IsClaimOrphaned(status, found) {
			continue // RUNNING / INTERRUPTED / STOPPING — live or recoverable, never reap
		}

		shipSymbol := ship.ShipSymbol()
		// Release under CAS-retry: re-apply ForceRelease on the FRESH row so a
		// concurrent writer's cargo/nav update on the same hull survives instead of
		// being last-write-wins clobbered by the FindActiveByPlayer snapshot. Skip
		// unless the hull is still on THIS orphaned container (a concurrent release or
		// re-claim -> changed=false), so a hull that moved on is never swept out from
		// under its new owner (RULINGS #7).
		_, changed, saveErr := h.shipRepo.SaveWithRetry(ctx, shipSymbol, cmd.PlayerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != containerID {
					return false, nil
				}
				sh.ForceRelease(releaseReasonScoutOrphanSwept, h.clock)
				return true, nil
			})
		if saveErr != nil {
			logger.Log("WARNING", fmt.Sprintf("Scout orphan sweep freed %s but failed to persist the release: %v", shipSymbol, saveErr), nil)
			continue
		}
		if !changed {
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Scout orphan swept: %s freed from orphaned container %s — returning to the idle pool for in-system re-seat", shipSymbol, containerID), map[string]interface{}{
			"action":             "scout_orphan_swept",
			"ship_symbol":        shipSymbol,
			"orphaned_container": containerID,
			"container_status":   status,
		})
	}
}

// warnUndersizedPosts emits a DEFERRED scout.post_undersized event for any STANDING
// post whose deterministic circuit math (markets / hulls x avgHop) cannot keep its
// markets within the post's own freshness target (layer 1). The event names the
// required hull count, so the fix (raise the budget) is spelled out.
//
// Scope: STANDING posts only (a sweep-once is a one-shot frontier pass with no standing
// freshness contract) with a positive freshness target and readable markets. It is pure
// observation: no post is mutated, a discovery/store error is swallowed (never aborts a
// reconcile), and with no event store wired (tests, pre-wiring) it is a no-op.
//
// Debounce (per post per condition-onset, not per 30s tick): a HasSince cooldown on any
// recent undersized event for the same system, processed or not — the same idiom the
// watchkeeper detectors use so a deferred event does not re-queue every tick while the
// post stays undersized.
func (h *RunScoutPostCoordinatorHandler) warnUndersizedPosts(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, posts []*domainScouting.ScoutPost) {
	if h.eventStore == nil {
		return // events not wired — warning disabled (pre-k7q5 behavior).
	}
	logger := common.LoggerFromContext(ctx)

	avgHop := scoutAvgHop(cmd)
	cooldown := time.Duration(cmd.UndersizedRewarnCooldownSecs) * time.Second
	if cooldown <= 0 {
		cooldown = defaultUndersizedRewarnCooldown
	}
	now := h.clock.Now()

	for _, post := range posts {
		if post.Kind != domainScouting.PostKindStanding {
			continue // sweep-once has no standing freshness contract to fail
		}
		freshness := post.FreshnessTarget
		if freshness <= 0 {
			continue // no contract to measure against — cannot assess
		}
		markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
		if err != nil {
			continue // transient discovery gap — never warn on missing data
		}
		hulls := post.HullBudget()
		if !domainScouting.IsUndersized(len(markets), hulls, avgHop, freshness) {
			continue
		}
		required := domainScouting.RequiredHulls(len(markets), avgHop, freshness)
		circuit := domainScouting.CircuitDuration(len(markets), hulls, avgHop)

		recent, err := h.eventStore.HasSince(ctx, cmd.PlayerID.Value(), captain.EventScoutPostUndersized, post.SystemSymbol, now.Add(-cooldown))
		if err != nil || recent {
			continue
		}
		_ = h.eventStore.Record(ctx, &captain.Event{
			Type: captain.EventScoutPostUndersized, Ship: post.SystemSymbol, PlayerID: cmd.PlayerID.Value(),
			Payload: fmt.Sprintf(`{"system":%q,"markets":%d,"hulls":%d,"required_hulls":%d,"freshness_secs":%d,"circuit_secs":%d}`,
				post.SystemSymbol, len(markets), hulls, required, int(freshness.Seconds()), int(circuit.Seconds())),
		})
		logger.Log("WARNING", fmt.Sprintf("Scout post %s undersized: %d markets over %d hull(s) ≈ %s circuit exceeds its %s freshness target — needs %d hulls", post.SystemSymbol, len(markets), hulls, circuit.Round(time.Second), freshness.Round(time.Second), required), map[string]interface{}{
			"action":         "scout_post_undersized",
			"system_symbol":  post.SystemSymbol,
			"markets":        len(markets),
			"hulls":          hulls,
			"required_hulls": required,
		})
	}
}

// recordScoutFreshness sets the scout_freshness_actual_seconds gauge for every POSTED
// system this pass is about to reconcile — i.e. exactly the systems in posts, one
// entry per active ScoutPost. A single provider read covers every system for the
// player (MarketFreshnessProvider.MaxAgeSecondsBySystem); a post whose system has no
// cached market rows yet simply has no entry in the returned map and is skipped this
// sweep — its gauge appears once a scan lands. Pure OBSERVATION (RULINGS #4): no
// provider wired, or a read error, is logged (once, not per-post) and the reconcile
// pass continues completely unaffected — a metrics gap must never block manning.
func (h *RunScoutPostCoordinatorHandler) recordScoutFreshness(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, posts []*domainScouting.ScoutPost) {
	if h.marketFreshnessProvider == nil {
		return
	}
	playerID := cmd.PlayerID.Value()
	ages, err := h.marketFreshnessProvider.MaxAgeSecondsBySystem(ctx, playerID)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Scout freshness gauge: failed to compute market ages: %v", err), nil)
		return
	}
	for _, post := range posts {
		age, ok := ages[post.SystemSymbol]
		if !ok {
			continue // nothing cached for this system yet — no series this sweep
		}
		metrics.RecordScoutFreshness(playerID, post.SystemSymbol, age)
	}
}

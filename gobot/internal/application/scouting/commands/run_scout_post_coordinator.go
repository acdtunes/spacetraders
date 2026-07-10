package commands

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

const (
	// scoutPostFleet is the ClaimShip operation/fleet name a post-manned satellite
	// is claimed under. It gates poaching: ClaimShip rejects a hull dedicated to any
	// OTHER fleet (RULINGS #7), and an unpinned satellite claims normally. The claim
	// is occupancy, not a permanent pin — the satellite is never AssignFleet'd, so
	// releasing it (sweep-once completion, restart) returns it to the general pool.
	scoutPostFleet = "scout"

	// defaultScoutPostTickSeconds is the reconcile cadence when the launch config
	// leaves it unset (RULINGS #5: parametrized, not hardcoded at the call site).
	defaultScoutPostTickSeconds = 30

	// marketplaceTrait selects the waypoints a post's tour scans — the same trait
	// the VRP scout-all-markets path keys on.
	marketplaceTrait = "MARKETPLACE"

	// repositionRetryBackoff bounds how often the coordinator will DISPATCH a
	// cross-gate relay for one post slot (sp-s232). The RUNNING-relay check already
	// stops a second dispatch while a relay is airborne; this covers the AFTER-failure
	// window so a relay that fails fast (an unroutable verdict that slipped past the
	// pre-dispatch BFS, an API refusal at a hop) does not hot-loop re-dispatch every
	// tick — the sp-py4n respawn-cap discipline applied to relays. In-memory and reset
	// on restart (conservative: at most one immediate retry after a daemon restart,
	// never a storm).
	repositionRetryBackoff = 5 * time.Minute

	// partitionAnchorFuelCapacity and partitionAnchorEngineSpeed are the synthetic
	// probe configs the VRP partitioner anchors at a common waypoint (sp-enry). The
	// partition is a division of a system's markets into N disjoint tours; it must be
	// STABLE across which specific probes are present (RULINGS: re-partition only on
	// hull-count change), so the partitioner is fed N identical anchored slots rather
	// than real ship locations, and the RESULT is frozen+persisted. These are typical
	// probe values; the exact numbers do not matter because the partition is computed
	// once per budget change, not re-optimized per tick.
	partitionAnchorFuelCapacity = 400
	partitionAnchorEngineSpeed  = 30
)

// RunScoutPostCoordinatorCommand launches the standing scout-post coordinator for
// a player (sp-cxpq). Like the contract fleet coordinator it runs an infinite
// reconcile loop inside a single Handle() call; the container wraps one iteration
// (CoordinatorOwnsIterations).
type RunScoutPostCoordinatorCommand struct {
	PlayerID         shared.PlayerID
	ContainerID      string
	TickIntervalSecs int
}

// RunScoutPostCoordinatorResponse reports reconcile progress. Because the loop is
// infinite it is only observed on context cancellation (shutdown).
type RunScoutPostCoordinatorResponse struct {
	Ticks  int
	Errors []string
}

// ContainerStatusQuery reads container lifecycle state so the reconciler can tell
// a live tour from a dead or completed one. Satisfied by the GORM container
// repository (ListByStatusSimple).
type ContainerStatusQuery interface {
	ListByStatusSimple(ctx context.Context, status string, playerID *int) ([]persistence.ContainerSummary, error)
}

// MarketWaypointProvider lists the marketplace waypoints in a system — the tour a
// post's hull flies. Satisfied by the GORM waypoint repository
// (ListBySystemWithTrait).
type MarketWaypointProvider interface {
	ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error)
}

// GateGraph resolves multi-jump routes over the persisted cross-system gate graph
// (sp-7gr2). The coordinator BFS-walks it to pick the FLEET-WIDE nearest idle
// satellite (fewest jump hops) to reposition to an unmanned post, and to fail closed
// when no satellite can reach it. Optional: nil disables repositioning entirely — the
// coordinator then parks a satellite-less post exactly as before (the sp-qxa4 park),
// so every pre-s232 caller/test behaves unchanged.
type GateGraph interface {
	Path(ctx context.Context, fromSystem, toSystem string, playerID int) ([]string, error)
}

// RunScoutPostCoordinatorHandler reconciles the desired-state posts table every
// tick. Each post has HullBudget() manning SLOTS — one for a single-hull post, N for
// a multi-probe post (sp-enry) — and every slot is manned, repaired, and repositioned
// independently. A multi-probe post's markets are first partitioned into N DISJOINT
// per-probe tours via the existing VRP machinery and frozen (re-partitioned only on a
// hull-budget change); each slot then behaves exactly like a single-hull post over its
// partition. The reconciler respawns any tour that died, mans an unmanned slot by
// claiming an idle satellite ALREADY IN THE POST'S SYSTEM (manning is in-system-only,
// sp-qxa4), releases any assignment whose hull drifted out of system so it can be
// re-matched, retires completed sweep-once posts, and never poaches a pinned hull. When
// a slot has no in-system satellite it JUMP-ROUTES the fleet-wide nearest idle satellite
// to it (sp-s232). It is the freshness backbone the tour planner's age cap and the
// analyst board both ride on.
type RunScoutPostCoordinatorHandler struct {
	postRepo       domainScouting.ScoutPostRepository
	shipRepo       navigation.ShipRepository
	daemonClient   daemon.DaemonClient
	containerQuery ContainerStatusQuery
	marketProvider MarketWaypointProvider
	clock          shared.Clock

	// gateGraph resolves jump-hop distances for fleet-wide reposition selection
	// (sp-s232). nil disables repositioning (the sp-qxa4 park is preserved), so it is
	// wired via SetGateGraph rather than the constructor — every existing caller/test
	// that never wires it behaves exactly as before.
	gateGraph GateGraph

	// graphProvider discovers a VIRGIN system's waypoints presence-free via the API when
	// the reposition target has zero KNOWN market waypoints (sp-nn0y), and supplies
	// waypoint coordinates to the VRP partitioner (sp-enry). It is the same cache-first
	// ISystemGraphProvider port scout_markets/assign_scouting_fleet use, and persists
	// discovered waypoints era-scoped via its BuildSystemGraph→Add path (sp-vapw). nil
	// disables virgin discovery and leaves the partitioner without coordinates (it still
	// partitions, just without geometry) — so it is wired via SetGraphProvider rather than
	// the constructor, and every existing caller/test that never wires it behaves identically.
	graphProvider system.ISystemGraphProvider

	// routingClient solves the VRP that partitions a multi-probe post's markets into N
	// disjoint tours (sp-enry). Reuses the SAME PartitionFleet the `workflow scout-markets`
	// verb uses — the routing service already solves the partition problem. nil disables
	// partitioning: a multi-probe post then cannot materialize its extra slots and parks
	// (fail-closed), while single-hull posts (which never partition) are unaffected. Wired
	// via SetRoutingClient so every pre-enry caller/test that never wires it is unchanged.
	routingClient routing.RoutingClient

	// repositionBackoffUntil rate-limits reposition DISPATCH per post slot (key
	// playerID|system[|slotIndex] → earliest next dispatch time) so a relay that fails
	// fast does not hot-loop re-dispatch (sp-s232). A single-hull post keeps the pre-enry
	// un-suffixed key. In-memory (reset on restart); guarded by repositionMu since the
	// handler is a registered singleton that could serve two players' coordinator ticks
	// concurrently.
	repositionMu           sync.Mutex
	repositionBackoffUntil map[string]time.Time
}

// NewRunScoutPostCoordinatorHandler wires the coordinator. clock defaults to the
// real clock when nil (production). The gate-graph resolver, graph provider, and
// routing client are optional and injected separately (SetGateGraph, SetGraphProvider,
// SetRoutingClient) — nil leaves repositioning / virgin discovery / partitioning
// disabled, so every pre-enry caller behaves as before.
func NewRunScoutPostCoordinatorHandler(
	postRepo domainScouting.ScoutPostRepository,
	shipRepo navigation.ShipRepository,
	daemonClient daemon.DaemonClient,
	containerQuery ContainerStatusQuery,
	marketProvider MarketWaypointProvider,
	clock shared.Clock,
) *RunScoutPostCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunScoutPostCoordinatorHandler{
		postRepo:               postRepo,
		shipRepo:               shipRepo,
		daemonClient:           daemonClient,
		containerQuery:         containerQuery,
		marketProvider:         marketProvider,
		clock:                  clock,
		repositionBackoffUntil: make(map[string]time.Time),
	}
}

// SetGateGraph wires the multi-jump gate-graph resolver (sp-s232). The daemon injects
// the same persisted, fetch-through gategraph.Service the trade-route circuit uses, so
// the reposition BFS and the circuit's travel() share one cache/graph. Mirrors the
// trade-route coordinator's SetGateGraph optional-injection idiom; nil (the default)
// leaves repositioning disabled and the sp-qxa4 park behavior intact.
func (h *RunScoutPostCoordinatorHandler) SetGateGraph(g GateGraph) {
	h.gateGraph = g
}

// SetGraphProvider wires the presence-free waypoint discoverer for virgin reposition
// targets (sp-nn0y) and the coordinate source for the VRP partitioner (sp-enry). The
// daemon injects the same graphService the `waypoint` verb and the scout-markets planner
// use, so discovery shares one cache/graph and persists era-scoped exactly as every other
// charting path. Mirrors SetGateGraph's optional-injection idiom; nil (the default) leaves
// the pre-nn0y park behavior intact.
func (h *RunScoutPostCoordinatorHandler) SetGraphProvider(g system.ISystemGraphProvider) {
	h.graphProvider = g
}

// SetRoutingClient wires the VRP fleet partitioner (sp-enry). The daemon injects the
// SAME routing client the scout-markets verb uses, so a multi-probe post's disjoint
// partition is solved by the routing service that already solves it. Optional-injection
// (like SetGateGraph): nil leaves partitioning disabled, so single-hull posts are
// unaffected and a multi-probe post parks fail-closed until a client is wired.
func (h *RunScoutPostCoordinatorHandler) SetRoutingClient(c routing.RoutingClient) {
	h.routingClient = c
}

// Handle runs the reconcile loop until the context is cancelled.
func (h *RunScoutPostCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunScoutPostCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	tick := time.Duration(cmd.TickIntervalSecs) * time.Second
	if tick <= 0 {
		tick = defaultScoutPostTickSeconds * time.Second
	}

	result := &RunScoutPostCoordinatorResponse{Errors: []string{}}
	logger.Log("INFO", fmt.Sprintf("Scout post coordinator starting (tick %s)", tick), map[string]interface{}{
		"action":       "scout_post_coordinator_start",
		"container_id": cmd.ContainerID,
	})

	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		if err := h.reconcileOnce(ctx, cmd); err != nil {
			result.Errors = append(result.Errors, err.Error())
			logger.Log("ERROR", fmt.Sprintf("Scout post reconcile failed: %v", err), nil)
		}
		result.Ticks++

		select {
		case <-time.After(tick):
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
}

// slotTarget pairs an unmanned slot with its owning post so pass 2 can man or
// reposition it with the post's markets, priority, and freshness (sp-enry).
type slotTarget struct {
	post *domainScouting.ScoutPost
	slot domainScouting.ScoutSlotRef
}

// reconcileOnce is one reconcile pass over the posts table. It is the unit the
// coordinator's tests drive directly (the Handle loop just calls it on a timer).
//
// A post has HullBudget() manning SLOTS (sp-enry): one for a single-hull post — the
// primary slot, byte-identical to the pre-enry behavior — or N for a multi-probe post,
// whose markets are first partitioned into N disjoint per-probe tours and frozen. Every
// pass below iterates SLOTS, not posts, so a dead probe on one slot heals without
// touching its siblings.
//
// Passes:
//   - Partition (sp-enry): (re)compute a multi-probe post's N disjoint partitions via VRP
//     ONLY when its hull budget changed (slot count != budget), and persist them — so a
//     restart reloads the frozen partitions and never re-tours, and a re-man reuses the same
//     partition.
//   - Pass 1 (manned slots): release any slot whose hull drifted out of the post's system
//     (sp-qxa4 repair — stop its tour, free the hull, clear the slot); retire a completed
//     sweep-once (release its hull, delete the post); free the hull of any other slot whose
//     tour is not running, clearing it so pass 2 re-mans it with the SAME partition. A
//     healthy in-system tour is left untouched.
//   - Pass 1.5 (repositioning slots): a slot with a relay in flight is left alone while its
//     container is RUNNING; when the relay ends the hull is reclaimed and the relay reference
//     cleared so pass 2 re-evaluates the slot.
//   - Pass 2a (in-system manning): claim an idle satellite ALREADY IN THE POST'S SYSTEM and
//     spawn its tour over the slot's markets (all markets for a single-hull post, the frozen
//     partition for a multi-probe slot). In-system only (sp-qxa4).
//   - Pass 2b (reposition, sp-s232): for a slot STILL unmanned, jump-route the FLEET-WIDE
//     nearest idle satellite to the post's system, then let the next tick's 2a man it.
func (h *RunScoutPostCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) error {
	posts, err := h.postRepo.ListActive(ctx, cmd.PlayerID.Value())
	if err != nil {
		return fmt.Errorf("failed to list scout posts: %w", err)
	}
	if len(posts) == 0 {
		return nil
	}

	running, err := h.containerIDSet(ctx, cmd, "RUNNING")
	if err != nil {
		return err
	}
	completed, err := h.containerIDSet(ctx, cmd, "COMPLETED")
	if err != nil {
		return err
	}

	removed := make(map[string]bool)

	// Partition pass (sp-enry): materialize each multi-probe post's disjoint tours.
	// A no-op for single-hull posts and for multi-probe posts already partitioned at
	// their current budget — it re-partitions ONLY on a hull-budget change.
	for _, post := range posts {
		h.ensurePartitions(ctx, cmd, post)
	}

	// Pass 1: manned slots.
	for _, post := range posts {
		if h.reconcileMannedSlots(ctx, cmd, post, running, completed, removed) {
			continue // post retired (sweep-once complete)
		}
	}

	// Pass 1.5: repositioning slots (sp-s232). A relay in flight (RUNNING) owns its slot —
	// pass 2 skips it. When the relay is no longer RUNNING it has landed, failed, or was
	// restart-interrupted; reclaim defensively and clear the relay reference so pass 2
	// re-evaluates the slot.
	for _, post := range posts {
		if removed[post.SystemSymbol] {
			continue
		}
		h.reconcileRepositioningSlots(ctx, cmd, post, running)
	}

	// Pass 2: man the unmanned slots, standing posts first.
	targets := h.unmannedSlotTargets(posts, removed)
	if len(targets) == 0 {
		return nil
	}

	idleSats, err := h.idleScoutSatellites(ctx, cmd)
	if err != nil {
		return err
	}

	// Pass 2a: man every slot that has an idle satellite ALREADY in its system (sp-qxa4
	// in-system-only manning). Doing this for ALL slots before any reposition guarantees an
	// in-system satellite is never repositioned AWAY from a slot that could man it locally.
	stillUnmanned := make([]slotTarget, 0, len(targets))
	for _, tgt := range targets {
		idx := selectInSystemSatellite(idleSats, tgt.post.SystemSymbol)
		if idx < 0 {
			stillUnmanned = append(stillUnmanned, tgt)
			continue
		}

		markets, err := h.slotMarkets(ctx, tgt.post, tgt.slot)
		if err != nil {
			common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to discover markets for post %s: %v", tgt.post.SystemSymbol, err), nil)
			continue
		}
		if len(markets) == 0 {
			// Nothing to scan (uncharted / no marketplace waypoints, or an un-partitioned
			// multi-probe slot). Don't burn the in-system satellite's claim on a zero-market
			// tour — leave it idle in system. Repositioning cannot help (the problem is
			// markets, not hull location), so this slot is NOT a 2b candidate.
			common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("No markets to scan for post %s slot yet — leaving unmanned this tick", tgt.post.SystemSymbol), nil)
			continue
		}

		sat := idleSats[idx]
		idleSats = append(idleSats[:idx], idleSats[idx+1:]...)

		tourID, err := h.spawnTour(ctx, cmd, tgt.post, sat.ShipSymbol(), markets)
		if err != nil {
			common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to man post %s with %s: %v", tgt.post.SystemSymbol, sat.ShipSymbol(), err), nil)
			continue
		}

		tgt.slot.SetAssignedHull(sat.ShipSymbol())
		tgt.slot.SetTourContainerID(tourID)
		if err := h.postRepo.Upsert(ctx, tgt.post); err != nil {
			common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Manned post %s but failed to persist assignment: %v", tgt.post.SystemSymbol, err), nil)
		}
	}

	// Pass 2b: jump-route the fleet-wide nearest idle satellite to each still-unmanned slot
	// (sp-s232). repositionUnmannedSlot fails closed — no gate graph, no idle satellite, no
	// reachable satellite, no known markets, or an active backoff parks the slot honest — so
	// with no gate graph wired this is exactly the pre-s232 park.
	for _, tgt := range stillUnmanned {
		h.repositionUnmannedSlot(ctx, cmd, tgt.post, tgt.slot, &idleSats)
	}

	return nil
}

// reconcileMannedSlots runs pass 1 over one post's slots. It returns true when the
// post was retired (a completed sweep-once), so the caller skips it in later passes.
func (h *RunScoutPostCoordinatorHandler) reconcileMannedSlots(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	running, completed, removed map[string]bool,
) bool {
	logger := common.LoggerFromContext(ctx)

	for _, slot := range post.Slots() {
		hull := slot.AssignedHull()
		if hull == "" {
			continue
		}
		tourID := slot.TourContainerID()

		// REPAIR (sp-qxa4): the assigned hull is no longer in the post's system. Its
		// in-system tour can never navigate the post's waypoints, so it crash-respawn-loops.
		// Release it unconditionally (even if momentarily RUNNING): stop the tour, free the
		// hull, clear the slot. Pass 2 then re-mans with an in-system satellite or parks.
		if h.hullOutOfSystem(ctx, cmd, hull, post.SystemSymbol) {
			_ = h.daemonClient.StopContainer(ctx, tourID)
			h.reclaimHullFromContainer(ctx, cmd, tourID, "scout_post_respawn")
			logger.Log("INFO", fmt.Sprintf("Released cross-system assignment: hull %s is not in post %s's system — returned to pool for in-system re-matching", hull, post.SystemSymbol), map[string]interface{}{
				"action":        "scout_post_cross_system_repair",
				"system_symbol": post.SystemSymbol,
				"ship_symbol":   hull,
			})
			slot.SetAssignedHull("")
			slot.SetTourContainerID("")
			if err := h.postRepo.Upsert(ctx, post); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to clear cross-system assignment on post %s: %v", post.SystemSymbol, err), nil)
			}
			continue
		}

		// A live tour is healthy — never disturb it.
		if tourID != "" && running[tourID] {
			continue
		}

		// A sweep-once post whose tour COMPLETED has done its one job: release the hull and
		// retire the post so its satellite flows to the next unmanned post. Sweep-once is
		// always single-hull (HullBudget clamps it), so this is the only slot.
		if post.Kind == domainScouting.PostKindSweepOnce && tourID != "" && completed[tourID] {
			h.releaseHull(ctx, cmd, hull, "sweep_once_complete")
			if err := h.postRepo.Remove(ctx, cmd.PlayerID.Value(), post.SystemSymbol); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to retire completed sweep-once post %s: %v", post.SystemSymbol, err), nil)
			} else {
				removed[post.SystemSymbol] = true
				logger.Log("INFO", fmt.Sprintf("Retired completed sweep-once post %s (hull %s released)", post.SystemSymbol, hull), map[string]interface{}{
					"action":        "scout_post_sweep_complete",
					"system_symbol": post.SystemSymbol,
				})
				return true
			}
			continue
		}

		// Otherwise the tour is dead/missing/crashed: free the hull and clear the slot. Pass 2
		// re-mans it — with this same hull, since it is idle in the post's system — over the
		// SAME partition (the slot's frozen markets are untouched), so it respawns within a tick.
		h.reclaimHullFromContainer(ctx, cmd, tourID, "scout_post_respawn")
		slot.SetAssignedHull("")
		slot.SetTourContainerID("")
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to clear assignment on post %s: %v", post.SystemSymbol, err), nil)
		}
	}
	return false
}

// reconcileRepositioningSlots runs pass 1.5 over one post's slots: reclaim any ended
// relay and clear its reference so pass 2 re-evaluates the slot (sp-s232).
func (h *RunScoutPostCoordinatorHandler) reconcileRepositioningSlots(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	running map[string]bool,
) {
	logger := common.LoggerFromContext(ctx)
	for _, slot := range post.Slots() {
		relayID := slot.RepositionContainerID()
		if slot.AssignedHull() != "" || relayID == "" {
			continue
		}
		if running[relayID] {
			continue // relay airborne — leave it; pass 2 skips this slot
		}
		h.reclaimHullFromContainer(ctx, cmd, relayID, "scout_reposition_ended")
		logger.Log("INFO", fmt.Sprintf("Scout reposition relay for post %s ended (container %s not running) — re-evaluating next tick", post.SystemSymbol, relayID), map[string]interface{}{
			"action":        "scout_reposition_relay_ended",
			"system_symbol": post.SystemSymbol,
		})
		slot.SetRepositionContainerID("")
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to clear ended reposition relay on post %s: %v", post.SystemSymbol, err), nil)
		}
	}
}

// unmannedSlotTargets collects every slot that pass 2 should man: unmanned, not
// repositioning, in a non-retired post. Standing posts sort before sweep-once (the
// freshness backbone is manned first), deterministic by system within a kind, and
// primary-before-extra within a post — so manning order is stable and testable.
func (h *RunScoutPostCoordinatorHandler) unmannedSlotTargets(posts []*domainScouting.ScoutPost, removed map[string]bool) []slotTarget {
	ordered := make([]*domainScouting.ScoutPost, 0, len(posts))
	for _, post := range posts {
		if removed[post.SystemSymbol] {
			continue
		}
		ordered = append(ordered, post)
	}
	sortPostsByPriority(ordered)

	var targets []slotTarget
	for _, post := range ordered {
		for _, slot := range post.Slots() {
			if slot.AssignedHull() != "" || slot.RepositionContainerID() != "" {
				continue
			}
			targets = append(targets, slotTarget{post: post, slot: slot})
		}
	}
	return targets
}

// ensurePartitions materializes a multi-probe post's N DISJOINT market partitions
// (sp-enry). It is a no-op for a single-hull post (HullBudget 1 → no partition, the
// primary tours all markets) and for a multi-probe post ALREADY partitioned at its
// current budget (len(ExtraSlots) == budget-1) — so it re-partitions ONLY on a
// hull-budget change, never per tick. On a budget change of a running post it stops the
// existing tours/relays (their markets change), reclaims their hulls, and rebuilds the
// slots with fresh partitions; pass 2 re-mans them. Fails closed: no routing client, no
// markets, or a VRP error leaves the post un-partitioned (it parks) and retries next tick.
//
// API-BUDGET INVARIANT (documented per the bead): partitioning changes WHERE probes scan,
// not HOW MUCH. Total scans/hour ≈ markets / freshness-target regardless of N — N smaller
// partitions each paced to the freshness target (circuitPaceInterval, scout_tour.go) sum to
// one scan per market per freshness window. More probes buy fresher data, NOT more API calls.
func (h *RunScoutPostCoordinatorHandler) ensurePartitions(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost) {
	logger := common.LoggerFromContext(ctx)

	budget := post.HullBudget()

	if budget <= 1 {
		// Single-hull (or sweep-once): a genuine single-hull post carries no partition
		// state and this is a no-op (byte-identical to pre-enry). But a post REDUCED from
		// multi-probe (hulls lowered to 1, or converted to sweep-once) still carries stale
		// extra slots / partition — tear them down so it reverts to the single-slot shape,
		// freeing the surplus probes to the pool. Pass 2 then re-mans the primary over ALL
		// markets.
		if len(post.ExtraSlots) > 0 || len(post.PrimaryPartition) > 0 {
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
		return
	}
	if len(post.ExtraSlots) == budget-1 {
		return // already partitioned at this budget — stable, no re-partition.
	}

	if h.routingClient == nil {
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
		logger.Log("INFO", fmt.Sprintf("No known marketplace waypoints in %s yet — cannot partition, post parks", post.SystemSymbol), map[string]interface{}{
			"action":        "scout_post_partition_no_markets",
			"system_symbol": post.SystemSymbol,
		})
		return
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

	action := "scout_post_partitioned"
	verb := "Partitioned"
	if repartition {
		action = "scout_post_repartitioned"
		verb = "Re-partitioned (hull budget changed)"
	}
	logger.Log("INFO", fmt.Sprintf("%s scout post %s into %d disjoint tours over %d markets", verb, post.SystemSymbol, budget, len(markets)), map[string]interface{}{
		"action":        action,
		"system_symbol": post.SystemSymbol,
		"hulls":         budget,
		"markets":       len(markets),
	})
}

// tearDownSlots stops every slot's tour/relay container and reclaims its hull ahead of
// a re-partition (sp-enry), then clears the assignments in memory. Best-effort: a hull
// the coordinator fails to reclaim here is reclaimed by pass 1 on a later tick.
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
}

// partitionMarkets solves the VRP that splits markets into n DISJOINT per-probe tours
// (sp-enry), reusing the SAME PartitionFleet the scout-markets verb uses. The N probes are
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

// slotMarkets returns the waypoints a slot's tour should scan (sp-enry): ALL the
// system's markets for a single-hull post (the pre-enry behavior, discovered fresh), or
// the slot's frozen partition for a multi-probe post.
func (h *RunScoutPostCoordinatorHandler) slotMarkets(ctx context.Context, post *domainScouting.ScoutPost, slot domainScouting.ScoutSlotRef) ([]string, error) {
	if post.HullBudget() <= 1 {
		return h.discoverMarkets(ctx, post.SystemSymbol)
	}
	return slot.Partition(), nil
}

// containerIDSet returns the set of container IDs in the given status for the player.
func (h *RunScoutPostCoordinatorHandler) containerIDSet(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, status string) (map[string]bool, error) {
	playerID := cmd.PlayerID.Value()
	summaries, err := h.containerQuery.ListByStatusSimple(ctx, status, &playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s containers: %w", status, err)
	}
	set := make(map[string]bool, len(summaries))
	for _, s := range summaries {
		set[s.ID] = true
	}
	return set, nil
}

// idleScoutSatellites returns the idle SATELLITE-role hulls eligible to man a post:
// idle, scout-type, and not dedicated to some OTHER fleet. The dedication filter is
// the first line of the poach guard (RULINGS #7); ClaimShip enforces it atomically
// as the second. Non-satellite hulls (the command frigate, haulers) are never
// returned, so a post can never claim one.
func (h *RunScoutPostCoordinatorHandler) idleScoutSatellites(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) ([]*navigation.Ship, error) {
	ships, err := h.shipRepo.FindIdleByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find idle ships: %w", err)
	}
	var sats []*navigation.Ship
	for _, ship := range ships {
		if !ship.IsScoutType() {
			continue
		}
		if fleet := ship.DedicatedFleet(); fleet != "" && fleet != scoutPostFleet {
			continue // pinned to another fleet — never poach
		}
		sats = append(sats, ship)
	}
	// Deterministic order so selection is stable across ticks and testable.
	sort.Slice(sats, func(i, j int) bool {
		return sats[i].ShipSymbol() < sats[j].ShipSymbol()
	})
	return sats, nil
}

// deriveScanInterval computes a post's probe market-scan cadence from its
// freshness target (sp-zixw): half the freshness window, clamped via
// clampScanInterval (scout_tour.go) to [scanIntervalFloor, scanIntervalCap] so
// neither an aggressive nor a lax freshness target can push the per-hull API cost
// outside the budgeted range. A zero/unset freshness target clamps to the floor,
// same as any other too-small value — the coordinator path has no "direct launch"
// default to fall back on.
func deriveScanInterval(freshness time.Duration) time.Duration {
	return clampScanInterval(freshness / 2)
}

// spawnTour persists a coordinator-managed scout_tour worker for hullSymbol over the
// slot's markets, atomically claims the hull to it, and starts it. The persisted config
// carries coordinator_id so restart recovery skips the tour and leaves respawning to this
// coordinator. Standing posts run an infinite tour; sweep-once posts a single one. The
// tour's ScanInterval is derived from the POST's freshness target, so every probe on a
// multi-probe post paces its own partition against one freshness target (sp-enry).
func (h *RunScoutPostCoordinatorHandler) spawnTour(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	hullSymbol string,
	markets []string,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	iterations := -1 // standing: keep the system fresh forever
	if post.Kind == domainScouting.PostKindSweepOnce {
		iterations = 1 // one pass, then the post auto-retires
	}

	workerID := utils.GenerateContainerID("scout_tour", hullSymbol)
	tourCmd := &ScoutTourCommand{
		PlayerID:      cmd.PlayerID,
		ShipSymbol:    hullSymbol,
		Markets:       markets,
		Iterations:    iterations,
		CoordinatorID: cmd.ContainerID,
		ScanInterval:  deriveScanInterval(post.FreshnessTarget),
	}

	if err := h.daemonClient.PersistContainer(ctx, daemon.ContainerKindScoutTour, workerID, uint(cmd.PlayerID.Value()), tourCmd); err != nil {
		return "", fmt.Errorf("failed to persist scout tour worker: %w", err)
	}

	// Atomic claim (l7h2): rejects a hull pinned to another fleet at the DB, so a
	// pin racing discovery can never be poached. %w so the poach-vector test can
	// distinguish a dedication rejection from a transient failure.
	if err := h.shipRepo.ClaimShip(ctx, hullSymbol, workerID, cmd.PlayerID, scoutPostFleet); err != nil {
		_ = h.daemonClient.StopContainer(ctx, workerID)
		return "", fmt.Errorf("failed to claim satellite %s: %w", hullSymbol, err)
	}

	if err := h.daemonClient.StartContainer(ctx, daemon.ContainerKindScoutTour, workerID); err != nil {
		h.releaseHull(ctx, cmd, hullSymbol, "scout_tour_start_failed")
		_ = h.daemonClient.StopContainer(ctx, workerID)
		return "", fmt.Errorf("failed to start scout tour worker: %w", err)
	}

	logger.Log("INFO", fmt.Sprintf("Manned scout post %s with %s (tour %s, %d markets, iterations %d)", post.SystemSymbol, hullSymbol, workerID, len(markets), iterations), map[string]interface{}{
		"action":        "scout_post_manned",
		"system_symbol": post.SystemSymbol,
		"ship_symbol":   hullSymbol,
		"container_id":  workerID,
		"kind":          string(post.Kind),
	})
	return workerID, nil
}

// repositionUnmannedSlot jump-routes the fleet-wide nearest idle satellite to a slot
// with no in-system hull (sp-s232). It FAILS CLOSED at every gap — no gate graph, no idle
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
) {
	logger := common.LoggerFromContext(ctx)
	key := backoffKey(cmd.PlayerID.Value(), post.SystemSymbol, slot.Index())

	// No gate graph wired, or no idle satellite left this tick → cannot reposition. Park
	// honest with the in-system reason (the pre-s232 / sp-qxa4 behavior and greppable message).
	if h.gateGraph == nil || len(*idleSats) == 0 {
		h.parkNoInSystemSatellite(ctx, post)
		return
	}

	// A recent relay for this slot failed — don't hot-loop re-dispatch (sp-py4n).
	if h.repositionBackedOff(key) {
		logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: reposition backing off after a recent relay — retrying shortly", post.SystemSymbol), map[string]interface{}{
			"action":        "scout_reposition_backoff",
			"system_symbol": post.SystemSymbol,
		})
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
		// Virgin frontier system (sp-nn0y): discover its waypoints presence-free, then retry.
		markets = h.discoverVirginMarkets(ctx, cmd, post, key)
		if len(markets) == 0 {
			return // parked honest by discoverVirginMarkets
		}
	}
	destWaypoint := pickRepositionDestination(markets)

	// Fleet-wide nearest idle satellite by jump-hop count (fail-closed on unroutable).
	idx, hops, ok := h.selectNearestSatelliteByHops(ctx, *idleSats, post.SystemSymbol, cmd.PlayerID.Value())
	if !ok {
		logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: no jump-routable satellite in the fleet — parked (fail-closed)", post.SystemSymbol), map[string]interface{}{
			"action":        "scout_reposition_unroutable",
			"system_symbol": post.SystemSymbol,
		})
		return
	}

	sat := (*idleSats)[idx]
	*idleSats = append((*idleSats)[:idx], (*idleSats)[idx+1:]...)

	relayID, err := h.spawnReposition(ctx, cmd, sat.ShipSymbol(), destWaypoint)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to dispatch reposition of %s to post %s: %v", sat.ShipSymbol(), post.SystemSymbol, err), nil)
		return
	}

	// Arm the backoff BEFORE persisting the relay reference: if the Upsert below fails,
	// the backoff still prevents an immediate second relay to this slot next tick.
	h.noteRepositionDispatch(key)
	slot.SetRepositionContainerID(relayID)
	if err := h.postRepo.Upsert(ctx, post); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Dispatched reposition for post %s but failed to persist relay reference: %v", post.SystemSymbol, err), nil)
	}

	logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: repositioning %s (%d jump(s), relay %s) → %s", post.SystemSymbol, sat.ShipSymbol(), hops, relayID, destWaypoint), map[string]interface{}{
		"action":        "scout_reposition_dispatch",
		"system_symbol": post.SystemSymbol,
		"ship_symbol":   sat.ShipSymbol(),
		"jumps":         hops,
		"destination":   destWaypoint,
		"relay":         relayID,
	})
}

// discoverVirginMarkets resolves the s232 bootstrap chicken-and-egg for a post whose
// system has ZERO known market waypoints (sp-nn0y): it DISCOVERS the system's waypoints
// presence-free via the graph provider's cache-first GetGraph, persisting them era-scoped,
// then re-reads. markets found → returns them (the caller repositions this tick); none →
// parks UNSERVICEABLE (charted but barren); API error → parks fail-closed. It arms the
// per-slot dispatch backoff (key) BEFORE the API call, so a marketless or API-erroring
// system is probed at most ONCE per window. With no graph provider wired it logs the pre-nn0y
// park verbatim, so every existing caller/test is unaffected.
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
// jump-routed there (sp-s232). Distance is the gate-graph BFS path length; a satellite
// whose Path errors is skipped (fail-closed). idleSats is pre-sorted by symbol, and the
// comparison is strict (< bestHops), so the lowest-symbol satellite wins an equal-hops tie.
func (h *RunScoutPostCoordinatorHandler) selectNearestSatelliteByHops(
	ctx context.Context,
	idleSats []*navigation.Ship,
	postSystem string,
	playerID int,
) (idx int, hops int, ok bool) {
	logger := common.LoggerFromContext(ctx)
	bestIdx, bestHops := -1, 0
	for i, sat := range idleSats {
		loc := sat.CurrentLocation()
		if loc == nil {
			continue // unknown location — cannot route
		}
		path, err := h.gateGraph.Path(ctx, loc.SystemSymbol, postSystem, playerID)
		if err != nil {
			logger.Log("INFO", fmt.Sprintf("Reposition candidate %s → %s unroutable this tick: %v", loc.SystemSymbol, postSystem, err), map[string]interface{}{
				"action": "scout_reposition_candidate_unroutable",
				"from":   loc.SystemSymbol,
				"to":     postSystem,
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
) (string, error) {
	workerID := utils.GenerateContainerID("scout_reposition", hullSymbol)
	repoCmd := &ScoutRepositionCommand{
		PlayerID:            cmd.PlayerID,
		ShipSymbol:          hullSymbol,
		DestinationWaypoint: destinationWaypoint,
		CoordinatorID:       cmd.ContainerID,
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
// idle satellite left this tick). The message text is preserved verbatim from the
// pre-s232 park so `container logs` greps and the sp-qxa4 park assertions still match.
func (h *RunScoutPostCoordinatorHandler) parkNoInSystemSatellite(ctx context.Context, post *domainScouting.ScoutPost) {
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Scout post %s unmanned: no in-system satellite — reposition one or wait", post.SystemSymbol), map[string]interface{}{
		"action":        "scout_post_unmanned_no_in_system_satellite",
		"system_symbol": post.SystemSymbol,
	})
}

// repositionBackedOff reports whether a reposition dispatch for key is currently within
// its backoff window (sp-s232). A nil/absent entry reads false.
func (h *RunScoutPostCoordinatorHandler) repositionBackedOff(key string) bool {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	until, ok := h.repositionBackoffUntil[key]
	return ok && h.clock.Now().Before(until)
}

// noteRepositionDispatch arms the per-slot dispatch backoff (sp-s232) so the next
// dispatch for this slot waits out repositionRetryBackoff — the anti-hot-loop bound.
func (h *RunScoutPostCoordinatorHandler) noteRepositionDispatch(key string) {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	if h.repositionBackoffUntil == nil {
		h.repositionBackoffUntil = make(map[string]time.Time)
	}
	h.repositionBackoffUntil[key] = h.clock.Now().Add(repositionRetryBackoff)
}

// backoffKey scopes the reposition backoff to (playerID, system, slot) so one player's
// relay to system S never rate-limits another player's post in the same-named system, and
// each slot of a multi-probe post repositions independently (sp-enry). The PRIMARY slot
// keeps the pre-enry un-suffixed key, so a single-hull post is byte-identical to s232.
func backoffKey(playerID int, system string, slotIndex int) string {
	if slotIndex < 0 {
		return fmt.Sprintf("%d|%s", playerID, system)
	}
	return fmt.Sprintf("%d|%s|%d", playerID, system, slotIndex)
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

// hullOutOfSystem reports whether a hull is currently NOT in system — the cross-system
// -assignment defect the repair pass heals (sp-qxa4). It fails safe: a hull that cannot be
// loaded, or whose location is unknown, is treated as in-system so a transient lookup gap
// never triggers a spurious release.
func (h *RunScoutPostCoordinatorHandler) hullOutOfSystem(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, hullSymbol, systemSymbol string) bool {
	if hullSymbol == "" {
		return false
	}
	ship, err := h.shipRepo.FindBySymbol(ctx, hullSymbol, cmd.PlayerID)
	if err != nil {
		return false // unknown hull — never release on a lookup failure
	}
	loc := ship.CurrentLocation()
	if loc == nil {
		return false // unknown location — conservative, leave the assignment alone
	}
	return loc.SystemSymbol != systemSymbol
}

// reclaimHullFromContainer frees any ship still assigned to a (now dead) worker
// container, returning it to idle so pass 2 can re-claim it. Best-effort and DB-only —
// the contract ReclaimShipsFromInterruptedWorkers pattern, shared by dead tours and ended
// reposition relays (the reason distinguishes them in the log).
func (h *RunScoutPostCoordinatorHandler) reclaimHullFromContainer(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, containerID, reason string) {
	logger := common.LoggerFromContext(ctx)
	if containerID == "" {
		return
	}
	ships, err := h.shipRepo.FindByContainer(ctx, containerID, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to load ships for dead container %s: %v", containerID, err), nil)
		return
	}
	for _, ship := range ships {
		if !ship.IsAssigned() {
			continue
		}
		ship.ForceRelease(reason, h.clock)
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to reclaim hull %s from container %s: %v", ship.ShipSymbol(), containerID, err), nil)
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Reclaimed hull %s from container %s", ship.ShipSymbol(), containerID), nil)
	}
}

// releaseHull frees a specific hull by symbol (sweep-once retirement, start-failure
// rollback). Best-effort.
func (h *RunScoutPostCoordinatorHandler) releaseHull(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, hullSymbol, reason string) {
	if hullSymbol == "" {
		return
	}
	logger := common.LoggerFromContext(ctx)
	ship, err := h.shipRepo.FindBySymbol(ctx, hullSymbol, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to load hull %s to release (%s): %v", hullSymbol, reason, err), nil)
		return
	}
	if !ship.IsAssigned() {
		return
	}
	ship.ForceRelease(reason, h.clock)
	if err := h.shipRepo.Save(ctx, ship); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to release hull %s (%s): %v", hullSymbol, reason, err), nil)
	}
}

// discoverMarkets returns the marketplace waypoint symbols in a system — the tour a
// post's hull scans.
func (h *RunScoutPostCoordinatorHandler) discoverMarkets(ctx context.Context, systemSymbol string) ([]string, error) {
	waypoints, err := h.marketProvider.ListBySystemWithTrait(ctx, systemSymbol, marketplaceTrait)
	if err != nil {
		return nil, err
	}
	markets := make([]string, 0, len(waypoints))
	for _, wp := range waypoints {
		markets = append(markets, wp.Symbol)
	}
	return markets, nil
}

// sortPostsByPriority orders unmanned posts so standing posts (the freshness
// backbone) are manned before sweep-once frontier posts, deterministic by system
// within a kind.
func sortPostsByPriority(posts []*domainScouting.ScoutPost) {
	sort.Slice(posts, func(i, j int) bool {
		ki, kj := postKindRank(posts[i].Kind), postKindRank(posts[j].Kind)
		if ki != kj {
			return ki < kj
		}
		return posts[i].SystemSymbol < posts[j].SystemSymbol
	})
}

func postKindRank(kind domainScouting.PostKind) int {
	if kind == domainScouting.PostKindStanding {
		return 0
	}
	return 1
}

// selectInSystemSatellite returns the index of an idle satellite already in the
// post's system, or -1 if none. Cross-system matching is intentionally impossible
// (sp-qxa4): the scout_tour worker navigates in-system only, so a cross-system
// assignment crash-respawn-loops. A slot with no in-system satellite is a reposition
// candidate (2b). idleSats is pre-sorted, so the choice is deterministic.
func selectInSystemSatellite(idleSats []*navigation.Ship, systemSymbol string) int {
	for i, sat := range idleSats {
		if sat.CurrentLocation() != nil && sat.CurrentLocation().SystemSymbol == systemSymbol {
			return i
		}
	}
	return -1
}

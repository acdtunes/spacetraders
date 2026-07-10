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
	// cross-gate relay for one post (sp-s232). The RUNNING-relay check already stops a
	// second dispatch while a relay is airborne; this covers the AFTER-failure window
	// so a relay that fails fast (an unroutable verdict that slipped past the
	// pre-dispatch BFS, an API refusal at a hop) does not hot-loop re-dispatch every
	// tick — the sp-py4n respawn-cap discipline applied to relays. In-memory and reset
	// on restart (conservative: at most one immediate retry after a daemon restart,
	// never a storm).
	repositionRetryBackoff = 5 * time.Minute
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
// tick: it respawns any post whose tour died, mans unmanned posts by claiming an
// idle satellite ALREADY IN THE POST'S SYSTEM (manning is in-system-only, sp-qxa4),
// releases any assignment whose hull has drifted out of the post's system so it can
// be re-matched, retires completed sweep-once posts, and never poaches a pinned hull.
// When a post has no in-system satellite it JUMP-ROUTES the fleet-wide nearest idle
// satellite to it (sp-s232) — a claimed cross-gate relay via the shared multi-jump
// travel machinery; manning stays in-system-only (the relay just moves the hull
// there first). A post with no reachable satellite parks honest, re-checked each tick.
// It is the freshness backbone the tour planner's age cap and the analyst board both
// ride on.
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
	// the reposition target has zero KNOWN market waypoints (sp-nn0y — the s232 bootstrap
	// chicken-and-egg: an unswept system has no market waypoint to relay to). It is the
	// same cache-first ISystemGraphProvider port scout_markets/assign_scouting_fleet use,
	// and persists discovered waypoints era-scoped via its BuildSystemGraph→Add path
	// (sp-vapw). nil disables virgin discovery — the reposition path then parks exactly as
	// before nn0y — so it is wired via SetGraphProvider rather than the constructor, and
	// every existing caller/test that never wires it behaves identically.
	graphProvider system.ISystemGraphProvider

	// repositionBackoffUntil rate-limits reposition DISPATCH per post (key
	// playerID|system → earliest next dispatch time) so a relay that fails fast does
	// not hot-loop re-dispatch (sp-s232). In-memory (reset on restart); guarded by
	// repositionMu since the handler is a registered singleton that could serve two
	// players' coordinator ticks concurrently.
	repositionMu           sync.Mutex
	repositionBackoffUntil map[string]time.Time
}

// NewRunScoutPostCoordinatorHandler wires the coordinator. clock defaults to the
// real clock when nil (production). The gate-graph resolver is optional and injected
// separately via SetGateGraph (sp-s232) — nil leaves repositioning disabled.
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
// targets (sp-nn0y). The daemon injects the same graphService the `waypoint` verb and the
// scout-markets planner use, so virgin discovery shares one cache/graph and persists
// era-scoped exactly as every other charting path. Mirrors SetGateGraph's optional-
// injection idiom; nil (the default) leaves the pre-nn0y park behavior intact.
func (h *RunScoutPostCoordinatorHandler) SetGraphProvider(g system.ISystemGraphProvider) {
	h.graphProvider = g
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

// reconcileOnce is one reconcile pass over the posts table. It is the unit the
// coordinator's tests drive directly (the Handle loop just calls it on a timer).
//
// Passes:
//   - Pass 1 (manned posts): release any post whose assigned hull is no longer in
//     the post's system (the sp-qxa4 cross-system defect — stop its tour, free the
//     hull, clear the assignment); retire a completed sweep-once (release its hull,
//     delete the post); free the hull of any other post whose tour is not running,
//     clearing the assignment so pass 2 re-mans it. A healthy in-system tour is left
//     untouched.
//   - Pass 1.5 (repositioning posts): a post with a relay in flight is left alone
//     while its container is RUNNING; when the relay ends (landed, failed, or restart-
//     interrupted) the hull is reclaimed and the relay reference cleared so pass 2
//     re-evaluates the post — 2a mans it if the satellite arrived in-system, else 2b
//     re-dispatches (backoff-gated).
//   - Pass 2a (in-system manning): standing posts before sweep-once, claim an idle
//     satellite ALREADY IN THE POST'S SYSTEM and spawn its tour. Manning is in-system
//     only (sp-qxa4) — the tour navigates in-system, so a cross-system hull would
//     crash-loop.
//   - Pass 2b (reposition, sp-s232): for a post STILL unmanned after 2a, jump-route
//     the FLEET-WIDE nearest idle satellite (fewest gate hops, via the gate-graph BFS)
//     to it as a claimed cross-gate relay, then let the next tick's 2a man it in-system.
//     A VIRGIN target (no KNOWN market waypoint to relay to) is first DISCOVERED presence-
//     free via the API and repositioned the same tick, or parked UNSERVICEABLE if it has
//     no marketplaces (sp-nn0y). Fail-closed: no gate graph, no reachable satellite, an
//     API discovery failure, or an active backoff → the post parks honest and is re-checked
//     next tick. In-system manning (2a) always wins over repositioning (2b) for the same
//     satellite, since a satellite that arrives idle in a post's system is claimed by 2a
//     before 2b runs.
func (h *RunScoutPostCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunScoutPostCoordinatorCommand) error {
	logger := common.LoggerFromContext(ctx)

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

	// Pass 1: manned posts.
	for _, post := range posts {
		if !post.IsManned() {
			continue
		}
		// REPAIR (sp-qxa4): the assigned hull is no longer in the post's system — a
		// cross-system assignment (the removed global fallback) or a satellite that
		// drifted away. Its in-system tour can never navigate the post's waypoints, so
		// it crash-respawn-loops. Release it unconditionally (even if the crash-loop is
		// momentarily RUNNING this tick): stop the tour so it is NOT respawned, free the
		// hull, and clear the assignment. Pass 2 then re-mans the post with an in-system
		// satellite or parks it. This heals the live incident at deploy — no manual
		// cleanup. Checked before the healthy-tour skip so a flickering-RUNNING loop
		// cannot slip past.
		if h.hullOutOfSystem(ctx, cmd, post) {
			_ = h.daemonClient.StopContainer(ctx, post.TourContainerID)
			h.reclaimHull(ctx, cmd, post)
			logger.Log("INFO", fmt.Sprintf("Released cross-system assignment: hull %s is not in post %s's system — returned to pool for in-system re-matching", post.AssignedHull, post.SystemSymbol), map[string]interface{}{
				"action":        "scout_post_cross_system_repair",
				"system_symbol": post.SystemSymbol,
				"ship_symbol":   post.AssignedHull,
			})
			post.AssignedHull = ""
			post.TourContainerID = ""
			if err := h.postRepo.Upsert(ctx, post); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to clear cross-system assignment on post %s: %v", post.SystemSymbol, err), nil)
			}
			continue
		}
		// A live tour is healthy — never disturb it.
		if post.TourContainerID != "" && running[post.TourContainerID] {
			continue
		}
		// A sweep-once post whose tour COMPLETED has done its one job: release the
		// hull and retire the post so its satellite flows to the next unmanned post.
		if post.Kind == domainScouting.PostKindSweepOnce && post.TourContainerID != "" && completed[post.TourContainerID] {
			h.releaseHull(ctx, cmd, post.AssignedHull, "sweep_once_complete")
			if err := h.postRepo.Remove(ctx, cmd.PlayerID.Value(), post.SystemSymbol); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to retire completed sweep-once post %s: %v", post.SystemSymbol, err), nil)
			} else {
				removed[post.SystemSymbol] = true
				logger.Log("INFO", fmt.Sprintf("Retired completed sweep-once post %s (hull %s released)", post.SystemSymbol, post.AssignedHull), map[string]interface{}{
					"action":        "scout_post_sweep_complete",
					"system_symbol": post.SystemSymbol,
				})
			}
			continue
		}
		// Otherwise the tour is dead/missing/crashed: free the hull and clear the
		// assignment. Pass 2 re-mans the post — with this same hull, since it is idle
		// in the post's system (the repair above already released any out-of-system
		// hull), so it respawns within a tick.
		h.reclaimHull(ctx, cmd, post)
		post.AssignedHull = ""
		post.TourContainerID = ""
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to clear assignment on post %s: %v", post.SystemSymbol, err), nil)
		}
	}

	// Pass 1.5: repositioning posts (sp-s232). A relay in flight (RUNNING) owns its
	// post — pass 2 skips it. When the relay is no longer RUNNING it has landed
	// (COMPLETED: the hull is idle in the post's system — pass 2a mans it), failed (the
	// hull is released wherever it stranded — pass 2b re-dispatches, backoff-gated), or
	// was restart-interrupted (the claim is preserved — reclaim frees it so pass 2b
	// re-dispatches from the hull's current position, travel() re-planning the remaining
	// hops). Reclaim defensively (a clean/failed exit already released the hull; an
	// interrupted one has not) and clear the relay reference either way.
	for _, post := range posts {
		if removed[post.SystemSymbol] || post.IsManned() || !post.IsRepositioning() {
			continue
		}
		if running[post.RepositionContainerID] {
			continue // relay airborne — leave it; pass 2 skips this post
		}
		h.reclaimRepositionHull(ctx, cmd, post)
		logger.Log("INFO", fmt.Sprintf("Scout reposition relay for post %s ended (container %s not running) — re-evaluating next tick", post.SystemSymbol, post.RepositionContainerID), map[string]interface{}{
			"action":        "scout_reposition_relay_ended",
			"system_symbol": post.SystemSymbol,
		})
		post.RepositionContainerID = ""
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to clear ended reposition relay on post %s: %v", post.SystemSymbol, err), nil)
		}
	}

	// Pass 2: man the unmanned posts, standing first. A post repositioning (relay still
	// airborne after pass 1.5) is excluded — the relay owns it until it lands or dies.
	unmanned := make([]*domainScouting.ScoutPost, 0, len(posts))
	for _, post := range posts {
		if removed[post.SystemSymbol] || post.IsManned() || post.IsRepositioning() {
			continue
		}
		unmanned = append(unmanned, post)
	}
	if len(unmanned) == 0 {
		return nil
	}
	sortPostsByPriority(unmanned)

	idleSats, err := h.idleScoutSatellites(ctx, cmd)
	if err != nil {
		return err
	}

	// Pass 2a: man every post that has an idle satellite ALREADY in its system (sp-qxa4
	// in-system-only manning). Doing this for ALL posts before any reposition guarantees
	// an in-system satellite is never repositioned AWAY from a post that could man it
	// locally — manning wins over relaying for the same hull. Posts left unmanned here
	// (no in-system satellite) fall through to 2b.
	stillUnmanned := make([]*domainScouting.ScoutPost, 0, len(unmanned))
	for _, post := range unmanned {
		idx := selectInSystemSatellite(idleSats, post.SystemSymbol)
		if idx < 0 {
			stillUnmanned = append(stillUnmanned, post)
			continue
		}

		markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to discover markets for post %s: %v", post.SystemSymbol, err), nil)
			continue
		}
		if len(markets) == 0 {
			// Nothing to scan (uncharted / no marketplace waypoints). Don't burn the
			// in-system satellite's claim on a zero-market tour — leave it idle in
			// system until the system is charted. Repositioning cannot help either (the
			// problem is markets, not hull location), so this post is NOT a 2b candidate.
			logger.Log("INFO", fmt.Sprintf("No known marketplace waypoints in %s yet — leaving post unmanned this tick", post.SystemSymbol), nil)
			continue
		}

		sat := idleSats[idx]
		idleSats = append(idleSats[:idx], idleSats[idx+1:]...)

		tourID, err := h.spawnTour(ctx, cmd, post, sat.ShipSymbol(), markets)
		if err != nil {
			// Claim rejection (a raced pin) or a transient spawn failure: the satellite
			// is consumed for this tick; the post stays unmanned and retries next tick.
			logger.Log("WARNING", fmt.Sprintf("Failed to man post %s with %s: %v", post.SystemSymbol, sat.ShipSymbol(), err), nil)
			continue
		}

		post.AssignedHull = sat.ShipSymbol()
		post.TourContainerID = tourID
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Manned post %s but failed to persist assignment: %v", post.SystemSymbol, err), nil)
		}
	}

	// Pass 2b: jump-route the fleet-wide nearest idle satellite to each still-unmanned
	// post (sp-s232). repositionUnmannedPost fails closed — no gate graph, no idle
	// satellite, no reachable satellite, no known markets, or an active backoff parks
	// the post honest — so with no gate graph wired this is exactly the pre-s232 park.
	for _, post := range stillUnmanned {
		h.repositionUnmannedPost(ctx, cmd, post, &idleSats)
	}

	return nil
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

// spawnTour persists a coordinator-managed scout_tour worker for hullSymbol,
// atomically claims the hull to it, and starts it. The persisted config carries
// coordinator_id so restart recovery skips the tour and leaves respawning to this
// coordinator. Standing posts run an infinite tour; sweep-once posts a single one.
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

// repositionUnmannedPost jump-routes the fleet-wide nearest idle satellite to a post
// with no in-system hull (sp-s232). It FAILS CLOSED at every gap — no gate graph, no
// idle satellite, an active backoff, an unserviceable/undiscoverable virgin system, or
// no jump-routable satellite — by parking the post honest and returning, so the post is
// simply re-checked next tick (nothing is spent, no hull is committed to an un-flyable
// relay). A virgin target with no KNOWN market waypoint is first discovered presence-free
// (discoverVirginMarkets, sp-nn0y) and repositioned the same tick. On success it claims
// the satellite to a new reposition container (the relay owns the hull for the whole
// flight, RULINGS #7), records the relay on the post, and arms the per-post dispatch
// backoff. idleSats is a pointer so a dispatched satellite is removed from the shared
// pool for the rest of this tick.
func (h *RunScoutPostCoordinatorHandler) repositionUnmannedPost(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	idleSats *[]*navigation.Ship,
) {
	logger := common.LoggerFromContext(ctx)

	// No gate graph wired, or no idle satellite left this tick → cannot reposition. Park
	// honest with the in-system reason (the pre-s232 / sp-qxa4 behavior and greppable
	// park message).
	if h.gateGraph == nil || len(*idleSats) == 0 {
		h.parkNoInSystemSatellite(ctx, post)
		return
	}

	// A recent relay for this post failed — don't hot-loop re-dispatch (sp-py4n).
	if h.repositionBackedOff(cmd.PlayerID.Value(), post.SystemSymbol) {
		logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: reposition backing off after a recent relay — retrying shortly", post.SystemSymbol), map[string]interface{}{
			"action":        "scout_reposition_backoff",
			"system_symbol": post.SystemSymbol,
		})
		return
	}

	// Repositioning to a system with nothing to scan is pointless, and travel() needs a
	// concrete destination waypoint — reuse the discovered markets (the same waypoints
	// the tour will scan on arrival).
	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to discover markets for reposition target %s: %v", post.SystemSymbol, err), nil)
		return
	}
	if len(markets) == 0 {
		// Virgin frontier system (sp-nn0y): the post's system has ZERO KNOWN market
		// waypoints, so there is no destination to relay to — the s232 bootstrap chicken-
		// and-egg, since nothing can scan a system no satellite can reach. DISCOVER its
		// waypoints presence-free via the API, then retry the read. discoverVirginMarkets
		// fails closed (parks honest) at every gap and arms the dispatch backoff so the
		// API is probed at most once per window, never per tick.
		markets = h.discoverVirginMarkets(ctx, cmd, post)
		if len(markets) == 0 {
			return // parked honest by discoverVirginMarkets (no discoverer / API error / unserviceable)
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
		// Claim rejection (a raced pin) or a transient spawn failure: the satellite is
		// consumed for this tick; the post stays unmanned and retries next tick.
		logger.Log("WARNING", fmt.Sprintf("Failed to dispatch reposition of %s to post %s: %v", sat.ShipSymbol(), post.SystemSymbol, err), nil)
		return
	}

	// Arm the backoff BEFORE persisting the relay reference: if the Upsert below fails,
	// the backoff still prevents an immediate second relay to this post next tick.
	h.noteRepositionDispatch(cmd.PlayerID.Value(), post.SystemSymbol)
	post.RepositionContainerID = relayID
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
// system has ZERO known market waypoints (sp-nn0y): the reposition destination IS a known
// market waypoint, and an unswept system has none, so the captain's frontier relay targets
// would park forever. It DISCOVERS the system's waypoints presence-free via the graph
// provider's cache-first GetGraph — the same fetch-through the `waypoint` verb and
// scout_markets use, which needs no local ship in the system — persisting them era-scoped
// (the unchanged BuildSystemGraph→waypointRepo.Add path, sp-vapw). It then re-reads:
//   - markets found → returns them; the caller repositions THIS tick.
//   - none found → the system is genuinely marketless: parks UNSERVICEABLE with a DISTINCT
//     reason, so the captain can tell 'not yet scanned' from 'nothing there' and remove it.
//   - API error → parks fail-closed with the pre-nn0y reason, retried next window.
//
// It arms the reposition dispatch backoff BEFORE the API call (reusing the s232 keying), so
// a marketless or API-erroring system is probed at most ONCE per window — the caller's
// pass-2b backoff check short-circuits every intervening tick (no per-tick API hammering).
// A system whose discovery finds markets is never re-discovered: the persisted rows satisfy
// the caller's discoverMarkets read directly next pass. With no graph provider wired it is a
// no-op that logs the pre-nn0y park verbatim, so every existing caller/test is unaffected.
func (h *RunScoutPostCoordinatorHandler) discoverVirginMarkets(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
) []string {
	logger := common.LoggerFromContext(ctx)

	// No discoverer wired → preserve the pre-nn0y park verbatim (its message text and
	// action still grep-match; disabled discovery cannot change existing behavior).
	if h.graphProvider == nil {
		logger.Log("INFO", fmt.Sprintf("No known marketplace waypoints in %s yet — cannot reposition (nothing to scan), post parks", post.SystemSymbol), map[string]interface{}{
			"action":        "scout_reposition_no_markets",
			"system_symbol": post.SystemSymbol,
		})
		return nil
	}

	// Arm the backoff BEFORE the API call: whether discovery finds markets, finds none, or
	// errors, this system is not probed again until the window elapses. On the found-markets
	// path the caller repositions immediately and the persisted rows short-circuit any
	// re-discovery next pass anyway, so the extra arm is harmless there.
	h.noteRepositionDispatch(cmd.PlayerID.Value(), post.SystemSymbol)

	if _, err := h.graphProvider.GetGraph(ctx, post.SystemSymbol, false, cmd.PlayerID.Value()); err != nil {
		// Fail closed: nothing spent, no hull committed — park and retry after the backoff.
		logger.Log("WARNING", fmt.Sprintf("Virgin-system waypoint discovery for reposition target %s failed: %v — post parks (fail-closed), retrying after backoff", post.SystemSymbol, err), map[string]interface{}{
			"action":        "scout_reposition_discovery_failed",
			"system_symbol": post.SystemSymbol,
		})
		return nil
	}

	// Discovery persisted the system's waypoints era-scoped — re-read the markets.
	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to re-read markets for %s after discovery: %v", post.SystemSymbol, err), nil)
		return nil
	}
	if len(markets) == 0 {
		// Discovery succeeded but the system genuinely has NO marketplaces — the post is
		// unserviceable. A distinct reason (not the 'not yet scanned' park) so the captain
		// knows the system was charted and found barren, and can remove the post.
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
// whose Path errors — a definitive unroutable verdict OR a transient store/fetch
// failure — is skipped (fail-closed: never dispatch a relay we cannot route), the gate
// graph re-fetching next tick. idleSats is pre-sorted by symbol, and the comparison is
// strict (< bestHops), so the lowest-symbol satellite wins an equal-hops tie —
// deterministic and testable.
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
// worker inherits the same restart-recovery semantics: the persisted config carries
// coordinator_id, so daemon restart marks it worker_interrupted (claim preserved) and
// leaves re-dispatch to this coordinator.
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

	// Atomic claim (l7h2): rejects a hull pinned to another fleet at the DB, so a pin
	// racing discovery can never be poached. %w so a dedication rejection is
	// distinguishable from a transient failure.
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

// reclaimRepositionHull frees any hull still assigned to a post's ended reposition
// relay (sp-s232), returning it to idle so pass 2 re-evaluates it. Best-effort and
// DB-only — the reclaimHull pattern, keyed on the relay container instead of the tour.
// A clean or failed relay exit already released the hull (this is a no-op then); a
// restart-interrupted relay did not (this is what frees it).
func (h *RunScoutPostCoordinatorHandler) reclaimRepositionHull(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost) {
	logger := common.LoggerFromContext(ctx)
	if post.RepositionContainerID == "" {
		return
	}
	ships, err := h.shipRepo.FindByContainer(ctx, post.RepositionContainerID, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to load ships for ended reposition relay %s: %v", post.RepositionContainerID, err), nil)
		return
	}
	for _, ship := range ships {
		if !ship.IsAssigned() {
			continue
		}
		ship.ForceRelease("scout_reposition_ended", h.clock)
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to reclaim satellite %s from ended relay %s: %v", ship.ShipSymbol(), post.RepositionContainerID, err), nil)
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Reclaimed satellite %s from ended reposition relay %s", ship.ShipSymbol(), post.RepositionContainerID), nil)
	}
}

// parkNoInSystemSatellite logs the honest, system-scoped park reason for an unmanned
// post that has no in-system satellite and cannot be repositioned (no gate graph, or no
// idle satellite left this tick). The message text is preserved verbatim from the
// pre-s232 park so `container logs` greps and the sp-qxa4 park assertions still match.
func (h *RunScoutPostCoordinatorHandler) parkNoInSystemSatellite(ctx context.Context, post *domainScouting.ScoutPost) {
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Scout post %s unmanned: no in-system satellite — reposition one or wait", post.SystemSymbol), map[string]interface{}{
		"action":        "scout_post_unmanned_no_in_system_satellite",
		"system_symbol": post.SystemSymbol,
	})
}

// repositionBackedOff reports whether a reposition dispatch for (playerID, system) is
// currently within its backoff window (sp-s232). A nil/absent entry reads false.
func (h *RunScoutPostCoordinatorHandler) repositionBackedOff(playerID int, system string) bool {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	until, ok := h.repositionBackoffUntil[backoffKey(playerID, system)]
	return ok && h.clock.Now().Before(until)
}

// noteRepositionDispatch arms the per-post dispatch backoff (sp-s232) so the next
// dispatch for this post waits out repositionRetryBackoff — the anti-hot-loop bound.
func (h *RunScoutPostCoordinatorHandler) noteRepositionDispatch(playerID int, system string) {
	h.repositionMu.Lock()
	defer h.repositionMu.Unlock()
	if h.repositionBackoffUntil == nil {
		h.repositionBackoffUntil = make(map[string]time.Time)
	}
	h.repositionBackoffUntil[backoffKey(playerID, system)] = h.clock.Now().Add(repositionRetryBackoff)
}

// backoffKey scopes the reposition backoff to (playerID, system) so one player's relay
// to system S never rate-limits another player's post in the same-named system.
func backoffKey(playerID int, system string) string {
	return fmt.Sprintf("%d|%s", playerID, system)
}

// pickRepositionDestination chooses the reposition target waypoint from a post's
// discovered markets — the lexicographically smallest, so the destination (and thus the
// dispatch log and the tests) is deterministic. Any market in the system serves: the
// relay lands the hull there and the next in-system tick's tour rotates its scan to
// start from wherever the hull sits. The caller has already ensured markets is non-empty.
func pickRepositionDestination(markets []string) string {
	best := markets[0]
	for _, m := range markets[1:] {
		if m < best {
			best = m
		}
	}
	return best
}

// hullOutOfSystem reports whether a manned post's assigned hull is currently NOT in
// the post's system — the cross-system-assignment defect the repair pass heals
// (sp-qxa4). It fails safe: a hull that cannot be loaded, or whose location is
// unknown, is treated as in-system so a transient lookup gap never triggers a
// spurious release. An unmanned post is trivially not out-of-system.
func (h *RunScoutPostCoordinatorHandler) hullOutOfSystem(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost) bool {
	if post.AssignedHull == "" {
		return false
	}
	ship, err := h.shipRepo.FindBySymbol(ctx, post.AssignedHull, cmd.PlayerID)
	if err != nil {
		return false // unknown hull — never release on a lookup failure
	}
	loc := ship.CurrentLocation()
	if loc == nil {
		return false // unknown location — conservative, leave the assignment alone
	}
	return loc.SystemSymbol != post.SystemSymbol
}

// reclaimHull frees any ship still assigned to a post's (now dead) tour container,
// returning it to idle so pass 2 can re-claim it. Best-effort and DB-only — the
// contract ReclaimShipsFromInterruptedWorkers pattern.
func (h *RunScoutPostCoordinatorHandler) reclaimHull(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost) {
	logger := common.LoggerFromContext(ctx)
	if post.TourContainerID == "" {
		return
	}
	ships, err := h.shipRepo.FindByContainer(ctx, post.TourContainerID, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to load ships for dead tour %s: %v", post.TourContainerID, err), nil)
		return
	}
	for _, ship := range ships {
		if !ship.IsAssigned() {
			continue
		}
		ship.ForceRelease("scout_post_respawn", h.clock)
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to reclaim hull %s from dead tour %s: %v", ship.ShipSymbol(), post.TourContainerID, err), nil)
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Reclaimed hull %s from dead tour %s", ship.ShipSymbol(), post.TourContainerID), nil)
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
// (sp-qxa4, the 9hu8/#14 in-system class): the scout_tour worker navigates in-system
// only (no multi-jump repositioning), so a cross-system assignment crash-respawn-
// loops. A post with no in-system satellite is UNSELECTABLE — the caller parks it
// with a reason rather than dispatching it to a crash. idleSats is pre-sorted, so the
// choice is deterministic. (The captain repositions satellites manually for now;
// jump-routing repositioning is a possible v2, deliberately not built here.)
func selectInSystemSatellite(idleSats []*navigation.Ship, systemSymbol string) int {
	for i, sat := range idleSats {
		if sat.CurrentLocation() != nil && sat.CurrentLocation().SystemSymbol == systemSymbol {
			return i
		}
	}
	return -1
}

package commands

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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

// RunScoutPostCoordinatorHandler reconciles the desired-state posts table every
// tick: it respawns any post whose tour died, mans unmanned posts by claiming an
// idle satellite ALREADY IN THE POST'S SYSTEM (matching is in-system-only, sp-qxa4),
// releases any assignment whose hull has drifted out of the post's system so it can
// be re-matched, retires completed sweep-once posts, parks a post with no in-system
// satellite (honest reason, re-checked every tick), and never poaches a pinned hull.
// It is the freshness backbone the tour planner's age cap and the analyst board both
// ride on.
type RunScoutPostCoordinatorHandler struct {
	postRepo       domainScouting.ScoutPostRepository
	shipRepo       navigation.ShipRepository
	daemonClient   daemon.DaemonClient
	containerQuery ContainerStatusQuery
	marketProvider MarketWaypointProvider
	clock          shared.Clock
}

// NewRunScoutPostCoordinatorHandler wires the coordinator. clock defaults to the
// real clock when nil (production).
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
		postRepo:       postRepo,
		shipRepo:       shipRepo,
		daemonClient:   daemonClient,
		containerQuery: containerQuery,
		marketProvider: marketProvider,
		clock:          clock,
	}
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
// Two passes:
//   - Pass 1 (manned posts): release any post whose assigned hull is no longer in
//     the post's system (the sp-qxa4 cross-system defect — stop its tour, free the
//     hull, clear the assignment); retire a completed sweep-once (release its hull,
//     delete the post); free the hull of any other post whose tour is not running,
//     clearing the assignment so pass 2 re-mans it. A healthy in-system tour is left
//     untouched.
//   - Pass 2 (unmanned posts): standing posts before sweep-once, claim an idle
//     satellite ALREADY IN THE POST'S SYSTEM and spawn its tour. A post with no
//     in-system satellite parks unmanned with an honest reason and is re-checked
//     next tick (self-heals when a satellite arrives). Cross-system matching is
//     never done — the tour navigates in-system only, so it would crash-loop.
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

	// Pass 2: man the unmanned posts, standing first.
	unmanned := make([]*domainScouting.ScoutPost, 0, len(posts))
	for _, post := range posts {
		if removed[post.SystemSymbol] || post.IsManned() {
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

	for _, post := range unmanned {
		if len(idleSats) == 0 {
			break // no idle satellites left this tick — remaining posts wait
		}

		// In-system-only matching (sp-qxa4): a post can only be manned by an idle
		// satellite already in its system. The scout_tour worker navigates in-system
		// only, so handing it a cross-system hull just crash-respawn-loops. No match
		// among the (non-empty) idle pool means satellites exist but are stranded in
		// other systems: park the post with an honest, actionable reason and re-check
		// next tick. It self-heals the moment a satellite arrives in-system.
		idx := selectInSystemSatellite(idleSats, post.SystemSymbol)
		if idx < 0 {
			logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: no in-system satellite — reposition one or wait", post.SystemSymbol), map[string]interface{}{
				"action":        "scout_post_unmanned_no_in_system_satellite",
				"system_symbol": post.SystemSymbol,
			})
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
			// system until the system is charted.
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

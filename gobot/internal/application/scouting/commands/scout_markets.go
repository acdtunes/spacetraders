package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ScoutMarketsCommand orchestrates fleet deployment for market scouting
// Uses VRP optimization to distribute markets across multiple ships
// Transactional reset (sp-8k9m): it re-partitions every requested hull, computing the
// whole re-man plan before tearing any existing tour down, so a failure never strands a
// post (see ScoutMarketsHandler.Handle).
type ScoutMarketsCommand struct {
	PlayerID     shared.PlayerID
	ShipSymbols  []string
	SystemSymbol string
	Markets      []string
	Iterations   int // Number of iterations (-1 for infinite)
}

// ScoutMarketsResponse contains container IDs and market assignments
type ScoutMarketsResponse struct {
	ContainerIDs     []string            // All container IDs (new + reused)
	Assignments      map[string][]string // ship_symbol -> markets assigned
	ReusedContainers []string            // Subset of ContainerIDs that were reused
}

// ScoutMarketsHandler handles the scout markets command
type ScoutMarketsHandler struct {
	shipRepo      navigation.ShipRepository
	graphProvider system.ISystemGraphProvider
	routingClient routing.RoutingClient
	daemonClient  daemon.DaemonClient
	clock         shared.Clock
}

// NewScoutMarketsHandler creates a new scout markets handler
func NewScoutMarketsHandler(
	shipRepo navigation.ShipRepository,
	graphProvider system.ISystemGraphProvider,
	routingClient routing.RoutingClient,
	daemonClient daemon.DaemonClient,
	clock shared.Clock,
) *ScoutMarketsHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &ScoutMarketsHandler{
		shipRepo:      shipRepo,
		graphProvider: graphProvider,
		routingClient: routingClient,
		daemonClient:  daemonClient,
		clock:         clock,
	}
}

// Handle executes the scout markets command as a TRANSACTIONAL reset (sp-8k9m): it
// re-partitions every requested hull over the system's markets, tearing the old tours
// down and spawning fresh ones. The teardown is the last thing it does, never the first.
//
// The prior order stopped-and-released every hull UP FRONT, then did the fallible re-man
// work (market check, ship-config load, graph read, VRP partition). Any failure after that
// unconditional teardown — an empty market set, a missing hull, an unreadable graph, a VRP
// error — left the system dark with nothing to re-man it (C81/SN21 went dark exactly this
// way). Here the whole re-man PLAN is computed first, read-only; only once it is in hand —
// so the re-man is guaranteed — are the old containers stopped and the new tours spawned.
// A failure in the planning phase aborts with an honest error and NOTHING has been torn
// down, so the existing posts keep running (verified-respawn-before-stop). Spawn-new-then-
// stop is not usable here because a scout-tour claims its hull at creation, and a hull
// cannot be claimed by two containers at once, so the old claim must be released first.
func (h *ScoutMarketsHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*ScoutMarketsCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// An empty market set is a no-op reset — there is nothing to re-man toward, so it must
	// not tear down the existing posts (the pre-fix code stopped them, THEN early-returned).
	if len(cmd.Markets) == 0 {
		return &ScoutMarketsResponse{
			ContainerIDs:     []string{},
			Assignments:      make(map[string][]string),
			ReusedContainers: []string{},
		}, nil
	}

	// PLAN (read-only, fallible): compute the full re-man plan for every requested hull
	// BEFORE tearing anything down. A reset repartitions all hulls, so every requested ship
	// is (re)assigned here. Any failure returns an honest error with nothing stopped or
	// released — the old posts keep running.
	shipConfigs, err := h.loadShipConfigurations(ctx, cmd.ShipSymbols, cmd.PlayerID)
	if err != nil {
		return nil, err
	}

	waypointData, err := h.loadSystemGraph(ctx, cmd.SystemSymbol, uint(cmd.PlayerID.Value()))
	if err != nil {
		return nil, err
	}

	assignments, err := h.calculateMarketAssignments(ctx, cmd.ShipSymbols, cmd.Markets, shipConfigs, waypointData)
	if err != nil {
		return nil, err
	}

	// A degenerate plan — no ship assigned ANY market (e.g. a VRP that returned an empty
	// partition rather than erroring) — is NOT a re-man (sp-8k9m). Refuse LOUDLY here,
	// before any teardown: tearing the posts down for a zero-market plan and reporting
	// success is the false-success class the captain flagged (a reset that reads "complete"
	// while it darkened the system). The old posts keep running.
	if totalAssignedMarkets(assignments) == 0 {
		return nil, fmt.Errorf("scout reset for %s computed no market assignments across %d ship(s) over %d market(s) — refusing to tear down existing posts for an empty re-man", cmd.SystemSymbol, len(cmd.ShipSymbols), len(cmd.Markets))
	}

	// Refuse a CROSS-SYSTEM assignment at the spawn seam (sp-8k9m finding f). A scout tour
	// is IN-SYSTEM by design — a scout post is per-system, and crossing gates is the
	// reconciler's ferry job, not a tour's. NavigateRoute is in-system, so a hull handed a
	// market in another system crash-loops on that waypoint ("not found in cache for system
	// <origin>") and sits claimed but idle — the 7 KN67 probes stuck touring PA62 markets.
	// Refuse LOUDLY here (before any teardown) rather than spawn a doomed tour: the hull must
	// be repositioned into the target system first, then manned in-system.
	if err := validateInSystemAssignments(assignments, shipConfigs); err != nil {
		return nil, err
	}

	// COMMIT (destructive): the plan is in hand, so the re-man is guaranteed. Only now stop
	// the old tours + release the hulls, then spawn the new tours over the fresh plan.
	if err := h.stopExistingContainers(ctx, cmd); err != nil {
		return nil, err
	}

	newContainerIDs, err := h.createScoutContainers(ctx, assignments, cmd)
	if err != nil {
		return nil, err
	}

	return &ScoutMarketsResponse{
		ContainerIDs:     newContainerIDs,
		Assignments:      assignments,
		ReusedContainers: []string{},
	}, nil
}

// stopExistingContainers stops all existing scouting containers and releases ship assignments
func (h *ScoutMarketsHandler) stopExistingContainers(ctx context.Context, cmd *ScoutMarketsCommand) error {
	logger := common.LoggerFromContext(ctx)

	for _, shipSymbol := range cmd.ShipSymbols {
		ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
		}

		if ship.IsAssigned() {
			containerID := ship.ContainerID()
			logger.Log("INFO", "Stopping existing scout container for reset", map[string]interface{}{
				"ship_symbol":  shipSymbol,
				"action":       "stop_existing_container",
				"container_id": containerID,
				"reason":       "scout_all_markets_reset",
			})

			if err := h.daemonClient.StopContainer(ctx, containerID); err != nil {
				logger.Log("WARNING", "Scout container stop failed", map[string]interface{}{
					"ship_symbol":  shipSymbol,
					"action":       "stop_container",
					"container_id": containerID,
					"error":        err.Error(),
				})
			}

			// Release ship assignment using Ship aggregate
			ship.ForceRelease("scout_all_markets_reset", h.clock)
			if err := h.shipRepo.Save(ctx, ship); err != nil {
				logger.Log("WARNING", "Ship assignment release failed", map[string]interface{}{
					"ship_symbol": shipSymbol,
					"action":      "release_ship",
					"error":       err.Error(),
				})
			}
		}
	}

	return nil
}

// loadShipConfigurations loads ship data and prepares routing configurations
func (h *ScoutMarketsHandler) loadShipConfigurations(
	ctx context.Context,
	shipSymbols []string,
	playerID shared.PlayerID,
) (map[string]*routing.ShipConfigData, error) {
	// OPTIMIZATION: Fetch all ships from cached list (1 API call instead of N)
	// The ship list is cached for 15 seconds in ShipRepository.FindAllByPlayer
	allShips, err := h.shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load ships: %w", err)
	}

	// Build lookup set for efficient filtering
	symbolSet := make(map[string]bool, len(shipSymbols))
	for _, s := range shipSymbols {
		symbolSet[s] = true
	}

	// Filter to only requested ships and build config map
	shipConfigs := make(map[string]*routing.ShipConfigData)
	for _, ship := range allShips {
		if symbolSet[ship.ShipSymbol()] {
			shipConfigs[ship.ShipSymbol()] = &routing.ShipConfigData{
				CurrentLocation: ship.CurrentLocation().Symbol,
				FuelCapacity:    ship.FuelCapacity(),
				EngineSpeed:     ship.EngineSpeed(),
			}
		}
	}

	// Verify we found all requested ships
	if len(shipConfigs) != len(shipSymbols) {
		for _, symbol := range shipSymbols {
			if _, found := shipConfigs[symbol]; !found {
				return nil, fmt.Errorf("ship %s not found in fleet", symbol)
			}
		}
	}

	return shipConfigs, nil
}

// loadSystemGraph fetches the navigation graph and converts waypoints to routing format
func (h *ScoutMarketsHandler) loadSystemGraph(
	ctx context.Context,
	systemSymbol string,
	playerID uint,
) ([]*system.WaypointData, error) {
	graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, int(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to get graph: %w", err)
	}

	waypointData, err := extractWaypointData(graphResult.Graph)
	if err != nil {
		return nil, fmt.Errorf("failed to extract waypoint data: %w", err)
	}

	return waypointData, nil
}

// calculateMarketAssignments determines which ships should visit which markets
func (h *ScoutMarketsHandler) calculateMarketAssignments(
	ctx context.Context,
	shipsNeedingContainers []string,
	markets []string,
	shipConfigs map[string]*routing.ShipConfigData,
	waypointData []*system.WaypointData,
) (map[string][]string, error) {
	if len(shipsNeedingContainers) == 1 {
		return map[string][]string{
			shipsNeedingContainers[0]: markets,
		}, nil
	}

	vrpRequest := &routing.VRPRequest{
		SystemSymbol:    "",
		ShipSymbols:     shipsNeedingContainers,
		MarketWaypoints: markets,
		ShipConfigs:     shipConfigs,
		AllWaypoints:    waypointData,
	}

	vrpResponse, err := h.routingClient.PartitionFleet(ctx, vrpRequest)
	if err != nil {
		return nil, fmt.Errorf("VRP optimization failed: %w", err)
	}

	assignments := make(map[string][]string)
	for shipSymbol, tourData := range vrpResponse.Assignments {
		assignments[shipSymbol] = tourData.Waypoints
	}

	return assignments, nil
}

// createScoutContainers creates container instances for each ship assignment
func (h *ScoutMarketsHandler) createScoutContainers(
	ctx context.Context,
	assignments map[string][]string,
	cmd *ScoutMarketsCommand,
) ([]string, error) {
	newContainerIDs := []string{}

	for shipSymbol, markets := range assignments {
		containerID := utils.GenerateContainerID("scout-tour", shipSymbol)

		scoutTourCmd := &ScoutTourCommand{
			PlayerID:   cmd.PlayerID,
			ShipSymbol: shipSymbol,
			Markets:    markets,
			Iterations: cmd.Iterations,
		}

		err := h.daemonClient.CreateScoutTourContainer(ctx, containerID, uint(cmd.PlayerID.Value()), scoutTourCmd)
		if err != nil {
			return nil, fmt.Errorf("failed to create container for %s: %w", shipSymbol, err)
		}

		newContainerIDs = append(newContainerIDs, containerID)
	}

	return newContainerIDs, nil
}

// validateInSystemAssignments refuses any ship→markets assignment that leaves the ship's
// own system (sp-8k9m finding f): a scout tour navigates in-system only, so a cross-system
// market waypoint is a doomed, crash-looping tour. Returns a loud error naming the ship, its
// system, and the offending waypoint; nil when every assignment is in-system (the normal
// case, since scout-markets is a per-system verb).
func validateInSystemAssignments(assignments map[string][]string, shipConfigs map[string]*routing.ShipConfigData) error {
	for ship, markets := range assignments {
		cfg, ok := shipConfigs[ship]
		if !ok || cfg == nil {
			return fmt.Errorf("scout reset has no location for ship %s — cannot verify its markets are in-system", ship)
		}
		shipSystem := shared.ExtractSystemSymbol(cfg.CurrentLocation)
		for _, market := range markets {
			if marketSystem := shared.ExtractSystemSymbol(market); marketSystem != shipSystem {
				return fmt.Errorf("scout reset would assign %s (in %s) a cross-system market %s (in %s) — a scout tour navigates in-system only; reposition the hull into %s first, then man it (sp-8k9m)", ship, shipSystem, market, marketSystem, marketSystem)
			}
		}
	}
	return nil
}

// totalAssignedMarkets sums the market waypoints assigned across all ships — 0 means the
// plan would man nothing, the degenerate-reset guard's trigger (sp-8k9m).
func totalAssignedMarkets(assignments map[string][]string) int {
	n := 0
	for _, markets := range assignments {
		n += len(markets)
	}
	return n
}

// extractWaypointData converts graph format to routing waypoint data
func extractWaypointData(graph *system.NavigationGraph) ([]*system.WaypointData, error) {
	waypointData := make([]*system.WaypointData, 0, len(graph.Waypoints))

	for symbol, waypoint := range graph.Waypoints {
		waypointData = append(waypointData, &system.WaypointData{
			Symbol:  symbol,
			X:       waypoint.X,
			Y:       waypoint.Y,
			HasFuel: waypoint.HasFuel,
		})
	}

	return waypointData, nil
}

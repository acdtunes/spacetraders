package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ScoutMarketsCommand orchestrates fleet deployment for market scouting
// Uses VRP optimization to distribute markets across multiple ships
// Idempotent: reuses existing containers for ships that already have them
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
	shipRepo           navigation.ShipRepository
	graphProvider      system.ISystemGraphProvider
	routingClient      routing.RoutingClient
	daemonClient       daemon.DaemonClient
	shipAssignmentRepo container.ShipAssignmentRepository
}

// NewScoutMarketsHandler creates a new scout markets handler
func NewScoutMarketsHandler(
	shipRepo navigation.ShipRepository,
	graphProvider system.ISystemGraphProvider,
	routingClient routing.RoutingClient,
	daemonClient daemon.DaemonClient,
	shipAssignmentRepo container.ShipAssignmentRepository,
) *ScoutMarketsHandler {
	return &ScoutMarketsHandler{
		shipRepo:           shipRepo,
		graphProvider:      graphProvider,
		routingClient:      routingClient,
		daemonClient:       daemonClient,
		shipAssignmentRepo: shipAssignmentRepo,
	}
}

// Handle executes the scout markets command
func (h *ScoutMarketsHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*ScoutMarketsCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// Cleanup phase
	if err := h.stopExistingContainers(ctx, cmd); err != nil {
		return nil, err
	}

	// Identify reuse opportunities
	shipsWithContainers, reusedContainers, shipsNeedingContainers, err := h.identifyContainerReuse(ctx, cmd.ShipSymbols, cmd.PlayerID)
	if err != nil {
		return nil, err
	}

	// Early return if all ships already have containers
	if response, shouldReturn, err := h.handleAllShipsHaveContainers(shipsWithContainers, reusedContainers); shouldReturn {
		return response, err
	}

	// Early return if no markets to scout
	if len(cmd.Markets) == 0 {
		return &ScoutMarketsResponse{
			ContainerIDs:     reusedContainers,
			Assignments:      make(map[string][]string),
			ReusedContainers: reusedContainers,
		}, nil
	}

	// Load ship data & graph
	shipConfigs, err := h.loadShipConfigurations(ctx, shipsNeedingContainers, cmd.PlayerID)
	if err != nil {
		return nil, err
	}

	waypointData, err := h.loadSystemGraph(ctx, cmd.SystemSymbol, uint(cmd.PlayerID.Value()))
	if err != nil {
		return nil, err
	}

	// Calculate assignments
	assignments, err := h.calculateMarketAssignments(ctx, shipsNeedingContainers, cmd.Markets, shipConfigs, waypointData)
	if err != nil {
		return nil, err
	}

	// Create containers
	newContainerIDs, err := h.createScoutContainers(ctx, assignments, cmd)
	if err != nil {
		return nil, err
	}

	return h.buildFinalResponse(reusedContainers, newContainerIDs, assignments, shipsWithContainers), nil
}

// stopExistingContainers stops all existing scouting containers and releases ship assignments
func (h *ScoutMarketsHandler) stopExistingContainers(ctx context.Context, cmd *ScoutMarketsCommand) error {
	logger := common.LoggerFromContext(ctx)

	for _, shipSymbol := range cmd.ShipSymbols {
		assignment, err := h.queryShipAssignment(ctx, shipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to query ship assignment for %s: %w", shipSymbol, err)
		}

		if assignment != nil && assignment.Status() == "active" {
			containerID := assignment.ContainerID()
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

			if err := h.shipAssignmentRepo.Release(ctx, shipSymbol, int(cmd.PlayerID.Value()), "scout_all_markets_reset"); err != nil {
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

// queryShipAssignment queries the database for a ship's current container assignment
func (h *ScoutMarketsHandler) queryShipAssignment(ctx context.Context, shipSymbol string, playerID shared.PlayerID) (*container.ShipAssignment, error) {
	return h.shipAssignmentRepo.FindByShip(ctx, shipSymbol, int(playerID.Value()))
}

// identifyContainerReuse determines which ships can reuse existing containers vs need new ones
func (h *ScoutMarketsHandler) identifyContainerReuse(
	ctx context.Context,
	shipSymbols []string,
	playerID shared.PlayerID,
) (map[string]string, []string, []string, error) {
	shipsWithContainers := make(map[string]string)
	reusedContainers := []string{}

	for _, shipSymbol := range shipSymbols {
		assignment, err := h.queryShipAssignment(ctx, shipSymbol, playerID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to query ship assignment for %s: %w", shipSymbol, err)
		}

		if assignment != nil && assignment.Status() == "active" {
			shipsWithContainers[shipSymbol] = assignment.ContainerID()
			reusedContainers = append(reusedContainers, assignment.ContainerID())
		}
	}

	shipsNeedingContainers := []string{}
	for _, ship := range shipSymbols {
		if _, exists := shipsWithContainers[ship]; !exists {
			shipsNeedingContainers = append(shipsNeedingContainers, ship)
		}
	}

	return shipsWithContainers, reusedContainers, shipsNeedingContainers, nil
}

// handleAllShipsHaveContainers handles the early return scenario when all ships already have containers
func (h *ScoutMarketsHandler) handleAllShipsHaveContainers(
	shipsWithContainers map[string]string,
	reusedContainers []string,
) (*ScoutMarketsResponse, bool, error) {
	if len(shipsWithContainers) == 0 {
		return nil, false, nil
	}

	allContainerIDs := []string{}
	assignments := make(map[string][]string)
	for ship, containerID := range shipsWithContainers {
		allContainerIDs = append(allContainerIDs, containerID)
		assignments[ship] = []string{}
	}

	return &ScoutMarketsResponse{
		ContainerIDs:     allContainerIDs,
		Assignments:      assignments,
		ReusedContainers: reusedContainers,
	}, true, nil
}

// loadShipConfigurations loads ship data and prepares routing configurations
func (h *ScoutMarketsHandler) loadShipConfigurations(
	ctx context.Context,
	shipSymbols []string,
	playerID shared.PlayerID,
) (map[string]*routing.ShipConfigData, error) {
	shipConfigs := make(map[string]*routing.ShipConfigData)

	for _, shipSymbol := range shipSymbols {
		shipData, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
		}

		shipConfigs[shipSymbol] = &routing.ShipConfigData{
			CurrentLocation: shipData.CurrentLocation().Symbol,
			FuelCapacity:    shipData.FuelCapacity(),
			EngineSpeed:     shipData.EngineSpeed(),
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

// buildFinalResponse assembles the final response with all container and assignment details
func (h *ScoutMarketsHandler) buildFinalResponse(
	reusedContainerIDs []string,
	newContainerIDs []string,
	assignments map[string][]string,
	shipsWithContainers map[string]string,
) *ScoutMarketsResponse {
	allContainerIDs := append(reusedContainerIDs, newContainerIDs...)

	for ship := range shipsWithContainers {
		if _, exists := assignments[ship]; !exists {
			assignments[ship] = []string{}
		}
	}

	return &ScoutMarketsResponse{
		ContainerIDs:     allContainerIDs,
		Assignments:      assignments,
		ReusedContainers: reusedContainerIDs,
	}
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

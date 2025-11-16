package scouting

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// ScoutMarketsCommand orchestrates fleet deployment for market scouting
// Uses VRP optimization to distribute markets across multiple ships
// Idempotent: reuses existing containers for ships that already have them
type ScoutMarketsCommand struct {
	PlayerID     uint
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
	shipRepo               navigation.ShipRepository
	graphProvider          system.ISystemGraphProvider
	routingClient          routing.RoutingClient
	daemonClient           daemon.DaemonClient
	shipAssignmentRepo     daemon.ShipAssignmentRepository
}

// NewScoutMarketsHandler creates a new scout markets handler
func NewScoutMarketsHandler(
	shipRepo navigation.ShipRepository,
	graphProvider system.ISystemGraphProvider,
	routingClient routing.RoutingClient,
	daemonClient daemon.DaemonClient,
	shipAssignmentRepo daemon.ShipAssignmentRepository,
) *ScoutMarketsHandler {
	return &ScoutMarketsHandler{
		shipRepo:               shipRepo,
		graphProvider:          graphProvider,
		routingClient:          routingClient,
		daemonClient:           daemonClient,
		shipAssignmentRepo:     shipAssignmentRepo,
	}
}

// Handle executes the scout markets command
func (h *ScoutMarketsHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*ScoutMarketsCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Query existing containers from daemon
	containers, err := h.daemonClient.ListContainers(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	// 2. Parse container IDs to find reusable scout-tour containers
	scoutContainerPattern := regexp.MustCompile(`^scout-tour-([a-z0-9-]+)-[a-f0-9]+$`)
	shipsWithContainers := make(map[string]string) // ship -> container_id
	reusedContainers := []string{}

	for _, container := range containers {
		if container.Status == "RUNNING" || container.Status == "STARTING" {
			matches := scoutContainerPattern.FindStringSubmatch(container.ID)
			if len(matches) == 2 {
				shipSymbol := strings.ToUpper(matches[1])
				// Normalize ship symbol (handle hyphens)
				shipSymbol = strings.ReplaceAll(shipSymbol, "-", "-")

				if _, exists := shipsWithContainers[shipSymbol]; !exists {
					shipsWithContainers[shipSymbol] = container.ID
					reusedContainers = append(reusedContainers, container.ID)
				}
			}
		}
	}

	// 3. Partition ships: with_containers vs needing_containers
	shipsNeedingContainers := []string{}
	for _, ship := range cmd.ShipSymbols {
		if _, exists := shipsWithContainers[ship]; !exists {
			shipsNeedingContainers = append(shipsNeedingContainers, ship)
		}
	}

	// 4. Early return if all ships have containers
	if len(shipsNeedingContainers) == 0 {
		allContainerIDs := []string{}
		assignments := make(map[string][]string)
		for ship, containerID := range shipsWithContainers {
			allContainerIDs = append(allContainerIDs, containerID)
			assignments[ship] = []string{} // Unknown assignments for reused
		}

		return &ScoutMarketsResponse{
			ContainerIDs:     allContainerIDs,
			Assignments:      assignments,
			ReusedContainers: reusedContainers,
		}, nil
	}

	// 5. Load ships and get current locations + specs
	shipConfigs := make(map[string]*routing.ShipConfigData)

	for _, shipSymbol := range shipsNeedingContainers {
		shipData, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, int(cmd.PlayerID))
		if err != nil {
			return nil, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
		}

		shipConfigs[shipSymbol] = &routing.ShipConfigData{
			CurrentLocation: shipData.CurrentLocation().Symbol,
			FuelCapacity:    shipData.FuelCapacity(),
			EngineSpeed:     shipData.EngineSpeed(),
		}
	}

	// 6. Get system graph
	graphResult, err := h.graphProvider.GetGraph(ctx, cmd.SystemSymbol, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get graph: %w", err)
	}

	// Convert graph to waypoint data
	waypointData, err := extractWaypointData(graphResult.Graph)
	if err != nil {
		return nil, fmt.Errorf("failed to extract waypoint data: %w", err)
	}

	// 7. Run VRP optimization
	fmt.Printf("[DEBUG ScoutMarkets] Ships needing containers: %d, Markets: %v\n", len(shipsNeedingContainers), cmd.Markets)
	var assignments map[string][]string
	if len(shipsNeedingContainers) == 1 {
		// Single ship: assign all markets
		fmt.Printf("[DEBUG ScoutMarkets] Single ship path - assigning all markets to %s\n", shipsNeedingContainers[0])
		assignments = map[string][]string{
			shipsNeedingContainers[0]: cmd.Markets,
		}
	} else {
		// Multi-ship: use VRP via routing client
		fmt.Printf("[DEBUG ScoutMarkets] Multi-ship path - calling VRP for %d ships and %d markets\n", len(shipsNeedingContainers), len(cmd.Markets))
		vrpRequest := &routing.VRPRequest{
			SystemSymbol:    cmd.SystemSymbol,
			ShipSymbols:     shipsNeedingContainers,
			MarketWaypoints: cmd.Markets,
			ShipConfigs:     shipConfigs,
			AllWaypoints:    waypointData,
		}

		vrpResponse, err := h.routingClient.PartitionFleet(ctx, vrpRequest)
		if err != nil {
			return nil, fmt.Errorf("VRP optimization failed: %w", err)
		}

		// Extract market assignments from VRP response
		assignments = make(map[string][]string)
		for shipSymbol, tourData := range vrpResponse.Assignments {
			fmt.Printf("[DEBUG ScoutMarkets] VRP assigned %d markets to %s: %v\n", len(tourData.Waypoints), shipSymbol, tourData.Waypoints)
			assignments[shipSymbol] = tourData.Waypoints
		}
	}

	// 8. Create scout-tour containers for ships needing them
	newContainerIDs := []string{}
	for shipSymbol, markets := range assignments {
		containerID := fmt.Sprintf("scout-tour-%s-%s",
			strings.ToLower(shipSymbol),
			generateShortUUID())

		scoutTourCmd := &ScoutTourCommand{
			PlayerID:   cmd.PlayerID,
			ShipSymbol: shipSymbol,
			Markets:    markets,
			Iterations: cmd.Iterations,
		}

		err := h.daemonClient.CreateScoutTourContainer(ctx, containerID, cmd.PlayerID, scoutTourCmd)
		if err != nil {
			return nil, fmt.Errorf("failed to create container for %s: %w", shipSymbol, err)
		}

		// Persist ship assignment to database
		assignment := daemon.NewShipAssignment(shipSymbol, int(cmd.PlayerID), containerID, nil)
		if err := h.shipAssignmentRepo.Insert(ctx, assignment); err != nil {
			return nil, fmt.Errorf("failed to persist ship assignment for %s: %w", shipSymbol, err)
		}

		newContainerIDs = append(newContainerIDs, containerID)
	}

	// 9. Combine results
	allContainerIDs := append(reusedContainers, newContainerIDs...)

	// Add reused containers to assignments (with empty markets list)
	for ship := range shipsWithContainers {
		if _, exists := assignments[ship]; !exists {
			assignments[ship] = []string{}
		}
	}

	return &ScoutMarketsResponse{
		ContainerIDs:     allContainerIDs,
		Assignments:      assignments,
		ReusedContainers: reusedContainers,
	}, nil
}

// generateShortUUID generates an 8-character hex UUID
func generateShortUUID() string {
	id := uuid.New()
	// Use first 8 characters of the UUID string (without hyphens)
	return strings.ReplaceAll(id.String(), "-", "")[:8]
}

// extractWaypointData converts graph format to routing waypoint data
func extractWaypointData(graph map[string]interface{}) ([]*system.WaypointData, error) {
	waypoints, ok := graph["waypoints"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid graph format: missing waypoints")
	}

	waypointData := make([]*system.WaypointData, 0, len(waypoints))
	for symbol, data := range waypoints {
		wpMap, ok := data.(map[string]interface{})
		if !ok {
			continue
		}

		x, _ := wpMap["x"].(float64)
		y, _ := wpMap["y"].(float64)
		hasFuel, _ := wpMap["has_fuel"].(bool)

		waypointData = append(waypointData, &system.WaypointData{
			Symbol:  symbol,
			X:       x,
			Y:       y,
			HasFuel: hasFuel,
		})
	}

	return waypointData, nil
}

package scouting

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// AssignScoutingFleetCommand automatically assigns all probe/satellite ships to market scouting
// Filters out FUEL_STATION marketplaces
type AssignScoutingFleetCommand struct {
	PlayerID     uint
	SystemSymbol string
}

// AssignScoutingFleetResponse contains the results of fleet assignment
type AssignScoutingFleetResponse struct {
	AssignedShips    []string            // Ship symbols assigned to scouting
	Assignments      map[string][]string // ship_symbol -> markets assigned
	ReusedContainers []string            // Container IDs that were reused
	ContainerIDs     []string            // All container IDs (new + reused)
}

// AssignScoutingFleetHandler handles the assign scouting fleet command
type AssignScoutingFleetHandler struct {
	shipRepo           navigation.ShipRepository
	waypointRepo       system.WaypointRepository
	graphProvider      system.ISystemGraphProvider
	routingClient      routing.RoutingClient
	daemonClient       daemon.DaemonClient
	shipAssignmentRepo daemon.ShipAssignmentRepository
}

// NewAssignScoutingFleetHandler creates a new assign scouting fleet handler
func NewAssignScoutingFleetHandler(
	shipRepo navigation.ShipRepository,
	waypointRepo system.WaypointRepository,
	graphProvider system.ISystemGraphProvider,
	routingClient routing.RoutingClient,
	daemonClient daemon.DaemonClient,
	shipAssignmentRepo daemon.ShipAssignmentRepository,
) *AssignScoutingFleetHandler {
	return &AssignScoutingFleetHandler{
		shipRepo:           shipRepo,
		waypointRepo:       waypointRepo,
		graphProvider:      graphProvider,
		routingClient:      routingClient,
		daemonClient:       daemonClient,
		shipAssignmentRepo: shipAssignmentRepo,
	}
}

// Handle executes the assign scouting fleet command
func (h *AssignScoutingFleetHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*AssignScoutingFleetCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Get all ships for the player
	ships, err := h.shipRepo.FindAllByPlayer(ctx, int(cmd.PlayerID))
	if err != nil {
		return nil, fmt.Errorf("failed to list ships: %w", err)
	}

	// 2. Filter ships to only probe/satellite types in the specified system
	scoutShips := h.filterScoutShips(ships, cmd.SystemSymbol)
	if len(scoutShips) == 0 {
		return nil, fmt.Errorf("no probe or satellite ships found")
	}

	// 3. Get all waypoints with MARKETPLACE trait in the system
	marketplaces, err := h.waypointRepo.ListBySystemWithTrait(ctx, cmd.SystemSymbol, "MARKETPLACE")
	if err != nil {
		return nil, fmt.Errorf("failed to list marketplaces: %w", err)
	}

	// 4. Filter out FUEL_STATION waypoints
	nonFuelStationMarkets := h.filterNonFuelStations(marketplaces)
	if len(nonFuelStationMarkets) == 0 {
		return nil, fmt.Errorf("no non-fuel-station marketplaces found")
	}

	// 5. Extract market symbols
	marketSymbols := make([]string, len(nonFuelStationMarkets))
	for i, waypoint := range nonFuelStationMarkets {
		marketSymbols[i] = waypoint.Symbol
	}

	// 6. Extract ship symbols
	shipSymbols := make([]string, len(scoutShips))
	for i, ship := range scoutShips {
		shipSymbols[i] = ship.ShipSymbol()
	}

	// 7. Use the existing ScoutMarketsHandler to assign ships
	// This handles VRP optimization, container reuse, and scout-tour creation
	scoutMarketsCmd := &ScoutMarketsCommand{
		PlayerID:     cmd.PlayerID,
		ShipSymbols:  shipSymbols,
		SystemSymbol: cmd.SystemSymbol,
		Markets:      marketSymbols,
		Iterations:   -1, // Infinite loop
	}

	scoutMarketsHandler := NewScoutMarketsHandler(
		h.shipRepo,
		h.graphProvider,
		h.routingClient,
		h.daemonClient,
		h.shipAssignmentRepo,
	)

	scoutResponse, err := scoutMarketsHandler.Handle(ctx, scoutMarketsCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to assign scout markets: %w", err)
	}

	// 8. Convert response
	scoutResult, ok := scoutResponse.(*ScoutMarketsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type from scout markets")
	}

	return &AssignScoutingFleetResponse{
		AssignedShips:    shipSymbols,
		Assignments:      scoutResult.Assignments,
		ReusedContainers: scoutResult.ReusedContainers,
		ContainerIDs:     scoutResult.ContainerIDs,
	}, nil
}

// filterScoutShips filters ships to only probe/drone types in the specified system
// Uses frame type information from the SpaceTraders API (FRAME_PROBE, FRAME_DRONE)
func (h *AssignScoutingFleetHandler) filterScoutShips(ships []*navigation.Ship, systemSymbol string) []*navigation.Ship {
	var scoutShips []*navigation.Ship

	for _, ship := range ships {
		// Check if ship is in the specified system
		shipSystem := extractSystemSymbol(ship.CurrentLocation().Symbol)
		if shipSystem != systemSymbol {
			continue
		}

		// Filter by frame type (probe or drone)
		if ship.IsScoutType() {
			scoutShips = append(scoutShips, ship)
		}
	}

	return scoutShips
}

// filterNonFuelStations filters out waypoints with FUEL_STATION type
func (h *AssignScoutingFleetHandler) filterNonFuelStations(waypoints []*shared.Waypoint) []*shared.Waypoint {
	var filtered []*shared.Waypoint

	for _, waypoint := range waypoints {
		// Check if waypoint has FUEL_STATION trait
		hasFuelStation := false
		for _, trait := range waypoint.Traits {
			if trait == "FUEL_STATION" {
				hasFuelStation = true
				break
			}
		}

		if !hasFuelStation {
			filtered = append(filtered, waypoint)
		}
	}

	return filtered
}

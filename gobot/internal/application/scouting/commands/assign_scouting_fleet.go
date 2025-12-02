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
)

// AssignScoutingFleetCommand automatically assigns all probe/satellite ships to market scouting
// Filters out FUEL_STATION marketplaces
type AssignScoutingFleetCommand struct {
	PlayerID     shared.PlayerID
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
	shipRepo      navigation.ShipRepository
	waypointRepo  system.WaypointRepository
	graphProvider system.ISystemGraphProvider
	routingClient routing.RoutingClient
	daemonClient  daemon.DaemonClient
	clock         shared.Clock
}

// NewAssignScoutingFleetHandler creates a new assign scouting fleet handler
func NewAssignScoutingFleetHandler(
	shipRepo navigation.ShipRepository,
	waypointRepo system.WaypointRepository,
	graphProvider system.ISystemGraphProvider,
	routingClient routing.RoutingClient,
	daemonClient daemon.DaemonClient,
	clock shared.Clock,
) *AssignScoutingFleetHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &AssignScoutingFleetHandler{
		shipRepo:      shipRepo,
		waypointRepo:  waypointRepo,
		graphProvider: graphProvider,
		routingClient: routingClient,
		daemonClient:  daemonClient,
		clock:         clock,
	}
}

// Handle executes the assign scouting fleet command
func (h *AssignScoutingFleetHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*AssignScoutingFleetCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	_, scoutShips, err := h.validateAndLoadShips(ctx, cmd)
	if err != nil {
		return nil, err
	}

	marketSymbols, err := h.loadAndFilterMarketplaces(ctx, cmd.SystemSymbol)
	if err != nil {
		return nil, err
	}

	scoutCmd := h.buildScoutMarketsCommand(cmd, extractShipSymbols(scoutShips), marketSymbols)

	scoutResult, err := h.executeScoutMarkets(ctx, scoutCmd)
	if err != nil {
		return nil, err
	}

	return h.buildResponse(extractShipSymbols(scoutShips), scoutResult), nil
}

// validateAndLoadShips loads all ships and filters for scout-capable ships
func (h *AssignScoutingFleetHandler) validateAndLoadShips(
	ctx context.Context,
	cmd *AssignScoutingFleetCommand,
) ([]*navigation.Ship, []*navigation.Ship, error) {
	ships, err := h.shipRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list ships: %w", err)
	}

	scoutShips := h.filterScoutShips(ships, cmd.SystemSymbol)
	if len(scoutShips) == 0 {
		return nil, nil, fmt.Errorf("no probe or satellite ships found")
	}

	return ships, scoutShips, nil
}

// loadAndFilterMarketplaces loads system marketplaces and filters out fuel stations
func (h *AssignScoutingFleetHandler) loadAndFilterMarketplaces(
	ctx context.Context,
	systemSymbol string,
) ([]string, error) {
	marketplaces, err := h.waypointRepo.ListBySystemWithTrait(ctx, systemSymbol, "MARKETPLACE")
	if err != nil {
		return nil, fmt.Errorf("failed to list marketplaces: %w", err)
	}

	nonFuelStationMarkets := h.filterNonFuelStations(marketplaces)
	if len(nonFuelStationMarkets) == 0 {
		return nil, fmt.Errorf("no non-fuel-station marketplaces found")
	}

	marketSymbols := make([]string, len(nonFuelStationMarkets))
	for i, waypoint := range nonFuelStationMarkets {
		marketSymbols[i] = waypoint.Symbol
	}

	return marketSymbols, nil
}

// buildScoutMarketsCommand constructs the ScoutMarketsCommand with all required parameters
func (h *AssignScoutingFleetHandler) buildScoutMarketsCommand(
	cmd *AssignScoutingFleetCommand,
	shipSymbols []string,
	marketSymbols []string,
) *ScoutMarketsCommand {
	return &ScoutMarketsCommand{
		PlayerID:     cmd.PlayerID,
		ShipSymbols:  shipSymbols,
		SystemSymbol: cmd.SystemSymbol,
		Markets:      marketSymbols,
		Iterations:   -1,
	}
}

// executeScoutMarkets creates the ScoutMarketsHandler and executes the command
func (h *AssignScoutingFleetHandler) executeScoutMarkets(
	ctx context.Context,
	scoutCmd *ScoutMarketsCommand,
) (*ScoutMarketsResponse, error) {
	scoutMarketsHandler := NewScoutMarketsHandler(
		h.shipRepo,
		h.graphProvider,
		h.routingClient,
		h.daemonClient,
		h.clock,
	)

	scoutResponse, err := scoutMarketsHandler.Handle(ctx, scoutCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to assign scout markets: %w", err)
	}

	scoutResult, ok := scoutResponse.(*ScoutMarketsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type from scout markets")
	}

	return scoutResult, nil
}

// buildResponse assembles the final response
func (h *AssignScoutingFleetHandler) buildResponse(
	shipSymbols []string,
	scoutResult *ScoutMarketsResponse,
) *AssignScoutingFleetResponse {
	return &AssignScoutingFleetResponse{
		AssignedShips:    shipSymbols,
		Assignments:      scoutResult.Assignments,
		ReusedContainers: scoutResult.ReusedContainers,
		ContainerIDs:     scoutResult.ContainerIDs,
	}
}

// extractShipSymbols extracts ship symbols from a slice of Ship entities
func extractShipSymbols(ships []*navigation.Ship) []string {
	shipSymbols := make([]string, len(ships))
	for i, ship := range ships {
		shipSymbols[i] = ship.ShipSymbol()
	}
	return shipSymbols
}

// filterScoutShips filters ships to only probe/drone types in the specified system
// Uses frame type information from the SpaceTraders API (FRAME_PROBE, FRAME_DRONE)
func (h *AssignScoutingFleetHandler) filterScoutShips(ships []*navigation.Ship, systemSymbol string) []*navigation.Ship {
	var scoutShips []*navigation.Ship

	for _, ship := range ships {
		// Check if ship is in the specified system
		shipSystem := ship.CurrentLocation().SystemSymbol
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
		if waypoint.Type != "FUEL_STATION" {
			filtered = append(filtered, waypoint)
		}
	}

	return filtered
}

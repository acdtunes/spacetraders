package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// NavigateShipCommand - HIGH-LEVEL command for ship navigation with route planning
//
// ✅ USE THIS for all application workflows that need ship navigation.
//
// This command handles:
// - Multi-hop route planning (via OR-Tools routing service)
// - Automatic refueling stops
// - Flight mode optimization (BURN > CRUISE > DRIFT)
// - Fuel constraint checking
// - Complete route execution
//
// This is the PRIMARY navigation command for business logic.
type NavigateShipCommand struct {
	ShipSymbol   string
	Destination  string
	PlayerID     shared.PlayerID
	PreferCruise bool // When true, prefer CRUISE over BURN (for asteroid ↔ market loop only)
}

// NavigateShipResponse represents the result of navigation
type NavigateShipResponse struct {
	Status          string // "completed", "already_at_destination"
	ArrivalTime     int
	CurrentLocation string
	FuelRemaining   int
	Route           *domainNavigation.Route
	Ship            *domainNavigation.Ship // Updated ship state after navigation
}

// NavigateShipHandler handles the NavigateShip command with full Python feature parity
// This is a thin orchestrator that delegates to specialized services:
// - WaypointEnricher: Enriches graph waypoints with fuel station data
// - RoutePlanner: Plans routes using routing client
// - RouteExecutor: Executes routes using mediator to orchestrate atomic commands
type NavigateShipHandler struct {
	shipRepo         domainNavigation.ShipRepository
	graphProvider    system.ISystemGraphProvider
	waypointEnricher *WaypointEnricher
	routePlanner     *RoutePlanner
	routeExecutor    *RouteExecutor
}

// NewNavigateShipHandler creates a new NavigateShipHandler with extracted services
func NewNavigateShipHandler(
	shipRepo domainNavigation.ShipRepository,
	graphProvider system.ISystemGraphProvider,
	waypointEnricher *WaypointEnricher,
	routePlanner *RoutePlanner,
	routeExecutor *RouteExecutor,
) *NavigateShipHandler {
	return &NavigateShipHandler{
		shipRepo:         shipRepo,
		graphProvider:    graphProvider,
		waypointEnricher: waypointEnricher,
		routePlanner:     routePlanner,
		routeExecutor:    routeExecutor,
	}
}

// Handle executes the NavigateShip command using extracted services
func (h *NavigateShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*NavigateShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *NavigateShipCommand")
	}

	logger := common.LoggerFromContext(ctx)

	ship, err := h.loadAndPrepareShip(ctx, cmd, logger)
	if err != nil {
		return nil, err
	}

	waypointObjects, systemSymbol, err := h.loadAndEnrichWaypoints(ctx, cmd, ship, logger)
	if err != nil {
		return nil, err
	}

	if err := h.validateWaypointCache(waypointObjects, ship, cmd.Destination, systemSymbol); err != nil {
		return nil, err
	}

	if ship.CurrentLocation().Symbol == cmd.Destination {
		logger.Log("INFO", "Ship already at destination", map[string]interface{}{
			"ship_symbol": ship.ShipSymbol(),
			"action":      "navigate",
			"destination": cmd.Destination,
			"result":      "already_present",
		})
		return h.handleAlreadyAtDestination(cmd, ship)
	}

	route, err := h.planAndExecuteRoute(ctx, cmd, ship, waypointObjects, systemSymbol, logger)
	if err != nil {
		return nil, err
	}

	finalShip, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship after navigation: %w", err)
	}

	return &NavigateShipResponse{
		Status:          "completed",
		ArrivalTime:     route.TotalTravelTime(),
		CurrentLocation: cmd.Destination,
		FuelRemaining:   finalShip.Fuel().Current,
		Route:           route,
		Ship:            finalShip,
	}, nil
}

func (h *NavigateShipHandler) loadAndPrepareShip(ctx context.Context, cmd *NavigateShipCommand, logger common.ContainerLogger) (*domainNavigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	logger.Log("INFO", "Ship navigation requested", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "navigate",
		"current":     ship.CurrentLocation().Symbol,
		"destination": cmd.Destination,
		"status":      string(ship.NavStatus()),
	})

	if ship.NavStatus() == domainNavigation.NavStatusInTransit {
		return h.waitForInTransitCompletion(ctx, cmd, ship, logger)
	}

	return ship, nil
}

func (h *NavigateShipHandler) waitForInTransitCompletion(ctx context.Context, cmd *NavigateShipCommand, ship *domainNavigation.Ship, logger common.ContainerLogger) (*domainNavigation.Ship, error) {
	logger.Log("INFO", "Ship in transit - waiting for arrival", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "wait_arrival",
		"status":      "IN_TRANSIT",
	})

	emptyRoute, err := domainNavigation.NewRoute(
		fmt.Sprintf("%s_wait_transit", ship.ShipSymbol()),
		ship.ShipSymbol(),
		cmd.PlayerID.Value(),
		[]*domainNavigation.RouteSegment{},
		ship.FuelCapacity(),
		false,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary route: %w", err)
	}

	if err := h.routeExecutor.ExecuteRoute(ctx, emptyRoute, ship, cmd.PlayerID); err != nil {
		return nil, fmt.Errorf("failed to wait for current transit: %w", err)
	}

	ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship after transit: %w", err)
	}

	logger.Log("INFO", "Ship arrived at destination", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "arrival_complete",
		"status":      string(ship.NavStatus()),
		"location":    ship.CurrentLocation().Symbol,
	})

	return ship, nil
}

func (h *NavigateShipHandler) loadAndEnrichWaypoints(ctx context.Context, cmd *NavigateShipCommand, ship *domainNavigation.Ship, logger common.ContainerLogger) (map[string]*shared.Waypoint, string, error) {
	systemSymbol := ship.CurrentLocation().SystemSymbol
	graphResult, err := h.graphProvider.GetGraph(ctx, systemSymbol, false, cmd.PlayerID.Value())
	if err != nil {
		return nil, "", fmt.Errorf("failed to get system graph: %w", err)
	}

	logger.Log("INFO", "System graph loaded", map[string]interface{}{
		"ship_symbol":   cmd.ShipSymbol,
		"action":        "load_graph",
		"system_symbol": systemSymbol,
		"source":        graphResult.Source,
	})

	waypointObjects, err := h.waypointEnricher.EnrichGraphWaypoints(ctx, graphResult.Graph, systemSymbol)
	if err != nil {
		return nil, "", fmt.Errorf("failed to enrich waypoints: %w", err)
	}

	return waypointObjects, systemSymbol, nil
}

func (h *NavigateShipHandler) planAndExecuteRoute(ctx context.Context, cmd *NavigateShipCommand, ship *domainNavigation.Ship, waypointObjects map[string]*shared.Waypoint, systemSymbol string, logger common.ContainerLogger) (*domainNavigation.Route, error) {
	logger.Log("INFO", "Route planning initiated", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "plan_route",
		"origin":      ship.CurrentLocation().Symbol,
		"destination": cmd.Destination,
	})

	route, err := h.routePlanner.PlanRoute(ctx, ship, cmd.Destination, waypointObjects, cmd.PreferCruise)
	if err != nil {
		return nil, fmt.Errorf("failed to plan route: %w", err)
	}

	if route == nil {
		return nil, h.buildNoRouteFoundError(ship, cmd.Destination, systemSymbol, waypointObjects)
	}

	defer func() {
		if r := recover(); r != nil {
			route.FailRoute(fmt.Sprintf("panic during execution: %v", r))
		}
	}()

	if err := h.routeExecutor.ExecuteRoute(ctx, route, ship, cmd.PlayerID); err != nil {
		route.FailRoute(err.Error())
		return nil, fmt.Errorf("failed to execute route: %w", err)
	}

	return route, nil
}

// validateWaypointCache validates waypoint cache has necessary data
func (h *NavigateShipHandler) validateWaypointCache(
	waypointObjects map[string]*shared.Waypoint,
	ship *domainNavigation.Ship,
	destination string,
	systemSymbol string,
) error {
	// Validate waypoint cache has waypoints
	if len(waypointObjects) == 0 {
		return fmt.Errorf("No waypoints found for system %s. "+
			"The waypoint cache is empty. Please sync waypoints from API first.", systemSymbol)
	}

	// Validate ship location exists in waypoint cache
	if _, ok := waypointObjects[ship.CurrentLocation().Symbol]; !ok {
		return fmt.Errorf("Waypoint %s not found in cache for system %s. "+
			"Ship location is missing from waypoint cache. Please sync waypoints from API.",
			ship.CurrentLocation().Symbol, systemSymbol)
	}

	// Validate destination exists in waypoint cache
	if _, ok := waypointObjects[destination]; !ok {
		return fmt.Errorf("Waypoint %s not found in cache for system %s. "+
			"Destination waypoint is missing from waypoint cache. Please sync waypoints from API.",
			destination, systemSymbol)
	}

	return nil
}

// handleAlreadyAtDestination handles the case where ship is already at destination
func (h *NavigateShipHandler) handleAlreadyAtDestination(
	cmd *NavigateShipCommand,
	ship *domainNavigation.Ship,
) (*NavigateShipResponse, error) {
	// Ship is already at destination - create empty route and mark as completed
	routeID := fmt.Sprintf("%s_already_at_destination", cmd.ShipSymbol)
	route, err := domainNavigation.NewRoute(
		routeID,
		cmd.ShipSymbol,
		cmd.PlayerID.Value(),
		[]*domainNavigation.RouteSegment{}, // No segments needed
		ship.FuelCapacity(),
		false,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create route: %w", err)
	}

	route.StartExecution()
	route.CompleteSegment() // Sets status to COMPLETED since no segments

	return &NavigateShipResponse{
		Status:          "already_at_destination",
		CurrentLocation: ship.CurrentLocation().Symbol,
		FuelRemaining:   ship.Fuel().Current,
		Route:           route,
		Ship:            ship, // Ship is already in correct state
	}, nil
}

// buildNoRouteFoundError builds detailed error message when routing fails
func (h *NavigateShipHandler) buildNoRouteFoundError(
	ship *domainNavigation.Ship,
	destination string,
	systemSymbol string,
	waypointObjects map[string]*shared.Waypoint,
) error {
	waypointCount := len(waypointObjects)
	fuelStations := 0
	for _, wp := range waypointObjects {
		if wp.HasFuel {
			fuelStations++
		}
	}

	return fmt.Errorf("no route found from %s to %s - "+
		"routing engine could not find a valid path - "+
		"system %s has %d waypoints cached with %d fuel stations - "+
		"ship fuel: %d/%d - "+
		"route may be unreachable or require multi-hop refueling not supported by current fuel levels",
		ship.CurrentLocation().Symbol, destination, systemSymbol,
		waypointCount, fuelStations, ship.Fuel().Current, ship.FuelCapacity())
}

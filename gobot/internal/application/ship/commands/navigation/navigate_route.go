package navigation

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Type aliases for utility classes from parent package
type WaypointEnricher = ship.WaypointEnricher
type RoutePlanner = ship.RoutePlanner
type RouteExecutor = ship.RouteExecutor

// NavigateRouteCommand - HIGH-LEVEL command for ship navigation with route planning
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
//
// To link transactions to a parent operation, add OperationContext to the context using
// shared.WithOperationContext() before sending this command.
type NavigateRouteCommand struct {
	ShipSymbol   string
	Destination  string
	PlayerID     shared.PlayerID
	PreferCruise bool // When true, prefer CRUISE over BURN (for asteroid ↔ market loop only)
}

// NavigateRouteResponse represents the result of navigation
type NavigateRouteResponse struct {
	Status          string // "completed", "already_at_destination"
	ArrivalTime     int
	CurrentLocation string
	FuelRemaining   int
	Route           *domainNavigation.Route
	Ship            *domainNavigation.Ship // Updated ship state after navigation
}

// NavigateRouteHandler handles the NavigateRoute command with full Python feature parity
// This is a thin orchestrator that delegates to specialized services:
// - WaypointEnricher: Enriches graph waypoints with fuel station data
// - RoutePlanner: Plans routes using routing client
// - RouteExecutor: Executes routes using mediator to orchestrate atomic commands
type NavigateRouteHandler struct {
	shipRepo         domainNavigation.ShipRepository
	graphProvider    system.ISystemGraphProvider
	waypointEnricher *WaypointEnricher
	routePlanner     *RoutePlanner
	routeExecutor    *RouteExecutor
}

// NewNavigateRouteHandler creates a new NavigateRouteHandler with extracted services
func NewNavigateRouteHandler(
	shipRepo domainNavigation.ShipRepository,
	graphProvider system.ISystemGraphProvider,
	waypointEnricher *WaypointEnricher,
	routePlanner *RoutePlanner,
	routeExecutor *RouteExecutor,
) *NavigateRouteHandler {
	return &NavigateRouteHandler{
		shipRepo:         shipRepo,
		graphProvider:    graphProvider,
		waypointEnricher: waypointEnricher,
		routePlanner:     routePlanner,
		routeExecutor:    routeExecutor,
	}
}

// Handle executes the NavigateRoute command using extracted services
func (h *NavigateRouteHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*NavigateRouteCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *NavigateRouteCommand")
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

	// OPTIMIZATION: Ship is updated in place by RouteExecutor (no reload needed)
	// RouteExecutor calls ship.UpdateFuelFromAPI() and ship.Arrive() during execution
	return &NavigateRouteResponse{
		Status:          "completed",
		ArrivalTime:     route.TotalTravelTime(),
		CurrentLocation: cmd.Destination,
		FuelRemaining:   ship.Fuel().Current,
		Route:           route,
		Ship:            ship,
	}, nil
}

func (h *NavigateRouteHandler) loadAndPrepareShip(ctx context.Context, cmd *NavigateRouteCommand, logger common.ContainerLogger) (*domainNavigation.Ship, error) {
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

func (h *NavigateRouteHandler) waitForInTransitCompletion(ctx context.Context, cmd *NavigateRouteCommand, ship *domainNavigation.Ship, logger common.ContainerLogger) (*domainNavigation.Ship, error) {
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

	// OPTIMIZATION: Ship is updated in place by RouteExecutor during waitForCurrentTransit
	// No need to reload - ship.Arrive() was already called

	logger.Log("INFO", "Ship arrived at destination", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "arrival_complete",
		"status":      string(ship.NavStatus()),
		"location":    ship.CurrentLocation().Symbol,
	})

	return ship, nil
}

func (h *NavigateRouteHandler) loadAndEnrichWaypoints(ctx context.Context, cmd *NavigateRouteCommand, ship *domainNavigation.Ship, logger common.ContainerLogger) (map[string]*shared.Waypoint, string, error) {
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

	// sp-g1g5: On a genuine in-system cache miss (origin absent, or an in-system
	// destination absent) the cache-first load is stale. Force-refresh the graph
	// from the API exactly once and re-enrich so navigation self-heals instead of
	// loud-failing at validateWaypointCache. Cross-system destinations are excluded
	// on purpose — a foreign-system waypoint never appears in this system's graph,
	// so a refresh would be a wasted API call. When all waypoints are present this
	// branch is skipped, keeping normal navigation byte-identical.
	if shouldForceRefreshWaypoints(waypointObjects, ship.CurrentLocation().Symbol, cmd.Destination, systemSymbol) {
		logger.Log("INFO", "Waypoint cache miss - force-refreshing system graph", map[string]interface{}{
			"ship_symbol":   cmd.ShipSymbol,
			"action":        "auto_sync_waypoints",
			"system_symbol": systemSymbol,
			"origin":        ship.CurrentLocation().Symbol,
			"destination":   cmd.Destination,
		})

		graphResult, err = h.graphProvider.GetGraph(ctx, systemSymbol, true, cmd.PlayerID.Value())
		if err != nil {
			return nil, "", fmt.Errorf("failed to force-refresh system graph: %w", err)
		}

		waypointObjects, err = h.waypointEnricher.EnrichGraphWaypoints(ctx, graphResult.Graph, systemSymbol)
		if err != nil {
			return nil, "", fmt.Errorf("failed to re-enrich waypoints after force-refresh: %w", err)
		}
	}

	return waypointObjects, systemSymbol, nil
}

// shouldForceRefreshWaypoints reports whether the cache-first waypoint load is
// missing data that a force-refresh could recover: the ship's origin waypoint,
// or an IN-SYSTEM destination. A missing cross-system destination is expected
// (it lives in another system's graph) and must NOT trigger a refresh. The
// trigger is deliberately narrow so it fires only on a genuine in-system cache
// miss — the same condition that would otherwise fail loudly at
// validateWaypointCache. See sp-g1g5.
func shouldForceRefreshWaypoints(waypoints map[string]*shared.Waypoint, origin, destination, systemSymbol string) bool {
	if _, ok := waypoints[origin]; !ok {
		return true
	}
	if _, ok := waypoints[destination]; !ok {
		return shared.ExtractSystemSymbol(destination) == systemSymbol
	}
	return false
}

func (h *NavigateRouteHandler) planAndExecuteRoute(ctx context.Context, cmd *NavigateRouteCommand, ship *domainNavigation.Ship, waypointObjects map[string]*shared.Waypoint, systemSymbol string, logger common.ContainerLogger) (*domainNavigation.Route, error) {
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
			if failErr := route.FailRoute(fmt.Sprintf("panic during execution: %v", r)); failErr != nil {
				logger.Log("ERROR", "Failed to mark route as failed after panic", map[string]interface{}{
					"ship_symbol": ship.ShipSymbol(),
					"action":      "fail_route",
					"error":       failErr.Error(),
				})
			}
		}
	}()

	if err := h.routeExecutor.ExecuteRoute(ctx, route, ship, cmd.PlayerID); err != nil {
		if failErr := route.FailRoute(err.Error()); failErr != nil {
			logger.Log("ERROR", "Failed to mark route as failed", map[string]interface{}{
				"ship_symbol": ship.ShipSymbol(),
				"action":      "fail_route",
				"error":       failErr.Error(),
			})
		}
		return nil, fmt.Errorf("failed to execute route: %w", err)
	}

	return route, nil
}

// validateWaypointCache validates waypoint cache has necessary data
func (h *NavigateRouteHandler) validateWaypointCache(
	waypointObjects map[string]*shared.Waypoint,
	ship *domainNavigation.Ship,
	destination string,
	systemSymbol string,
) error {
	// Validate waypoint cache has waypoints
	if len(waypointObjects) == 0 {
		return fmt.Errorf("no waypoints found for system %s. "+
			"The waypoint cache is empty. Please sync waypoints from API first", systemSymbol)
	}

	// Validate ship location exists in waypoint cache
	if _, ok := waypointObjects[ship.CurrentLocation().Symbol]; !ok {
		return fmt.Errorf("waypoint %s not found in cache for system %s. "+
			"Ship location is missing from waypoint cache. Please sync waypoints from API",
			ship.CurrentLocation().Symbol, systemSymbol)
	}

	// Validate destination exists in waypoint cache
	if _, ok := waypointObjects[destination]; !ok {
		return fmt.Errorf("waypoint %s not found in cache for system %s. "+
			"Destination waypoint is missing from waypoint cache. Please sync waypoints from API",
			destination, systemSymbol)
	}

	return nil
}

// handleAlreadyAtDestination handles the case where ship is already at destination
func (h *NavigateRouteHandler) handleAlreadyAtDestination(
	cmd *NavigateRouteCommand,
	ship *domainNavigation.Ship,
) (*NavigateRouteResponse, error) {
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

	return &NavigateRouteResponse{
		Status:          "already_at_destination",
		CurrentLocation: ship.CurrentLocation().Symbol,
		FuelRemaining:   ship.Fuel().Current,
		Route:           route,
		Ship:            ship, // Ship is already in correct state
	}, nil
}

// buildNoRouteFoundError builds detailed error message when routing fails
func (h *NavigateRouteHandler) buildNoRouteFoundError(
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

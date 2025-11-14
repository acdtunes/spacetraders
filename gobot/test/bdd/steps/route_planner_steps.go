package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type routePlannerContext struct {
	ship                *domainNavigation.Ship
	waypoints           map[string]*shared.Waypoint
	mockRoutingClient   *mockRoutingClientForPlanner
	routePlanner        *appShip.RoutePlanner
	plannedRoute        *domainNavigation.Route
	planningErr         error
	destination         string
}

// mockRoutingClientForPlanner provides configurable mock behavior for testing
type mockRoutingClientForPlanner struct {
	responseToReturn *domainRouting.RouteResponse
	errorToReturn    error
}

func (m *mockRoutingClientForPlanner) PlanRoute(ctx context.Context, request *domainRouting.RouteRequest) (*domainRouting.RouteResponse, error) {
	if m.errorToReturn != nil {
		return nil, m.errorToReturn
	}
	return m.responseToReturn, nil
}

func (m *mockRoutingClientForPlanner) OptimizeTour(ctx context.Context, request *domainRouting.TourRequest) (*domainRouting.TourResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockRoutingClientForPlanner) PartitionFleet(ctx context.Context, request *domainRouting.VRPRequest) (*domainRouting.VRPResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (ctx *routePlannerContext) reset() {
	ctx.ship = nil
	ctx.waypoints = make(map[string]*shared.Waypoint)
	ctx.mockRoutingClient = &mockRoutingClientForPlanner{}
	ctx.routePlanner = appShip.NewRoutePlanner(ctx.mockRoutingClient)
	ctx.plannedRoute = nil
	ctx.planningErr = nil
	ctx.destination = ""
}

// Given steps

func (ctx *routePlannerContext) aShipExistsWithCurrentFuelAndCapacity(shipSymbol string, currentFuel, capacity int) error {
	// Create ship at default location (will be set by next step)
	waypoint, _ := shared.NewWaypoint("X1-START", 0, 0)
	fuel, err := shared.NewFuel(currentFuel, capacity)
	if err != nil {
		return err
	}
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	ship, err := domainNavigation.NewShip(
		shipSymbol, 1, waypoint, fuel, capacity,
		40, cargo, 30, domainNavigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	ctx.ship = ship
	return nil
}

func (ctx *routePlannerContext) theShipIsAtWaypointWithCoordinates(waypointSymbol string, x, y float64) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}
	waypoint.SystemSymbol = "X1"
	ctx.waypoints[waypointSymbol] = waypoint

	// Update ship's location
	fuel := ctx.ship.Fuel()
	cargo := ctx.ship.Cargo()
	ship, err := domainNavigation.NewShip(
		ctx.ship.ShipSymbol(), ctx.ship.PlayerID(), waypoint,
		fuel, ctx.ship.FuelCapacity(), ctx.ship.CargoCapacity(),
		cargo, ctx.ship.EngineSpeed(), ctx.ship.NavStatus(),
	)
	if err != nil {
		return err
	}

	ctx.ship = ship
	return nil
}

func (ctx *routePlannerContext) aDestinationWaypointExistsAtCoordinates(waypointSymbol string, x, y float64) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}
	waypoint.SystemSymbol = "X1"
	ctx.waypoints[waypointSymbol] = waypoint
	ctx.destination = waypointSymbol
	return nil
}

func (ctx *routePlannerContext) aWaypointExistsAtCoordinatesWithFuelStation(waypointSymbol string, x, y float64) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}
	waypoint.SystemSymbol = "X1"
	waypoint.HasFuel = true
	ctx.waypoints[waypointSymbol] = waypoint
	return nil
}

func (ctx *routePlannerContext) theShipIsAtAFuelStationWaypoint(waypointSymbol string) error {
	// Get or create waypoint
	waypoint, exists := ctx.waypoints[waypointSymbol]
	if !exists {
		var err error
		waypoint, err = shared.NewWaypoint(waypointSymbol, 0, 0)
		if err != nil {
			return err
		}
		waypoint.SystemSymbol = "X1"
		ctx.waypoints[waypointSymbol] = waypoint
	}
	waypoint.HasFuel = true

	// Update ship's location
	fuel := ctx.ship.Fuel()
	cargo := ctx.ship.Cargo()
	ship, err := domainNavigation.NewShip(
		ctx.ship.ShipSymbol(), ctx.ship.PlayerID(), waypoint,
		fuel, ctx.ship.FuelCapacity(), ctx.ship.CargoCapacity(),
		cargo, ctx.ship.EngineSpeed(), ctx.ship.NavStatus(),
	)
	if err != nil {
		return err
	}

	ctx.ship = ship
	return nil
}

func (ctx *routePlannerContext) theRoutingClientReturnsASingleStepDirectRoute() error {
	ctx.mockRoutingClient.responseToReturn = &domainRouting.RouteResponse{
		Steps: []*domainRouting.RouteStepData{
			{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    ctx.destination,
				FuelCost:    30,
				TimeSeconds: 120,
				Mode:        "CRUISE",
			},
		},
		TotalFuelCost:    30,
		TotalTimeSeconds: 120,
		TotalDistance:    111.8,
	}
	return nil
}

func (ctx *routePlannerContext) theRoutingClientReturnsARouteWithRefuelingStop() error {
	ctx.mockRoutingClient.responseToReturn = &domainRouting.RouteResponse{
		Steps: []*domainRouting.RouteStepData{
			{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    "X1-FUEL",
				FuelCost:    20,
				TimeSeconds: 60,
				Mode:        "CRUISE",
			},
			{
				Action:      domainRouting.RouteActionRefuel,
				Waypoint:    "X1-FUEL",
				FuelCost:    0,
				TimeSeconds: 10,
				Mode:        "",
			},
			{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    ctx.destination,
				FuelCost:    25,
				TimeSeconds: 70,
				Mode:        "CRUISE",
			},
		},
		TotalFuelCost:    45,
		TotalTimeSeconds: 140,
		TotalDistance:    100.0,
	}
	return nil
}

func (ctx *routePlannerContext) theRoutingClientReturnsARouteWithRefuelBeforeDeparture() error {
	ctx.mockRoutingClient.responseToReturn = &domainRouting.RouteResponse{
		Steps: []*domainRouting.RouteStepData{
			{
				Action:      domainRouting.RouteActionRefuel,
				Waypoint:    ctx.ship.CurrentLocation().Symbol,
				FuelCost:    0,
				TimeSeconds: 10,
				Mode:        "",
			},
			{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    ctx.destination,
				FuelCost:    50,
				TimeSeconds: 150,
				Mode:        "BURN",
			},
		},
		TotalFuelCost:    50,
		TotalTimeSeconds: 160,
		TotalDistance:    120.0,
	}
	return nil
}

func (ctx *routePlannerContext) theRoutingClientReturnsARouteWithFlightMode(mode string) error {
	ctx.mockRoutingClient.responseToReturn = &domainRouting.RouteResponse{
		Steps: []*domainRouting.RouteStepData{
			{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    ctx.destination,
				FuelCost:    50,
				TimeSeconds: 80,
				Mode:        mode,
			},
		},
		TotalFuelCost:    50,
		TotalTimeSeconds: 80,
		TotalDistance:    111.8,
	}
	return nil
}

func (ctx *routePlannerContext) theRoutingClientReturnsAnEmptyRoute() error {
	ctx.mockRoutingClient.responseToReturn = &domainRouting.RouteResponse{
		Steps:            []*domainRouting.RouteStepData{},
		TotalFuelCost:    0,
		TotalTimeSeconds: 0,
		TotalDistance:    0,
	}
	return nil
}

func (ctx *routePlannerContext) theRoutingClientReturnsAnError(errorMsg string) error {
	ctx.mockRoutingClient.errorToReturn = fmt.Errorf("%s", errorMsg)
	return nil
}

func (ctx *routePlannerContext) theRoutingClientReturnsAComplexRouteWithMultipleRefuelStops() error {
	ctx.mockRoutingClient.responseToReturn = &domainRouting.RouteResponse{
		Steps: []*domainRouting.RouteStepData{
			{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    "X1-FUEL-1",
				FuelCost:    15,
				TimeSeconds: 40,
				Mode:        "CRUISE",
			},
			{
				Action:      domainRouting.RouteActionRefuel,
				Waypoint:    "X1-FUEL-1",
				FuelCost:    0,
				TimeSeconds: 10,
				Mode:        "",
			},
			{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    "X1-FUEL-2",
				FuelCost:    20,
				TimeSeconds: 50,
				Mode:        "CRUISE",
			},
			{
				Action:      domainRouting.RouteActionRefuel,
				Waypoint:    "X1-FUEL-2",
				FuelCost:    0,
				TimeSeconds: 10,
				Mode:        "",
			},
			{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    ctx.destination,
				FuelCost:    18,
				TimeSeconds: 45,
				Mode:        "CRUISE",
			},
		},
		TotalFuelCost:    53,
		TotalTimeSeconds: 155,
		TotalDistance:    120.0,
	}
	return nil
}

func (ctx *routePlannerContext) theRoutingClientReturnsARouteWithOnlyRefuelSteps() error {
	ctx.mockRoutingClient.responseToReturn = &domainRouting.RouteResponse{
		Steps: []*domainRouting.RouteStepData{
			{
				Action:      domainRouting.RouteActionRefuel,
				Waypoint:    ctx.ship.CurrentLocation().Symbol,
				FuelCost:    0,
				TimeSeconds: 10,
				Mode:        "",
			},
		},
		TotalFuelCost:    0,
		TotalTimeSeconds: 10,
		TotalDistance:    0,
	}
	return nil
}

func (ctx *routePlannerContext) theRoutingClientReturnsASingleStepRouteWithTotalTimeSeconds(totalTime int) error {
	ctx.mockRoutingClient.responseToReturn = &domainRouting.RouteResponse{
		Steps: []*domainRouting.RouteStepData{
			{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    ctx.destination,
				FuelCost:    30,
				TimeSeconds: totalTime,
				Mode:        "CRUISE",
			},
		},
		TotalFuelCost:    30,
		TotalTimeSeconds: totalTime,
		TotalDistance:    111.8,
	}
	return nil
}

func (ctx *routePlannerContext) theRoutingClientReturnsARouteToUnknownWaypoint(unknownWaypoint string) error {
	ctx.mockRoutingClient.responseToReturn = &domainRouting.RouteResponse{
		Steps: []*domainRouting.RouteStepData{
			{
				Action:      domainRouting.RouteActionTravel,
				Waypoint:    unknownWaypoint,
				FuelCost:    30,
				TimeSeconds: 120,
				Mode:        "CRUISE",
			},
		},
		TotalFuelCost:    30,
		TotalTimeSeconds: 120,
		TotalDistance:    111.8,
	}
	return nil
}

// When steps

func (ctx *routePlannerContext) iPlanARouteFromShipLocationTo(destination string) error {
	ctx.destination = destination
	route, err := ctx.routePlanner.PlanRoute(
		context.Background(),
		ctx.ship,
		destination,
		ctx.waypoints,
	)
	ctx.plannedRoute = route
	ctx.planningErr = err
	return nil
}

// Then steps

func (ctx *routePlannerContext) theRouteShouldBeCreatedSuccessfully() error {
	if ctx.planningErr != nil {
		return fmt.Errorf("expected route to be created successfully but got error: %v", ctx.planningErr)
	}
	if ctx.plannedRoute == nil {
		return fmt.Errorf("expected route to be created but got nil")
	}
	return nil
}

func (ctx *routePlannerContext) theRouteShouldHaveSegments(expectedCount int) error {
	if ctx.plannedRoute == nil {
		return fmt.Errorf("no route was created")
	}
	actualCount := len(ctx.plannedRoute.Segments())
	if actualCount != expectedCount {
		return fmt.Errorf("expected %d segments but got %d", expectedCount, actualCount)
	}
	return nil
}

func (ctx *routePlannerContext) theRouteShouldNotRequireRefuelBeforeDeparture() error {
	if ctx.plannedRoute == nil {
		return fmt.Errorf("no route was created")
	}
	if ctx.plannedRoute.RefuelBeforeDeparture() {
		return fmt.Errorf("expected route to not require refuel before departure but it does")
	}
	return nil
}

func (ctx *routePlannerContext) theRouteShouldRequireRefuelBeforeDeparture() error {
	if ctx.plannedRoute == nil {
		return fmt.Errorf("no route was created")
	}
	if !ctx.plannedRoute.RefuelBeforeDeparture() {
		return fmt.Errorf("expected route to require refuel before departure but it doesn't")
	}
	return nil
}

func (ctx *routePlannerContext) theRouteShouldHaveAtLeastSegment(minCount int) error {
	if ctx.plannedRoute == nil {
		return fmt.Errorf("no route was created")
	}
	actualCount := len(ctx.plannedRoute.Segments())
	if actualCount < minCount {
		return fmt.Errorf("expected at least %d segments but got %d", minCount, actualCount)
	}
	return nil
}

func (ctx *routePlannerContext) segmentShouldTravelFromTo(segmentNum int, from, to string) error {
	if ctx.plannedRoute == nil {
		return fmt.Errorf("no route was created")
	}
	segments := ctx.plannedRoute.Segments()
	if segmentNum < 1 || segmentNum > len(segments) {
		return fmt.Errorf("segment %d does not exist (route has %d segments)", segmentNum, len(segments))
	}

	segment := segments[segmentNum-1] // Convert to 0-indexed
	if segment.FromWaypoint.Symbol != from {
		return fmt.Errorf("segment %d expected to start from %s but starts from %s", segmentNum, from, segment.FromWaypoint.Symbol)
	}
	if segment.ToWaypoint.Symbol != to {
		return fmt.Errorf("segment %d expected to end at %s but ends at %s", segmentNum, to, segment.ToWaypoint.Symbol)
	}
	return nil
}

func (ctx *routePlannerContext) segmentShouldUseFlightMode(segmentNum int, expectedMode string) error {
	if ctx.plannedRoute == nil {
		return fmt.Errorf("no route was created")
	}
	segments := ctx.plannedRoute.Segments()
	if segmentNum < 1 || segmentNum > len(segments) {
		return fmt.Errorf("segment %d does not exist (route has %d segments)", segmentNum, len(segments))
	}

	segment := segments[segmentNum-1]
	actualMode := segment.FlightMode.Name()
	if actualMode != expectedMode {
		return fmt.Errorf("segment %d expected flight mode %s but got %s", segmentNum, expectedMode, actualMode)
	}
	return nil
}

func (ctx *routePlannerContext) segmentShouldNotRequireRefuel(segmentNum int) error {
	if ctx.plannedRoute == nil {
		return fmt.Errorf("no route was created")
	}
	segments := ctx.plannedRoute.Segments()
	if segmentNum < 1 || segmentNum > len(segments) {
		return fmt.Errorf("segment %d does not exist (route has %d segments)", segmentNum, len(segments))
	}

	segment := segments[segmentNum-1]
	if segment.RequiresRefuel {
		return fmt.Errorf("segment %d should not require refuel but it does", segmentNum)
	}
	return nil
}

func (ctx *routePlannerContext) segmentShouldRequireRefuel(segmentNum int) error {
	if ctx.plannedRoute == nil {
		return fmt.Errorf("no route was created")
	}
	segments := ctx.plannedRoute.Segments()
	if segmentNum < 1 || segmentNum > len(segments) {
		return fmt.Errorf("segment %d does not exist (route has %d segments)", segmentNum, len(segments))
	}

	segment := segments[segmentNum-1]
	if !segment.RequiresRefuel {
		return fmt.Errorf("segment %d should require refuel but it doesn't", segmentNum)
	}
	return nil
}

func (ctx *routePlannerContext) routePlanningShouldFailWithError(expectedError string) error {
	if ctx.planningErr == nil {
		return fmt.Errorf("expected route planning to fail with error but it succeeded")
	}
	if ctx.planningErr.Error() != expectedError {
		return fmt.Errorf("expected error '%s' but got '%s'", expectedError, ctx.planningErr.Error())
	}
	return nil
}

func (ctx *routePlannerContext) theRouteIDShouldBe(expectedID string) error {
	if ctx.plannedRoute == nil {
		return fmt.Errorf("no route was created")
	}
	actualID := ctx.plannedRoute.RouteID()
	if actualID != expectedID {
		return fmt.Errorf("expected route ID '%s' but got '%s'", expectedID, actualID)
	}
	return nil
}

// Register steps

func InitializeRoutePlannerScenario(sc *godog.ScenarioContext) {
	plannerCtx := &routePlannerContext{}

	sc.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		plannerCtx.reset()
		return ctx, nil
	})

	// Given steps
	sc.Step(`^a ship "([^"]*)" exists with current fuel (\d+) and capacity (\d+)$`, plannerCtx.aShipExistsWithCurrentFuelAndCapacity)
	sc.Step(`^the ship is at waypoint "([^"]*)" with coordinates \(([^,]+), ([^)]+)\)$`, plannerCtx.theShipIsAtWaypointWithCoordinates)
	sc.Step(`^a destination waypoint "([^"]*)" exists at coordinates \(([^,]+), ([^)]+)\)$`, plannerCtx.aDestinationWaypointExistsAtCoordinates)
	sc.Step(`^a waypoint "([^"]*)" exists at coordinates \(([^,]+), ([^)]+)\) with fuel station$`, plannerCtx.aWaypointExistsAtCoordinatesWithFuelStation)
	sc.Step(`^the ship is at a fuel station waypoint "([^"]*)"$`, plannerCtx.theShipIsAtAFuelStationWaypoint)
	sc.Step(`^the routing client returns a single-step direct route$`, plannerCtx.theRoutingClientReturnsASingleStepDirectRoute)
	sc.Step(`^the routing client returns a route with refueling stop$`, plannerCtx.theRoutingClientReturnsARouteWithRefuelingStop)
	sc.Step(`^the routing client returns a route with refuel before departure$`, plannerCtx.theRoutingClientReturnsARouteWithRefuelBeforeDeparture)
	sc.Step(`^the routing client returns a route with ([A-Z]+) flight mode$`, plannerCtx.theRoutingClientReturnsARouteWithFlightMode)
	sc.Step(`^the routing client returns an empty route$`, plannerCtx.theRoutingClientReturnsAnEmptyRoute)
	sc.Step(`^the routing client returns an error "([^"]*)"$`, plannerCtx.theRoutingClientReturnsAnError)
	sc.Step(`^the routing client returns a complex route with multiple refuel stops$`, plannerCtx.theRoutingClientReturnsAComplexRouteWithMultipleRefuelStops)
	sc.Step(`^the routing client returns a route with only refuel steps$`, plannerCtx.theRoutingClientReturnsARouteWithOnlyRefuelSteps)
	sc.Step(`^the routing client returns a single-step route with total time (\d+) seconds$`, plannerCtx.theRoutingClientReturnsASingleStepRouteWithTotalTimeSeconds)
	sc.Step(`^the routing client returns a route to unknown waypoint "([^"]*)"$`, plannerCtx.theRoutingClientReturnsARouteToUnknownWaypoint)

	// When steps
	sc.Step(`^I plan a route from ship location to "([^"]*)"$`, plannerCtx.iPlanARouteFromShipLocationTo)

	// Then steps
	sc.Step(`^the planned route should be created successfully$`, plannerCtx.theRouteShouldBeCreatedSuccessfully)
	sc.Step(`^the planned route should have (\d+) segments?$`, plannerCtx.theRouteShouldHaveSegments)
	sc.Step(`^the planned route should not require refuel before departure$`, plannerCtx.theRouteShouldNotRequireRefuelBeforeDeparture)
	sc.Step(`^the planned route should require refuel before departure$`, plannerCtx.theRouteShouldRequireRefuelBeforeDeparture)
	sc.Step(`^the planned route should have at least (\d+) segments?$`, plannerCtx.theRouteShouldHaveAtLeastSegment)
	sc.Step(`^planned segment (\d+) should travel from "([^"]*)" to "([^"]*)"$`, plannerCtx.segmentShouldTravelFromTo)
	sc.Step(`^planned segment (\d+) should use flight mode "([^"]*)"$`, plannerCtx.segmentShouldUseFlightMode)
	sc.Step(`^planned segment (\d+) should not require refuel$`, plannerCtx.segmentShouldNotRequireRefuel)
	sc.Step(`^planned segment (\d+) should require refuel$`, plannerCtx.segmentShouldRequireRefuel)
	sc.Step(`^route planning should fail with error "([^"]*)"$`, plannerCtx.routePlanningShouldFailWithError)
	sc.Step(`^the planned route ID should be "([^"]*)"$`, plannerCtx.theRouteIDShouldBe)
}

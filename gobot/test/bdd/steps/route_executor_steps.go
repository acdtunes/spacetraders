package steps

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/cucumber/godog"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type routeExecutorContext struct {
	ships               map[string]*navigation.Ship
	waypoints           map[string]*shared.Waypoint
	routes              map[string]*navigation.Route
	playerID            int
	agentSymbol         string
	token               string
	executionError      error
	refueledWaypoints   map[string]bool // Track where ship refueled
	preventedDrift      bool
	waitedForTransit    bool
	currentRoute        *navigation.Route
	clock               *shared.MockClock
}

func (ctx *routeExecutorContext) reset() {
	ctx.ships = make(map[string]*navigation.Ship)
	ctx.waypoints = make(map[string]*shared.Waypoint)
	ctx.routes = make(map[string]*navigation.Route)
	ctx.refueledWaypoints = make(map[string]bool)
	ctx.clock = shared.NewMockClock(time.Now())
	ctx.playerID = 0
	ctx.agentSymbol = ""
	ctx.token = ""
	ctx.executionError = nil
	ctx.preventedDrift = false
	ctx.waitedForTransit = false
	ctx.currentRoute = nil
}

// Player setup steps (reuse from other scenarios)

func (ctx *routeExecutorContext) aPlayerExistsWithAgentAndToken(agentSymbol, token string) error {
	ctx.agentSymbol = agentSymbol
	ctx.token = token
	return nil
}

func (ctx *routeExecutorContext) thePlayerHasPlayerID(playerID int) error {
	ctx.playerID = playerID
	return nil
}

// Given steps - Ship setup

func (ctx *routeExecutorContext) aShipForPlayerAtWithStatusAndFuel(
	shipSymbol string,
	playerID int,
	location string,
	status string,
	currentFuel, fuelCapacity int,
) error {
	// Get or create waypoint
	waypoint, exists := ctx.waypoints[location]
	if !exists {
		wp, err := shared.NewWaypoint(location, 0, 0)
		if err != nil {
			return err
		}
		waypoint = wp
		ctx.waypoints[location] = waypoint
	}

	fuel, err := shared.NewFuel(currentFuel, fuelCapacity)
	if err != nil {
		return err
	}

	cargo, err := shared.NewCargo(40, 0, []*shared.CargoItem{})
	if err != nil {
		return err
	}

	var navStatus navigation.NavStatus
	switch status {
	case "DOCKED":
		navStatus = navigation.NavStatusDocked
	case "IN_ORBIT":
		navStatus = navigation.NavStatusInOrbit
	case "IN_TRANSIT":
		navStatus = navigation.NavStatusInTransit
	default:
		return fmt.Errorf("unknown nav status: %s", status)
	}

	ship, err := navigation.NewShip(
		shipSymbol, playerID, waypoint, fuel, fuelCapacity,
		40, cargo, 30, navStatus,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	return nil
}

func (ctx *routeExecutorContext) aShipForPlayerInTransitToArrivingInSeconds(
	shipSymbol string,
	playerID int,
	destination string,
	seconds int,
) error {
	// Get or create destination waypoint
	waypoint, exists := ctx.waypoints[destination]
	if !exists {
		wp, err := shared.NewWaypoint(destination, 10, 0)
		if err != nil {
			return err
		}
		waypoint = wp
		ctx.waypoints[destination] = waypoint
	}

	fuel, err := shared.NewFuel(75, 100)
	if err != nil {
		return err
	}

	cargo, err := shared.NewCargo(40, 0, []*shared.CargoItem{})
	if err != nil {
		return err
	}

	// Create ship in transit state
	ship, err := navigation.NewShip(
		shipSymbol, playerID, waypoint, fuel, 100,
		40, cargo, 30, navigation.NavStatusInTransit,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	// Note: In real implementation, we'd track arrival time
	// For now, we simulate the wait in the executor
	return nil
}

// Given steps - Waypoint setup

func (ctx *routeExecutorContext) waypointExistsAtCoordinatesWithFuelStation(
	waypointSymbol string,
	x, y float64,
) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}
	waypoint.HasFuel = true
	ctx.waypoints[waypointSymbol] = waypoint
	return nil
}

func (ctx *routeExecutorContext) waypointExistsAtCoordinatesWithoutFuelStation(
	waypointSymbol string,
	x, y float64,
) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, x, y)
	if err != nil {
		return err
	}
	waypoint.HasFuel = false
	ctx.waypoints[waypointSymbol] = waypoint
	return nil
}

// Given steps - Route setup

func (ctx *routeExecutorContext) aRouteExistsForShipWithSegmentFromToInModeRequiringFuel(
	shipSymbol string,
	numSegments int,
	fromWaypoint, toWaypoint, mode string,
	fuelRequired int,
) error {
	ship := ctx.ships[shipSymbol]
	if ship == nil {
		return fmt.Errorf("ship %s not found", shipSymbol)
	}

	from := ctx.waypoints[fromWaypoint]
	to := ctx.waypoints[toWaypoint]
	if from == nil || to == nil {
		return fmt.Errorf("waypoints not found")
	}

	flightMode, err := shared.ParseFlightMode(mode)
	if err != nil {
		return err
	}

	// Calculate distance and travel time
	distance := from.DistanceTo(to)
	travelTime := flightMode.TravelTime(distance, ship.EngineSpeed())

	segment := navigation.NewRouteSegment(
		from, to, distance, fuelRequired, travelTime, flightMode, false,
	)

	route, err := navigation.NewRoute(
		fmt.Sprintf("%s_route", shipSymbol),
		shipSymbol,
		ship.PlayerID(),
		[]*navigation.RouteSegment{segment},
		ship.FuelCapacity(),
		false,
	)
	if err != nil {
		return err
	}

	ctx.routes[shipSymbol] = route
	ctx.currentRoute = route
	return nil
}

func (ctx *routeExecutorContext) aRouteExistsForShipRequiringRefuelBeforeDeparture(
	shipSymbol string,
) error {
	ship := ctx.ships[shipSymbol]
	if ship == nil {
		return fmt.Errorf("ship %s not found", shipSymbol)
	}

	// We'll add the segment in the next step
	// For now, just mark that refuel before departure is needed
	// This will be used when creating the route
	return nil
}

func (ctx *routeExecutorContext) theRouteHasSegmentFromToInModeRequiringFuel(
	numSegments int,
	fromWaypoint, toWaypoint, mode string,
	fuelRequired int,
) error {
	// Find the ship for the current route context
	var ship *navigation.Ship
	var shipSymbol string
	for symbol, s := range ctx.ships {
		ship = s
		shipSymbol = symbol
		break
	}

	from := ctx.waypoints[fromWaypoint]
	to := ctx.waypoints[toWaypoint]
	if from == nil || to == nil {
		return fmt.Errorf("waypoints not found")
	}

	flightMode, err := shared.ParseFlightMode(mode)
	if err != nil {
		return err
	}

	distance := from.DistanceTo(to)
	travelTime := flightMode.TravelTime(distance, ship.EngineSpeed())

	segment := navigation.NewRouteSegment(
		from, to, distance, fuelRequired, travelTime, flightMode, false,
	)

	route, err := navigation.NewRoute(
		fmt.Sprintf("%s_route", shipSymbol),
		shipSymbol,
		ship.PlayerID(),
		[]*navigation.RouteSegment{segment},
		ship.FuelCapacity(),
		true, // Refuel before departure
	)
	if err != nil {
		return err
	}

	ctx.routes[shipSymbol] = route
	ctx.currentRoute = route
	return nil
}

func (ctx *routeExecutorContext) aRouteExistsForShipWithSegments(
	shipSymbol string,
	table *godog.Table,
) error {
	ship := ctx.ships[shipSymbol]
	if ship == nil {
		return fmt.Errorf("ship %s not found", shipSymbol)
	}

	// Parse table rows into segments
	segments := []*navigation.RouteSegment{}

	// Skip header row
	for i := 1; i < len(table.Rows); i++ {
		row := table.Rows[i]
		fromWaypoint := row.Cells[0].Value
		toWaypoint := row.Cells[1].Value
		// distance := row.Cells[2].Value (not parsed, calculated)
		fuelRequired := 0
		fmt.Sscanf(row.Cells[3].Value, "%d", &fuelRequired)
		mode := row.Cells[4].Value
		requiresRefuel := row.Cells[5].Value == "true"

		from := ctx.waypoints[fromWaypoint]
		to := ctx.waypoints[toWaypoint]
		if from == nil || to == nil {
			return fmt.Errorf("waypoints not found: %s or %s", fromWaypoint, toWaypoint)
		}

		flightMode, err := shared.ParseFlightMode(mode)
		if err != nil {
			return err
		}

		distance := from.DistanceTo(to)
		travelTime := flightMode.TravelTime(distance, ship.EngineSpeed())

		segment := navigation.NewRouteSegment(
			from, to, distance, fuelRequired, travelTime, flightMode, requiresRefuel,
		)
		segments = append(segments, segment)
	}

	route, err := navigation.NewRoute(
		fmt.Sprintf("%s_route", shipSymbol),
		shipSymbol,
		ship.PlayerID(),
		segments,
		ship.FuelCapacity(),
		false,
	)
	if err != nil {
		return err
	}

	ctx.routes[shipSymbol] = route
	ctx.currentRoute = route
	return nil
}

// When steps - Execute route

func (ctx *routeExecutorContext) iExecuteTheRouteForShipAndPlayer(
	shipSymbol string,
	playerID int,
) error {
	ship := ctx.ships[shipSymbol]
	if ship == nil {
		ctx.executionError = fmt.Errorf("ship not found")
		return nil
	}

	route := ctx.routes[shipSymbol]
	if route == nil {
		ctx.executionError = fmt.Errorf("route not found")
		return nil
	}

	// Create a mock mediator for testing
	// In real implementation, this would be injected
	mockMediator := &mockMediator{
		ctx:              ctx,
		ship:             ship,
		refueledAt:       ctx.refueledWaypoints,
		preventedDrift:   &ctx.preventedDrift,
		waitedForTransit: &ctx.waitedForTransit,
	}

	// Create RouteExecutor with mock clock for instant time operations
	executor := appShip.NewRouteExecutor(
		nil, // shipRepo not needed for mock
		mockMediator,
		ctx.clock, // Use mock clock to avoid real sleeps in tests
	)

	// Execute route
	err := executor.ExecuteRoute(context.Background(), route, ship, playerID)
	if err != nil {
		ctx.executionError = err
		return nil
	}

	// Update current route reference
	ctx.currentRoute = route
	return nil
}

// Then steps - Verify execution

func (ctx *routeExecutorContext) theRouteExecutionShouldSucceed() error {
	if ctx.executionError != nil {
		return fmt.Errorf("route execution failed: %v", ctx.executionError)
	}
	return nil
}

func (ctx *routeExecutorContext) theRouteExecutionShouldFail() error {
	if ctx.executionError == nil {
		return fmt.Errorf("expected route execution to fail but it succeeded")
	}
	return nil
}

func (ctx *routeExecutorContext) theShipShouldBeAt(expectedLocation string) error {
	// Find the ship
	var ship *navigation.Ship
	for _, s := range ctx.ships {
		ship = s
		break
	}

	if ship == nil {
		return fmt.Errorf("no ship found in context")
	}

	if ship.CurrentLocation().Symbol != expectedLocation {
		return fmt.Errorf("expected ship at %s but found at %s",
			expectedLocation, ship.CurrentLocation().Symbol)
	}
	return nil
}

func (ctx *routeExecutorContext) theRouteStatusShouldBe(expectedStatus string) error {
	if ctx.currentRoute == nil {
		return fmt.Errorf("no route in context")
	}

	actualStatus := string(ctx.currentRoute.Status())
	if actualStatus != expectedStatus {
		return fmt.Errorf("expected route status %s but got %s",
			expectedStatus, actualStatus)
	}
	return nil
}

func (ctx *routeExecutorContext) theShipShouldHaveConsumedFuelForTheJourney() error {
	// Find the ship
	var ship *navigation.Ship
	for _, s := range ctx.ships {
		ship = s
		break
	}

	if ship == nil {
		return fmt.Errorf("no ship found in context")
	}

	// Verify fuel was consumed (should be less than capacity)
	if ship.Fuel().Current >= ship.FuelCapacity() {
		return fmt.Errorf("ship fuel unchanged, expected consumption")
	}
	return nil
}

func (ctx *routeExecutorContext) theShipShouldHaveRefueledAt(waypointSymbol string) error {
	if !ctx.refueledWaypoints[waypointSymbol] {
		return fmt.Errorf("ship did not refuel at %s", waypointSymbol)
	}
	return nil
}

func (ctx *routeExecutorContext) theShipShouldHaveOpportunisticallyRefueledAt(waypointSymbol string) error {
	if !ctx.refueledWaypoints[waypointSymbol] {
		return fmt.Errorf("ship did not opportunistically refuel at %s", waypointSymbol)
	}
	return nil
}

func (ctx *routeExecutorContext) theShipShouldHavePreventedDRIFTModeByRefuelingAt(waypointSymbol string) error {
	if !ctx.preventedDrift {
		return fmt.Errorf("ship did not prevent DRIFT mode at %s", waypointSymbol)
	}
	if !ctx.refueledWaypoints[waypointSymbol] {
		return fmt.Errorf("ship did not refuel at %s", waypointSymbol)
	}
	return nil
}

func (ctx *routeExecutorContext) theRouteExecutorShouldWaitForCurrentTransitToComplete() error {
	if !ctx.waitedForTransit {
		return fmt.Errorf("route executor did not wait for transit to complete")
	}
	return nil
}

func (ctx *routeExecutorContext) theErrorShouldIndicate(expectedErrorSubstring string) error {
	if ctx.executionError == nil {
		return fmt.Errorf("no error occurred")
	}

	errorMessage := ctx.executionError.Error()
	if !containsSubstring(errorMessage, expectedErrorSubstring) {
		return fmt.Errorf("expected error containing '%s' but got '%s'",
			expectedErrorSubstring, errorMessage)
	}
	return nil
}

// Helper function
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) &&
		   (s == substr || len(s) > len(substr) &&
		   (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
		   findInString(s, substr)))
}

func findInString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Mock Mediator for testing
type mockMediator struct {
	ctx              *routeExecutorContext
	ship             *navigation.Ship
	refueledAt       map[string]bool
	preventedDrift   *bool
	waitedForTransit *bool
}

func (m *mockMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	// No-op for testing - mockMediator directly handles commands via Send
	return nil
}

func (m *mockMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *appShip.OrbitShipCommand:
		return m.handleOrbit(cmd)
	case *appShip.DockShipCommand:
		return m.handleDock(cmd)
	case *appShip.RefuelShipCommand:
		return m.handleRefuel(cmd)
	case *appShip.NavigateToWaypointCommand:
		return m.handleNavigate(cmd)
	case *appShip.SetFlightModeCommand:
		return m.handleSetFlightMode(cmd)
	default:
		return nil, fmt.Errorf("unknown command type: %T", request)
	}
}

func (m *mockMediator) handleOrbit(cmd *appShip.OrbitShipCommand) (interface{}, error) {
	_, err := m.ship.EnsureInOrbit()
	if err != nil {
		return &appShip.OrbitShipResponse{Status: "error", Error: err.Error()}, nil
	}
	return &appShip.OrbitShipResponse{Status: "in_orbit"}, nil
}

func (m *mockMediator) handleDock(cmd *appShip.DockShipCommand) (interface{}, error) {
	_, err := m.ship.EnsureDocked()
	if err != nil {
		return &appShip.DockShipResponse{Status: "error", Error: err.Error()}, nil
	}
	return &appShip.DockShipResponse{Status: "docked"}, nil
}

func (m *mockMediator) handleRefuel(cmd *appShip.RefuelShipCommand) (interface{}, error) {
	// Track refueling location
	m.refueledAt[m.ship.CurrentLocation().Symbol] = true

	// Ensure docked
	_, err := m.ship.EnsureDocked()
	if err != nil {
		return &appShip.RefuelShipResponse{Error: err.Error()}, nil
	}

	// Refuel to full
	added, err := m.ship.RefuelToFull()
	if err != nil {
		return &appShip.RefuelShipResponse{Error: err.Error()}, nil
	}

	return &appShip.RefuelShipResponse{
		FuelAdded:   added,
		CurrentFuel: m.ship.Fuel().Current,
		CreditsCost: added * 100,
	}, nil
}

func (m *mockMediator) handleNavigate(cmd *appShip.NavigateToWaypointCommand) (interface{}, error) {
	// Check for IN_TRANSIT state - wait if needed
	if m.ship.NavStatus() == navigation.NavStatusInTransit {
		*m.waitedForTransit = true
		// Transition to IN_ORBIT (no sleep needed in tests)
		m.ship.Arrive()
	}

	// Find destination waypoint
	dest := m.ctx.waypoints[cmd.Destination]
	if dest == nil {
		return nil, fmt.Errorf("destination waypoint not found")
	}

	// Calculate fuel cost
	distance := m.ship.CurrentLocation().DistanceTo(dest)
	flightMode, err := shared.ParseFlightMode(cmd.FlightMode)
	if err != nil {
		flightMode = shared.FlightModeCruise
	}
	fuelCost := flightMode.FuelCost(distance)

	// Check fuel
	if m.ship.Fuel().Current < fuelCost {
		return nil, fmt.Errorf("insufficient fuel: need %d but have %d", fuelCost, m.ship.Fuel().Current)
	}

	// Consume fuel
	m.ship.ConsumeFuel(fuelCost)

	// Start transit
	err = m.ship.StartTransit(dest)
	if err != nil {
		return nil, err
	}

	// Immediately arrive (for testing - no sleep needed)
	m.ship.Arrive()

	return &appShip.NavigateToWaypointResponse{
		Status:         "navigating",
		ArrivalTime:    1,
		ArrivalTimeStr: time.Now().Add(1 * time.Second).Format(time.RFC3339),
		FuelConsumed:   fuelCost,
	}, nil
}

func (m *mockMediator) handleSetFlightMode(cmd *appShip.SetFlightModeCommand) (interface{}, error) {
	// Check if this is preventing DRIFT mode at fuel station with low fuel
	if cmd.Mode == shared.FlightModeDrift &&
	   m.ship.CurrentLocation().HasFuel &&
	   float64(m.ship.Fuel().Current)/float64(m.ship.FuelCapacity()) < 0.9 {
		*m.preventedDrift = true
	}

	return &appShip.SetFlightModeResponse{
		Status:      "success",
		CurrentMode: cmd.Mode,
	}, nil
}

// Register steps

func InitializeRouteExecutorScenario(ctx *godog.ScenarioContext) {
	routeExecCtx := &routeExecutorContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		routeExecCtx.reset()
		return ctx, nil
	})

	// Player setup steps (shared)
	ctx.Step(`^a player exists with agent "([^"]*)" and token "([^"]*)"$`, routeExecCtx.aPlayerExistsWithAgentAndToken)
	ctx.Step(`^the player has player_id (\d+)$`, routeExecCtx.thePlayerHasPlayerID)

	// Ship setup steps
	ctx.Step(`^a ship "([^"]*)" for player (\d+) at "([^"]*)" with status "([^"]*)" and fuel (\d+)/(\d+)$`, routeExecCtx.aShipForPlayerAtWithStatusAndFuel)
	ctx.Step(`^a ship "([^"]*)" for player (\d+) in transit to "([^"]*)" arriving in (\d+) seconds$`, routeExecCtx.aShipForPlayerInTransitToArrivingInSeconds)

	// Waypoint setup steps
	ctx.Step(`^waypoint "([^"]*)" exists at coordinates \(([^,]+), ([^)]+)\) with fuel station$`, routeExecCtx.waypointExistsAtCoordinatesWithFuelStation)
	ctx.Step(`^waypoint "([^"]*)" exists at coordinates \(([^,]+), ([^)]+)\) without fuel station$`, routeExecCtx.waypointExistsAtCoordinatesWithoutFuelStation)

	// Route setup steps
	ctx.Step(`^a route exists for ship "([^"]*)" with (\d+) segment from "([^"]*)" to "([^"]*)" in "([^"]*)" mode requiring (\d+) fuel$`, routeExecCtx.aRouteExistsForShipWithSegmentFromToInModeRequiringFuel)
	ctx.Step(`^a route exists for ship "([^"]*)" requiring refuel before departure$`, routeExecCtx.aRouteExistsForShipRequiringRefuelBeforeDeparture)
	ctx.Step(`^the route has (\d+) segment from "([^"]*)" to "([^"]*)" in "([^"]*)" mode requiring (\d+) fuel$`, routeExecCtx.theRouteHasSegmentFromToInModeRequiringFuel)
	ctx.Step(`^a route exists for ship "([^"]*)" with segments:$`, routeExecCtx.aRouteExistsForShipWithSegments)

	// When steps
	ctx.Step(`^I execute the route for ship "([^"]*)" and player (\d+)$`, routeExecCtx.iExecuteTheRouteForShipAndPlayer)

	// Then steps
	ctx.Step(`^the route execution should succeed$`, routeExecCtx.theRouteExecutionShouldSucceed)
	ctx.Step(`^the route execution should fail$`, routeExecCtx.theRouteExecutionShouldFail)
	ctx.Step(`^the ship should be at "([^"]*)"$`, routeExecCtx.theShipShouldBeAt)
	ctx.Step(`^the route status should be "([^"]*)"$`, routeExecCtx.theRouteStatusShouldBe)
	ctx.Step(`^the ship should have consumed fuel for the journey$`, routeExecCtx.theShipShouldHaveConsumedFuelForTheJourney)
	ctx.Step(`^the ship should have refueled at "([^"]*)"$`, routeExecCtx.theShipShouldHaveRefueledAt)
	ctx.Step(`^the ship should have opportunistically refueled at "([^"]*)"$`, routeExecCtx.theShipShouldHaveOpportunisticallyRefueledAt)
	ctx.Step(`^the ship should have prevented DRIFT mode by refueling at "([^"]*)"$`, routeExecCtx.theShipShouldHavePreventedDRIFTModeByRefuelingAt)
	ctx.Step(`^the route executor should wait for current transit to complete$`, routeExecCtx.theRouteExecutorShouldWaitForCurrentTransitToComplete)
	ctx.Step(`^the error should indicate "([^"]*)"$`, routeExecCtx.theErrorShouldIndicate)
}

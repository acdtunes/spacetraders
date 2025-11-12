package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type routeContext struct {
	route     *navigation.Route
	segment   *navigation.RouteSegment
	segments  []*navigation.RouteSegment
	waypoints map[string]*shared.Waypoint
	err       error
	intResult int
}

func (rc *routeContext) reset() {
	rc.route = nil
	rc.segment = nil
	rc.segments = []*navigation.RouteSegment{}
	rc.waypoints = make(map[string]*shared.Waypoint)
	rc.err = nil
	rc.intResult = 0
}

func (rc *routeContext) testWaypointsAreAvailable(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		symbol := row.Cells[0].Value
		x := parseFloat(row.Cells[1].Value)
		y := parseFloat(row.Cells[2].Value)
		waypoint, _ := shared.NewWaypoint(symbol, x, y)
		rc.waypoints[symbol] = waypoint
	}
	return nil
}

func (rc *routeContext) iCreateARouteSegmentFromToWith(from, to string, table *godog.Table) error {
	fromWP := rc.waypoints[from]
	toWP := rc.waypoints[to]

	var distance float64
	var fuelRequired, travelTime int
	var flightMode shared.FlightMode
	var requiresRefuel bool

	for i, row := range table.Rows {
		if i == 0 {
			continue
		}
		key := row.Cells[0].Value
		val := row.Cells[1].Value

		switch key {
		case "distance":
			distance = parseFloat(val)
		case "fuel_required":
			fuelRequired = parseInt(val)
		case "travel_time":
			travelTime = parseInt(val)
		case "flight_mode":
			flightMode = parseFlightMode(val)
		case "requires_refuel":
			requiresRefuel = parseBool(val)
		}
	}

	rc.segment = navigation.NewRouteSegment(
		fromWP, toWP, distance, fuelRequired, travelTime, flightMode, requiresRefuel,
	)
	rc.segments = append(rc.segments, rc.segment)
	return nil
}

func (rc *routeContext) theSegmentShouldHaveFromWaypoint(symbol string) error {
	if rc.segment.FromWaypoint.Symbol != symbol {
		return fmt.Errorf("expected from_waypoint '%s' but got '%s'", symbol, rc.segment.FromWaypoint.Symbol)
	}
	return nil
}

func (rc *routeContext) theSegmentShouldHaveToWaypoint(symbol string) error {
	if rc.segment.ToWaypoint.Symbol != symbol {
		return fmt.Errorf("expected to_waypoint '%s' but got '%s'", symbol, rc.segment.ToWaypoint.Symbol)
	}
	return nil
}

func (rc *routeContext) theSegmentShouldHaveDistance(distance float64) error {
	if rc.segment.Distance != distance {
		return fmt.Errorf("expected distance %.1f but got %.1f", distance, rc.segment.Distance)
	}
	return nil
}

func (rc *routeContext) theSegmentShouldHaveFuelRequired(fuel int) error {
	if rc.segment.FuelRequired != fuel {
		return fmt.Errorf("expected fuel_required %d but got %d", fuel, rc.segment.FuelRequired)
	}
	return nil
}

func (rc *routeContext) theSegmentShouldHaveTravelTime(time int) error {
	if rc.segment.TravelTime != time {
		return fmt.Errorf("expected travel_time %d but got %d", time, rc.segment.TravelTime)
	}
	return nil
}

func (rc *routeContext) theSegmentShouldHaveFlightMode(mode string) error {
	if rc.segment.FlightMode.Name() != mode {
		return fmt.Errorf("expected flight_mode '%s' but got '%s'", mode, rc.segment.FlightMode.Name())
	}
	return nil
}

func (rc *routeContext) theSegmentShouldHaveRequiresRefuel(expected bool) error {
	if rc.segment.RequiresRefuel != expected {
		return fmt.Errorf("expected requires_refuel %t but got %t", expected, rc.segment.RequiresRefuel)
	}
	return nil
}

func (rc *routeContext) aRouteSegmentFromToWithDistance(from, to string, distance float64) error {
	fromWP := rc.waypoints[from]
	toWP := rc.waypoints[to]
	rc.segment = navigation.NewRouteSegment(
		fromWP, toWP, distance, 50, 100, shared.FlightModeCruise, false,
	)
	rc.segments = append(rc.segments, rc.segment)
	return nil
}

func (rc *routeContext) iCreateARouteWith(table *godog.Table) error {
	var routeID, shipSymbol string
	var playerID, shipFuelCapacity int

	for i, row := range table.Rows {
		if i == 0 {
			continue
		}
		key := row.Cells[0].Value
		val := row.Cells[1].Value

		switch key {
		case "route_id":
			routeID = val
		case "ship_symbol":
			shipSymbol = val
		case "player_id":
			playerID = parseInt(val)
		case "ship_fuel_capacity":
			shipFuelCapacity = parseInt(val)
		}
	}

	rc.route, rc.err = navigation.NewRoute(
		routeID, shipSymbol, playerID, rc.segments, shipFuelCapacity, false,
	)
	return rc.err
}

func (rc *routeContext) theRouteShouldHaveRouteID(routeID string) error {
	if rc.route.RouteID() != routeID {
		return fmt.Errorf("expected route_id '%s' but got '%s'", routeID, rc.route.RouteID())
	}
	return nil
}

func (rc *routeContext) theRouteShouldHaveShipSymbol(shipSymbol string) error {
	if rc.route.ShipSymbol() != shipSymbol {
		return fmt.Errorf("expected ship_symbol '%s' but got '%s'", shipSymbol, rc.route.ShipSymbol())
	}
	return nil
}

func (rc *routeContext) theRouteShouldHavePlayerID(playerID int) error {
	if rc.route.PlayerID() != playerID {
		return fmt.Errorf("expected player_id %d but got %d", playerID, rc.route.PlayerID())
	}
	return nil
}

func (rc *routeContext) theRouteShouldHaveSegments(count int) error {
	if len(rc.route.Segments()) != count {
		return fmt.Errorf("expected %d segments but got %d", count, len(rc.route.Segments()))
	}
	return nil
}

func (rc *routeContext) theRouteShouldHaveStatus(status string) error {
	if string(rc.route.Status()) != status {
		return fmt.Errorf("expected status '%s' but got '%s'", status, rc.route.Status())
	}
	return nil
}

func (rc *routeContext) iCreateARouteWithEmptySegments() error {
	rc.route, rc.err = navigation.NewRoute(
		"route-1", "SHIP-1", 1, []*navigation.RouteSegment{}, 100, false,
	)
	return rc.err
}

func (rc *routeContext) iAttemptToCreateARouteWithDisconnectedSegments() error {
	rc.route, rc.err = navigation.NewRoute(
		"route-1", "SHIP-1", 1, rc.segments, 100, false,
	)
	return nil
}

func (rc *routeContext) theRouteCreationShouldFailWithError(expectedError string) error {
	if rc.err == nil {
		return fmt.Errorf("expected error containing '%s' but got no error", expectedError)
	}
	if !strings.Contains(rc.err.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedError, rc.err.Error())
	}
	return nil
}

func (rc *routeContext) aRouteSegmentFromToRequiringFuel(from, to string, fuel int) error {
	fromWP := rc.waypoints[from]
	toWP := rc.waypoints[to]
	rc.segment = navigation.NewRouteSegment(
		fromWP, toWP, 100.0, fuel, 100, shared.FlightModeCruise, false,
	)
	rc.segments = append(rc.segments, rc.segment)
	return nil
}

func (rc *routeContext) iAttemptToCreateARouteWithFuelCapacity(capacity int) error {
	rc.route, rc.err = navigation.NewRoute(
		"route-1", "SHIP-1", 1, rc.segments, capacity, false,
	)
	return nil
}

func (rc *routeContext) iCreateARouteWithFuelCapacity(capacity int) error {
	rc.route, rc.err = navigation.NewRoute(
		"route-1", "SHIP-1", 1, rc.segments, capacity, false,
	)
	return rc.err
}

func (rc *routeContext) theRouteShouldBeCreatedSuccessfully() error {
	if rc.route == nil {
		return fmt.Errorf("expected route to be created but got nil")
	}
	return nil
}

// Helper functions
func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

func parseInt(s string) int {
	var i int
	fmt.Sscanf(s, "%d", &i)
	return i
}

func parseBool(s string) bool {
	return strings.ToLower(s) == "true"
}

func parseFlightMode(s string) shared.FlightMode {
	switch s {
	case "CRUISE":
		return shared.FlightModeCruise
	case "DRIFT":
		return shared.FlightModeDrift
	case "BURN":
		return shared.FlightModeBurn
	case "STEALTH":
		return shared.FlightModeStealth
	default:
		return shared.FlightModeCruise
	}
}

// InitializeRouteScenario registers all route-related step definitions
func InitializeRouteScenario(ctx *godog.ScenarioContext) {
	rc := &routeContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		rc.reset()
		return ctx, nil
	})

	// Route segment steps
	ctx.Step(`^test waypoints are available:$`, rc.testWaypointsAreAvailable)
	ctx.Step(`^I create a route segment from "([^"]*)" to "([^"]*)" with:$`, rc.iCreateARouteSegmentFromToWith)
	ctx.Step(`^the segment should have from_waypoint "([^"]*)"$`, rc.theSegmentShouldHaveFromWaypoint)
	ctx.Step(`^the segment should have to_waypoint "([^"]*)"$`, rc.theSegmentShouldHaveToWaypoint)
	ctx.Step(`^the segment should have distance ([0-9.]+)$`, rc.theSegmentShouldHaveDistance)
	ctx.Step(`^the segment should have fuel_required (\d+)$`, rc.theSegmentShouldHaveFuelRequired)
	ctx.Step(`^the segment should have travel_time (\d+)$`, rc.theSegmentShouldHaveTravelTime)
	ctx.Step(`^the segment should have flight_mode "([^"]*)"$`, rc.theSegmentShouldHaveFlightMode)
	ctx.Step(`^the segment should have requires_refuel (true|false)$`, rc.theSegmentShouldHaveRequiresRefuel)

	// Route creation steps
	ctx.Step(`^a route segment from "([^"]*)" to "([^"]*)" with distance ([0-9.]+)$`, rc.aRouteSegmentFromToWithDistance)
	ctx.Step(`^I create a route with:$`, rc.iCreateARouteWith)
	ctx.Step(`^the route should have route_id "([^"]*)"$`, rc.theRouteShouldHaveRouteID)
	ctx.Step(`^the route should have ship_symbol "([^"]*)"$`, rc.theRouteShouldHaveShipSymbol)
	ctx.Step(`^the route should have player_id (\d+)$`, rc.theRouteShouldHavePlayerID)
	ctx.Step(`^the route should have (\d+) segments$`, rc.theRouteShouldHaveSegments)
	ctx.Step(`^the route should have status "([^"]*)"$`, rc.theRouteShouldHaveStatus)
	ctx.Step(`^I create a route with empty segments$`, rc.iCreateARouteWithEmptySegments)
	ctx.Step(`^I attempt to create a route with disconnected segments$`, rc.iAttemptToCreateARouteWithDisconnectedSegments)
	ctx.Step(`^the route creation should fail with error "([^"]*)"$`, rc.theRouteCreationShouldFailWithError)
	ctx.Step(`^a route segment from "([^"]*)" to "([^"]*)" requiring (\d+) fuel$`, rc.aRouteSegmentFromToRequiringFuel)
	ctx.Step(`^I attempt to create a route with fuel capacity (\d+)$`, rc.iAttemptToCreateARouteWithFuelCapacity)
	ctx.Step(`^I create a route with fuel capacity (\d+)$`, rc.iCreateARouteWithFuelCapacity)
	ctx.Step(`^the route should be created successfully$`, rc.theRouteShouldBeCreatedSuccessfully)

	// Add more steps as needed (execution, calculations, etc.)
	// Note: Complete implementation would include all remaining steps from the feature files
}

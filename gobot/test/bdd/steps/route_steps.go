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
	route          *navigation.Route
	segment        *navigation.RouteSegment
	segments       []*navigation.RouteSegment
	waypoints      map[string]*shared.Waypoint
	err            error
	intResult      int
	floatResult    float64
	boolResult     bool
	segmentResult  *navigation.RouteSegment
	segmentsResult []*navigation.RouteSegment
}

func (rc *routeContext) reset() {
	rc.route = nil
	rc.segment = nil
	rc.segments = []*navigation.RouteSegment{}
	rc.waypoints = make(map[string]*shared.Waypoint)
	rc.err = nil
	rc.intResult = 0
	rc.floatResult = 0
	rc.boolResult = false
	rc.segmentResult = nil
	rc.segmentsResult = nil
}

func (rc *routeContext) testWaypointsAreAvailable(table *godog.Table) error {
	// Godog table.Rows does NOT include header row, only data rows
	for _, row := range table.Rows {
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

	// Godog table.Rows does NOT include header row, only data rows
	for _, row := range table.Rows {
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

func (rc *routeContext) theSegmentShouldHaveRequiresRefuel(expectedStr string) error {
	expected := expectedStr == "true"
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

	// Godog table.Rows does NOT include header row, only data rows
	for _, row := range table.Rows {
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

// ============================================================================
// Route Execution Steps
// ============================================================================

func (rc *routeContext) aNewlyCreatedRoute() error {
	// Create a simple route with 2 segments
	return rc.aRouteWithSegments(2)
}

func (rc *routeContext) aRouteInStatus(status string) error {
	// Create a route with 2 segments
	if err := rc.aRouteWithSegments(2); err != nil {
		return err
	}

	// Transition to the desired status
	switch status {
	case "PLANNED":
		// Already in PLANNED status
	case "EXECUTING":
		rc.route.StartExecution()
	case "COMPLETED":
		rc.route.StartExecution()
		rc.route.CompleteSegment()
		rc.route.CompleteSegment()
	case "FAILED":
		rc.route.StartExecution()
		rc.route.FailRoute("Test failure")
	case "ABORTED":
		rc.route.StartExecution()
		rc.route.AbortRoute("Test abort")
	}
	return nil
}

func (rc *routeContext) aRouteInStatusWithSegments(status string, count int) error {
	if err := rc.aRouteWithSegments(count); err != nil {
		return err
	}

	// Transition to the desired status
	switch status {
	case "PLANNED":
		// Already in PLANNED status
	case "EXECUTING":
		rc.route.StartExecution()
	case "COMPLETED":
		rc.route.StartExecution()
		for i := 0; i < count; i++ {
			rc.route.CompleteSegment()
		}
	case "FAILED":
		rc.route.StartExecution()
		rc.route.FailRoute("Test failure")
	}
	return nil
}

func (rc *routeContext) theCurrentSegmentIndexIs(index int) error {
	// This is a Given step - we need to advance the route to this index
	for i := 0; i < index; i++ {
		rc.route.CompleteSegment()
	}
	return nil
}

func (rc *routeContext) iStartRouteExecution() error {
	rc.err = rc.route.StartExecution()
	return rc.err
}

func (rc *routeContext) iAttemptToStartRouteExecution() error {
	rc.err = rc.route.StartExecution()
	return nil
}

func (rc *routeContext) iCompleteTheCurrentSegment() error {
	rc.err = rc.route.CompleteSegment()
	return rc.err
}

func (rc *routeContext) iAttemptToCompleteTheCurrentSegment() error {
	rc.err = rc.route.CompleteSegment()
	return nil
}

func (rc *routeContext) theCurrentSegmentIndexShouldBe(expectedIndex int) error {
	actualIndex := rc.route.CurrentSegmentIndex()
	if actualIndex != expectedIndex {
		return fmt.Errorf("expected current_segment_index %d but got %d", expectedIndex, actualIndex)
	}
	return nil
}

func (rc *routeContext) theOperationShouldFailWithError(expectedError string) error {
	if rc.err == nil {
		return fmt.Errorf("expected error containing '%s' but got no error", expectedError)
	}
	if !strings.Contains(rc.err.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedError, rc.err.Error())
	}
	return nil
}

func (rc *routeContext) iFailTheRouteWithReason(reason string) error {
	rc.route.FailRoute(reason)
	return nil
}

func (rc *routeContext) iAbortTheRouteWithReason(reason string) error {
	rc.route.AbortRoute(reason)
	return nil
}

// ============================================================================
// Route Calculation Steps
// ============================================================================

func (rc *routeContext) aRouteWithSegmentsHavingDistances(table *godog.Table) error {
	rc.segments = []*navigation.RouteSegment{}
	// Create waypoints for the segments
	for i, row := range table.Rows {
		distance := parseFloat(row.Cells[0].Value)
		fromSymbol := fmt.Sprintf("X1-W%d", i)
		toSymbol := fmt.Sprintf("X1-W%d", i+1)

		fromWP, _ := shared.NewWaypoint(fromSymbol, 0.0, float64(i*100))
		toWP, _ := shared.NewWaypoint(toSymbol, 0.0, float64((i+1)*100))
		rc.waypoints[fromSymbol] = fromWP
		rc.waypoints[toSymbol] = toWP

		segment := navigation.NewRouteSegment(fromWP, toWP, distance, 50, 100, shared.FlightModeCruise, false)
		rc.segments = append(rc.segments, segment)
	}

	// Create the route
	rc.route, rc.err = navigation.NewRoute("route-1", "SHIP-1", 1, rc.segments, 1000, false)
	return rc.err
}

func (rc *routeContext) aRouteWithSegmentsRequiringFuel(table *godog.Table) error {
	rc.segments = []*navigation.RouteSegment{}
	for i, row := range table.Rows {
		fuel := parseInt(row.Cells[0].Value)
		fromSymbol := fmt.Sprintf("X1-W%d", i)
		toSymbol := fmt.Sprintf("X1-W%d", i+1)

		fromWP, _ := shared.NewWaypoint(fromSymbol, 0.0, float64(i*100))
		toWP, _ := shared.NewWaypoint(toSymbol, 0.0, float64((i+1)*100))
		rc.waypoints[fromSymbol] = fromWP
		rc.waypoints[toSymbol] = toWP

		segment := navigation.NewRouteSegment(fromWP, toWP, 100.0, fuel, 100, shared.FlightModeCruise, false)
		rc.segments = append(rc.segments, segment)
	}

	rc.route, rc.err = navigation.NewRoute("route-1", "SHIP-1", 1, rc.segments, 1000, false)
	return rc.err
}

func (rc *routeContext) aRouteWithSegmentsHavingTravelTimes(table *godog.Table) error {
	rc.segments = []*navigation.RouteSegment{}
	for i, row := range table.Rows {
		travelTime := parseInt(row.Cells[0].Value)
		fromSymbol := fmt.Sprintf("X1-W%d", i)
		toSymbol := fmt.Sprintf("X1-W%d", i+1)

		fromWP, _ := shared.NewWaypoint(fromSymbol, 0.0, float64(i*100))
		toWP, _ := shared.NewWaypoint(toSymbol, 0.0, float64((i+1)*100))
		rc.waypoints[fromSymbol] = fromWP
		rc.waypoints[toSymbol] = toWP

		segment := navigation.NewRouteSegment(fromWP, toWP, 100.0, 50, travelTime, shared.FlightModeCruise, false)
		rc.segments = append(rc.segments, segment)
	}

	rc.route, rc.err = navigation.NewRoute("route-1", "SHIP-1", 1, rc.segments, 1000, false)
	return rc.err
}

func (rc *routeContext) aRouteWithASingleSegment(table *godog.Table) error {
	var distance float64
	var fuelRequired, travelTime int

	for _, row := range table.Rows {
		key := row.Cells[0].Value
		val := row.Cells[1].Value

		switch key {
		case "distance":
			distance = parseFloat(val)
		case "fuel_required":
			fuelRequired = parseInt(val)
		case "travel_time":
			travelTime = parseInt(val)
		}
	}

	fromWP, _ := shared.NewWaypoint("X1-A1", 0.0, 0.0)
	toWP, _ := shared.NewWaypoint("X1-B2", 100.0, 0.0)
	segment := navigation.NewRouteSegment(fromWP, toWP, distance, fuelRequired, travelTime, shared.FlightModeCruise, false)
	rc.segments = []*navigation.RouteSegment{segment}

	rc.route, rc.err = navigation.NewRoute("route-1", "SHIP-1", 1, rc.segments, 1000, false)
	return rc.err
}

func (rc *routeContext) iCalculateTheTotalDistance() error {
	rc.floatResult = rc.route.TotalDistance()
	return nil
}

func (rc *routeContext) iCalculateTheTotalFuelRequired() error {
	rc.intResult = rc.route.TotalFuelRequired()
	return nil
}

func (rc *routeContext) iCalculateTheTotalTravelTime() error {
	rc.intResult = rc.route.TotalTravelTime()
	return nil
}

func (rc *routeContext) iCalculateRouteTotals() error {
	rc.floatResult = rc.route.TotalDistance()
	rc.intResult = rc.route.TotalFuelRequired()
	// Also store travel time in a separate field if needed
	return nil
}

func (rc *routeContext) theTotalDistanceShouldBe(expected float64) error {
	if rc.floatResult != expected {
		return fmt.Errorf("expected total distance %.1f but got %.1f", expected, rc.floatResult)
	}
	return nil
}

func (rc *routeContext) theTotalFuelRequiredShouldBe(expected int) error {
	if rc.intResult != expected {
		return fmt.Errorf("expected total fuel required %d but got %d", expected, rc.intResult)
	}
	return nil
}

func (rc *routeContext) theTotalTravelTimeShouldBe(expected int) error {
	actual := rc.route.TotalTravelTime()
	if actual != expected {
		return fmt.Errorf("expected total travel time %d but got %d", expected, actual)
	}
	return nil
}

// ============================================================================
// Current Segment Steps
// ============================================================================

func (rc *routeContext) aRouteWithSegments(count int) error {
	rc.segments = []*navigation.RouteSegment{}
	for i := 0; i < count; i++ {
		fromSymbol := fmt.Sprintf("X1-W%d", i)
		toSymbol := fmt.Sprintf("X1-W%d", i+1)

		fromWP, _ := shared.NewWaypoint(fromSymbol, 0.0, float64(i*100))
		toWP, _ := shared.NewWaypoint(toSymbol, 0.0, float64((i+1)*100))
		rc.waypoints[fromSymbol] = fromWP
		rc.waypoints[toSymbol] = toWP

		segment := navigation.NewRouteSegment(fromWP, toWP, 100.0, 50, 100, shared.FlightModeCruise, false)
		rc.segments = append(rc.segments, segment)
	}

	rc.route, rc.err = navigation.NewRoute("route-1", "SHIP-1", 1, rc.segments, 1000, false)
	return rc.err
}

func (rc *routeContext) iGetTheCurrentSegment() error {
	rc.segmentResult = rc.route.CurrentSegment()
	return nil
}

func (rc *routeContext) itShouldBeTheFirstSegment() error {
	if rc.segmentResult == nil {
		return fmt.Errorf("expected first segment but got nil")
	}
	if len(rc.segments) == 0 {
		return fmt.Errorf("no segments available")
	}
	expected := rc.segments[0]
	if rc.segmentResult != expected {
		return fmt.Errorf("expected first segment but got different segment")
	}
	return nil
}

func (rc *routeContext) itShouldBeTheSecondSegment() error {
	if rc.segmentResult == nil {
		return fmt.Errorf("expected second segment but got nil")
	}
	if len(rc.segments) < 2 {
		return fmt.Errorf("not enough segments available")
	}
	expected := rc.segments[1]
	if rc.segmentResult != expected {
		return fmt.Errorf("expected second segment but got different segment")
	}
	return nil
}

func (rc *routeContext) theCurrentSegmentShouldBeNil() error {
	if rc.segmentResult != nil {
		return fmt.Errorf("expected nil but got segment")
	}
	return nil
}

// ============================================================================
// Remaining Segments Steps
// ============================================================================

func (rc *routeContext) iGetTheRemainingSegments() error {
	rc.segmentsResult = rc.route.RemainingSegments()
	return nil
}

func (rc *routeContext) thereShouldBeRemainingSegments(count int) error {
	actual := len(rc.segmentsResult)
	if actual != count {
		return fmt.Errorf("expected %d remaining segments but got %d", count, actual)
	}
	return nil
}

func (rc *routeContext) theRemainingSegmentShouldBeTheSecondSegment() error {
	if len(rc.segmentsResult) == 0 {
		return fmt.Errorf("expected remaining segments but got empty list")
	}
	if len(rc.segments) < 2 {
		return fmt.Errorf("not enough segments available")
	}
	expected := rc.segments[1]
	if rc.segmentsResult[0] != expected {
		return fmt.Errorf("expected second segment but got different segment")
	}
	return nil
}

// ============================================================================
// Get Current Segment Index Steps
// ============================================================================

func (rc *routeContext) iGetTheCurrentSegmentIndex() error {
	rc.intResult = rc.route.CurrentSegmentIndex()
	return nil
}

func (rc *routeContext) theIndexShouldBe(expected int) error {
	if rc.intResult != expected {
		return fmt.Errorf("expected index %d but got %d", expected, rc.intResult)
	}
	return nil
}

// ============================================================================
// Route Query Steps
// ============================================================================

func (rc *routeContext) aRouteWithRefuelBeforeDeparture(refuelStr string) error {
	refuel := refuelStr == "true"
	rc.segments = []*navigation.RouteSegment{}

	fromWP, _ := shared.NewWaypoint("X1-A1", 0.0, 0.0)
	toWP, _ := shared.NewWaypoint("X1-B2", 100.0, 0.0)
	segment := navigation.NewRouteSegment(fromWP, toWP, 100.0, 50, 100, shared.FlightModeCruise, false)
	rc.segments = append(rc.segments, segment)

	rc.route, rc.err = navigation.NewRoute("route-1", "SHIP-1", 1, rc.segments, 1000, refuel)
	return rc.err
}

func (rc *routeContext) hasRefuelAtStartShouldReturn(expectedStr string) error {
	expected := expectedStr == "true"
	actual := rc.route.HasRefuelAtStart()
	if actual != expected {
		return fmt.Errorf("expected HasRefuelAtStart %t but got %t", expected, actual)
	}
	return nil
}

func (rc *routeContext) iCallNextSegment() error {
	rc.segmentResult = rc.route.NextSegment()
	return nil
}

func (rc *routeContext) itShouldReturnTheFirstSegment() error {
	return rc.itShouldBeTheFirstSegment()
}

func (rc *routeContext) itShouldReturnTheSecondSegment() error {
	return rc.itShouldBeTheSecondSegment()
}

func (rc *routeContext) itShouldReturnNil() error {
	if rc.segmentResult != nil {
		return fmt.Errorf("expected nil but got segment")
	}
	return nil
}

func (rc *routeContext) iCallIsComplete() error {
	rc.boolResult = rc.route.IsComplete()
	return nil
}

func (rc *routeContext) itShouldReturnBool(expectedStr string) error {
	expected := expectedStr == "true"
	if rc.boolResult != expected {
		return fmt.Errorf("expected %t but got %t", expected, rc.boolResult)
	}
	return nil
}

func (rc *routeContext) iCallIsFailed() error {
	rc.boolResult = rc.route.IsFailed()
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

	// Route execution steps
	ctx.Step(`^a newly created route$`, rc.aNewlyCreatedRoute)
	ctx.Step(`^a route in "([^"]*)" status$`, rc.aRouteInStatus)
	ctx.Step(`^a route in "([^"]*)" status with (\d+) segments$`, rc.aRouteInStatusWithSegments)
	ctx.Step(`^the current_segment_index is (\d+)$`, rc.theCurrentSegmentIndexIs)
	ctx.Step(`^I start route execution$`, rc.iStartRouteExecution)
	ctx.Step(`^I attempt to start route execution$`, rc.iAttemptToStartRouteExecution)
	ctx.Step(`^I complete the current segment$`, rc.iCompleteTheCurrentSegment)
	ctx.Step(`^I attempt to complete the current segment$`, rc.iAttemptToCompleteTheCurrentSegment)
	ctx.Step(`^the current_segment_index should be (\d+)$`, rc.theCurrentSegmentIndexShouldBe)
	ctx.Step(`^the operation should fail with error "([^"]*)"$`, rc.theOperationShouldFailWithError)
	ctx.Step(`^I fail the route with reason "([^"]*)"$`, rc.iFailTheRouteWithReason)
	ctx.Step(`^I abort the route with reason "([^"]*)"$`, rc.iAbortTheRouteWithReason)

	// Route calculation steps
	ctx.Step(`^a route with segments having distances:$`, rc.aRouteWithSegmentsHavingDistances)
	ctx.Step(`^a route with segments requiring fuel:$`, rc.aRouteWithSegmentsRequiringFuel)
	ctx.Step(`^a route with segments having travel times:$`, rc.aRouteWithSegmentsHavingTravelTimes)
	ctx.Step(`^a route with a single segment:$`, rc.aRouteWithASingleSegment)
	ctx.Step(`^I calculate the total distance$`, rc.iCalculateTheTotalDistance)
	ctx.Step(`^I calculate the total fuel required$`, rc.iCalculateTheTotalFuelRequired)
	ctx.Step(`^I calculate the total travel time$`, rc.iCalculateTheTotalTravelTime)
	ctx.Step(`^I calculate route totals$`, rc.iCalculateRouteTotals)
	ctx.Step(`^the total distance should be ([0-9.]+)$`, rc.theTotalDistanceShouldBe)
	ctx.Step(`^the total fuel required should be (\d+)$`, rc.theTotalFuelRequiredShouldBe)
	ctx.Step(`^the total travel time should be (\d+)$`, rc.theTotalTravelTimeShouldBe)

	// Current segment steps
	ctx.Step(`^a route with (\d+) segments$`, rc.aRouteWithSegments)
	ctx.Step(`^I get the current segment$`, rc.iGetTheCurrentSegment)
	ctx.Step(`^it should be the first segment$`, rc.itShouldBeTheFirstSegment)
	ctx.Step(`^it should be the second segment$`, rc.itShouldBeTheSecondSegment)
	ctx.Step(`^the current segment should be nil$`, rc.theCurrentSegmentShouldBeNil)

	// Remaining segments steps
	ctx.Step(`^I get the remaining segments$`, rc.iGetTheRemainingSegments)
	ctx.Step(`^there should be (\d+) remaining segments?$`, rc.thereShouldBeRemainingSegments)
	ctx.Step(`^the remaining segment should be the second segment$`, rc.theRemainingSegmentShouldBeTheSecondSegment)

	// Get current segment index steps
	ctx.Step(`^I get the current segment index$`, rc.iGetTheCurrentSegmentIndex)
	ctx.Step(`^the index should be (\d+)$`, rc.theIndexShouldBe)

	// Route query steps
	ctx.Step(`^a route with refuel_before_departure (true|false)$`, rc.aRouteWithRefuelBeforeDeparture)
	ctx.Step(`^HasRefuelAtStart should return (true|false)$`, rc.hasRefuelAtStartShouldReturn)
	ctx.Step(`^I call NextSegment$`, rc.iCallNextSegment)
	ctx.Step(`^it should return the first segment$`, rc.itShouldReturnTheFirstSegment)
	ctx.Step(`^it should return the second segment$`, rc.itShouldReturnTheSecondSegment)
	ctx.Step(`^it should return nil$`, rc.itShouldReturnNil)
	ctx.Step(`^I call IsComplete$`, rc.iCallIsComplete)
	ctx.Step(`^it should return (true|false)$`, rc.itShouldReturnBool)
	ctx.Step(`^I call IsFailed$`, rc.iCallIsFailed)
}

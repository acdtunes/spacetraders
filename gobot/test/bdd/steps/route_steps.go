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
	waypointsMap      map[string]*shared.Waypoint
	segments          []*navigation.RouteSegment
	route             *navigation.Route
	err               error
	boolResult        bool
	floatResult       float64
	intResult         int
	currentSegment    *navigation.RouteSegment
	remainingSegments []*navigation.RouteSegment
	stringResult      string
	routeIDResult     string
	shipSymbolResult  string
	playerIDResult    int
}

func (rc *routeContext) reset() {
	rc.waypointsMap = make(map[string]*shared.Waypoint)
	rc.segments = nil
	rc.route = nil
	rc.err = nil
	rc.boolResult = false
	rc.floatResult = 0
	rc.intResult = 0
	rc.currentSegment = nil
	rc.remainingSegments = nil
}

// Waypoint setup steps

func (rc *routeContext) waypoints(table *godog.Table) error {
	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		symbol := row.Cells[0].Value
		var x, y float64
		fmt.Sscanf(row.Cells[1].Value, "%f", &x)
		fmt.Sscanf(row.Cells[2].Value, "%f", &y)

		waypoint, err := shared.NewWaypoint(symbol, x, y)
		if err != nil {
			return err
		}
		rc.waypointsMap[symbol] = waypoint
	}
	return nil
}

// Route segment setup steps

func (rc *routeContext) routeSegments(table *godog.Table) error {
	rc.segments = make([]*navigation.RouteSegment, 0)

	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}

		fromSymbol := row.Cells[0].Value
		toSymbol := row.Cells[1].Value
		var distance float64
		var fuel, time int

		fmt.Sscanf(row.Cells[2].Value, "%f", &distance)
		fmt.Sscanf(row.Cells[3].Value, "%d", &fuel)
		fmt.Sscanf(row.Cells[4].Value, "%d", &time)
		modeStr := row.Cells[5].Value

		fromWaypoint, exists := rc.waypointsMap[fromSymbol]
		if !exists {
			return fmt.Errorf("waypoint %s not found", fromSymbol)
		}
		toWaypoint, exists := rc.waypointsMap[toSymbol]
		if !exists {
			return fmt.Errorf("waypoint %s not found", toSymbol)
		}

		var mode shared.FlightMode
		switch modeStr {
		case "CRUISE":
			mode = shared.FlightModeCruise
		case "BURN":
			mode = shared.FlightModeBurn
		case "DRIFT":
			mode = shared.FlightModeDrift
		case "STEALTH":
			mode = shared.FlightModeStealth
		default:
			mode = shared.FlightModeCruise
		}

		segment := navigation.NewRouteSegment(
			fromWaypoint,
			toWaypoint,
			distance,
			fuel,
			time,
			mode,
			false, // requiresRefuel
		)
		rc.segments = append(rc.segments, segment)
	}
	return nil
}

// Route creation steps

func (rc *routeContext) iCreateARouteForShipPlayerFuelCapacity(
	shipSymbol string, playerID, fuelCapacity int,
) error {
	rc.route, rc.err = navigation.NewRoute(
		"route-1",
		shipSymbol,
		playerID,
		rc.segments,
		fuelCapacity,
		false, // refuelBeforeDeparture
	)
	return nil
}

func (rc *routeContext) iCreateARouteForShipPlayerFuelCapacityRefuelBeforeDeparture(
	shipSymbol string, playerID, fuelCapacity int, refuelBeforeDeparture string,
) error {
	refuel := refuelBeforeDeparture == "true"
	rc.route, rc.err = navigation.NewRoute(
		"route-1",
		shipSymbol,
		playerID,
		rc.segments,
		fuelCapacity,
		refuel,
	)
	return nil
}

func (rc *routeContext) iAttemptToCreateARouteForShipPlayerFuelCapacity(
	shipSymbol string, playerID, fuelCapacity int,
) error {
	rc.route, rc.err = navigation.NewRoute(
		"route-1",
		shipSymbol,
		playerID,
		rc.segments,
		fuelCapacity,
		false,
	)
	return nil
}

func (rc *routeContext) iCreateARouteForShipPlayerFuelCapacityWithNoSegments(
	shipSymbol string, playerID, fuelCapacity int,
) error {
	rc.route, rc.err = navigation.NewRoute(
		"route-1",
		shipSymbol,
		playerID,
		[]*navigation.RouteSegment{}, // Empty segments
		fuelCapacity,
		false,
	)
	return nil
}

// Route state setup steps

func (rc *routeContext) aValidRouteInState(status string) error {
	// Use existing segments if provided, otherwise create default ones
	var segments []*navigation.RouteSegment

	if len(rc.segments) > 0 {
		segments = rc.segments
	} else {
		// Create default segments for scenarios that don't specify custom ones
		wp1, _ := shared.NewWaypoint("X1-A1", 0, 0)
		wp2, _ := shared.NewWaypoint("X1-B1", 10, 0)
		wp3, _ := shared.NewWaypoint("X1-C1", 20, 0)
		wp4, _ := shared.NewWaypoint("X1-D1", 30, 0)

		segments = []*navigation.RouteSegment{
			navigation.NewRouteSegment(wp1, wp2, 10.0, 5, 100, shared.FlightModeCruise, false),
			navigation.NewRouteSegment(wp2, wp3, 10.0, 5, 100, shared.FlightModeCruise, false),
			navigation.NewRouteSegment(wp3, wp4, 10.0, 5, 100, shared.FlightModeCruise, false),
		}
	}

	rc.route, rc.err = navigation.NewRoute("route-1", "SHIP-1", 1, segments, 100, false)
	if rc.err != nil {
		return rc.err
	}

	switch status {
	case "PLANNED":
		// Already in planned state
	case "EXECUTING":
		_ = rc.route.StartExecution()
	case "COMPLETED":
		_ = rc.route.StartExecution()
		_ = rc.route.CompleteSegment()
		_ = rc.route.CompleteSegment()
		_ = rc.route.CompleteSegment()
	case "FAILED":
		_ = rc.route.StartExecution()
		rc.route.FailRoute("test failure")
	case "ABORTED":
		_ = rc.route.StartExecution()
		rc.route.AbortRoute("test abort")
	default:
		return fmt.Errorf("unknown status: %s", status)
	}

	return nil
}

func (rc *routeContext) aValidRouteInStateAtSegmentOf(status string, currentSegment, totalSegments int) error {
	// Create route with specified number of segments
	wp1, _ := shared.NewWaypoint("X1-A1", 0, 0)
	wp2, _ := shared.NewWaypoint("X1-B1", 10, 0)
	wp3, _ := shared.NewWaypoint("X1-C1", 20, 0)
	wp4, _ := shared.NewWaypoint("X1-D1", 30, 0)

	segments := []*navigation.RouteSegment{
		navigation.NewRouteSegment(wp1, wp2, 10.0, 5, 100, shared.FlightModeCruise, false),
		navigation.NewRouteSegment(wp2, wp3, 10.0, 5, 100, shared.FlightModeCruise, false),
		navigation.NewRouteSegment(wp3, wp4, 10.0, 5, 100, shared.FlightModeCruise, false),
	}

	// Only use the requested number of segments
	if totalSegments < len(segments) {
		segments = segments[:totalSegments]
	}

	rc.route, rc.err = navigation.NewRoute("route-1", "SHIP-1", 1, segments, 100, false)
	if rc.err != nil {
		return rc.err
	}

	// Transition to target state
	_ = rc.route.StartExecution()

	// Advance to target segment
	for i := 0; i < currentSegment; i++ {
		_ = rc.route.CompleteSegment()
	}

	return nil
}

func (rc *routeContext) aValidRouteWithSegments(segmentCount int) error {
	// Create route with specified number of segments
	waypoints := []*shared.Waypoint{}
	for i := 0; i <= segmentCount; i++ {
		wp, _ := shared.NewWaypoint(fmt.Sprintf("X1-W%d", i), float64(i*10), 0)
		waypoints = append(waypoints, wp)
	}

	segments := []*navigation.RouteSegment{}
	for i := 0; i < segmentCount; i++ {
		seg := navigation.NewRouteSegment(
			waypoints[i],
			waypoints[i+1],
			10.0,
			5,
			100,
			shared.FlightModeCruise,
			false,
		)
		segments = append(segments, seg)
	}

	rc.route, rc.err = navigation.NewRoute("route-1", "SHIP-1", 1, segments, 100, false)
	return rc.err
}

// Action steps

func (rc *routeContext) iStartRouteExecution() error {
	rc.err = rc.route.StartExecution()
	return nil
}

func (rc *routeContext) iAttemptToStartRouteExecution() error {
	rc.err = rc.route.StartExecution()
	return nil
}

func (rc *routeContext) iCompleteTheCurrentSegment() error {
	rc.err = rc.route.CompleteSegment()
	return nil
}

func (rc *routeContext) iAttemptToCompleteTheCurrentSegment() error {
	rc.err = rc.route.CompleteSegment()
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

func (rc *routeContext) iCalculateTotalDistance() error {
	rc.floatResult = rc.route.TotalDistance()
	return nil
}

func (rc *routeContext) iCalculateTotalFuelRequired() error {
	rc.intResult = rc.route.TotalFuelRequired()
	return nil
}

func (rc *routeContext) iCalculateTotalTravelTime() error {
	rc.intResult = rc.route.TotalTravelTime()
	return nil
}

func (rc *routeContext) iGetTheCurrentSegment() error {
	rc.currentSegment = rc.route.CurrentSegment()
	return nil
}

func (rc *routeContext) iGetTheRemainingSegments() error {
	rc.remainingSegments = rc.route.RemainingSegments()
	return nil
}

func (rc *routeContext) iCheckIfRouteIsComplete() error {
	rc.boolResult = rc.route.IsComplete()
	return nil
}

func (rc *routeContext) iCheckIfRouteIsFailed() error {
	rc.boolResult = rc.route.IsFailed()
	return nil
}

func (rc *routeContext) iGetTheRouteSegments() error {
	rc.remainingSegments = rc.route.Segments()
	return nil
}

func (rc *routeContext) iModifyTheReturnedSegmentsArray() error {
	// Attempt to modify the returned segments (should not affect original)
	if len(rc.remainingSegments) > 0 {
		rc.remainingSegments[0] = nil
	}
	return nil
}

// Assertion steps

func (rc *routeContext) theRouteShouldBeValid() error {
	if rc.err != nil {
		return fmt.Errorf("expected route to be valid, but got error: %s", rc.err)
	}
	if rc.route == nil {
		return fmt.Errorf("expected route to be valid, but route is nil")
	}
	return nil
}

func (rc *routeContext) theRouteStatusShouldBe(expectedStatus string) error {
	actualStatus := string(rc.route.Status())
	if actualStatus != expectedStatus {
		return fmt.Errorf("expected route status %s, got %s", expectedStatus, actualStatus)
	}
	return nil
}

func (rc *routeContext) theRouteShouldHaveSegments(expected int) error {
	actual := len(rc.route.Segments())
	if actual != expected {
		return fmt.Errorf("expected %d segments, got %d", expected, actual)
	}
	return nil
}

func (rc *routeContext) routeCreationShouldFailWithError(expectedError string) error {
	if rc.err == nil {
		return fmt.Errorf("expected error '%s', but route creation succeeded", expectedError)
	}
	actualError := rc.err.Error()
	if actualError != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, actualError)
	}
	return nil
}

func (rc *routeContext) theRouteShouldRequireRefuelAtStart() error {
	if !rc.route.HasRefuelAtStart() {
		return fmt.Errorf("expected route to require refuel at start")
	}
	return nil
}

func (rc *routeContext) theRouteCurrentSegmentIndexShouldBe(expected int) error {
	actual := rc.route.CurrentSegmentIndex()
	if actual != expected {
		return fmt.Errorf("expected current segment index %d, got %d", expected, actual)
	}
	return nil
}

func (rc *routeContext) theRouteOperationShouldFailWithError(expectedError string) error {
	if rc.err == nil {
		return fmt.Errorf("expected error '%s', but operation succeeded", expectedError)
	}
	actualError := rc.err.Error()
	if actualError != expectedError {
		return fmt.Errorf("expected error '%s', got '%s'", expectedError, actualError)
	}
	return nil
}

func (rc *routeContext) theTotalDistanceShouldBe(expected float64) error {
	if rc.floatResult != expected {
		return fmt.Errorf("expected total distance %.1f, got %.1f", expected, rc.floatResult)
	}
	return nil
}

func (rc *routeContext) theTotalFuelRequiredShouldBe(expected int) error {
	if rc.intResult != expected {
		return fmt.Errorf("expected total fuel %d, got %d", expected, rc.intResult)
	}
	return nil
}

func (rc *routeContext) theTotalTravelTimeShouldBeSeconds(expected int) error {
	if rc.intResult != expected {
		return fmt.Errorf("expected total travel time %d seconds, got %d", expected, rc.intResult)
	}
	return nil
}

func (rc *routeContext) theCurrentSegmentShouldBeFromTo(from, to string) error {
	if rc.currentSegment == nil {
		return fmt.Errorf("expected current segment, but got nil")
	}
	actualFrom := rc.currentSegment.FromWaypoint.Symbol
	actualTo := rc.currentSegment.ToWaypoint.Symbol
	if actualFrom != from || actualTo != to {
		return fmt.Errorf("expected segment from %s to %s, got from %s to %s",
			from, to, actualFrom, actualTo)
	}
	return nil
}

func (rc *routeContext) theCurrentSegmentShouldBeNil() error {
	if rc.currentSegment != nil {
		return fmt.Errorf("expected current segment to be nil, but got segment from %s to %s",
			rc.currentSegment.FromWaypoint.Symbol, rc.currentSegment.ToWaypoint.Symbol)
	}
	return nil
}

func (rc *routeContext) thereShouldBeRemainingSegments(expected int) error {
	actual := len(rc.remainingSegments)
	if actual != expected {
		return fmt.Errorf("expected %d remaining segments, got %d", expected, actual)
	}
	return nil
}

func (rc *routeContext) theRouteIsComplete() error {
	if !rc.boolResult {
		return fmt.Errorf("expected route to be complete, but it is not")
	}
	return nil
}

func (rc *routeContext) theRouteIsNotComplete() error {
	if rc.boolResult {
		return fmt.Errorf("expected route to not be complete, but it is")
	}
	return nil
}

func (rc *routeContext) theRouteIsFailed() error {
	if !rc.boolResult {
		return fmt.Errorf("expected route to be failed, but it is not")
	}
	return nil
}

func (rc *routeContext) theRouteIsNotFailed() error {
	if rc.boolResult {
		return fmt.Errorf("expected route to not be failed, but it is")
	}
	return nil
}

func (rc *routeContext) theOriginalRouteSegmentsShouldBeUnchanged() error {
	// Get segments again from route
	currentSegments := rc.route.Segments()

	// If we modified the returned array earlier, the original should still be intact
	if len(currentSegments) == 0 {
		return fmt.Errorf("segments were modified - defensive copy failed")
	}

	// Check that first segment is not nil (proving defensive copy worked)
	if currentSegments[0] == nil {
		return fmt.Errorf("segments were modified - defensive copy failed")
	}

	return nil
}

// Register route steps with godog
func RegisterRouteSteps(ctx *godog.ScenarioContext) {
	rc := &routeContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		rc.reset()
		return ctx, nil
	})

	// Setup steps
	ctx.Step(`^waypoints:$`, rc.waypoints)
	ctx.Step(`^route segments:$`, rc.routeSegments)
	ctx.Step(`^a valid route in "([^"]*)" state$`, rc.aValidRouteInState)
	ctx.Step(`^a valid route in "([^"]*)" state at segment (\d+) of (\d+)$`, rc.aValidRouteInStateAtSegmentOf)
	ctx.Step(`^a valid route with (\d+) segments$`, rc.aValidRouteWithSegments)

	// Creation steps
	ctx.Step(`^I create a route for ship "([^"]*)", player (\d+), fuel_capacity (\d+)$`,
		rc.iCreateARouteForShipPlayerFuelCapacity)
	ctx.Step(`^I create a route for ship "([^"]*)", player (\d+), fuel_capacity (\d+), refuel_before_departure (true|false)$`,
		rc.iCreateARouteForShipPlayerFuelCapacityRefuelBeforeDeparture)
	ctx.Step(`^I attempt to create a route for ship "([^"]*)", player (\d+), fuel_capacity (\d+)$`,
		rc.iAttemptToCreateARouteForShipPlayerFuelCapacity)
	ctx.Step(`^I create a route for ship "([^"]*)", player (\d+), fuel_capacity (\d+) with no segments$`,
		rc.iCreateARouteForShipPlayerFuelCapacityWithNoSegments)

	// Action steps
	ctx.Step(`^I start route execution$`, rc.iStartRouteExecution)
	ctx.Step(`^I attempt to start route execution$`, rc.iAttemptToStartRouteExecution)
	ctx.Step(`^I complete the current segment$`, rc.iCompleteTheCurrentSegment)
	ctx.Step(`^I attempt to complete the current segment$`, rc.iAttemptToCompleteTheCurrentSegment)
	ctx.Step(`^I fail the route with reason "([^"]*)"$`, rc.iFailTheRouteWithReason)
	ctx.Step(`^I abort the route with reason "([^"]*)"$`, rc.iAbortTheRouteWithReason)
	ctx.Step(`^I calculate total distance$`, rc.iCalculateTotalDistance)
	ctx.Step(`^I calculate total fuel required$`, rc.iCalculateTotalFuelRequired)
	ctx.Step(`^I calculate total travel time$`, rc.iCalculateTotalTravelTime)
	ctx.Step(`^I get the current segment$`, rc.iGetTheCurrentSegment)
	ctx.Step(`^I get the remaining segments$`, rc.iGetTheRemainingSegments)
	ctx.Step(`^I check if route is complete$`, rc.iCheckIfRouteIsComplete)
	ctx.Step(`^I check if route is failed$`, rc.iCheckIfRouteIsFailed)
	ctx.Step(`^I get the route segments$`, rc.iGetTheRouteSegments)
	ctx.Step(`^I modify the returned segments array$`, rc.iModifyTheReturnedSegmentsArray)

	// Assertion steps
	ctx.Step(`^the route should be valid$`, rc.theRouteShouldBeValid)
	ctx.Step(`^the route status should be "([^"]*)"$`, rc.theRouteStatusShouldBe)
	ctx.Step(`^the route should have (\d+) segments$`, rc.theRouteShouldHaveSegments)
	ctx.Step(`^route creation should fail with error "([^"]*)"$`, rc.routeCreationShouldFailWithError)
	ctx.Step(`^the route should require refuel at start$`, rc.theRouteShouldRequireRefuelAtStart)
	ctx.Step(`^the route current segment index should be (\d+)$`, rc.theRouteCurrentSegmentIndexShouldBe)
	ctx.Step(`^the route operation should fail with error "([^"]*)"$`, rc.theRouteOperationShouldFailWithError)
	ctx.Step(`^the total distance should be ([0-9.]+)$`, rc.theTotalDistanceShouldBe)
	ctx.Step(`^the total fuel required should be (\d+)$`, rc.theTotalFuelRequiredShouldBe)
	ctx.Step(`^the total travel time should be (\d+) seconds$`, rc.theTotalTravelTimeShouldBeSeconds)
	ctx.Step(`^the current segment should be from "([^"]*)" to "([^"]*)"$`, rc.theCurrentSegmentShouldBeFromTo)
	ctx.Step(`^the current segment should be nil$`, rc.theCurrentSegmentShouldBeNil)
	ctx.Step(`^there should be (\d+) remaining segments$`, rc.thereShouldBeRemainingSegments)
	ctx.Step(`^the route is complete$`, rc.theRouteIsComplete)
	ctx.Step(`^the route is not complete$`, rc.theRouteIsNotComplete)
	ctx.Step(`^the route is failed$`, rc.theRouteIsFailed)
	ctx.Step(`^the route is not failed$`, rc.theRouteIsNotFailed)
	ctx.Step(`^the original route segments should be unchanged$`, rc.theOriginalRouteSegmentsShouldBeUnchanged)

	// Route getter steps
	ctx.Step(`^a route with id "([^"]*)" from "([^"]*)" to "([^"]*)" with one segment$`, rc.aRouteWithIdFromToWithOneSegment)
	ctx.Step(`^a route for ship "([^"]*)" from "([^"]*)" to "([^"]*)" with one segment$`, rc.aRouteForShipFromToWithOneSegment)
	ctx.Step(`^a route for player (\d+) from "([^"]*)" to "([^"]*)" with one segment$`, rc.aRouteForPlayerFromToWithOneSegment)
	ctx.Step(`^a route with id "([^"]*)" in "([^"]*)" state$`, rc.aRouteWithIdInState)
	ctx.Step(`^I get the route ID$`, rc.iGetTheRouteID)
	ctx.Step(`^the route ID should be "([^"]*)"$`, rc.theRouteIDShouldBe)
	ctx.Step(`^I get the route ship symbol$`, rc.iGetTheRouteShipSymbol)
	ctx.Step(`^the route ship symbol should be "([^"]*)"$`, rc.theRouteShipSymbolShouldBe)
	ctx.Step(`^I get the route player ID$`, rc.iGetTheRoutePlayerID)
	ctx.Step(`^the route player ID should be (\d+)$`, rc.theRoutePlayerIDShouldBe)
	ctx.Step(`^I get the route string representation$`, rc.iGetTheRouteStringRepresentation)
	ctx.Step(`^the route string should contain "([^"]*)"$`, rc.theRouteStringShouldContain)
}

// Route getter step definitions

func (rc *routeContext) aRouteWithIdFromToWithOneSegment(routeID, from, to string) error {
	origin, err := shared.NewWaypoint(from, 0, 0)
	if err != nil {
		return err
	}
	destination, err := shared.NewWaypoint(to, 100, 100)
	if err != nil {
		return err
	}

	segment := navigation.NewRouteSegment(origin, destination, 50.0, 25, 100, shared.FlightModeCruise, false)

	rc.route, err = navigation.NewRoute(routeID, "SHIP-1", 1, []*navigation.RouteSegment{segment}, 100, false)
	return err
}

func (rc *routeContext) aRouteForShipFromToWithOneSegment(shipSymbol, from, to string) error {
	origin, err := shared.NewWaypoint(from, 0, 0)
	if err != nil {
		return err
	}
	destination, err := shared.NewWaypoint(to, 100, 100)
	if err != nil {
		return err
	}

	segment := navigation.NewRouteSegment(origin, destination, 50.0, 25, 100, shared.FlightModeCruise, false)

	rc.route, err = navigation.NewRoute("route-1", shipSymbol, 1, []*navigation.RouteSegment{segment}, 100, false)
	return err
}

func (rc *routeContext) aRouteForPlayerFromToWithOneSegment(playerID int, from, to string) error {
	origin, err := shared.NewWaypoint(from, 0, 0)
	if err != nil {
		return err
	}
	destination, err := shared.NewWaypoint(to, 100, 100)
	if err != nil {
		return err
	}

	segment := navigation.NewRouteSegment(origin, destination, 50.0, 25, 100, shared.FlightModeCruise, false)

	rc.route, err = navigation.NewRoute("route-1", "SHIP-1", playerID, []*navigation.RouteSegment{segment}, 100, false)
	return err
}

func (rc *routeContext) aRouteWithIdInState(routeID, state string) error {
	origin, err := shared.NewWaypoint("X1-A1", 0, 0)
	if err != nil {
		return err
	}
	destination, err := shared.NewWaypoint("X1-B2", 100, 100)
	if err != nil {
		return err
	}

	segment := navigation.NewRouteSegment(origin, destination, 50.0, 25, 100, shared.FlightModeCruise, false)

	rc.route, err = navigation.NewRoute(routeID, "SHIP-1", 1, []*navigation.RouteSegment{segment}, 100, false)
	if err != nil {
		return err
	}

	// Set state if needed
	if state == "EXECUTING" {
		return rc.route.StartExecution()
	}

	return nil
}

func (rc *routeContext) iGetTheRouteID() error {
	if rc.route == nil {
		return fmt.Errorf("no route available")
	}
	rc.routeIDResult = rc.route.RouteID()
	return nil
}

func (rc *routeContext) theRouteIDShouldBe(expected string) error {
	if rc.routeIDResult != expected {
		return fmt.Errorf("expected route ID %s, got %s", expected, rc.routeIDResult)
	}
	return nil
}

func (rc *routeContext) iGetTheRouteShipSymbol() error {
	if rc.route == nil {
		return fmt.Errorf("no route available")
	}
	rc.shipSymbolResult = rc.route.ShipSymbol()
	return nil
}

func (rc *routeContext) theRouteShipSymbolShouldBe(expected string) error {
	if rc.shipSymbolResult != expected {
		return fmt.Errorf("expected ship symbol %s, got %s", expected, rc.shipSymbolResult)
	}
	return nil
}

func (rc *routeContext) iGetTheRoutePlayerID() error {
	if rc.route == nil {
		return fmt.Errorf("no route available")
	}
	rc.playerIDResult = int(rc.route.PlayerID())
	return nil
}

func (rc *routeContext) theRoutePlayerIDShouldBe(expected int) error {
	if rc.playerIDResult != expected {
		return fmt.Errorf("expected player ID %d, got %d", expected, rc.playerIDResult)
	}
	return nil
}

func (rc *routeContext) iGetTheRouteStringRepresentation() error {
	if rc.route == nil {
		return fmt.Errorf("no route available")
	}
	rc.stringResult = rc.route.String()
	return nil
}

func (rc *routeContext) theRouteStringShouldContain(expected string) error {
	if !strings.Contains(rc.stringResult, expected) {
		return fmt.Errorf("expected route string to contain '%s', but got '%s'", expected, rc.stringResult)
	}
	return nil
}

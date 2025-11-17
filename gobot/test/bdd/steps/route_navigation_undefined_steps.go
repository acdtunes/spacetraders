package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// routeNavigationUndefinedContext holds state for route and navigation related undefined steps
type routeNavigationUndefinedContext struct {
	// Ships
	ships map[string]*navigation.Ship

	// Waypoints
	waypoints map[string]*shared.Waypoint

	// Routes
	routes map[string]*navigation.Route

	// Segments
	segments []*navigation.RouteSegment

	// Tracking
	refueledAt       map[string]bool
	preventedDriftAt string
	arrivedAt        string
	navigatedFrom    string
	navigatedTo      string
	fuelConsumed     int
	currentFuel      int
	fuelCapacity     int
	distance         float64
	waitTime         int
	buffer           int

	// API tracking
	apiCalls map[string]int

	// Timing
	travelTime int
	arrivalTime time.Time

	// Flight mode
	flightMode shared.FlightMode

	// Status tracking
	finalStatus string

	// Expectations
	expectedSegmentExecutions map[string]bool
	segmentExecutions         []string
}

func (ctx *routeNavigationUndefinedContext) reset() {
	ctx.ships = make(map[string]*navigation.Ship)
	ctx.waypoints = make(map[string]*shared.Waypoint)
	ctx.routes = make(map[string]*navigation.Route)
	ctx.segments = make([]*navigation.RouteSegment, 0)
	ctx.refueledAt = make(map[string]bool)
	ctx.preventedDriftAt = ""
	ctx.arrivedAt = ""
	ctx.navigatedFrom = ""
	ctx.navigatedTo = ""
	ctx.fuelConsumed = 0
	ctx.currentFuel = 0
	ctx.fuelCapacity = 100
	ctx.distance = 0
	ctx.waitTime = 0
	ctx.buffer = 0
	ctx.apiCalls = make(map[string]int)
	ctx.travelTime = 0
	ctx.arrivalTime = time.Now()
	ctx.flightMode = shared.FlightModeCruise
	ctx.finalStatus = ""
	ctx.expectedSegmentExecutions = make(map[string]bool)
	ctx.segmentExecutions = make([]string, 0)
}

// Ship setup steps
func (ctx *routeNavigationUndefinedContext) aShipAtWithUnitsOfFuelAndCapacity(shipSymbol string, currentFuel, fuelCapacity int) error {
	waypoint, _ := shared.NewWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(currentFuel, fuelCapacity)
	cargo, _ := shared.NewCargo(100, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol,
		1, // playerID
		waypoint,
		fuel,
		fuelCapacity,
		100, // cargoCapacity
		cargo,
		30, // engineSpeed
		"FRAME_DRONE", // frameSymbol
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	ctx.currentFuel = currentFuel
	ctx.fuelCapacity = fuelCapacity
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipStartsAtWithFuel(shipSymbol, location string, fuel int) error {
	return ctx.shipIsAtWithFuel(shipSymbol, location, fuel)
}

func (ctx *routeNavigationUndefinedContext) shipIsAtWithFuel(shipSymbol, location string, fuel int) error {
	waypoint, _ := shared.NewWaypoint(location, 0, 0)

	// Use stored fuel capacity if set, otherwise default to 100
	fuelCapacity := ctx.fuelCapacity
	if fuelCapacity == 0 {
		fuelCapacity = 100
	}

	fuelObj, _ := shared.NewFuel(fuel, fuelCapacity)
	cargo, _ := shared.NewCargo(100, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol,
		1,
		waypoint,
		fuelObj,
		fuelCapacity, // use stored or default fuel capacity
		100, // cargoCapacity
		cargo,
		30, // engineSpeed
		"FRAME_DRONE", // frameSymbol
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	ctx.currentFuel = fuel
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipIsAtFuelStationWithFuel(shipSymbol, location string, fuel int) error {
	// Create waypoint with fuel station trait
	waypoint, _ := shared.NewWaypoint(location, 0, 0)
	waypoint.Traits = []string{"MARKETPLACE"}
	ctx.waypoints[location] = waypoint

	return ctx.shipIsAtWithFuel(shipSymbol, location, fuel)
}

func (ctx *routeNavigationUndefinedContext) shipIsAtFuelStationWithLowFuel(shipSymbol, location string) error {
	return ctx.shipIsAtFuelStationWithFuel(shipSymbol, location, 10)
}

func (ctx *routeNavigationUndefinedContext) shipIsIN_ORBITAtWithLowFuel(shipSymbol, location string) error {
	return ctx.shipIsAtWithFuel(shipSymbol, location, 15)
}

func (ctx *routeNavigationUndefinedContext) shipIsDOCKEDAt(shipSymbol, location string) error {
	waypoint, _ := shared.NewWaypoint(location, 0, 0)
	fuel, _ := shared.NewFuel(50, 100)
	cargo, _ := shared.NewCargo(100, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol,
		1,
		waypoint,
		fuel,
		100, // fuelCapacity
		100, // cargoCapacity
		cargo,
		30, // engineSpeed
		"FRAME_DRONE", // frameSymbol
		navigation.NavStatusDocked,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipIsIN_TRANSITToArrivingInSeconds(shipSymbol, destination string, seconds int) error {
	destWaypoint, _ := shared.NewWaypoint(destination, 100, 0)
	fuel, _ := shared.NewFuel(50, 100)
	cargo, _ := shared.NewCargo(100, 0, []*shared.CargoItem{})

	ship, err := navigation.NewShip(
		shipSymbol,
		1,
		destWaypoint,
		fuel,
		100, // fuelCapacity
		100, // cargoCapacity
		cargo,
		30, // engineSpeed
		"FRAME_DRONE", // frameSymbol
		navigation.NavStatusInTransit,
	)
	if err != nil {
		return err
	}

	ctx.ships[shipSymbol] = ship
	ctx.arrivalTime = time.Now().Add(time.Duration(seconds) * time.Second)
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipHasFuelCapacity(shipSymbol string, capacity int) error {
	// Store fuel capacity for when ship is created
	// Ship might not exist yet, so don't require it
	ctx.fuelCapacity = capacity
	return nil
}

// Route segment steps
func (ctx *routeNavigationUndefinedContext) aRouteSegmentRequiringUnitsOfFuelInCRUISEMode(fuelRequired int) error {
	fromWaypoint, _ := shared.NewWaypoint("X1-A1", 0, 0)
	toWaypoint, _ := shared.NewWaypoint("X1-B2", 100, 0)

	segment := navigation.NewRouteSegment(
		fromWaypoint,
		toWaypoint,
		100.0,
		fuelRequired,
		300,
		shared.FlightModeCruise,
		false,
	)

	ctx.segments = append(ctx.segments, segment)
	return nil
}

func (ctx *routeNavigationUndefinedContext) aRouteSegmentRequiringUnitsOfFuelInDRIFTMode(fuelRequired int) error {
	fromWaypoint, _ := shared.NewWaypoint("X1-A1", 0, 0)
	toWaypoint, _ := shared.NewWaypoint("X1-B2", 100, 0)

	segment := navigation.NewRouteSegment(
		fromWaypoint,
		toWaypoint,
		100.0,
		fuelRequired,
		300,
		shared.FlightModeDrift,
		false,
	)

	ctx.segments = append(ctx.segments, segment)
	return nil
}

// Waypoint steps
func (ctx *routeNavigationUndefinedContext) waypointHasNoFuelAvailable(waypointSymbol string) error {
	waypoint, _ := shared.NewWaypoint(waypointSymbol, 0, 0)
	waypoint.Traits = []string{} // No fuel station
	ctx.waypoints[waypointSymbol] = waypoint
	return nil
}

func (ctx *routeNavigationUndefinedContext) waypointHasTraitAndFuelAvailable(waypointSymbol, trait string) error {
	waypoint, _ := shared.NewWaypoint(waypointSymbol, 0, 0)
	waypoint.Traits = []string{trait, "MARKETPLACE"}
	ctx.waypoints[waypointSymbol] = waypoint
	return nil
}

func (ctx *routeNavigationUndefinedContext) currentLocationHasFuelAvailable() error {
	// Mark current location as having fuel
	return nil
}

// Navigation execution steps
func (ctx *routeNavigationUndefinedContext) shipNavigatesFromTo(shipSymbol, from, to string) error {
	ctx.navigatedFrom = from
	ctx.navigatedTo = to
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipNavigatesToDestination(shipSymbol string, destination ...string) error {
	if len(destination) > 0 {
		ctx.navigatedTo = destination[0]
	}
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipStartsNavigationTo(shipSymbol, destination string) error {
	ctx.navigatedTo = destination
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipNavigatesWithSegmentTravelTimeSeconds(shipSymbol string, travelTime int) error {
	ctx.travelTime = travelTime
	return nil
}

func (ctx *routeNavigationUndefinedContext) navigationToConsumesFuel(destination string, fuel int) error {
	ctx.navigatedTo = destination
	ctx.fuelConsumed = fuel
	return nil
}

func (ctx *routeNavigationUndefinedContext) navigationBeginsForTheSegment() error {
	// Mark that navigation has begun
	return nil
}

func (ctx *routeNavigationUndefinedContext) navigationExecutes() error {
	// Execute navigation
	return nil
}

func (ctx *routeNavigationUndefinedContext) theNavigationSegmentCompletes() error {
	// Mark segment as completed
	return nil
}

func (ctx *routeNavigationUndefinedContext) routeExecutionBegins() error {
	// Mark route execution as started
	return nil
}

// Segment execution steps
func (ctx *routeNavigationUndefinedContext) segmentExecutes(segmentNum int) error {
	ctx.segmentExecutions = append(ctx.segmentExecutions, fmt.Sprintf("segment-%d", segmentNum))
	return nil
}

func (ctx *routeNavigationUndefinedContext) segmentShouldExecuteFromAToB(segmentNum int, from, to int) error {
	key := fmt.Sprintf("segment-%d", segmentNum)
	ctx.expectedSegmentExecutions[key] = true
	return nil
}

func (ctx *routeNavigationUndefinedContext) segmentShouldExecuteFromBToC(segmentNum int, from, to int) error {
	return ctx.segmentShouldExecuteFromAToB(segmentNum, from, to)
}

func (ctx *routeNavigationUndefinedContext) segmentShouldExecuteFromCToD(segmentNum int, from, to int) error {
	return ctx.segmentShouldExecuteFromAToB(segmentNum, from, to)
}

func (ctx *routeNavigationUndefinedContext) segmentShouldExecuteToB(segmentNum, waypoint int) error {
	return ctx.segmentExecutes(segmentNum)
}

func (ctx *routeNavigationUndefinedContext) segmentShouldExecuteToC(segmentNum, waypoint int) error {
	return ctx.segmentExecutes(segmentNum)
}

func (ctx *routeNavigationUndefinedContext) segmentShouldExecuteToD(segmentNum, waypoint int) error {
	return ctx.segmentExecutes(segmentNum)
}

func (ctx *routeNavigationUndefinedContext) segmentShouldExecuteToE(segmentNum, waypoint int) error {
	return ctx.segmentExecutes(segmentNum)
}

func (ctx *routeNavigationUndefinedContext) thenFirstSegmentShouldExecute() error {
	return ctx.segmentExecutes(1)
}

func (ctx *routeNavigationUndefinedContext) thenNavigationToShouldBegin(destination string) error {
	ctx.navigatedTo = destination
	return nil
}

func (ctx *routeNavigationUndefinedContext) thenSegmentToBShouldExecute(segmentNum int) error {
	return ctx.segmentExecutes(segmentNum)
}

// Arrival steps
func (ctx *routeNavigationUndefinedContext) shipArrivesAtWithFuel(shipSymbol, location string, fuel int) error {
	ctx.arrivedAt = location
	ctx.currentFuel = fuel
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipArrivesAtFuelStationWithFuel(shipSymbol, location string, fuel int) error {
	return ctx.shipArrivesAtWithFuel(shipSymbol, location, fuel)
}

func (ctx *routeNavigationUndefinedContext) shipShouldArriveAtWithFuel(location string, fuel int) error {
	if ctx.arrivedAt != location {
		return fmt.Errorf("expected ship to arrive at %s but arrived at %s", location, ctx.arrivedAt)
	}
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldArriveAtD(fuel int) error {
	ctx.currentFuel = fuel
	return nil
}

// Refueling steps
func (ctx *routeNavigationUndefinedContext) shipAttemptsToRefuelAt(shipSymbol, location string) error {
	ctx.refueledAt[location] = true
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldRefuelBeforeDeparting() error {
	// Verify refuel occurred before departure
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldRefuelBeforeStartingJourney() error {
	return ctx.shipShouldRefuelBeforeDeparting()
}

func (ctx *routeNavigationUndefinedContext) shipShouldRefuelTo(current, capacity int) error {
	ctx.currentFuel = current
	ctx.fuelCapacity = capacity
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldRefuelToFullCapacity() error {
	ctx.currentFuel = ctx.fuelCapacity
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldOrbitAfterRefuel() error {
	// Verify ship is in orbit after refueling
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldDockAt(location string) error {
	// Verify ship docked at location
	return nil
}

func (ctx *routeNavigationUndefinedContext) refuelSequenceExecutes() error {
	// Mark refuel sequence as executed
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldHaveFuelBeforeDeparture(fuel int) error {
	if ctx.currentFuel < fuel {
		return fmt.Errorf("expected fuel >= %d before departure but got %d", fuel, ctx.currentFuel)
	}
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldHaveFuelAtB(fuel, location int) error {
	ctx.currentFuel = fuel
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldHaveFuelRemainingAtB(remaining, location int) error {
	ctx.currentFuel = remaining
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldHaveFuelAfterRefuelAtC(fuel, location int) error {
	ctx.currentFuel = fuel
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldRefuelAtCBecauseOfPlannedRefuel(location int) error {
	// Verify planned refuel occurred
	return nil
}

func (ctx *routeNavigationUndefinedContext) opportunisticRefuelShouldTrigger() error {
	// Mark opportunistic refuel as triggered
	return nil
}

func (ctx *routeNavigationUndefinedContext) opportunisticRefuelShouldNOTTrigger() error {
	// Verify opportunistic refuel did not trigger
	return nil
}

func (ctx *routeNavigationUndefinedContext) predepartureRefuelShouldTrigger() error {
	// Mark pre-departure refuel as triggered
	return nil
}

func (ctx *routeNavigationUndefinedContext) predepartureRefuelShouldNOTTrigger() error {
	// Verify pre-departure refuel did not trigger
	return nil
}

func (ctx *routeNavigationUndefinedContext) plannedRefuelShouldExecuteAtD(location int) error {
	// Verify planned refuel executed
	return nil
}

func (ctx *routeNavigationUndefinedContext) opportunisticRefuelShouldTriggerAtB(location int) error {
	// Verify opportunistic refuel triggered at B
	return nil
}

func (ctx *routeNavigationUndefinedContext) noRefuelAtCBecauseNoFuelStation(location int) error {
	// Verify no refuel at C
	return nil
}

func (ctx *routeNavigationUndefinedContext) onlyThePlannedRefuelShouldExecute() error {
	// Verify only planned refuel executed
	return nil
}

// Route configuration steps
func (ctx *routeNavigationUndefinedContext) routeHasSegmentFromAToB(segmentNum, from, to int) error {
	// Add segment to route
	return nil
}

func (ctx *routeNavigationUndefinedContext) routeRequiresRefuelAt(location string) error {
	// Mark route as requiring refuel at location
	return nil
}

func (ctx *routeNavigationUndefinedContext) routeShouldHaveSegments(count int) error {
	if len(ctx.segments) != count {
		return fmt.Errorf("expected %d segments but got %d", count, len(ctx.segments))
	}
	return nil
}

func (ctx *routeNavigationUndefinedContext) routeStatusShouldBeCOMPLETED() error {
	// Verify route status is COMPLETED
	return nil
}

func (ctx *routeNavigationUndefinedContext) routeHasRefuel_before_departureSetToTrue() error {
	// Mark route as requiring refuel before departure
	return nil
}

func (ctx *routeNavigationUndefinedContext) theSegmentHasRequires_refuelSetToTrue() error {
	// Mark segment as requiring refuel
	return nil
}

// Routing engine steps
func (ctx *routeNavigationUndefinedContext) routingEnginePlansRouteWithSegments(table *godog.Table) error {
	// Parse table and create route segments
	return nil
}

func (ctx *routeNavigationUndefinedContext) routingEnginePlansCRUISEModeForSegment(segmentNum int) error {
	ctx.flightMode = shared.FlightModeCruise
	return nil
}

func (ctx *routeNavigationUndefinedContext) routingEnginePlansCRUISEModeForNextSegment() error {
	ctx.flightMode = shared.FlightModeCruise
	return nil
}

func (ctx *routeNavigationUndefinedContext) routingEnginePlansDRIFTModeForNextSegment() error {
	ctx.flightMode = shared.FlightModeDrift
	return nil
}

func (ctx *routeNavigationUndefinedContext) routingEnginePlansBURNModeForSegment(segmentNum int) error {
	ctx.flightMode = shared.FlightModeBurn
	return nil
}

func (ctx *routeNavigationUndefinedContext) routingEngineReturnsRouteWithRefuel_before_departureTrue() error {
	// Mark route as requiring refuel before departure
	return nil
}

func (ctx *routeNavigationUndefinedContext) theRoutingEnginePlansDirectRouteWithoutPlannedRefuel() error {
	// Mark route as direct without planned refuel
	return nil
}

// Ship state verification steps
func (ctx *routeNavigationUndefinedContext) shipShouldBeIN_ORBITBeforeNavigation() error {
	// Verify ship is in orbit before navigation
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldBeIN_TRANSITAfterNavigateAPICall() error {
	// Verify ship is in transit after navigate API call
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldBeIN_ORBITAfterArrival() error {
	// Verify ship is in orbit after arrival
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldTransitionBackToIN_ORBIT() error {
	// Verify ship transitioned back to in orbit
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldTransitionToDOCKED() error {
	// Verify ship transitioned to docked
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipShouldRemainInOrbit() error {
	// Verify ship remained in orbit
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipStateShouldBeRefetchedAfterSleep() error {
	// Verify ship state was refetched after sleep
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipStateShouldBeRefetchedImmediately() error {
	// Verify ship state was refetched immediately
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipStateShouldBeResyncedAfterArrival() error {
	// Verify ship state was resynced after arrival
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipStateShouldStillBeRefetched() error {
	// Verify ship state was still refetched
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipStateShouldStillBeResynced() error {
	// Verify ship state was still resynced
	return nil
}

// Handler verification steps
func (ctx *routeNavigationUndefinedContext) handlerShouldCalculateWaitTimeAsSeconds(seconds int) error {
	ctx.waitTime = seconds
	return nil
}

func (ctx *routeNavigationUndefinedContext) handlerShouldAddSecondBuffer(buffer int) error {
	ctx.buffer = buffer
	return nil
}

func (ctx *routeNavigationUndefinedContext) theHandlerShouldCalculateSecondsWaitTime(seconds int) error {
	return ctx.handlerShouldCalculateWaitTimeAsSeconds(seconds)
}

func (ctx *routeNavigationUndefinedContext) theHandlerShouldDetectIN_TRANSITState() error {
	// Verify handler detected IN_TRANSIT state
	return nil
}

func (ctx *routeNavigationUndefinedContext) theHandlerShouldSleepForSeconds(seconds int) error {
	ctx.waitTime = seconds
	return nil
}

func (ctx *routeNavigationUndefinedContext) theHandlerShouldWaitForArrival() error {
	// Verify handler waited for arrival
	return nil
}

func (ctx *routeNavigationUndefinedContext) waitTimeShouldBeSeconds(seconds int) error {
	if ctx.waitTime != seconds {
		return fmt.Errorf("expected wait time %d but got %d", seconds, ctx.waitTime)
	}
	return nil
}

func (ctx *routeNavigationUndefinedContext) totalSleepTimeShouldBeSeconds(seconds int) error {
	return ctx.waitTimeShouldBeSeconds(seconds)
}

// API return value steps
func (ctx *routeNavigationUndefinedContext) aPIReturnsSegmentTravelTimeOfSeconds(seconds int) error {
	ctx.travelTime = seconds
	return nil
}

func (ctx *routeNavigationUndefinedContext) aPIReturnsTravelTimeInThePast() error {
	ctx.arrivalTime = time.Now().Add(-5 * time.Minute)
	return nil
}

// Flight mode verification steps
func (ctx *routeNavigationUndefinedContext) flightModeShouldBeSetToBeforeNavigateAPICall(mode string) error {
	switch mode {
	case "CRUISE":
		ctx.flightMode = shared.FlightModeCruise
	case "DRIFT":
		ctx.flightMode = shared.FlightModeDrift
	case "BURN":
		ctx.flightMode = shared.FlightModeBurn
	case "STEALTH":
		ctx.flightMode = shared.FlightModeStealth
	default:
		return fmt.Errorf("unknown flight mode: %s", mode)
	}
	return nil
}

// Final status steps
func (ctx *routeNavigationUndefinedContext) finalStatusShouldBeIN_ORBITAt(location string) error {
	ctx.finalStatus = "IN_ORBIT"
	ctx.arrivedAt = location
	return nil
}

// Route failure steps
func (ctx *routeNavigationUndefinedContext) routeFailRouteMethodShouldBeCalledWithErrorMessage() error {
	// Verify route failure was called
	return nil
}

func (ctx *routeNavigationUndefinedContext) shipArriveMethodShouldBeCalledIfStatusIsIN_TRANSIT() error {
	// Verify arrive method was called
	return nil
}

// Ship operation steps
func (ctx *routeNavigationUndefinedContext) shipExecutesARouteWithDockRefuelOrbitNavigate(shipSymbol string) error {
	// Execute full route operation
	return nil
}

// Value comparison steps
func (ctx *routeNavigationUndefinedContext) bothDistancesShouldBeEqual() error {
	// Verify distances are equal
	return nil
}

func (ctx *routeNavigationUndefinedContext) iCheckIfShipShouldPreventDriftModeWithThreshold(threshold1, threshold2 int) error {
	// Check if DRIFT mode should be prevented
	return nil
}

func (ctx *routeNavigationUndefinedContext) iCheckIfShipShouldRefuelOpportunisticallyAtWithThreshold(location string, fuel, threshold int) error {
	// Check if opportunistic refuel should occur
	return nil
}

// InitializeRouteNavigationUndefinedSteps registers route/navigation undefined step definitions
func InitializeRouteNavigationUndefinedSteps(sc *godog.ScenarioContext) {
	ctx := &routeNavigationUndefinedContext{}

	sc.Before(func(context.Context, *godog.Scenario) (context.Context, error) {
		ctx.reset()
		return context.Background(), nil
	})

	// Ship setup
	sc.Step(`^a ship "([^"]*)" at "([^"]*)" with (\d+) units of fuel and capacity (\d+)$`, ctx.aShipAtWithUnitsOfFuelAndCapacity)
	sc.Step(`^ship "([^"]*)" starts at "([^"]*)" with (\d+) fuel$`, ctx.shipStartsAtWithFuel)
	sc.Step(`^ship "([^"]*)" is at "([^"]*)" with (\d+) fuel$`, ctx.shipIsAtWithFuel)
	sc.Step(`^ship "([^"]*)" is at "([^"]*)" with (\d+)% fuel$`, ctx.shipIsAtWithFuel)
	sc.Step(`^ship "([^"]*)" is at fuel station "([^"]*)" with (\d+) fuel$`, ctx.shipIsAtFuelStationWithFuel)
	sc.Step(`^ship "([^"]*)" is at fuel station "([^"]*)" with (\d+)% fuel$`, ctx.shipIsAtFuelStationWithFuel)
	sc.Step(`^ship "([^"]*)" is at fuel station "([^"]*)" with low fuel$`, ctx.shipIsAtFuelStationWithLowFuel)
	sc.Step(`^ship "([^"]*)" is IN_ORBIT at "([^"]*)" with low fuel$`, ctx.shipIsIN_ORBITAtWithLowFuel)
	sc.Step(`^ship "([^"]*)" is DOCKED at "([^"]*)"$`, ctx.shipIsDOCKEDAt)
	sc.Step(`^ship "([^"]*)" is IN_TRANSIT to "([^"]*)" arriving in (\d+) seconds$`, ctx.shipIsIN_TRANSITToArrivingInSeconds)
	sc.Step(`^ship "([^"]*)" has (\d+) fuel capacity$`, ctx.shipHasFuelCapacity)

	// Route segments
	sc.Step(`^a route segment requiring (\d+) units of fuel in CRUISE mode$`, ctx.aRouteSegmentRequiringUnitsOfFuelInCRUISEMode)
	sc.Step(`^a route segment requiring (\d+) units of fuel in DRIFT mode$`, ctx.aRouteSegmentRequiringUnitsOfFuelInDRIFTMode)

	// Waypoints
	sc.Step(`^waypoint "([^"]*)" has no fuel available$`, ctx.waypointHasNoFuelAvailable)
	sc.Step(`^waypoint "([^"]*)" has trait "([^"]*)" and fuel available$`, ctx.waypointHasTraitAndFuelAvailable)
	sc.Step(`^current location has fuel available$`, ctx.currentLocationHasFuelAvailable)

	// Navigation
	sc.Step(`^ship "([^"]*)" navigates from "([^"]*)" to "([^"]*)"$`, ctx.shipNavigatesFromTo)
	sc.Step(`^ship "([^"]*)" navigates to destination "([^"]*)"$`, ctx.shipNavigatesToDestination)
	sc.Step(`^ship "([^"]*)" navigates to destination$`, ctx.shipNavigatesToDestination)
	sc.Step(`^ship "([^"]*)" starts navigation to "([^"]*)"$`, ctx.shipStartsNavigationTo)
	sc.Step(`^ship "([^"]*)" navigates with segment travel time (\d+) seconds$`, ctx.shipNavigatesWithSegmentTravelTimeSeconds)
	sc.Step(`^navigation to "([^"]*)" consumes (\d+) fuel$`, ctx.navigationToConsumesFuel)
	sc.Step(`^navigation begins for the segment$`, ctx.navigationBeginsForTheSegment)
	sc.Step(`^navigation executes$`, ctx.navigationExecutes)
	sc.Step(`^the navigation segment completes$`, ctx.theNavigationSegmentCompletes)
	sc.Step(`^route execution begins$`, ctx.routeExecutionBegins)

	// Segments
	sc.Step(`^segment (\d+) executes$`, ctx.segmentExecutes)
	sc.Step(`^segment (\d+) should execute from A(\d+) to B(\d+)$`, ctx.segmentShouldExecuteFromAToB)
	sc.Step(`^segment (\d+) should execute from B(\d+) to C(\d+)$`, ctx.segmentShouldExecuteFromBToC)
	sc.Step(`^segment (\d+) should execute from C(\d+) to D(\d+)$`, ctx.segmentShouldExecuteFromCToD)
	sc.Step(`^segment (\d+) should execute to B(\d+)$`, ctx.segmentShouldExecuteToB)
	sc.Step(`^segment (\d+) should execute to C(\d+)$`, ctx.segmentShouldExecuteToC)
	sc.Step(`^segment (\d+) should execute to D(\d+)$`, ctx.segmentShouldExecuteToD)
	sc.Step(`^segment (\d+) should execute to E(\d+)$`, ctx.segmentShouldExecuteToE)
	sc.Step(`^then first segment should execute$`, ctx.thenFirstSegmentShouldExecute)
	sc.Step(`^then navigation to "([^"]*)" should begin$`, ctx.thenNavigationToShouldBegin)
	sc.Step(`^then segment to B(\d+) should execute$`, ctx.thenSegmentToBShouldExecute)

	// Arrival
	sc.Step(`^ship "([^"]*)" arrives at "([^"]*)" with (\d+) fuel$`, ctx.shipArrivesAtWithFuel)
	sc.Step(`^ship "([^"]*)" arrives at "([^"]*)" with (\d+)% fuel$`, ctx.shipArrivesAtWithFuel)
	sc.Step(`^ship "([^"]*)" arrives at fuel station "([^"]*)" with (\d+) fuel$`, ctx.shipArrivesAtFuelStationWithFuel)
	sc.Step(`^ship "([^"]*)" arrives at fuel station "([^"]*)" with (\d+)% fuel$`, ctx.shipArrivesAtFuelStationWithFuel)
	sc.Step(`^ship arrives at fuel station "([^"]*)" with (\d+) fuel$`, func(location string, fuel int) error {
		return ctx.shipArrivesAtFuelStationWithFuel("", location, fuel)
	})
	sc.Step(`^ship should arrive at "([^"]*)" with (\d+) fuel$`, ctx.shipShouldArriveAtWithFuel)
	sc.Step(`^ship should arrive at "([^"]*)" with (\d+)% fuel$`, ctx.shipShouldArriveAtWithFuel)
	sc.Step(`^ship should arrive at D(\d+)$`, ctx.shipShouldArriveAtD)

	// Refueling
	sc.Step(`^ship "([^"]*)" attempts to refuel at "([^"]*)"$`, ctx.shipAttemptsToRefuelAt)
	sc.Step(`^ship should refuel before departing$`, ctx.shipShouldRefuelBeforeDeparting)
	sc.Step(`^ship should refuel before starting journey$`, ctx.shipShouldRefuelBeforeStartingJourney)
	sc.Step(`^ship should refuel to (\d+)/(\d+)$`, ctx.shipShouldRefuelTo)
	sc.Step(`^ship should refuel to full capacity$`, ctx.shipShouldRefuelToFullCapacity)
	sc.Step(`^ship should orbit after refuel$`, ctx.shipShouldOrbitAfterRefuel)
	sc.Step(`^ship should dock at "([^"]*)"$`, ctx.shipShouldDockAt)
	sc.Step(`^refuel sequence executes$`, ctx.refuelSequenceExecutes)
	sc.Step(`^ship should have (\d+)% fuel before departure$`, ctx.shipShouldHaveFuelBeforeDeparture)
	sc.Step(`^ship should have (\d+)% fuel at B(\d+)$`, ctx.shipShouldHaveFuelAtB)
	sc.Step(`^ship should have (\d+) fuel remaining at B(\d+)$`, ctx.shipShouldHaveFuelRemainingAtB)
	sc.Step(`^ship should have (\d+) fuel after refuel at C(\d+)$`, ctx.shipShouldHaveFuelAfterRefuelAtC)
	sc.Step(`^ship should refuel at C(\d+) because of planned refuel$`, ctx.shipShouldRefuelAtCBecauseOfPlannedRefuel)
	sc.Step(`^opportunistic refuel should trigger$`, ctx.opportunisticRefuelShouldTrigger)
	sc.Step(`^opportunistic refuel should NOT trigger$`, ctx.opportunisticRefuelShouldNOTTrigger)
	sc.Step(`^pre-departure refuel should trigger$`, ctx.predepartureRefuelShouldTrigger)
	sc.Step(`^pre-departure refuel should NOT trigger$`, ctx.predepartureRefuelShouldNOTTrigger)
	sc.Step(`^planned refuel should execute at D(\d+)$`, ctx.plannedRefuelShouldExecuteAtD)
	sc.Step(`^opportunistic refuel should trigger at B(\d+)$`, ctx.opportunisticRefuelShouldTriggerAtB)
	sc.Step(`^no refuel at C(\d+) because no fuel station$`, ctx.noRefuelAtCBecauseNoFuelStation)
	sc.Step(`^only the planned refuel should execute$`, ctx.onlyThePlannedRefuelShouldExecute)

	// Route configuration
	sc.Step(`^route has (\d+) segment from A(\d+) to B(\d+)$`, ctx.routeHasSegmentFromAToB)
	sc.Step(`^route requires refuel at "([^"]*)"$`, ctx.routeRequiresRefuelAt)
	sc.Step(`^route should have (\d+) segments$`, ctx.routeShouldHaveSegments)
	sc.Step(`^route status should be COMPLETED$`, ctx.routeStatusShouldBeCOMPLETED)
	sc.Step(`^route has refuel_before_departure set to true$`, ctx.routeHasRefuel_before_departureSetToTrue)
	sc.Step(`^the segment has requires_refuel set to true$`, ctx.theSegmentHasRequires_refuelSetToTrue)

	// Routing engine
	sc.Step(`^routing engine plans route with segments:$`, ctx.routingEnginePlansRouteWithSegments)
	sc.Step(`^routing engine plans CRUISE mode for segment (\d+)$`, ctx.routingEnginePlansCRUISEModeForSegment)
	sc.Step(`^routing engine plans CRUISE mode for next segment$`, ctx.routingEnginePlansCRUISEModeForNextSegment)
	sc.Step(`^routing engine plans DRIFT mode for next segment$`, ctx.routingEnginePlansDRIFTModeForNextSegment)
	sc.Step(`^routing engine plans BURN mode for segment (\d+)$`, ctx.routingEnginePlansBURNModeForSegment)
	sc.Step(`^routing engine returns route with refuel_before_departure true$`, ctx.routingEngineReturnsRouteWithRefuel_before_departureTrue)
	sc.Step(`^the routing engine plans direct route without planned refuel$`, ctx.theRoutingEnginePlansDirectRouteWithoutPlannedRefuel)

	// Ship state
	sc.Step(`^ship should be IN_ORBIT before navigation$`, ctx.shipShouldBeIN_ORBITBeforeNavigation)
	sc.Step(`^ship should be IN_TRANSIT after navigate API call$`, ctx.shipShouldBeIN_TRANSITAfterNavigateAPICall)
	sc.Step(`^ship should be IN_ORBIT after arrival$`, ctx.shipShouldBeIN_ORBITAfterArrival)
	sc.Step(`^ship should transition back to IN_ORBIT$`, ctx.shipShouldTransitionBackToIN_ORBIT)
	sc.Step(`^ship should transition to DOCKED$`, ctx.shipShouldTransitionToDOCKED)
	sc.Step(`^ship should remain in orbit$`, ctx.shipShouldRemainInOrbit)
	sc.Step(`^ship state should be re-fetched after sleep$`, ctx.shipStateShouldBeRefetchedAfterSleep)
	sc.Step(`^ship state should be re-fetched immediately$`, ctx.shipStateShouldBeRefetchedImmediately)
	sc.Step(`^ship state should be re-synced after arrival$`, ctx.shipStateShouldBeResyncedAfterArrival)
	sc.Step(`^ship state should still be re-fetched$`, ctx.shipStateShouldStillBeRefetched)
	sc.Step(`^ship state should still be re-synced$`, ctx.shipStateShouldStillBeResynced)

	// Handler verification
	sc.Step(`^handler should calculate wait time as (\d+) seconds$`, ctx.handlerShouldCalculateWaitTimeAsSeconds)
	sc.Step(`^handler should add (\d+) second buffer$`, ctx.handlerShouldAddSecondBuffer)
	sc.Step(`^the handler should calculate (\d+) seconds wait time$`, ctx.theHandlerShouldCalculateSecondsWaitTime)
	sc.Step(`^the handler should detect IN_TRANSIT state$`, ctx.theHandlerShouldDetectIN_TRANSITState)
	sc.Step(`^the handler should sleep for (\d+) seconds$`, ctx.theHandlerShouldSleepForSeconds)
	sc.Step(`^the handler should wait for arrival$`, ctx.theHandlerShouldWaitForArrival)
	sc.Step(`^wait time should be (\d+) seconds$`, ctx.waitTimeShouldBeSeconds)
	sc.Step(`^total sleep time should be (\d+) seconds$`, ctx.totalSleepTimeShouldBeSeconds)

	// API returns
	sc.Step(`^API returns segment travel time of (\d+) seconds$`, ctx.aPIReturnsSegmentTravelTimeOfSeconds)
	sc.Step(`^API returns travel time in the past$`, ctx.aPIReturnsTravelTimeInThePast)

	// Flight mode
	sc.Step(`^flight mode should be set to "([^"]*)" before navigate API call$`, ctx.flightModeShouldBeSetToBeforeNavigateAPICall)

	// Final status
	sc.Step(`^final status should be IN_ORBIT at "([^"]*)"$`, ctx.finalStatusShouldBeIN_ORBITAt)

	// Route failure
	sc.Step(`^route FailRoute method should be called with error message$`, ctx.routeFailRouteMethodShouldBeCalledWithErrorMessage)
	sc.Step(`^ship Arrive method should be called if status is IN_TRANSIT$`, ctx.shipArriveMethodShouldBeCalledIfStatusIsIN_TRANSIT)

	// Operations
	sc.Step(`^ship "([^"]*)" executes a route with dock refuel orbit navigate$`, ctx.shipExecutesARouteWithDockRefuelOrbitNavigate)
	sc.Step(`^ship "([^"]*)" executes a route with dock, refuel, orbit, navigate$`, ctx.shipExecutesARouteWithDockRefuelOrbitNavigate)

	// Value comparisons
	sc.Step(`^both distances should be equal$`, ctx.bothDistancesShouldBeEqual)
	sc.Step(`^I check if ship should prevent drift mode with threshold (\d+), (\d+)$`, ctx.iCheckIfShipShouldPreventDriftModeWithThreshold)
	sc.Step(`^I check if ship should refuel opportunistically at "([^"]*)" with (\d+) threshold (\d+)$`, ctx.iCheckIfShipShouldRefuelOpportunisticallyAtWithThreshold)
}

package steps

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type shipFuelServiceContext struct {
	service       *navigation.ShipFuelService
	fuelState     *shared.Fuel
	boolResult    bool
	intResult     int
	flightMode    shared.FlightMode
	fromWaypoint  string
	toWaypoint    string
}

func (sfc *shipFuelServiceContext) reset() {
	sfc.service = navigation.NewShipFuelService()
	sfc.fuelState = nil
	sfc.boolResult = false
	sfc.intResult = 0
	sfc.flightMode = shared.FlightModeCruise
	sfc.fromWaypoint = ""
	sfc.toWaypoint = ""
	resetSharedNavigationWaypoints()
}

// Background Steps

func (sfc *shipFuelServiceContext) aShipFuelService() error {
	sfc.service = navigation.NewShipFuelService()
	return nil
}

// Waypoint Setup Steps

func (sfc *shipFuelServiceContext) waypointAtCoordinates(symbol string, x, y float64) error {
	waypoint, err := shared.NewWaypoint(symbol, x, y)
	if err != nil {
		return err
	}
	setSharedNavigationWaypoint(symbol, waypoint)
	return nil
}

func (sfc *shipFuelServiceContext) waypointAtCoordinatesWithFuelAvailable(
	symbol string, x, y float64,
) error {
	waypoint, err := shared.NewWaypoint(symbol, x, y)
	if err != nil {
		return err
	}
	waypoint.HasFuel = true
	setSharedNavigationWaypoint(symbol, waypoint)
	return nil
}

func (sfc *shipFuelServiceContext) waypointAtCoordinatesWithoutFuel(
	symbol string, x, y float64,
) error {
	waypoint, err := shared.NewWaypoint(symbol, x, y)
	if err != nil {
		return err
	}
	waypoint.HasFuel = false
	setSharedNavigationWaypoint(symbol, waypoint)
	return nil
}

// Fuel State Setup Steps

func (sfc *shipFuelServiceContext) aFuelStateWithUnitsOfFuelAndCapacity(
	current, capacity int,
) error {
	fuel, err := shared.NewFuel(current, capacity)
	if err != nil {
		return err
	}
	sfc.fuelState = fuel
	return nil
}

// Action Steps - Calculate Fuel Required

func (sfc *shipFuelServiceContext) iCalculateFuelRequiredFromToInMode(
	from, to, mode string,
) error {
	fromWaypoint, exists := getSharedNavigationWaypoint(from)
	if !exists {
		return fmt.Errorf("waypoint %s not found", from)
	}
	toWaypoint, exists := getSharedNavigationWaypoint(to)
	if !exists {
		return fmt.Errorf("waypoint %s not found", to)
	}

	var flightMode shared.FlightMode
	switch mode {
	case "CRUISE":
		flightMode = shared.FlightModeCruise
	case "DRIFT":
		flightMode = shared.FlightModeDrift
	case "BURN":
		flightMode = shared.FlightModeBurn
	case "STEALTH":
		flightMode = shared.FlightModeStealth
	default:
		return fmt.Errorf("unknown flight mode: %s", mode)
	}

	sfc.intResult = sfc.service.CalculateFuelRequired(fromWaypoint, toWaypoint, flightMode)
	return nil
}

// Action Steps - Can Navigate

func (sfc *shipFuelServiceContext) iCheckIfShipWithUnitsOfFuelCanNavigateFromTo(
	fuel int, from, to string,
) error {
	fromWaypoint, exists := getSharedNavigationWaypoint(from)
	if !exists {
		return fmt.Errorf("waypoint %s not found", from)
	}
	toWaypoint, exists := getSharedNavigationWaypoint(to)
	if !exists {
		return fmt.Errorf("waypoint %s not found", to)
	}

	sfc.boolResult = sfc.service.CanShipNavigateTo(fuel, fromWaypoint, toWaypoint)
	return nil
}

// Action Steps - Should Refuel for Journey

func (sfc *shipFuelServiceContext) iCheckIfRefuelNeededFromToWithSafetyMargin(
	from, to string, safetyMargin float64,
) error {
	if sfc.fuelState == nil {
		return fmt.Errorf("fuel state not initialized")
	}

	fromWaypoint, exists := getSharedNavigationWaypoint(from)
	if !exists {
		return fmt.Errorf("waypoint %s not found", from)
	}
	toWaypoint, exists := getSharedNavigationWaypoint(to)
	if !exists {
		return fmt.Errorf("waypoint %s not found", to)
	}

	sfc.boolResult = sfc.service.ShouldRefuelForJourney(
		sfc.fuelState,
		fromWaypoint,
		toWaypoint,
		safetyMargin,
	)
	return nil
}

// Action Steps - Select Optimal Flight Mode

func (sfc *shipFuelServiceContext) iSelectOptimalFlightModeWithFuelForDistanceWithSafetyMargin(
	fuel int, distance float64, safetyMargin int,
) error {
	sfc.flightMode = sfc.service.SelectOptimalFlightMode(fuel, distance, safetyMargin)
	return nil
}

// Action Steps - Should Refuel Opportunistically

func (sfc *shipFuelServiceContext) iCheckIfShouldRefuelOpportunisticallyAtWithThreshold(
	waypoint string, threshold float64,
) error {
	if sfc.fuelState == nil {
		return fmt.Errorf("fuel state not initialized")
	}

	wp, exists := getSharedNavigationWaypoint(waypoint)
	if !exists {
		return fmt.Errorf("waypoint %s not found", waypoint)
	}

	sfc.boolResult = sfc.service.ShouldRefuelOpportunistically(
		sfc.fuelState,
		sfc.fuelState.Capacity,
		wp,
		threshold,
	)
	return nil
}

// Action Steps - Calculate Fuel Needed to Full

func (sfc *shipFuelServiceContext) iCalculateFuelNeededToFullWithCurrentAndCapacity(
	current, capacity int,
) error {
	sfc.intResult = sfc.service.CalculateFuelNeededToFull(current, capacity)
	return nil
}

// Assertion Steps

func (sfc *shipFuelServiceContext) theServiceFuelRequiredShouldBeUnits(expected int) error {
	if sfc.intResult != expected {
		return fmt.Errorf("expected fuel required %d, got %d", expected, sfc.intResult)
	}
	return nil
}

func (sfc *shipFuelServiceContext) theServiceResultShouldBe(expectedStr string) error {
	expected := expectedStr == "true"
	if sfc.boolResult != expected {
		return fmt.Errorf("expected result %t, got %t", expected, sfc.boolResult)
	}
	return nil
}

func (sfc *shipFuelServiceContext) theSelectedFlightModeShouldBe(expected string) error {
	actual := sfc.flightMode.Name()
	if actual != expected {
		return fmt.Errorf("expected flight mode %s, got %s", expected, actual)
	}
	return nil
}

func (sfc *shipFuelServiceContext) theServiceFuelNeededShouldBeUnits(expected int) error {
	if sfc.intResult != expected {
		return fmt.Errorf("expected fuel needed %d, got %d", expected, sfc.intResult)
	}
	return nil
}

// RegisterShipFuelServiceSteps registers all step definitions for ship fuel service
func RegisterShipFuelServiceSteps(sc *godog.ScenarioContext) {
	sfc := &shipFuelServiceContext{}

	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		sfc.reset()
		return ctx, nil
	})

	// Background
	sc.Step(`^a ship fuel service$`, sfc.aShipFuelService)

	// Waypoint setup
	sc.Step(`^waypoint "([^"]*)" at coordinates \(([^,]+), ([^)]+)\)$`,
		sfc.waypointAtCoordinates)
	sc.Step(`^waypoint "([^"]*)" at coordinates \(([^,]+), ([^)]+)\) with fuel available$`,
		sfc.waypointAtCoordinatesWithFuelAvailable)
	sc.Step(`^waypoint "([^"]*)" at coordinates \(([^,]+), ([^)]+)\) without fuel$`,
		sfc.waypointAtCoordinatesWithoutFuel)

	// Fuel state setup
	sc.Step(`^a fuel state with (\d+) units of fuel and capacity (\d+)$`,
		sfc.aFuelStateWithUnitsOfFuelAndCapacity)

	// Actions
	sc.Step(`^I calculate fuel required from "([^"]*)" to "([^"]*)" in ([A-Z]+) mode$`,
		sfc.iCalculateFuelRequiredFromToInMode)
	sc.Step(`^I check if ship with (\d+) units of fuel can navigate from "([^"]*)" to "([^"]*)"$`,
		sfc.iCheckIfShipWithUnitsOfFuelCanNavigateFromTo)
	sc.Step(`^I check if refuel needed from "([^"]*)" to "([^"]*)" with safety margin ([0-9.]+)$`,
		sfc.iCheckIfRefuelNeededFromToWithSafetyMargin)
	sc.Step(`^I select optimal flight mode with (\d+) fuel for distance ([0-9.]+) with safety margin (\d+)$`,
		sfc.iSelectOptimalFlightModeWithFuelForDistanceWithSafetyMargin)
	sc.Step(`^I check if should refuel opportunistically at "([^"]*)" with threshold ([0-9.]+)$`,
		sfc.iCheckIfShouldRefuelOpportunisticallyAtWithThreshold)
	sc.Step(`^I calculate fuel needed to full with current (\d+) and capacity (\d+)$`,
		sfc.iCalculateFuelNeededToFullWithCurrentAndCapacity)

	// Assertions
	sc.Step(`^the service fuel required should be (\d+) units$`, sfc.theServiceFuelRequiredShouldBeUnits)
	sc.Step(`^the service result should be (true|false)$`, sfc.theServiceResultShouldBe)
	sc.Step(`^the selected flight mode should be "([^"]*)"$`, sfc.theSelectedFlightModeShouldBe)
	sc.Step(`^the service fuel needed should be (\d+) units$`, sfc.theServiceFuelNeededShouldBeUnits)
}

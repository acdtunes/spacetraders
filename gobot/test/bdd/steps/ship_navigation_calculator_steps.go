package steps

import (
	"context"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type shipNavigationCalculatorContext struct {
	calculator     *navigation.ShipNavigationCalculator
	waypoints      map[string]*shared.Waypoint
	intResult      int
	floatResult    float64
	boolResult     bool
}

func (snc *shipNavigationCalculatorContext) reset() {
	snc.calculator = navigation.NewShipNavigationCalculator()
	snc.waypoints = make(map[string]*shared.Waypoint)
	snc.intResult = 0
	snc.floatResult = 0.0
	snc.boolResult = false
}

// Background Steps

func (snc *shipNavigationCalculatorContext) aShipNavigationCalculator() error {
	snc.calculator = navigation.NewShipNavigationCalculator()
	return nil
}

// Waypoint Setup Steps (reusing from fuel service)

func (snc *shipNavigationCalculatorContext) waypointAtCoordinates(symbol string, x, y float64) error {
	waypoint, err := shared.NewWaypoint(symbol, x, y)
	if err != nil {
		return err
	}
	snc.waypoints[symbol] = waypoint
	return nil
}

// Action Steps - Calculate Travel Time

func (snc *shipNavigationCalculatorContext) iCalculateTravelTimeFromToInModeWithEngineSpeed(
	from, to, mode string, engineSpeed int,
) error {
	fromWaypoint, exists := snc.waypoints[from]
	if !exists {
		return fmt.Errorf("waypoint %s not found", from)
	}
	toWaypoint, exists := snc.waypoints[to]
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

	snc.intResult = snc.calculator.CalculateTravelTime(
		fromWaypoint,
		toWaypoint,
		flightMode,
		engineSpeed,
	)
	return nil
}

// Action Steps - Calculate Distance

func (snc *shipNavigationCalculatorContext) iCalculateDistanceFromTo(from, to string) error {
	fromWaypoint, exists := snc.waypoints[from]
	if !exists {
		return fmt.Errorf("waypoint %s not found", from)
	}
	toWaypoint, exists := snc.waypoints[to]
	if !exists {
		return fmt.Errorf("waypoint %s not found", to)
	}

	snc.floatResult = snc.calculator.CalculateDistance(fromWaypoint, toWaypoint)
	return nil
}

// Action Steps - Is At Location

func (snc *shipNavigationCalculatorContext) iCheckIfAtLocationWhenCurrentIs(
	target, current string,
) error {
	currentWaypoint, exists := snc.waypoints[current]
	if !exists {
		return fmt.Errorf("current waypoint %s not found", current)
	}
	targetWaypoint, exists := snc.waypoints[target]
	if !exists {
		return fmt.Errorf("target waypoint %s not found", target)
	}

	snc.boolResult = snc.calculator.IsAtLocation(currentWaypoint, targetWaypoint)
	return nil
}

// Assertion Steps

func (snc *shipNavigationCalculatorContext) theTravelTimeShouldBeSeconds(expected int) error {
	if snc.intResult != expected {
		return fmt.Errorf("expected travel time %d seconds, got %d", expected, snc.intResult)
	}
	return nil
}

func (snc *shipNavigationCalculatorContext) theDistanceShouldBe(expected float64) error {
	// Use a small epsilon for floating point comparison
	epsilon := 0.0001
	if math.Abs(snc.floatResult-expected) > epsilon {
		return fmt.Errorf("expected distance %f, got %f", expected, snc.floatResult)
	}
	return nil
}

func (snc *shipNavigationCalculatorContext) theResultShouldBe(expectedStr string) error {
	expected := expectedStr == "true"
	if snc.boolResult != expected {
		return fmt.Errorf("expected result %t, got %t", expected, snc.boolResult)
	}
	return nil
}

// RegisterShipNavigationCalculatorSteps registers all step definitions for ship navigation calculator
func RegisterShipNavigationCalculatorSteps(sc *godog.ScenarioContext) {
	snc := &shipNavigationCalculatorContext{}

	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		snc.reset()
		return ctx, nil
	})

	// Background
	sc.Step(`^a ship navigation calculator$`, snc.aShipNavigationCalculator)

	// Waypoint setup (shared with fuel service)
	sc.Step(`^waypoint "([^"]*)" at coordinates \(([^,]+), ([^)]+)\)$`,
		snc.waypointAtCoordinates)

	// Actions
	sc.Step(`^I calculate travel time from "([^"]*)" to "([^"]*)" in ([A-Z]+) mode with engine speed (\d+)$`,
		snc.iCalculateTravelTimeFromToInModeWithEngineSpeed)
	sc.Step(`^I calculate distance from "([^"]*)" to "([^"]*)"$`,
		snc.iCalculateDistanceFromTo)
	sc.Step(`^I check if at location "([^"]*)" when current is "([^"]*)"$`,
		snc.iCheckIfAtLocationWhenCurrentIs)

	// Assertions
	sc.Step(`^the travel time should be (\d+) seconds$`, snc.theTravelTimeShouldBeSeconds)
	sc.Step(`^the distance should be ([0-9.]+)$`, snc.theDistanceShouldBe)
	sc.Step(`^the result should be (true|false)$`, snc.theResultShouldBe)
}

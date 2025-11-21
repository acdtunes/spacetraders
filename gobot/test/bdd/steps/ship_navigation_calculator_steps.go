package steps

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

// Shared waypoints registry for navigation-related step contexts
// This allows fuel service and navigation calculator contexts to share waypoints
var (
	sharedNavigationWaypoints   = make(map[string]*shared.Waypoint)
	sharedNavigationWaypointsMu sync.RWMutex
)

func resetSharedNavigationWaypoints() {
	sharedNavigationWaypointsMu.Lock()
	defer sharedNavigationWaypointsMu.Unlock()
	sharedNavigationWaypoints = make(map[string]*shared.Waypoint)
}

func setSharedNavigationWaypoint(symbol string, waypoint *shared.Waypoint) {
	sharedNavigationWaypointsMu.Lock()
	defer sharedNavigationWaypointsMu.Unlock()
	sharedNavigationWaypoints[symbol] = waypoint
}

func getSharedNavigationWaypoint(symbol string) (*shared.Waypoint, bool) {
	sharedNavigationWaypointsMu.RLock()
	defer sharedNavigationWaypointsMu.RUnlock()
	wp, exists := sharedNavigationWaypoints[symbol]
	return wp, exists
}

type shipNavigationCalculatorContext struct {
	calculator     *navigation.ShipNavigationCalculator
	intResult      int
	floatResult    float64
	boolResult     bool
}

func (snc *shipNavigationCalculatorContext) reset() {
	snc.calculator = navigation.NewShipNavigationCalculator()
	snc.intResult = 0
	snc.floatResult = 0.0
	snc.boolResult = false
	resetSharedNavigationWaypoints()
}

// Background Steps

func (snc *shipNavigationCalculatorContext) aShipNavigationCalculator() error {
	snc.calculator = navigation.NewShipNavigationCalculator()
	return nil
}

// Action Steps - Calculate Travel Time

func (snc *shipNavigationCalculatorContext) iCalculateTravelTimeFromToInModeWithEngineSpeed(
	from, to, mode string, engineSpeed int,
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
	fromWaypoint, exists := getSharedNavigationWaypoint(from)
	if !exists {
		return fmt.Errorf("waypoint %s not found", from)
	}
	toWaypoint, exists := getSharedNavigationWaypoint(to)
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
	currentWaypoint, exists := getSharedNavigationWaypoint(current)
	if !exists {
		return fmt.Errorf("current waypoint %s not found", current)
	}
	targetWaypoint, exists := getSharedNavigationWaypoint(target)
	if !exists {
		return fmt.Errorf("target waypoint %s not found", target)
	}

	snc.boolResult = snc.calculator.IsAtLocation(currentWaypoint, targetWaypoint)
	return nil
}

// Assertion Steps

func (snc *shipNavigationCalculatorContext) theCalculatorTravelTimeShouldBeSeconds(expected int) error {
	if snc.intResult != expected {
		return fmt.Errorf("expected travel time %d seconds, got %d", expected, snc.intResult)
	}
	return nil
}

func (snc *shipNavigationCalculatorContext) theCalculatorDistanceShouldBe(expected float64) error {
	// Use a small epsilon for floating point comparison
	epsilon := 0.0001
	if math.Abs(snc.floatResult-expected) > epsilon {
		return fmt.Errorf("expected distance %f, got %f", expected, snc.floatResult)
	}
	return nil
}

func (snc *shipNavigationCalculatorContext) theCalculatorResultShouldBe(expectedStr string) error {
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

	// Note: Waypoint setup steps are handled by ship_fuel_service_steps.go
	// Both contexts share the sharedNavigationWaypoints registry

	// Actions
	sc.Step(`^I calculate travel time from "([^"]*)" to "([^"]*)" in ([A-Z]+) mode with engine speed (\d+)$`,
		snc.iCalculateTravelTimeFromToInModeWithEngineSpeed)
	sc.Step(`^I calculate navigation distance from "([^"]*)" to "([^"]*)"$`,
		snc.iCalculateDistanceFromTo)
	sc.Step(`^I check if at location "([^"]*)" when current is "([^"]*)"$`,
		snc.iCheckIfAtLocationWhenCurrentIs)

	// Assertions
	sc.Step(`^the calculator travel time should be (\d+) seconds$`, snc.theCalculatorTravelTimeShouldBeSeconds)
	sc.Step(`^the calculator distance should be ([0-9.]+)$`, snc.theCalculatorDistanceShouldBe)
	sc.Step(`^the calculator result should be (true|false)$`, snc.theCalculatorResultShouldBe)
}

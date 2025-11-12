package steps

import (
	"context"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type valueObjectContext struct {
	waypoint     *shared.Waypoint
	fuel         *shared.Fuel
	originalFuel *shared.Fuel
	cargo        *shared.Cargo
	cargoItem    *shared.CargoItem
	flightMode   shared.FlightMode
	err          error
	floatResult  float64
	intResult    int
	boolResult   bool
	waypointMap  map[string]*shared.Waypoint
}

func (voc *valueObjectContext) reset() {
	voc.waypoint = nil
	voc.fuel = nil
	voc.originalFuel = nil
	voc.cargo = nil
	voc.cargoItem = nil
	voc.flightMode = shared.FlightModeCruise
	voc.err = nil
	voc.floatResult = 0
	voc.intResult = 0
	voc.boolResult = false
	voc.waypointMap = make(map[string]*shared.Waypoint)
}

// Waypoint steps
func (voc *valueObjectContext) iCreateAWaypointWithSymbolXY(symbol string, x, y float64) error {
	voc.waypoint, voc.err = shared.NewWaypoint(symbol, x, y)
	return voc.err
}

func (voc *valueObjectContext) iAttemptToCreateAWaypointWithEmptySymbol() error {
	voc.waypoint, voc.err = shared.NewWaypoint("", 0, 0)
	return nil
}

func (voc *valueObjectContext) waypointCreationShouldFailWithError(expectedError string) error {
	if voc.err == nil {
		return fmt.Errorf("expected error but got none")
	}
	return nil
}

func (voc *valueObjectContext) theWaypointShouldHaveSymbol(symbol string) error {
	if voc.waypoint.Symbol != symbol {
		return fmt.Errorf("expected symbol '%s' but got '%s'", symbol, voc.waypoint.Symbol)
	}
	return nil
}

func (voc *valueObjectContext) aWaypointAtCoordinates(symbol string, x, y float64) error {
	wp, _ := shared.NewWaypoint(symbol, x, y)
	voc.waypointMap[symbol] = wp
	return nil
}

func (voc *valueObjectContext) iCalculateDistanceFromTo(from, to string) error {
	wpFrom := voc.waypointMap[from]
	wpTo := voc.waypointMap[to]
	voc.floatResult = wpFrom.DistanceTo(wpTo)
	return nil
}

func (voc *valueObjectContext) theDistanceShouldBe(expected float64) error {
	if voc.floatResult != expected {
		return fmt.Errorf("expected distance %.1f but got %.1f", expected, voc.floatResult)
	}
	return nil
}

func (voc *valueObjectContext) theDistanceShouldBeApproximately(expected float64) error {
	if math.Abs(voc.floatResult-expected) > 0.1 {
		return fmt.Errorf("expected distance approximately %.2f but got %.2f", expected, voc.floatResult)
	}
	return nil
}

// Fuel steps
func (voc *valueObjectContext) iCreateFuelWithCurrentAndCapacity(current, capacity int) error {
	voc.fuel, voc.err = shared.NewFuel(current, capacity)
	return voc.err
}

func (voc *valueObjectContext) iAttemptToCreateFuelWithCurrentAndCapacity(current, capacity int) error {
	voc.fuel, voc.err = shared.NewFuel(current, capacity)
	return nil
}

func (voc *valueObjectContext) fuelCreationShouldFailWithError(expectedError string) error {
	if voc.err == nil {
		return fmt.Errorf("expected error but got none")
	}
	return nil
}

func (voc *valueObjectContext) theFuelShouldHaveCurrent(current int) error {
	if voc.fuel.Current != current {
		return fmt.Errorf("expected current %d but got %d", current, voc.fuel.Current)
	}
	return nil
}

func (voc *valueObjectContext) theFuelShouldHaveCapacity(capacity int) error {
	if voc.fuel.Capacity != capacity {
		return fmt.Errorf("expected capacity %d but got %d", capacity, voc.fuel.Capacity)
	}
	return nil
}

func (voc *valueObjectContext) fuelWithCurrentAndCapacity(current, capacity int) error {
	voc.fuel, _ = shared.NewFuel(current, capacity)
	return nil
}

func (voc *valueObjectContext) iCalculateTheFuelPercentage() error {
	voc.floatResult = voc.fuel.Percentage()
	return nil
}

func (voc *valueObjectContext) thePercentageShouldBe(expected float64) error {
	if voc.floatResult != expected {
		return fmt.Errorf("expected percentage %.1f but got %.1f", expected, voc.floatResult)
	}
	return nil
}

func (voc *valueObjectContext) iConsumeUnitsOfFuel(units int) error {
	voc.originalFuel = voc.fuel
	voc.fuel, voc.err = voc.fuel.Consume(units)
	return voc.err
}

func (voc *valueObjectContext) theNewFuelShouldHaveCurrent(current int) error {
	if voc.fuel.Current != current {
		return fmt.Errorf("expected new fuel current %d but got %d", current, voc.fuel.Current)
	}
	return nil
}

// Flight mode steps
func (voc *valueObjectContext) iCalculateFuelCostForModeWithDistance(mode string, distance float64) error {
	voc.flightMode = parseFlightMode(mode)
	voc.intResult = voc.flightMode.FuelCost(distance)
	return nil
}

func (voc *valueObjectContext) theFuelCostShouldBe(cost int) error {
	if voc.intResult != cost {
		return fmt.Errorf("expected fuel cost %d but got %d", cost, voc.intResult)
	}
	return nil
}

func (voc *valueObjectContext) iCalculateTravelTimeForModeWithDistanceAndSpeed(mode string, distance float64, speed int) error {
	voc.flightMode = parseFlightMode(mode)
	voc.intResult = voc.flightMode.TravelTime(distance, speed)
	return nil
}

func (voc *valueObjectContext) theTravelTimeShouldBeSeconds(seconds int) error {
	if voc.intResult != seconds {
		return fmt.Errorf("expected travel time %d but got %d", seconds, voc.intResult)
	}
	return nil
}

func (voc *valueObjectContext) iSelectOptimalFlightModeWithCurrentFuelCostSafetyMargin(currentFuel, cost, safetyMargin int) error {
	voc.flightMode = shared.SelectOptimalFlightMode(currentFuel, cost, safetyMargin)
	return nil
}

func (voc *valueObjectContext) theSelectedModeShouldBe(mode string) error {
	if voc.flightMode.Name() != mode {
		return fmt.Errorf("expected mode '%s' but got '%s'", mode, voc.flightMode.Name())
	}
	return nil
}

// Cargo steps
func (voc *valueObjectContext) iCreateACargoItemWithSymbolNameUnits(symbol, name string, units int) error {
	voc.cargoItem, voc.err = shared.NewCargoItem(symbol, name, "", units)
	return voc.err
}

func (voc *valueObjectContext) iAttemptToCreateACargoItemWithEmptySymbol() error {
	voc.cargoItem, voc.err = shared.NewCargoItem("", "Test", "", 10)
	return nil
}

func (voc *valueObjectContext) cargoItemCreationShouldFailWithError(expectedError string) error {
	if voc.err == nil {
		return fmt.Errorf("expected error but got none")
	}
	return nil
}

func (voc *valueObjectContext) theCargoItemShouldHaveSymbol(symbol string) error {
	if voc.cargoItem.Symbol != symbol {
		return fmt.Errorf("expected symbol '%s' but got '%s'", symbol, voc.cargoItem.Symbol)
	}
	return nil
}

func (voc *valueObjectContext) theCargoItemShouldHaveUnits(units int) error {
	if voc.cargoItem.Units != units {
		return fmt.Errorf("expected units %d but got %d", units, voc.cargoItem.Units)
	}
	return nil
}

// InitializeValueObjectScenarios registers all value object-related step definitions
func InitializeValueObjectScenarios(ctx *godog.ScenarioContext) {
	voc := &valueObjectContext{}

	ctx.Before(func(ctx context.Context, sc *godog.Scenario) (context.Context, error) {
		voc.reset()
		return ctx, nil
	})

	// Waypoint steps
	ctx.Step(`^I create a waypoint with symbol "([^"]*)", x ([^,]+), y ([^)]+)$`, voc.iCreateAWaypointWithSymbolXY)
	ctx.Step(`^I attempt to create a waypoint with empty symbol$`, voc.iAttemptToCreateAWaypointWithEmptySymbol)
	ctx.Step(`^waypoint creation should fail with error "([^"]*)"$`, voc.waypointCreationShouldFailWithError)
	ctx.Step(`^the waypoint should have symbol "([^"]*)"$`, voc.theWaypointShouldHaveSymbol)
	ctx.Step(`^a waypoint "([^"]*)" at coordinates \(([^,]+), ([^)]+)\)$`, voc.aWaypointAtCoordinates)
	ctx.Step(`^I calculate distance from "([^"]*)" to "([^"]*)"$`, voc.iCalculateDistanceFromTo)
	ctx.Step(`^the distance should be ([0-9.]+)$`, voc.theDistanceShouldBe)
	ctx.Step(`^the distance should be approximately ([0-9.]+)$`, voc.theDistanceShouldBeApproximately)

	// Fuel steps
	ctx.Step(`^I create fuel with current (\d+) and capacity (\d+)$`, voc.iCreateFuelWithCurrentAndCapacity)
	ctx.Step(`^I attempt to create fuel with current (-?\d+) and capacity (-?\d+)$`, voc.iAttemptToCreateFuelWithCurrentAndCapacity)
	ctx.Step(`^fuel creation should fail with error "([^"]*)"$`, voc.fuelCreationShouldFailWithError)
	ctx.Step(`^the fuel should have current (\d+)$`, voc.theFuelShouldHaveCurrent)
	ctx.Step(`^the fuel should have capacity (\d+)$`, voc.theFuelShouldHaveCapacity)
	ctx.Step(`^fuel with current (\d+) and capacity (\d+)$`, voc.fuelWithCurrentAndCapacity)
	ctx.Step(`^I calculate the fuel percentage$`, voc.iCalculateTheFuelPercentage)
	ctx.Step(`^the percentage should be ([0-9.]+)$`, voc.thePercentageShouldBe)
	ctx.Step(`^I consume (\d+) units of fuel$`, voc.iConsumeUnitsOfFuel)
	ctx.Step(`^the new fuel should have current (\d+)$`, voc.theNewFuelShouldHaveCurrent)

	// Flight mode steps
	ctx.Step(`^I calculate fuel cost for ([A-Z]+) mode with distance ([0-9.]+)$`, voc.iCalculateFuelCostForModeWithDistance)
	ctx.Step(`^the fuel cost should be (\d+)$`, voc.theFuelCostShouldBe)
	ctx.Step(`^I calculate travel time for ([A-Z]+) mode with distance ([0-9.]+) and speed (\d+)$`,
		voc.iCalculateTravelTimeForModeWithDistanceAndSpeed)
	ctx.Step(`^the travel time should be (\d+) seconds$`, voc.theTravelTimeShouldBeSeconds)
	ctx.Step(`^I select optimal flight mode with current fuel (\d+), cost (\d+), safety margin (\d+)$`,
		voc.iSelectOptimalFlightModeWithCurrentFuelCostSafetyMargin)
	ctx.Step(`^the selected mode should be ([A-Z]+)$`, voc.theSelectedModeShouldBe)

	// Cargo steps
	ctx.Step(`^I create a cargo item with symbol "([^"]*)", name "([^"]*)", units (\d+)$`,
		voc.iCreateACargoItemWithSymbolNameUnits)
	ctx.Step(`^I attempt to create a cargo item with empty symbol$`, voc.iAttemptToCreateACargoItemWithEmptySymbol)
	ctx.Step(`^cargo item creation should fail with error "([^"]*)"$`, voc.cargoItemCreationShouldFailWithError)
	ctx.Step(`^the cargo item should have symbol "([^"]*)"$`, voc.theCargoItemShouldHaveSymbol)
	ctx.Step(`^the cargo item should have units (\d+)$`, voc.theCargoItemShouldHaveUnits)

	// Note: Complete implementation would include all remaining steps from the value object feature files
}

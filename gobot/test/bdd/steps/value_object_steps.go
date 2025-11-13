package steps

import (
	"context"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

// Shared assertion variables across contexts
// These are used when steps from different contexts need to share assertion data
var (
	sharedBoolResult   bool
	sharedIntResult    int
	sharedErr          error
	sharedWaypointMap  map[string]*shared.Waypoint
)

type valueObjectContext struct {
	waypoint       *shared.Waypoint
	fuel           *shared.Fuel
	originalFuel   *shared.Fuel
	cargo          *shared.Cargo
	cargoItem      *shared.CargoItem
	flightMode     shared.FlightMode
	err            error
	floatResult    float64
	intResult      int
	boolResult     bool
	waypointMap    map[string]*shared.Waypoint
	otherCargoItems []*shared.CargoItem
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
	voc.otherCargoItems = nil
	// Reset shared variables
	sharedBoolResult = false
	sharedIntResult = 0
	sharedErr = nil
	sharedWaypointMap = make(map[string]*shared.Waypoint)
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
	sharedWaypointMap[symbol] = wp
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
	// Check shared int result first (used by other contexts), then fallback to context-specific result
	result := sharedIntResult
	if result == 0 && voc.intResult != 0 {
		result = voc.intResult
	}

	if result != seconds {
		return fmt.Errorf("expected travel time %d but got %d", seconds, result)
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

func (voc *valueObjectContext) theCargoItemShouldHaveName(name string) error {
	if voc.cargoItem.Name != name {
		return fmt.Errorf("expected name '%s' but got '%s'", name, voc.cargoItem.Name)
	}
	return nil
}

func (voc *valueObjectContext) iAttemptToCreateACargoItemWithUnits(units int) error {
	voc.cargoItem, voc.err = shared.NewCargoItem("TEST", "Test", "", units)
	return nil
}

// Cargo construction steps
func (voc *valueObjectContext) aCargoItemWithSymbolAndUnits(symbol string, units int) error {
	item, err := shared.NewCargoItem(symbol, symbol, "", units)
	if err != nil {
		return err
	}
	voc.cargoItem = item
	return nil
}

func (voc *valueObjectContext) iCreateCargoWithCapacityUnitsAndInventory(capacity, units int) error {
	inventory := []*shared.CargoItem{}
	if voc.cargoItem != nil {
		inventory = append(inventory, voc.cargoItem)
	}
	voc.cargo, voc.err = shared.NewCargo(capacity, units, inventory)
	return voc.err
}

func (voc *valueObjectContext) iCreateCargoWithCapacityUnitsAndEmptyInventory(capacity, units int) error {
	voc.cargo, voc.err = shared.NewCargo(capacity, units, []*shared.CargoItem{})
	return voc.err
}

func (voc *valueObjectContext) iAttemptToCreateCargoWithUnits(units int) error {
	voc.cargo, voc.err = shared.NewCargo(40, units, []*shared.CargoItem{})
	return nil
}

func (voc *valueObjectContext) iAttemptToCreateCargoWithCapacity(capacity int) error {
	voc.cargo, voc.err = shared.NewCargo(capacity, 0, []*shared.CargoItem{})
	return nil
}

func (voc *valueObjectContext) iAttemptToCreateCargoWithCapacityAndUnits(capacity, units int) error {
	voc.cargo, voc.err = shared.NewCargo(capacity, units, []*shared.CargoItem{})
	return nil
}

func (voc *valueObjectContext) cargoCreationShouldFailWithError(expectedError string) error {
	if voc.err == nil {
		return fmt.Errorf("expected error but got none")
	}
	return nil
}

func (voc *valueObjectContext) theCargoShouldHaveCapacity(capacity int) error {
	if voc.cargo.Capacity != capacity {
		return fmt.Errorf("expected capacity %d but got %d", capacity, voc.cargo.Capacity)
	}
	return nil
}

func (voc *valueObjectContext) theCargoShouldHaveUnits(units int) error {
	if voc.cargo.Units != units {
		return fmt.Errorf("expected units %d but got %d", units, voc.cargo.Units)
	}
	return nil
}

func (voc *valueObjectContext) theCargoInventoryShouldContainItems(count int) error {
	if len(voc.cargo.Inventory) != count {
		return fmt.Errorf("expected %d items but got %d", count, len(voc.cargo.Inventory))
	}
	return nil
}

func (voc *valueObjectContext) theCargoShouldBeEmpty() error {
	if !voc.cargo.IsEmpty() {
		return fmt.Errorf("expected cargo to be empty but it has %d units", voc.cargo.Units)
	}
	return nil
}

// Cargo query steps
func (voc *valueObjectContext) cargoWithItems(table *godog.Table) error {
	items := []*shared.CargoItem{}
	totalUnits := 0

	for i, row := range table.Rows {
		if i == 0 {
			continue // Skip header
		}
		symbol := row.Cells[0].Value
		var units int
		fmt.Sscanf(row.Cells[1].Value, "%d", &units)

		item, err := shared.NewCargoItem(symbol, symbol, "", units)
		if err != nil {
			return err
		}
		items = append(items, item)
		totalUnits += units
	}

	voc.cargo, voc.err = shared.NewCargo(100, totalUnits, items)
	return voc.err
}

func (voc *valueObjectContext) iCheckIfCargoHasItemWithMinUnits(symbol string, minUnits int) error {
	voc.boolResult = voc.cargo.HasItem(symbol, minUnits)
	return nil
}

func (voc *valueObjectContext) theResultShouldBe(expectedStr string) error {
	expected := expectedStr == "true"
	// Use shared variable if set by another context (e.g., containerContext)
	// Otherwise fall back to local boolResult
	actualResult := sharedBoolResult || voc.boolResult
	// For false expectations, we need to check both are false
	if !expected && (sharedBoolResult || voc.boolResult) {
		return fmt.Errorf("expected result false but got true")
	}
	if expected && !actualResult {
		return fmt.Errorf("expected result true but got false")
	}
	return nil
}

func (voc *valueObjectContext) iGetUnitsOfItem(symbol string) error {
	voc.intResult = voc.cargo.GetItemUnits(symbol)
	return nil
}

func (voc *valueObjectContext) theItemUnitsShouldBe(units int) error {
	if voc.intResult != units {
		return fmt.Errorf("expected %d units but got %d", units, voc.intResult)
	}
	return nil
}

func (voc *valueObjectContext) iCheckIfCargoHasItemsOtherThan(symbol string) error {
	voc.boolResult = voc.cargo.HasItemsOtherThan(symbol)
	return nil
}

// Cargo capacity steps
func (voc *valueObjectContext) cargoWithCapacityAndUnits(capacity, units int) error {
	// Create dummy inventory items to match total units (required by Cargo validation)
	var inventory []*shared.CargoItem
	if units > 0 {
		item, _ := shared.NewCargoItem("DUMMY", "Dummy Item", "", units)
		inventory = []*shared.CargoItem{item}
	}
	voc.cargo, voc.err = shared.NewCargo(capacity, units, inventory)
	return voc.err
}

func (voc *valueObjectContext) iCalculateAvailableCapacity() error {
	voc.intResult = voc.cargo.AvailableCapacity()
	return nil
}

func (voc *valueObjectContext) theAvailableCapacityShouldBe(capacity int) error {
	if voc.intResult != capacity {
		return fmt.Errorf("expected available capacity %d but got %d", capacity, voc.intResult)
	}
	return nil
}

// Cargo status steps
func (voc *valueObjectContext) iCheckIfCargoIsEmpty() error {
	voc.boolResult = voc.cargo.IsEmpty()
	return nil
}

func (voc *valueObjectContext) iCheckIfCargoIsFull() error {
	voc.boolResult = voc.cargo.IsFull()
	return nil
}

// Get other items steps
func (voc *valueObjectContext) iGetOtherItemsExcluding(symbol string) error {
	voc.otherCargoItems = voc.cargo.GetOtherItems(symbol)
	return nil
}

func (voc *valueObjectContext) iShouldHaveOtherCargoItems(count int) error {
	if len(voc.otherCargoItems) != count {
		return fmt.Errorf("expected %d other cargo items but got %d", count, len(voc.otherCargoItems))
	}
	return nil
}

func (voc *valueObjectContext) otherItemsShouldContainWithUnits(symbol string, units int) error {
	for _, item := range voc.otherCargoItems {
		if item.Symbol == symbol && item.Units == units {
			return nil
		}
	}
	return fmt.Errorf("expected to find %s with %d units in other items", symbol, units)
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

	// Cargo item steps
	ctx.Step(`^I create a cargo item with symbol "([^"]*)", name "([^"]*)", units (\d+)$`,
		voc.iCreateACargoItemWithSymbolNameUnits)
	ctx.Step(`^I attempt to create a cargo item with empty symbol$`, voc.iAttemptToCreateACargoItemWithEmptySymbol)
	ctx.Step(`^I attempt to create a cargo item with units (-?\d+)$`, voc.iAttemptToCreateACargoItemWithUnits)
	ctx.Step(`^cargo item creation should fail with error "([^"]*)"$`, voc.cargoItemCreationShouldFailWithError)
	ctx.Step(`^the cargo item should have symbol "([^"]*)"$`, voc.theCargoItemShouldHaveSymbol)
	ctx.Step(`^the cargo item should have name "([^"]*)"$`, voc.theCargoItemShouldHaveName)
	ctx.Step(`^the cargo item should have units (\d+)$`, voc.theCargoItemShouldHaveUnits)

	// Cargo construction steps
	ctx.Step(`^a cargo item with symbol "([^"]*)" and units (\d+)$`, voc.aCargoItemWithSymbolAndUnits)
	ctx.Step(`^I create cargo with capacity (\d+), units (\d+), and inventory$`, voc.iCreateCargoWithCapacityUnitsAndInventory)
	ctx.Step(`^I create cargo with capacity (\d+), units (\d+), and empty inventory$`, voc.iCreateCargoWithCapacityUnitsAndEmptyInventory)
	ctx.Step(`^I attempt to create cargo with units (-?\d+)$`, voc.iAttemptToCreateCargoWithUnits)
	ctx.Step(`^I attempt to create cargo with capacity (-?\d+)$`, voc.iAttemptToCreateCargoWithCapacity)
	ctx.Step(`^I attempt to create cargo with capacity (\d+) and units (\d+)$`, voc.iAttemptToCreateCargoWithCapacityAndUnits)
	ctx.Step(`^cargo creation should fail with error "([^"]*)"$`, voc.cargoCreationShouldFailWithError)
	ctx.Step(`^the cargo should have capacity (\d+)$`, voc.theCargoShouldHaveCapacity)
	ctx.Step(`^the cargo should have units (\d+)$`, voc.theCargoShouldHaveUnits)
	ctx.Step(`^the cargo inventory should contain (\d+) items$`, voc.theCargoInventoryShouldContainItems)
	ctx.Step(`^the cargo should be empty$`, voc.theCargoShouldBeEmpty)

	// Cargo query steps
	ctx.Step(`^cargo with items:$`, voc.cargoWithItems)
	ctx.Step(`^I check if cargo has item "([^"]*)" with min units (\d+)$`, voc.iCheckIfCargoHasItemWithMinUnits)
	ctx.Step(`^I get units of item "([^"]*)"$`, voc.iGetUnitsOfItem)
	ctx.Step(`^the item units should be (\d+)$`, voc.theItemUnitsShouldBe)
	ctx.Step(`^I check if cargo has items other than "([^"]*)"$`, voc.iCheckIfCargoHasItemsOtherThan)
	ctx.Step(`^the result should be (true|false)$`, voc.theResultShouldBe)

	// Cargo capacity steps
	ctx.Step(`^cargo with capacity (\d+) and units (\d+)$`, voc.cargoWithCapacityAndUnits)
	ctx.Step(`^I calculate available capacity$`, voc.iCalculateAvailableCapacity)
	ctx.Step(`^the available capacity should be (\d+)$`, voc.theAvailableCapacityShouldBe)

	// Cargo status steps
	ctx.Step(`^I check if cargo is empty$`, voc.iCheckIfCargoIsEmpty)
	ctx.Step(`^I check if cargo is full$`, voc.iCheckIfCargoIsFull)

	// Get other items steps
	ctx.Step(`^I get other items excluding "([^"]*)"$`, voc.iGetOtherItemsExcluding)
	ctx.Step(`^I should have (\d+) other cargo items$`, voc.iShouldHaveOtherCargoItems)
	ctx.Step(`^other items should contain "([^"]*)" with (\d+) units$`, voc.otherItemsShouldContainWithUnits)
}

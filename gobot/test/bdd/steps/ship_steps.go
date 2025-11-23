package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/cucumber/godog"
)

type shipContext struct {
	ship              *navigation.Ship
	clonedShip        *navigation.Ship
	err               error
	boolResult        bool
	intResult         int
	stringResult      string
	waypoints         map[string]*shared.Waypoint
	flightMode        shared.FlightMode
	stateChangeResult bool
	fuelService       *navigation.ShipFuelService
	navigationCalc    *navigation.ShipNavigationCalculator
}

func (sc *shipContext) reset() {
	sc.ship = nil
	sc.clonedShip = nil
	sc.err = nil
	sc.boolResult = false
	sc.intResult = 0
	sc.stringResult = ""
	sc.waypoints = sharedWaypointMap  // Use shared waypoint map
	sc.flightMode = shared.FlightModeCruise
	sc.stateChangeResult = false
	sc.fuelService = navigation.NewShipFuelService()
	sc.navigationCalc = navigation.NewShipNavigationCalculator()
	// Reset shared ship (shared with value_object_steps)
	sharedShip = nil
}

// Helper to create a default waypoint
func (sc *shipContext) getOrCreateWaypoint(symbol string, x, y float64) *shared.Waypoint {
	if wp, exists := sc.waypoints[symbol]; exists {
		return wp
	}
	wp, _ := shared.NewWaypoint(symbol, x, y)
	sc.waypoints[symbol] = wp
	sharedWaypointMap[symbol] = wp // Also update shared map
	return wp
}

// Helper to create cargo with proper inventory for testing
func (sc *shipContext) createCargoWithUnits(capacity, units int) (*shared.Cargo, error) {
	// Create dummy inventory items to match total units (required by Cargo validation)
	var inventory []*shared.CargoItem
	if units > 0 {
		item, err := shared.NewCargoItem("DUMMY", "Dummy Item", "", units)
		if err != nil {
			return nil, err
		}
		inventory = []*shared.CargoItem{item}
	}
	return shared.NewCargo(capacity, units, inventory)
}

// Ship Initialization Steps

func (sc *shipContext) iCreateAShipWithSymbolPlayerAtFuelCargoSpeedStatus(
	symbol string, playerID int, location string, fuelCurrent, fuelCapacity, cargoUnits, cargoCapacity, speed int, status string,
) error {
	waypoint := sc.getOrCreateWaypoint(location, 0, 0)
	fuel, err := shared.NewFuel(fuelCurrent, fuelCapacity)
	if err != nil {
		return err
	}
	cargo, err := sc.createCargoWithUnits(cargoCapacity, cargoUnits)
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
		navStatus = navigation.NavStatusInOrbit
	}

	sc.ship, sc.err = navigation.NewShip(
		symbol, shared.MustNewPlayerID(playerID), waypoint, fuel, fuelCapacity,
		cargoCapacity, cargo, speed, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, navStatus,
	)
	return sc.err
}

func (sc *shipContext) iAttemptToCreateAShipWithEmptyShipSymbol() error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return nil
}

func (sc *shipContext) iAttemptToCreateAShipWithPlayerID(playerID int) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	// Use NewPlayerID to capture validation errors instead of MustNewPlayerID which panics
	playerIDValue, err := shared.NewPlayerID(playerID)
	if err != nil {
		sc.err = err
		return nil
	}

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", playerIDValue, waypoint, fuel, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return nil
}

func (sc *shipContext) iAttemptToCreateAShipWithFuelCapacity(fuelCapacity int) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(0, 0)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, fuelCapacity, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return nil
}

func (sc *shipContext) iAttemptToCreateAShipWithFuelObjectCapacityButFuelCapacityParameter(fuelObjCap, fuelCapParam int) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(fuelObjCap, fuelObjCap)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, fuelCapParam, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return nil
}

func (sc *shipContext) iAttemptToCreateAShipWithCargoCapacity(cargoCapacity int) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(0, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, cargoCapacity, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return nil
}

func (sc *shipContext) iAttemptToCreateAShipWithCargoUnits(cargoUnits int) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	// Try to create cargo with the specified units - will fail for negative
	cargo, err := sc.createCargoWithUnits(40, cargoUnits)
	if err != nil {
		sc.err = err
		return nil
	}

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return nil
}

func (sc *shipContext) iAttemptToCreateAShipWithCargoCapacityAndCargoUnits(capacity, units int) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, err := sc.createCargoWithUnits(capacity, units)
	if err != nil {
		sc.err = err
		return nil
	}

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, capacity, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return nil
}

func (sc *shipContext) iAttemptToCreateAShipWithEngineSpeed(speed int) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, speed, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return nil
}

func (sc *shipContext) iAttemptToCreateAShipWithNavStatus(status string) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, navigation.NavStatus(status),
	)
	return nil
}

func (sc *shipContext) shipCreationShouldFailWithError(expectedError string) error {
	if sc.err == nil {
		return fmt.Errorf("expected error containing '%s' but got no error", expectedError)
	}
	if !strings.Contains(sc.err.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedError, sc.err.Error())
	}
	return nil
}

// Ship Property Verification Steps

func (sc *shipContext) theShipShouldHaveSymbol(symbol string) error {
	if sc.ship.ShipSymbol() != symbol {
		return fmt.Errorf("expected symbol '%s' but got '%s'", symbol, sc.ship.ShipSymbol())
	}
	return nil
}

func (sc *shipContext) theShipShouldHavePlayerID(playerID int) error {
	if sc.ship.PlayerID().Value() != playerID {
		return fmt.Errorf("expected player_id %d but got %d", playerID, sc.ship.PlayerID().Value())
	}
	return nil
}

func (sc *shipContext) theShipShouldBeAtLocation(location string) error {
	if sc.ship.CurrentLocation().Symbol != location {
		return fmt.Errorf("expected location '%s' but got '%s'", location, sc.ship.CurrentLocation().Symbol)
	}
	return nil
}

func (sc *shipContext) theShipShouldHaveUnitsOfFuel(units int) error {
	if sc.ship.Fuel().Current != units {
		return fmt.Errorf("expected %d units of fuel but got %d", units, sc.ship.Fuel().Current)
	}
	return nil
}

func (sc *shipContext) theShipFuelCapacityShouldBe(capacity int) error {
	if sc.ship.FuelCapacity() != capacity {
		return fmt.Errorf("expected fuel capacity %d but got %d", capacity, sc.ship.FuelCapacity())
	}
	return nil
}

func (sc *shipContext) theShipCargoCapacityShouldBe(capacity int) error {
	if sc.ship.CargoCapacity() != capacity {
		return fmt.Errorf("expected cargo capacity %d but got %d", capacity, sc.ship.CargoCapacity())
	}
	return nil
}

func (sc *shipContext) theShipCargoUnitsShouldBe(units int) error {
	if sc.ship.CargoUnits() != units {
		return fmt.Errorf("expected cargo units %d but got %d", units, sc.ship.CargoUnits())
	}
	return nil
}

func (sc *shipContext) theShipEngineSpeedShouldBe(speed int) error {
	if sc.ship.EngineSpeed() != speed {
		return fmt.Errorf("expected engine speed %d but got %d", speed, sc.ship.EngineSpeed())
	}
	return nil
}

func (sc *shipContext) theShipShouldBeInOrbit() error {
	if !sc.ship.IsInOrbit() {
		return fmt.Errorf("expected ship to be in orbit but status is %s", sc.ship.NavStatus())
	}
	return nil
}

// Navigation State Machine Steps

func (sc *shipContext) aDockedShipAt(location string) error {
	waypoint := sc.getOrCreateWaypoint(location, 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusDocked,)
	return sc.err
}

func (sc *shipContext) aShipInOrbitAt(location string) error {
	waypoint := sc.getOrCreateWaypoint(location, 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return sc.err
}

func (sc *shipContext) aShipInOrbitAtWithUnitsOfFuel(location string, fuelUnits int) error {
	waypoint := sc.getOrCreateWaypoint(location, 0, 0)
	fuel, _ := shared.NewFuel(fuelUnits, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return sc.err
}

func (sc *shipContext) aShipInTransitTo(destination string) error {
	waypoint := sc.getOrCreateWaypoint(destination, 100, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInTransit,)
	return sc.err
}

func (sc *shipContext) theShipDeparts() error {
	_, sc.err = sc.ship.EnsureInOrbit()
	return nil
}

func (sc *shipContext) iAttemptToDepartTheShip() error {
	_, sc.err = sc.ship.EnsureInOrbit()
	sharedErr = sc.err
	return nil
}

func (sc *shipContext) theShipShouldNotBeDocked() error {
	if sc.ship.IsDocked() {
		return fmt.Errorf("expected ship not to be docked but it is")
	}
	return nil
}

func (sc *shipContext) theShipDocks() error {
	_, sc.err = sc.ship.EnsureDocked()
	return nil
}

func (sc *shipContext) iAttemptToDockTheShip() error {
	_, sc.err = sc.ship.EnsureDocked()
	sharedErr = sc.err
	return nil
}

func (sc *shipContext) theShipShouldBeDocked() error {
	if !sc.ship.IsDocked() {
		return fmt.Errorf("expected ship to be docked but status is %s", sc.ship.NavStatus())
	}
	return nil
}

func (sc *shipContext) theShipShouldNotBeInOrbit() error {
	if sc.ship.IsInOrbit() {
		return fmt.Errorf("expected ship not to be in orbit but it is")
	}
	return nil
}

func (sc *shipContext) theShipStartsTransitTo(destination string) error {
	destWaypoint := sc.getOrCreateWaypoint(destination, 100, 0)
	sc.err = sc.ship.StartTransit(destWaypoint)
	return nil
}

func (sc *shipContext) iAttemptToStartTransitTo(destination string) error {
	destWaypoint := sc.getOrCreateWaypoint(destination, 100, 0)
	sc.err = sc.ship.StartTransit(destWaypoint)
	sharedErr = sc.err
	return nil
}

func (sc *shipContext) theShipShouldBeInTransit() error {
	if !sc.ship.IsInTransit() {
		return fmt.Errorf("expected ship to be in transit but status is %s", sc.ship.NavStatus())
	}
	return nil
}

func (sc *shipContext) theShipShouldNotBeInTransit() error {
	if sc.ship.IsInTransit() {
		return fmt.Errorf("expected ship not to be in transit but it is")
	}
	return nil
}

func (sc *shipContext) theShipArrives() error {
	sc.err = sc.ship.Arrive()
	return nil
}

func (sc *shipContext) iAttemptToArriveTheShip() error {
	sc.err = sc.ship.Arrive()
	sharedErr = sc.err
	return nil
}

func (sc *shipContext) iEnsureTheShipIsInOrbit() error {
	sc.stateChangeResult, sc.err = sc.ship.EnsureInOrbit()
	return nil
}

func (sc *shipContext) iAttemptToEnsureTheShipIsInOrbit() error {
	sc.stateChangeResult, sc.err = sc.ship.EnsureInOrbit()
	sharedErr = sc.err
	return nil
}

func (sc *shipContext) theStateChangeResultShouldBe(expectedStr string) error {
	expected := expectedStr == "true"
	if sc.stateChangeResult != expected {
		return fmt.Errorf("expected state change result %t but got %t", expected, sc.stateChangeResult)
	}
	return nil
}

func (sc *shipContext) iEnsureTheShipIsDocked() error {
	sc.stateChangeResult, sc.err = sc.ship.EnsureDocked()
	return nil
}

func (sc *shipContext) iAttemptToEnsureTheShipIsDocked() error {
	sc.stateChangeResult, sc.err = sc.ship.EnsureDocked()
	sharedErr = sc.err
	return nil
}

func (sc *shipContext) theOperationShouldFailWithError(expectedError string) error {
	// Check both local context error and shared error (for cross-context assertions)
	actualErr := sc.err
	if actualErr == nil {
		actualErr = sharedErr // Check shared error from other contexts (e.g., value object validation)
	}

	if actualErr == nil {
		return fmt.Errorf("expected error containing '%s' but got no error", expectedError)
	}
	if !strings.Contains(actualErr.Error(), expectedError) {
		return fmt.Errorf("expected error containing '%s' but got '%s'", expectedError, actualErr.Error())
	}
	return nil
}

// Fuel Management Steps

func (sc *shipContext) aShipWithUnitsOfFuel(units int) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(units, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	sharedShip = sc.ship // Share ship with other contexts
	return sc.err
}

func (sc *shipContext) aShipWithUnitsOfFuelAndCapacity(current, capacity int) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(current, capacity)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, capacity, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return sc.err
}

func (sc *shipContext) theShipConsumesUnitsOfFuel(units int) error {
	sc.err = sc.ship.ConsumeFuel(units)
	return nil
}

func (sc *shipContext) iAttemptToConsumeUnitsOfFuel(units int) error {
	sc.err = sc.ship.ConsumeFuel(units)
	sharedErr = sc.err
	return nil
}

func (sc *shipContext) theShipRefuelsUnits(units int) error {
	sc.err = sc.ship.Refuel(units)
	return nil
}

func (sc *shipContext) iAttemptToRefuelUnits(units int) error {
	sc.err = sc.ship.Refuel(units)
	sharedErr = sc.err
	return nil
}

func (sc *shipContext) theShipRefuelsToFull() error {
	sc.intResult, sc.err = sc.ship.RefuelToFull()
	sharedIntResult = sc.intResult
	return nil
}

func (sc *shipContext) theFuelAddedShouldBeUnits(units int) error {
	if sc.intResult != units {
		return fmt.Errorf("expected %d units added but got %d", units, sc.intResult)
	}
	return nil
}

// Navigation Calculation Steps

func (sc *shipContext) aShipAtWithCoordinatesAndUnitsOfFuel(location string, x, y float64, fuel int) error {
	waypoint := sc.getOrCreateWaypoint(location, x, y)
	fuelObj, _ := shared.NewFuel(fuel, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuelObj, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return sc.err
}

func (sc *shipContext) aWaypointAtCoordinates(symbol string, x, y float64) error {
	sc.getOrCreateWaypoint(symbol, x, y)
	return nil
}

func (sc *shipContext) iCheckIfTheShipCanNavigateTo(destination string) error {
	destWaypoint := sc.waypoints[destination]
	sc.boolResult = sc.fuelService.CanShipNavigateTo(sc.ship.Fuel().Current, sc.ship.CurrentLocation(), destWaypoint)
	sharedBoolResult = sc.boolResult
	return nil
}

func (sc *shipContext) theResultShouldBe(expectedStr string) error {
	expected := expectedStr == "true"
	if sc.boolResult != expected {
		return fmt.Errorf("expected result %t but got %t", expected, sc.boolResult)
	}
	return nil
}

func (sc *shipContext) aShipAtWithCoordinates(location string, x, y float64) error {
	waypoint := sc.getOrCreateWaypoint(location, x, y)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return sc.err
}

func (sc *shipContext) iCalculateFuelRequiredToWithMode(destination, mode string) error {
	destWaypoint := sc.waypoints[destination]
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
	}
	sc.intResult = sc.fuelService.CalculateFuelRequired(sc.ship.CurrentLocation(), destWaypoint, flightMode)
	sharedIntResult = sc.intResult
	return nil
}

func (sc *shipContext) theFuelRequiredShouldBeUnits(units int) error {
	if sc.intResult != units {
		return fmt.Errorf("expected fuel required %d but got %d", units, sc.intResult)
	}
	return nil
}

func (sc *shipContext) iCheckIfTheShipNeedsRefuelForJourneyTo(destination string) error {
	destWaypoint := sc.waypoints[destination]
	sc.boolResult = sc.fuelService.ShouldRefuelForJourney(sc.ship.Fuel(), sc.ship.CurrentLocation(), destWaypoint, 0.1)
	sharedBoolResult = sc.boolResult
	return nil
}

func (sc *shipContext) iCheckIfTheShipNeedsRefuelForJourneyToWithSafetyMargin(destination string, margin float64) error {
	destWaypoint := sc.waypoints[destination]
	sc.boolResult = sc.fuelService.ShouldRefuelForJourney(sc.ship.Fuel(), sc.ship.CurrentLocation(), destWaypoint, margin)
	sharedBoolResult = sc.boolResult
	return nil
}

func (sc *shipContext) aShipAtWithCoordinatesAndEngineSpeed(location string, x, y float64, speed int) error {
	waypoint := sc.getOrCreateWaypoint(location, x, y)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, speed, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return sc.err
}

func (sc *shipContext) iCalculateTravelTimeToWithMode(destination, mode string) error {
	destWaypoint := sc.waypoints[destination]
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
	}
	sc.intResult = sc.navigationCalc.CalculateTravelTime(sc.ship.CurrentLocation(), destWaypoint, flightMode, sc.ship.EngineSpeed())
	sharedIntResult = sc.intResult
	return nil
}

func (sc *shipContext) theTravelTimeShouldBeSeconds(seconds int) error {
	if sc.intResult != seconds {
		return fmt.Errorf("expected travel time %d but got %d", seconds, sc.intResult)
	}
	return nil
}

func (sc *shipContext) aShipWithUnitsOfFuelAtDistance(fuel int, distance float64) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)

	// Set fuel capacity to a large value to test fuel-based selection logic
	// Capacity should be large enough that percentage isn't the limiting factor
	capacity := 400
	if fuel > capacity {
		capacity = fuel * 2
	}

	fuelObj, err := shared.NewFuel(fuel, capacity)
	if err != nil {
		return fmt.Errorf("failed to create fuel: %w", err)
	}

	cargo, err := shared.NewCargo(40, 0, []*shared.CargoItem{})
	if err != nil {
		return fmt.Errorf("failed to create cargo: %w", err)
	}

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuelObj, capacity, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return sc.err
}

func (sc *shipContext) iSelectOptimalFlightModeForDistance(distance float64) error {
	sc.flightMode = sc.fuelService.SelectOptimalFlightMode(sc.ship.Fuel().Current, distance, navigation.DefaultFuelSafetyMargin)
	return nil
}

func (sc *shipContext) theSelectedModeShouldBe(mode string) error {
	expectedMode := mode
	actualMode := sc.flightMode.Name()
	if actualMode != expectedMode {
		return fmt.Errorf("expected mode '%s' but got '%s'", expectedMode, actualMode)
	}
	return nil
}

// Cargo Management Steps

func (sc *shipContext) aShipWithCargoCapacityAndCargoUnits(capacity, units int) error {
	waypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, err := sc.createCargoWithUnits(capacity, units)
	if err != nil {
		return err
	}

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, capacity, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	sharedShip = sc.ship // Share ship with other contexts
	return sc.err
}

func (sc *shipContext) iCheckIfTheShipHasCargoSpaceForUnits(units int) error {
	sc.boolResult = sc.ship.HasCargoSpace(units)
	sharedBoolResult = sc.boolResult
	return nil
}

func (sc *shipContext) iCheckAvailableCargoSpace() error {
	sc.intResult = sc.ship.AvailableCargoSpace()
	sharedIntResult = sc.intResult
	return nil
}

func (sc *shipContext) theAvailableSpaceShouldBeUnits(units int) error {
	if sc.intResult != units {
		return fmt.Errorf("expected available space %d but got %d", units, sc.intResult)
	}
	return nil
}

func (sc *shipContext) iCheckIfCargoIsEmpty() error {
	sc.boolResult = sc.ship.IsCargoEmpty()
	sharedBoolResult = sc.boolResult
	return nil
}

func (sc *shipContext) iCheckIfCargoIsFull() error {
	sc.boolResult = sc.ship.IsCargoFull()
	sharedBoolResult = sc.boolResult
	return nil
}

// State Query Steps

func (sc *shipContext) aShipAt(location string) error {
	waypoint := sc.getOrCreateWaypoint(location, 0, 0)
	fuel, _ := shared.NewFuel(100, 100)
	cargo, _ := shared.NewCargo(40, 0, []*shared.CargoItem{})

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	return sc.err
}

func (sc *shipContext) iCheckIfTheShipIsDocked() error {
	sc.boolResult = sc.ship.IsDocked()
	sharedBoolResult = sc.boolResult
	return nil
}

func (sc *shipContext) iCheckIfTheShipIsInOrbit() error {
	sc.boolResult = sc.ship.IsInOrbit()
	sharedBoolResult = sc.boolResult
	return nil
}

func (sc *shipContext) iCheckIfTheShipIsInTransit() error {
	sc.boolResult = sc.ship.IsInTransit()
	sharedBoolResult = sc.boolResult
	return nil
}

func (sc *shipContext) iCheckIfTheShipIsAtLocation(location string) error {
	waypoint := sc.waypoints[location]
	if waypoint == nil {
		waypoint = sc.getOrCreateWaypoint(location, 0, 0)
	}
	sc.boolResult = sc.navigationCalc.IsAtLocation(sc.ship.CurrentLocation(), waypoint)
	sharedBoolResult = sc.boolResult
	return nil
}

// Refueling and Flight Mode Decision Steps

func (sc *shipContext) aShipAtWithUnitsOfFuelAndCapacity(location string, currentFuel, fuelCapacity int) error {
	waypoint := sc.getOrCreateWaypoint(location, 0, 0)
	fuel, err := shared.NewFuel(currentFuel, fuelCapacity)
	if err != nil {
		return err
	}
	cargo, err := sc.createCargoWithUnits(40, 0)
	if err != nil {
		return err
	}

	sc.ship, sc.err = navigation.NewShip(
		"SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, fuelCapacity, 40, cargo, 30, "FRAME_EXPLORER", "", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	sharedShip = sc.ship // Update shared ship for cross-context steps
	return sc.err
}

func (sc *shipContext) waypointHasTraitAndFuelAvailable(waypointSymbol, trait string) error {
	waypoint := sc.getOrCreateWaypoint(waypointSymbol, 0, 0)
	waypoint.Traits = []string{trait}
	waypoint.HasFuel = true
	sc.waypoints[waypointSymbol] = waypoint
	sharedWaypointMap[waypointSymbol] = waypoint
	return nil
}

func (sc *shipContext) waypointHasNoFuelAvailable(waypointSymbol string) error {
	waypoint := sc.getOrCreateWaypoint(waypointSymbol, 0, 0)
	waypoint.HasFuel = false
	sc.waypoints[waypointSymbol] = waypoint
	sharedWaypointMap[waypointSymbol] = waypoint
	return nil
}

func (sc *shipContext) iCheckIfShipShouldRefuelOpportunisticallyAt(waypointSymbol string, threshold float64) error {
	waypoint := sc.waypoints[waypointSymbol]
	if waypoint == nil {
		waypoint = sc.getOrCreateWaypoint(waypointSymbol, 0, 0)
	}

	// Check if waypoint has fuel available and ship fuel is below threshold
	fuelPercentage := float64(sc.ship.Fuel().Current) / float64(sc.ship.Fuel().Capacity)
	hasFuel := waypoint.HasFuel || (len(waypoint.Traits) > 0 && contains(waypoint.Traits, "MARKETPLACE"))

	sc.boolResult = hasFuel && (fuelPercentage < threshold)
	sharedBoolResult = sc.boolResult
	return nil
}

// RouteSegment context for drift mode prevention tests
var testRouteSegment *navigation.RouteSegment

func (sc *shipContext) aRouteSegmentRequiringUnitsOfFuelInDRIFTMode(fuelRequired int) error {
	fromWaypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	toWaypoint := sc.getOrCreateWaypoint("X1-B2", 100, 0)

	testRouteSegment = navigation.NewRouteSegment(
		fromWaypoint,
		toWaypoint,
		100.0,
		fuelRequired,
		300,
		shared.FlightModeDrift,
		false,
	)
	return nil
}

func (sc *shipContext) aRouteSegmentRequiringUnitsOfFuelInCRUISEMode(fuelRequired int) error {
	fromWaypoint := sc.getOrCreateWaypoint("X1-A1", 0, 0)
	toWaypoint := sc.getOrCreateWaypoint("X1-B2", 100, 0)

	testRouteSegment = navigation.NewRouteSegment(
		fromWaypoint,
		toWaypoint,
		100.0,
		fuelRequired,
		300,
		shared.FlightModeCruise,
		false,
	)
	return nil
}

func (sc *shipContext) iCheckIfShipShouldPreventDriftModeWithThreshold(threshold float64) error {
	if testRouteSegment == nil {
		return fmt.Errorf("no route segment defined")
	}

	// Check if ship should prevent DRIFT mode based on fuel threshold
	// Prevent DRIFT if:
	// 1. Segment uses DRIFT mode
	// 2. Current fuel percentage is below threshold
	fuelPercentage := float64(sc.ship.Fuel().Current) / float64(sc.ship.Fuel().Capacity)
	isDriftMode := testRouteSegment.FlightMode == shared.FlightModeDrift

	sc.boolResult = isDriftMode && (fuelPercentage < threshold)
	sharedBoolResult = sc.boolResult
	return nil
}

// Helper function to check if slice contains string
func contains(slice []string, str string) bool {
	for _, s := range slice {
		if s == str {
			return true
		}
	}
	return false
}

// InitializeShipScenario registers all ship-related step definitions
func InitializeShipScenario(ctx *godog.ScenarioContext) {
	sc := &shipContext{}

	ctx.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		sc.reset()
		return ctx, nil
	})

	ctx.After(func(ctx context.Context, sc *godog.Scenario, err error) (context.Context, error) {
		return ctx, nil
	})

	// Ship initialization
	ctx.Step(`^I create a ship with symbol "([^"]*)", player (\d+), at "([^"]*)", fuel (\d+)/(\d+), cargo (\d+)/(\d+), speed (\d+), status "([^"]*)"$`,
		sc.iCreateAShipWithSymbolPlayerAtFuelCargoSpeedStatus)
	ctx.Step(`^I attempt to create a ship with empty ship_symbol$`, sc.iAttemptToCreateAShipWithEmptyShipSymbol)
	ctx.Step(`^I attempt to create a ship with player_id (\d+)$`, sc.iAttemptToCreateAShipWithPlayerID)
	ctx.Step(`^I attempt to create a ship with player_id (-?\d+)$`, sc.iAttemptToCreateAShipWithPlayerID)
	ctx.Step(`^I attempt to create a ship with fuel_capacity (-?\d+)$`, sc.iAttemptToCreateAShipWithFuelCapacity)
	ctx.Step(`^I attempt to create a ship with fuel object capacity (\d+) but fuel_capacity parameter (\d+)$`,
		sc.iAttemptToCreateAShipWithFuelObjectCapacityButFuelCapacityParameter)
	ctx.Step(`^I attempt to create a ship with cargo_capacity (-?\d+)$`, sc.iAttemptToCreateAShipWithCargoCapacity)
	ctx.Step(`^I attempt to create a ship with cargo_units (-?\d+)$`, sc.iAttemptToCreateAShipWithCargoUnits)
	ctx.Step(`^I attempt to create a ship with cargo_capacity (\d+) and cargo_units (\d+)$`,
		sc.iAttemptToCreateAShipWithCargoCapacityAndCargoUnits)
	ctx.Step(`^I attempt to create a ship with engine_speed (-?\d+)$`, sc.iAttemptToCreateAShipWithEngineSpeed)
	ctx.Step(`^I attempt to create a ship with nav_status "([^"]*)"$`, sc.iAttemptToCreateAShipWithNavStatus)
	ctx.Step(`^ship creation should fail with error "([^"]*)"$`, sc.shipCreationShouldFailWithError)

	// Ship properties
	ctx.Step(`^the ship should have symbol "([^"]*)"$`, sc.theShipShouldHaveSymbol)
	ctx.Step(`^the ship should have player_id (\d+)$`, sc.theShipShouldHavePlayerID)
	ctx.Step(`^the ship should be at location "([^"]*)"$`, sc.theShipShouldBeAtLocation)
	ctx.Step(`^the ship should have (\d+) units of fuel$`, sc.theShipShouldHaveUnitsOfFuel)
	ctx.Step(`^the ship fuel capacity should be (\d+)$`, sc.theShipFuelCapacityShouldBe)
	ctx.Step(`^the ship cargo capacity should be (\d+)$`, sc.theShipCargoCapacityShouldBe)
	ctx.Step(`^the ship cargo units should be (\d+)$`, sc.theShipCargoUnitsShouldBe)
	ctx.Step(`^the ship engine speed should be (\d+)$`, sc.theShipEngineSpeedShouldBe)
	ctx.Step(`^the ship should be in orbit$`, sc.theShipShouldBeInOrbit)

	// Navigation state machine
	ctx.Step(`^a docked ship at "([^"]*)"$`, sc.aDockedShipAt)
	ctx.Step(`^a ship in orbit at "([^"]*)"$`, sc.aShipInOrbitAt)
	ctx.Step(`^a ship in orbit at "([^"]*)" with (\d+) units of fuel$`, sc.aShipInOrbitAtWithUnitsOfFuel)
	ctx.Step(`^a ship in transit to "([^"]*)"$`, sc.aShipInTransitTo)
	ctx.Step(`^the ship departs$`, sc.theShipDeparts)
	ctx.Step(`^I attempt to depart the ship$`, sc.iAttemptToDepartTheShip)
	ctx.Step(`^the ship should not be docked$`, sc.theShipShouldNotBeDocked)
	ctx.Step(`^the ship docks$`, sc.theShipDocks)
	ctx.Step(`^I attempt to dock the ship$`, sc.iAttemptToDockTheShip)
	ctx.Step(`^the ship should be docked$`, sc.theShipShouldBeDocked)
	ctx.Step(`^the ship should not be in orbit$`, sc.theShipShouldNotBeInOrbit)
	ctx.Step(`^the ship starts transit to "([^"]*)"$`, sc.theShipStartsTransitTo)
	ctx.Step(`^I attempt to start transit to "([^"]*)"$`, sc.iAttemptToStartTransitTo)
	ctx.Step(`^the ship should be in transit$`, sc.theShipShouldBeInTransit)
	ctx.Step(`^the ship should not be in transit$`, sc.theShipShouldNotBeInTransit)
	ctx.Step(`^the ship arrives$`, sc.theShipArrives)
	ctx.Step(`^I attempt to arrive the ship$`, sc.iAttemptToArriveTheShip)
	ctx.Step(`^I ensure the ship is in orbit$`, sc.iEnsureTheShipIsInOrbit)
	ctx.Step(`^I attempt to ensure the ship is in orbit$`, sc.iAttemptToEnsureTheShipIsInOrbit)
	ctx.Step(`^the state change result should be (true|false)$`, sc.theStateChangeResultShouldBe)
	ctx.Step(`^I ensure the ship is docked$`, sc.iEnsureTheShipIsDocked)
	ctx.Step(`^I attempt to ensure the ship is docked$`, sc.iAttemptToEnsureTheShipIsDocked)
	ctx.Step(`^the operation should fail with error "([^"]*)"$`, sc.theOperationShouldFailWithError)

	// Fuel management
	ctx.Step(`^a ship with (\d+) units of fuel$`, sc.aShipWithUnitsOfFuel)
	ctx.Step(`^a ship with (\d+) units of fuel and capacity (\d+)$`, sc.aShipWithUnitsOfFuelAndCapacity)
	ctx.Step(`^the ship consumes (\d+) units of fuel$`, sc.theShipConsumesUnitsOfFuel)
	ctx.Step(`^I attempt to consume (-?\d+) units of fuel$`, sc.iAttemptToConsumeUnitsOfFuel)
	ctx.Step(`^the ship refuels (\d+) units$`, sc.theShipRefuelsUnits)
	ctx.Step(`^I attempt to refuel (-?\d+) units$`, sc.iAttemptToRefuelUnits)
	ctx.Step(`^the ship refuels to full$`, sc.theShipRefuelsToFull)
	ctx.Step(`^the fuel added should be (\d+) units$`, sc.theFuelAddedShouldBeUnits)

	// Navigation calculations
	ctx.Step(`^a ship at "([^"]*)" with coordinates \(([^,]+), ([^)]+)\) and (\d+) units of fuel$`,
		sc.aShipAtWithCoordinatesAndUnitsOfFuel)
	ctx.Step(`^a waypoint "([^"]*)" at coordinates \(([^,]+), ([^)]+)\)$`, sc.aWaypointAtCoordinates)
	ctx.Step(`^I check if the ship can navigate to "([^"]*)"$`, sc.iCheckIfTheShipCanNavigateTo)
	ctx.Step(`^the result should be (true|false)$`, sc.theResultShouldBe)
	ctx.Step(`^a ship at "([^"]*)" with coordinates \(([^,]+), ([^)]+)\)$`, sc.aShipAtWithCoordinates)
	ctx.Step(`^I calculate fuel required to "([^"]*)" with ([A-Z]+) mode$`, sc.iCalculateFuelRequiredToWithMode)
	ctx.Step(`^the fuel required should be (\d+) units$`, sc.theFuelRequiredShouldBeUnits)
	ctx.Step(`^I check if the ship needs refuel for journey to "([^"]*)"$`, sc.iCheckIfTheShipNeedsRefuelForJourneyTo)
	ctx.Step(`^I check if the ship needs refuel for journey to "([^"]*)" with safety margin ([0-9.]+)$`,
		sc.iCheckIfTheShipNeedsRefuelForJourneyToWithSafetyMargin)
	ctx.Step(`^a ship at "([^"]*)" with coordinates \(([^,]+), ([^)]+)\) and engine speed (\d+)$`,
		sc.aShipAtWithCoordinatesAndEngineSpeed)
	ctx.Step(`^I calculate travel time to "([^"]*)" with ([A-Z]+) mode$`, sc.iCalculateTravelTimeToWithMode)
	ctx.Step(`^the travel time should be (\d+) seconds$`, sc.theTravelTimeShouldBeSeconds)
	ctx.Step(`^a ship with (\d+) units of fuel at distance ([0-9.]+)$`, sc.aShipWithUnitsOfFuelAtDistance)
	ctx.Step(`^I select optimal flight mode for distance ([0-9.]+)$`, sc.iSelectOptimalFlightModeForDistance)
	ctx.Step(`^the ship's selected mode should be ([A-Z]+)$`, sc.theSelectedModeShouldBe)

	// Cargo management
	ctx.Step(`^a ship with cargo capacity (\d+) and cargo units (\d+)$`, sc.aShipWithCargoCapacityAndCargoUnits)
	ctx.Step(`^I check if the ship has cargo space for (\d+) units$`, sc.iCheckIfTheShipHasCargoSpaceForUnits)
	ctx.Step(`^I check available cargo space$`, sc.iCheckAvailableCargoSpace)
	ctx.Step(`^the available space should be (\d+) units$`, sc.theAvailableSpaceShouldBeUnits)
	ctx.Step(`^I check if cargo is empty$`, sc.iCheckIfCargoIsEmpty)
	ctx.Step(`^I check if cargo is full$`, sc.iCheckIfCargoIsFull)

	// State queries
	ctx.Step(`^a ship at "([^"]*)"$`, sc.aShipAt)
	ctx.Step(`^I check if the ship is docked$`, sc.iCheckIfTheShipIsDocked)
	ctx.Step(`^I check if the ship is in orbit$`, sc.iCheckIfTheShipIsInOrbit)
	ctx.Step(`^I check if the ship is in transit$`, sc.iCheckIfTheShipIsInTransit)
	ctx.Step(`^I check if the ship is at location "([^"]*)"$`, sc.iCheckIfTheShipIsAtLocation)

	// Refueling and flight mode decisions
	ctx.Step(`^a ship at "([^"]*)" with (\d+) units of fuel and capacity (\d+)$`, sc.aShipAtWithUnitsOfFuelAndCapacity)
	ctx.Step(`^waypoint "([^"]*)" has trait "([^"]*)" and fuel available$`, sc.waypointHasTraitAndFuelAvailable)
	ctx.Step(`^waypoint "([^"]*)" has no fuel available$`, sc.waypointHasNoFuelAvailable)
	ctx.Step(`^I check if ship should refuel opportunistically at "([^"]*)" with threshold ([0-9.]+)$`, sc.iCheckIfShipShouldRefuelOpportunisticallyAt)
	ctx.Step(`^a route segment requiring (\d+) units of fuel in DRIFT mode$`, sc.aRouteSegmentRequiringUnitsOfFuelInDRIFTMode)
	ctx.Step(`^a route segment requiring (\d+) units of fuel in CRUISE mode$`, sc.aRouteSegmentRequiringUnitsOfFuelInCRUISEMode)
	ctx.Step(`^I check if ship should prevent drift mode with threshold ([0-9.]+)$`, sc.iCheckIfShipShouldPreventDriftModeWithThreshold)

	// Ship type detection
	ctx.Step(`^a ship with frame symbol "([^"]*)"$`, sc.aShipWithFrameSymbol)
	ctx.Step(`^a ship with role "([^"]*)"$`, sc.aShipWithRole)
	ctx.Step(`^a ship with symbol "([^"]*)" with frame "([^"]*)" and role "([^"]*)"$`, sc.aShipWithSymbolWithFrameAndRole)
	ctx.Step(`^I check if the ship is a probe$`, sc.iCheckIfTheShipIsAProbe)
	ctx.Step(`^I check if the ship is a drone$`, sc.iCheckIfTheShipIsADrone)
	ctx.Step(`^I check if the ship is a scout type$`, sc.iCheckIfTheShipIsAScoutType)

	// Clone at location
	ctx.Step(`^I clone the ship at location "([^"]*)" with (\d+) units of fuel$`, sc.iCloneTheShipAtLocationWithUnitsOfFuel)
	ctx.Step(`^the cloned ship should be at location "([^"]*)"$`, sc.theClonedShipShouldBeAtLocation)
	ctx.Step(`^the cloned ship should have (\d+) units of fuel$`, sc.theClonedShipShouldHaveUnitsOfFuel)
	ctx.Step(`^the cloned ship should be in orbit$`, sc.theClonedShipShouldBeInOrbit)
	ctx.Step(`^the cloned ship should have same ship symbol as original$`, sc.theClonedShipShouldHaveSameShipSymbolAsOriginal)
	ctx.Step(`^the cloned ship should have same cargo capacity as original$`, sc.theClonedShipShouldHaveSameCargoCapacityAsOriginal)
	ctx.Step(`^the cloned ship should have ship symbol "([^"]*)"$`, sc.theClonedShipShouldHaveShipSymbol)
	ctx.Step(`^the cloned ship should have frame symbol "([^"]*)"$`, sc.theClonedShipShouldHaveFrameSymbol)
	ctx.Step(`^the cloned ship should have role "([^"]*)"$`, sc.theClonedShipShouldHaveRole)

	// Ship helper methods
	ctx.Step(`^I get the ship string representation$`, sc.iGetTheShipStringRepresentation)
	ctx.Step(`^the ship string should contain "([^"]*)"$`, sc.theShipStringShouldContain)
	ctx.Step(`^a ship at waypoint "([^"]*)" with empty cargo$`, sc.aShipAtWaypointWithEmptyCargo)
	ctx.Step(`^cargo should be empty$`, sc.cargoShouldBeEmpty)
	ctx.Step(`^cargo should not be empty$`, sc.cargoShouldNotBeEmpty)
	ctx.Step(`^cargo should be full$`, sc.cargoShouldBeFull)
	ctx.Step(`^cargo should not be full$`, sc.cargoShouldNotBeFull)
	ctx.Step(`^a ship at waypoint "([^"]*)" with (\d+) units of "([^"]*)" and capacity (\d+)$`, sc.aShipAtWaypointWithUnitsOfAndCapacity)
	ctx.Step(`^I check if ship has cargo space for (\d+) units$`, sc.iCheckIfShipHasCargoSpaceForUnits)
	ctx.Step(`^ship should have cargo space$`, sc.shipShouldHaveCargoSpace)
	ctx.Step(`^ship should not have cargo space$`, sc.shipShouldNotHaveCargoSpace)
	ctx.Step(`^available cargo space should be (\d+)$`, sc.availableCargoSpaceShouldBe)
}

// ============================================================================
// Ship Type Detection Steps
// ============================================================================

func (sc *shipContext) aShipWithFrameSymbol(frameSymbol string) error {
	playerID := shared.MustNewPlayerID(1)
	ship, err := navigation.NewShip(
		"TEST-SHIP",
		playerID,
		&shared.Waypoint{Symbol: "X1-A1", X: 0, Y: 0, Type: "PLANET"},
		&shared.Fuel{Current: 100, Capacity: 100},
		100,
		40,
		&shared.Cargo{Capacity: 40, Units: 0, Inventory: []*shared.CargoItem{}},
		10,
		frameSymbol,                 // Frame symbol
		"COMMAND",                   // Default role
		[]*navigation.ShipModule{}, // modules

		navigation.NavStatusInOrbit,// Nav status
	)
	if err != nil {
		return err
	}
	sc.ship = ship
	return nil
}

func (sc *shipContext) aShipWithRole(role string) error {
	playerID := shared.MustNewPlayerID(1)
	ship, err := navigation.NewShip(
		"TEST-SHIP",
		playerID,
		&shared.Waypoint{Symbol: "X1-A1", X: 0, Y: 0, Type: "PLANET"},
		&shared.Fuel{Current: 100, Capacity: 100},
		100,
		40,
		&shared.Cargo{Capacity: 40, Units: 0, Inventory: []*shared.CargoItem{}},
		10,
		"FRAME_FRIGATE",             // Default frame
		role,                        // Role
		[]*navigation.ShipModule{}, // modules

		navigation.NavStatusInOrbit,// Nav status
	)
	if err != nil {
		return err
	}
	sc.ship = ship
	return nil
}

func (sc *shipContext) aShipWithSymbolWithFrameAndRole(symbol, frameSymbol, role string) error {
	playerID := shared.MustNewPlayerID(1)
	ship, err := navigation.NewShip(
		symbol,
		playerID,
		&shared.Waypoint{Symbol: "X1-A1", X: 0, Y: 0, Type: "PLANET"},
		&shared.Fuel{Current: 100, Capacity: 100},
		100,
		40,
		&shared.Cargo{Capacity: 40, Units: 0, Inventory: []*shared.CargoItem{}},
		10,
		frameSymbol,
		role,
		[]*navigation.ShipModule{}, // modules

		navigation.NavStatusInOrbit,// Nav status
	)
	if err != nil {
		return err
	}
	sc.ship = ship
	return nil
}

func (sc *shipContext) iCheckIfTheShipIsAProbe() error {
	sc.boolResult = sc.ship.IsProbe()
	sharedBoolResult = sc.boolResult
	return nil
}

func (sc *shipContext) iCheckIfTheShipIsADrone() error {
	sc.boolResult = sc.ship.IsDrone()
	sharedBoolResult = sc.boolResult
	return nil
}

func (sc *shipContext) iCheckIfTheShipIsAScoutType() error {
	sc.boolResult = sc.ship.IsScoutType()
	sharedBoolResult = sc.boolResult
	return nil
}

// ============================================================================
// Clone At Location Steps
// ============================================================================

func (sc *shipContext) iCloneTheShipAtLocationWithUnitsOfFuel(waypointSymbol string, fuel int) error {
	waypoint, exists := sc.waypoints[waypointSymbol]
	if !exists {
		return fmt.Errorf("waypoint %s not found", waypointSymbol)
	}
	sc.clonedShip = sc.ship.CloneAtLocation(waypoint, fuel)
	return nil
}

func (sc *shipContext) theClonedShipShouldBeAtLocation(expectedLocation string) error {
	if sc.clonedShip == nil {
		return fmt.Errorf("cloned ship is nil")
	}
	actual := sc.clonedShip.CurrentLocation().Symbol
	if actual != expectedLocation {
		return fmt.Errorf("expected cloned ship at %s, got %s", expectedLocation, actual)
	}
	return nil
}

func (sc *shipContext) theClonedShipShouldHaveUnitsOfFuel(expectedFuel int) error {
	if sc.clonedShip == nil {
		return fmt.Errorf("cloned ship is nil")
	}
	actual := sc.clonedShip.Fuel().Current
	if actual != expectedFuel {
		return fmt.Errorf("expected cloned ship to have %d fuel, got %d", expectedFuel, actual)
	}
	return nil
}

func (sc *shipContext) theClonedShipShouldBeInOrbit() error {
	if sc.clonedShip == nil {
		return fmt.Errorf("cloned ship is nil")
	}
	if !sc.clonedShip.IsInOrbit() {
		return fmt.Errorf("expected cloned ship to be in orbit, got nav status %v", sc.clonedShip.NavStatus())
	}
	return nil
}

func (sc *shipContext) theClonedShipShouldHaveSameShipSymbolAsOriginal() error {
	if sc.clonedShip == nil {
		return fmt.Errorf("cloned ship is nil")
	}
	if sc.clonedShip.ShipSymbol() != sc.ship.ShipSymbol() {
		return fmt.Errorf("expected cloned ship symbol %s, got %s",
			sc.ship.ShipSymbol(), sc.clonedShip.ShipSymbol())
	}
	return nil
}

func (sc *shipContext) theClonedShipShouldHaveSameCargoCapacityAsOriginal() error {
	if sc.clonedShip == nil {
		return fmt.Errorf("cloned ship is nil")
	}
	if sc.clonedShip.CargoCapacity() != sc.ship.CargoCapacity() {
		return fmt.Errorf("expected cloned ship cargo capacity %d, got %d",
			sc.ship.CargoCapacity(), sc.clonedShip.CargoCapacity())
	}
	return nil
}

func (sc *shipContext) theClonedShipShouldHaveShipSymbol(expected string) error {
	if sc.clonedShip == nil {
		return fmt.Errorf("cloned ship is nil")
	}
	actual := sc.clonedShip.ShipSymbol()
	if actual != expected {
		return fmt.Errorf("expected cloned ship symbol %s, got %s", expected, actual)
	}
	return nil
}

// Ship helper method implementations

func (sc *shipContext) iGetTheShipStringRepresentation() error {
	if sc.ship == nil {
		return fmt.Errorf("ship is nil")
	}
	sc.stringResult = sc.ship.String()
	return nil
}

func (sc *shipContext) theShipStringShouldContain(expected string) error {
	if !strings.Contains(sc.stringResult, expected) {
		return fmt.Errorf("expected ship string to contain '%s', but got '%s'", expected, sc.stringResult)
	}
	return nil
}

func (sc *shipContext) aShipAtWaypointWithEmptyCargo(waypointSymbol string) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	if err != nil {
		return err
	}

	fuel, err := shared.NewFuel(50, 100)
	if err != nil {
		return err
	}

	cargo, err := shared.NewCargo(100, 0, []*shared.CargoItem{})
	if err != nil {
		return err
	}

	sc.ship, err = navigation.NewShip("SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, 100, cargo, 10, "FRAME_MINER", "EXCAVATOR", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	sharedShip = sc.ship  // Update shared ship for cross-context steps
	return err
}

func (sc *shipContext) cargoShouldBeEmpty() error {
	if !sharedBoolResult {
		return fmt.Errorf("expected cargo to be empty, but it was not")
	}
	return nil
}

func (sc *shipContext) cargoShouldNotBeEmpty() error {
	if sharedBoolResult {
		return fmt.Errorf("expected cargo to not be empty, but it was")
	}
	return nil
}

func (sc *shipContext) cargoShouldBeFull() error {
	if !sharedBoolResult {
		return fmt.Errorf("expected cargo to be full, but it was not")
	}
	return nil
}

func (sc *shipContext) cargoShouldNotBeFull() error {
	if sharedBoolResult {
		return fmt.Errorf("expected cargo to not be full, but it was")
	}
	return nil
}

func (sc *shipContext) aShipAtWaypointWithUnitsOfAndCapacity(waypointSymbol string, units int, itemSymbol string, capacity int) error {
	waypoint, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	if err != nil {
		return err
	}

	fuel, err := shared.NewFuel(50, 100)
	if err != nil {
		return err
	}

	item, err := shared.NewCargoItem(itemSymbol, itemSymbol, "Test item", units)
	if err != nil {
		return err
	}

	cargo, err := shared.NewCargo(capacity, units, []*shared.CargoItem{item})
	if err != nil {
		return err
	}

	sc.ship, err = navigation.NewShip("SHIP-1", shared.MustNewPlayerID(1), waypoint, fuel, 100, capacity, cargo, 10, "FRAME_MINER", "EXCAVATOR", []*navigation.ShipModule{}, // modules
 navigation.NavStatusInOrbit,)
	sharedShip = sc.ship  // Update shared ship for cross-context steps
	return err
}

func (sc *shipContext) iCheckIfShipHasCargoSpaceForUnits(requiredUnits int) error {
	if sc.ship == nil {
		return fmt.Errorf("ship is nil")
	}
	sc.boolResult = sc.ship.HasCargoSpace(requiredUnits)
	return nil
}

func (sc *shipContext) shipShouldHaveCargoSpace() error {
	if !sc.boolResult {
		return fmt.Errorf("expected ship to have cargo space, but it did not")
	}
	return nil
}

func (sc *shipContext) shipShouldNotHaveCargoSpace() error {
	if sc.boolResult {
		return fmt.Errorf("expected ship to not have cargo space, but it did")
	}
	return nil
}

func (sc *shipContext) theClonedShipShouldHaveFrameSymbol(expected string) error {
	if sc.clonedShip == nil {
		return fmt.Errorf("cloned ship is nil")
	}
	actual := sc.clonedShip.FrameSymbol()
	if actual != expected {
		return fmt.Errorf("expected cloned ship frame symbol %s, got %s", expected, actual)
	}
	return nil
}

func (sc *shipContext) theClonedShipShouldHaveRole(expected string) error {
	if sc.clonedShip == nil {
		return fmt.Errorf("cloned ship is nil")
	}
	actual := sc.clonedShip.Role()
	if actual != expected {
		return fmt.Errorf("expected cloned ship role %s, got %s", expected, actual)
	}
	return nil
}

func (sc *shipContext) availableCargoSpaceShouldBe(expected int) error {
	if sc.intResult != expected {
		return fmt.Errorf("expected available cargo space to be %d, got %d", expected, sc.intResult)
	}
	return nil
}

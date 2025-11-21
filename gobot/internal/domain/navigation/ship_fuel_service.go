package navigation

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ShipFuelService provides fuel management calculations and decisions for ships
// This service contains stateless fuel-related logic extracted from the Ship entity
// to improve separation of concerns and testability.
type ShipFuelService struct{}

// NewShipFuelService creates a new fuel service instance
func NewShipFuelService() *ShipFuelService {
	return &ShipFuelService{}
}

// CalculateFuelRequired calculates fuel required for a trip between two waypoints
// using the specified flight mode.
//
// Parameters:
//   - from: Starting waypoint
//   - to: Destination waypoint
//   - mode: Flight mode to use
//
// Returns:
//   - Fuel units required for the journey
func (s *ShipFuelService) CalculateFuelRequired(
	from *shared.Waypoint,
	to *shared.Waypoint,
	mode shared.FlightMode,
) int {
	distance := from.DistanceTo(to)
	return mode.FuelCost(distance)
}

// CanShipNavigateTo checks if a ship has enough fuel to navigate to destination
// using the most fuel-efficient mode (DRIFT).
//
// Parameters:
//   - currentFuel: Ship's current fuel
//   - from: Starting waypoint
//   - to: Destination waypoint
//
// Returns:
//   - true if ship has sufficient fuel
func (s *ShipFuelService) CanShipNavigateTo(
	currentFuel int,
	from *shared.Waypoint,
	to *shared.Waypoint,
) bool {
	distance := from.DistanceTo(to)
	minFuelRequired := shared.FlightModeDrift.FuelCost(distance)
	return currentFuel >= minFuelRequired
}

// ShouldRefuelForJourney determines if a ship needs refueling before a journey
// based on fuel requirements and a safety margin.
//
// Parameters:
//   - fuel: Ship's current fuel state
//   - from: Starting waypoint
//   - to: Destination waypoint
//   - safetyMargin: Safety margin multiplier (e.g., 0.1 = 10% extra)
//
// Returns:
//   - true if refueling is needed
func (s *ShipFuelService) ShouldRefuelForJourney(
	fuel *shared.Fuel,
	from *shared.Waypoint,
	to *shared.Waypoint,
	safetyMargin float64,
) bool {
	distance := from.DistanceTo(to)
	fuelRequired := shared.FlightModeCruise.FuelCost(distance)
	return !fuel.CanTravel(fuelRequired, safetyMargin)
}

// SelectOptimalFlightMode selects the best flight mode for a journey based on
// available fuel. Prioritizes faster modes when fuel permits, with a safety margin.
//
// Parameters:
//   - currentFuel: Ship's current fuel level
//   - distance: Distance to destination
//   - safetyMargin: Minimum fuel to keep as reserve
//
// Returns:
//   - Optimal flight mode (BURN, CRUISE, or DRIFT)
func (s *ShipFuelService) SelectOptimalFlightMode(
	currentFuel int,
	distance float64,
	safetyMargin int,
) shared.FlightMode {
	cruiseCost := shared.FlightModeCruise.FuelCost(distance)
	return shared.SelectOptimalFlightMode(currentFuel, cruiseCost, safetyMargin)
}

// ShouldRefuelOpportunistically determines if a ship should refuel at a waypoint
// even if not originally planned (defense-in-depth safety check).
//
// Returns true if:
//   - Waypoint has fuel available
//   - Ship's fuel is below safety threshold
//   - Ship has fuel capacity > 0
//
// Parameters:
//   - fuel: Ship's current fuel state
//   - fuelCapacity: Ship's maximum fuel capacity
//   - waypoint: Current waypoint
//   - safetyThreshold: Fuel percentage threshold (e.g., 0.9 = 90%)
//
// Returns:
//   - true if opportunistic refueling is recommended
func (s *ShipFuelService) ShouldRefuelOpportunistically(
	fuel *shared.Fuel,
	fuelCapacity int,
	waypoint *shared.Waypoint,
	safetyThreshold float64,
) bool {
	if fuelCapacity == 0 {
		return false
	}

	if !waypoint.HasFuel {
		return false
	}

	fuelPercentage := float64(fuel.Current) / float64(fuelCapacity)
	return fuelPercentage < safetyThreshold
}

// CalculateFuelNeededToFull calculates how much fuel is needed to fill ship to capacity
//
// Parameters:
//   - currentFuel: Ship's current fuel level
//   - fuelCapacity: Ship's maximum fuel capacity
//
// Returns:
//   - Fuel units needed to reach full capacity
func (s *ShipFuelService) CalculateFuelNeededToFull(currentFuel int, fuelCapacity int) int {
	fuelNeeded := fuelCapacity - currentFuel
	if fuelNeeded < 0 {
		return 0
	}
	return fuelNeeded
}

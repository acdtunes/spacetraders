package navigation

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ShipFuelService provides fuel management calculations and decisions for ships.
//
// This service contains stateless fuel-related logic extracted from the Ship entity
// to improve separation of concerns and testability. All fuel-related decisions
// should go through this service to ensure consistency.
//
// # Fuel Safety Policies
//
// The service implements several safety policies to prevent fuel emergencies:
//
// 1. Safety Thresholds:
//   - Conservative: 90% (default) - Maintain high fuel levels
//   - Balanced: 70% - Moderate fuel reserves
//   - Minimal: 10-20% - Only refuel when necessary
//   - Safety margins are expressed as percentages (0.0 to 1.0)
//
// 2. Refueling Strategies:
//   - Opportunistic: Refuel at fuel stations when below threshold
//   - Preventive: Top off before a leg the tank cannot cover
//   - Journey-based: Ensure sufficient fuel for planned routes
//
// 3. Flight Mode Selection:
//   - Prioritizes BURN mode when fuel permits (fastest)
//   - Falls back to CRUISE when fuel is moderate
//   - Never degrades below CRUISE: a leg the tank cannot cover is refuelled
//   - Maintains safety margin to prevent running out mid-flight
//
// 4. Fuel Percentage Calculations:
//   - All percentage calculations MUST use Fuel.Percentage()
//   - This ensures consistency across the codebase
//   - Returns percentage as 0-100 (not 0.0-1.0)
//
// # Usage Examples
//
//	service := NewShipFuelService()
//
//	// Check if ship can reach destination
//	canNavigate := service.CanShipNavigateTo(currentFuel, from, to)
//
//	// Determine if refueling needed before journey
//	needsRefuel := service.ShouldRefuelForJourney(fuel, from, to, 0.1)
//
//	// Select optimal flight mode based on available fuel
//	mode := service.SelectOptimalFlightMode(currentFuel, distance, safetyMargin)
//
//	// Check for opportunistic refueling
//	shouldRefuel := service.ShouldRefuelOpportunistically(fuel, capacity, waypoint, 0.9)
type ShipFuelService struct{}

func NewShipFuelService() *ShipFuelService {
	return &ShipFuelService{}
}

func (s *ShipFuelService) CalculateFuelRequired(
	from *shared.Waypoint,
	to *shared.Waypoint,
	mode shared.FlightMode,
) int {
	distance := from.DistanceTo(to)
	return mode.FuelCost(distance)
}

// CanShipNavigateTo checks if a ship has enough fuel to navigate to destination.
// CRUISE is the slowest mode a leg is ever flown at, so its cost is the bar: a
// tank that cannot clear it does not make the trip, it makes a refuel stop.
func (s *ShipFuelService) CanShipNavigateTo(
	currentFuel int,
	from *shared.Waypoint,
	to *shared.Waypoint,
) bool {
	distance := from.DistanceTo(to)
	minFuelRequired := shared.FlightModeCruise.FuelCost(distance)
	return currentFuel >= minFuelRequired
}

// ShouldRefuelForJourney determines if a ship needs refueling before a
// journey. safetyMargin is a fractional multiplier (e.g., 0.1 = 10% extra),
// not an absolute fuel amount.
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
// The second return is false when the tank affords no mode at all.
func (s *ShipFuelService) SelectOptimalFlightMode(
	currentFuel int,
	distance float64,
	safetyMargin int,
) (shared.FlightMode, bool) {
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
func (s *ShipFuelService) ShouldRefuelOpportunistically(
	fuel *shared.Fuel,
	fuelCapacity int,
	waypoint *shared.Waypoint,
	safetyThreshold float64,
) bool {
	if fuelCapacity == 0 {
		return false
	}

	if !waypoint.CanRefuel() {
		return false
	}

	// Use Fuel.Percentage() for consistent fuel percentage calculations
	fuelPercentage := fuel.Percentage() / 100.0
	return fuelPercentage < safetyThreshold
}

func (s *ShipFuelService) CalculateFuelNeededToFull(currentFuel int, fuelCapacity int) int {
	fuelNeeded := fuelCapacity - currentFuel
	if fuelNeeded < 0 {
		return 0
	}
	return fuelNeeded
}

// ShouldTopOffBeforeDeparture determines if a ship must refuel at the waypoint it
// is leaving. A leg is never degraded to a slower mode to fit the tank, so fuel the
// leg ahead needs has to be bought where fuel is actually sold.
//
// Returns true if the departure waypoint sells fuel and either:
//   - The tank cannot cover the leg ahead at its mode, plus the safety margin
//   - Fuel is below the strategy's safety threshold
func (s *ShipFuelService) ShouldTopOffBeforeDeparture(
	fuel *shared.Fuel,
	fuelCapacity int,
	segmentFlightMode shared.FlightMode,
	segmentDistance float64,
	fromWaypoint *shared.Waypoint,
	safetyThreshold float64,
) bool {
	if fuelCapacity == 0 {
		return false
	}

	// The whole waypoint, not a caller-derived bit: CanRefuel reads the permanent type too.
	if fromWaypoint == nil || !fromWaypoint.CanRefuel() {
		return false
	}

	legMode := segmentFlightMode.ForRouteLeg()
	if fuel.Current < legMode.FuelCost(segmentDistance)+DefaultFuelSafetyMargin {
		return true
	}

	// Use Fuel.Percentage() for consistent fuel percentage calculations
	fuelPercentage := fuel.Percentage() / 100.0
	return fuelPercentage < safetyThreshold
}

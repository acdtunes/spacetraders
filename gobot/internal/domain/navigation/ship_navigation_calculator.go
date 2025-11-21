package navigation

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ShipNavigationCalculator provides navigation-related calculations for ships
// This service contains stateless navigation logic extracted from the Ship entity
// to improve separation of concerns and testability.
type ShipNavigationCalculator struct{}

// NewShipNavigationCalculator creates a new navigation calculator instance
func NewShipNavigationCalculator() *ShipNavigationCalculator {
	return &ShipNavigationCalculator{}
}

// CalculateTravelTime calculates travel time between two waypoints
// using the specified flight mode and engine speed.
//
// Parameters:
//   - from: Starting waypoint
//   - to: Destination waypoint
//   - mode: Flight mode to use
//   - engineSpeed: Ship's engine speed
//
// Returns:
//   - Travel time in seconds
func (c *ShipNavigationCalculator) CalculateTravelTime(
	from *shared.Waypoint,
	to *shared.Waypoint,
	mode shared.FlightMode,
	engineSpeed int,
) int {
	distance := from.DistanceTo(to)
	return mode.TravelTime(distance, engineSpeed)
}

// CalculateDistance calculates Euclidean distance between two waypoints
//
// Parameters:
//   - from: Starting waypoint
//   - to: Destination waypoint
//
// Returns:
//   - Distance as a float64
func (c *ShipNavigationCalculator) CalculateDistance(
	from *shared.Waypoint,
	to *shared.Waypoint,
) float64 {
	return from.DistanceTo(to)
}

// IsAtLocation checks if two waypoints are the same location
//
// Parameters:
//   - current: Current waypoint
//   - target: Target waypoint
//
// Returns:
//   - true if waypoints have the same symbol
func (c *ShipNavigationCalculator) IsAtLocation(
	current *shared.Waypoint,
	target *shared.Waypoint,
) bool {
	return current.Symbol == target.Symbol
}

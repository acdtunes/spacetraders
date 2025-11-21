package strategies

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// RefuelStrategy defines the interface for different refueling strategies.
//
// This strategy pattern allows RouteExecutor to be Open/Closed for extension
// of refuel decision logic without modifying its implementation.
//
// The strategy is consulted at two points during route execution:
//  1. Before departure: Should we refuel before leaving this waypoint?
//  2. After arrival: Should we refuel after arriving at this waypoint?
//
// Implementations can optimize for different goals:
//   - Conservative: Maintain high fuel levels (current default)
//   - Cost-optimized: Only refuel at cheap stations
//   - Speed-optimized: Minimize refuel stops
//   - Adaptive: Adjust based on route characteristics
type RefuelStrategy interface {
	// ShouldRefuelBeforeDeparture determines if the ship should refuel before
	// departing from the current waypoint.
	//
	// This is typically used to prevent running out of fuel mid-flight (DRIFT mode).
	ShouldRefuelBeforeDeparture(ship *navigation.Ship, segment *navigation.RouteSegment) bool

	// ShouldRefuelAfterArrival determines if the ship should refuel after
	// arriving at a waypoint.
	//
	// This is typically used for opportunistic refueling when passing through
	// fuel stations, even if not strictly necessary.
	ShouldRefuelAfterArrival(ship *navigation.Ship, segment *navigation.RouteSegment) bool

	// GetStrategyName returns a human-readable name for logging and debugging.
	GetStrategyName() string
}

// ConservativeRefuelStrategy implements a cautious refueling approach.
//
// This strategy maintains high fuel levels to minimize risk of running out:
//   - Refuels before departure if fuel would drop below threshold
//   - Opportunistically refuels at fuel stations when below threshold
//   - Default threshold: 90% fuel capacity
//
// This is the default strategy and matches the original hardcoded behavior.
type ConservativeRefuelStrategy struct {
	threshold float64 // Fuel percentage threshold (0.0 to 1.0)
}

// NewConservativeRefuelStrategy creates a conservative strategy with the given threshold.
//
// The threshold represents the fuel percentage below which refueling is triggered.
// Example: 0.9 means refuel when fuel drops below 90% capacity.
//
// Typical values:
//   - 0.9 (90%): Very conservative, frequent refueling (default)
//   - 0.7 (70%): Balanced approach
//   - 0.5 (50%): Moderate risk tolerance
func NewConservativeRefuelStrategy(threshold float64) *ConservativeRefuelStrategy {
	return &ConservativeRefuelStrategy{
		threshold: threshold,
	}
}

// NewDefaultRefuelStrategy creates a conservative strategy with the default 90% threshold.
//
// This maintains backward compatibility with the original hardcoded behavior.
func NewDefaultRefuelStrategy() *ConservativeRefuelStrategy {
	return NewConservativeRefuelStrategy(0.9)
}

// ShouldRefuelBeforeDeparture checks if fuel would drop below threshold during flight.
func (s *ConservativeRefuelStrategy) ShouldRefuelBeforeDeparture(ship *navigation.Ship, segment *navigation.RouteSegment) bool {
	return ship.ShouldPreventDriftMode(segment, s.threshold)
}

// ShouldRefuelAfterArrival checks if at a fuel station with fuel below threshold.
func (s *ConservativeRefuelStrategy) ShouldRefuelAfterArrival(ship *navigation.Ship, segment *navigation.RouteSegment) bool {
	// Only opportunistically refuel if not already planned
	if segment.RequiresRefuel {
		return false
	}
	return ship.ShouldRefuelOpportunistically(segment.ToWaypoint, s.threshold)
}

// GetStrategyName returns the strategy name for logging.
func (s *ConservativeRefuelStrategy) GetStrategyName() string {
	return "conservative"
}

// MinimalRefuelStrategy implements a minimal refueling approach.
//
// This strategy only refuels when absolutely necessary to reach the next waypoint:
//   - Only refuels if insufficient fuel for next segment
//   - No opportunistic refueling
//   - Minimizes time spent refueling
//
// Use case: Speed-critical routes where minimizing stops is more important than
// maintaining high fuel reserves.
type MinimalRefuelStrategy struct{}

// NewMinimalRefuelStrategy creates a minimal refuel strategy.
func NewMinimalRefuelStrategy() *MinimalRefuelStrategy {
	return &MinimalRefuelStrategy{}
}

// ShouldRefuelBeforeDeparture only refuels if insufficient fuel to reach destination.
func (s *MinimalRefuelStrategy) ShouldRefuelBeforeDeparture(ship *navigation.Ship, segment *navigation.RouteSegment) bool {
	// Use a very low threshold (10% buffer only)
	return ship.ShouldPreventDriftMode(segment, 0.1)
}

// ShouldRefuelAfterArrival never refuels opportunistically.
func (s *MinimalRefuelStrategy) ShouldRefuelAfterArrival(ship *navigation.Ship, segment *navigation.RouteSegment) bool {
	// Never refuel opportunistically - only when planned
	return false
}

// GetStrategyName returns the strategy name for logging.
func (s *MinimalRefuelStrategy) GetStrategyName() string {
	return "minimal"
}

// AlwaysTopOffStrategy implements an aggressive refueling approach.
//
// This strategy refuels at every opportunity to maintain maximum fuel:
//   - Always refuels at fuel stations
//   - Maintains 100% fuel when possible
//   - Maximizes flight mode options
//
// Use case: Exploration routes where having maximum fuel provides flexibility
// for detours or unexpected long jumps.
type AlwaysTopOffStrategy struct{}

// NewAlwaysTopOffStrategy creates an always-top-off strategy.
func NewAlwaysTopOffStrategy() *AlwaysTopOffStrategy {
	return &AlwaysTopOffStrategy{}
}

// ShouldRefuelBeforeDeparture always refuels if at a fuel station.
func (s *AlwaysTopOffStrategy) ShouldRefuelBeforeDeparture(ship *navigation.Ship, segment *navigation.RouteSegment) bool {
	// Always refuel if fuel is not at maximum and we're at a fuel station
	if !segment.FromWaypoint.IsMarketplace() {
		return false
	}

	return ship.Fuel().Percentage() < 1.0
}

// ShouldRefuelAfterArrival always refuels if at a fuel station and not at max.
func (s *AlwaysTopOffStrategy) ShouldRefuelAfterArrival(ship *navigation.Ship, segment *navigation.RouteSegment) bool {
	// Check if destination has fuel available
	if !segment.ToWaypoint.IsMarketplace() {
		return false
	}

	// Refuel if not at maximum capacity
	return ship.Fuel().Percentage() < 1.0
}

// GetStrategyName returns the strategy name for logging.
func (s *AlwaysTopOffStrategy) GetStrategyName() string {
	return "always_top_off"
}

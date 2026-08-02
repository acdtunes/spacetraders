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
	// This is what keeps a leg flyable at the mode it was planned at, since a leg
	// is never degraded to a slower mode to fit the tank.
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
	threshold   float64 // Fuel percentage threshold (0.0 to 1.0)
	fuelService *navigation.ShipFuelService
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
		threshold:   threshold,
		fuelService: navigation.NewShipFuelService(),
	}
}

// NewDefaultRefuelStrategy creates a conservative strategy with the default 90% threshold.
//
// This maintains backward compatibility with the original hardcoded behavior.
func NewDefaultRefuelStrategy() *ConservativeRefuelStrategy {
	return NewConservativeRefuelStrategy(0.9)
}

// ShouldRefuelBeforeDeparture checks if the leg ahead is unaffordable or fuel
// would drop below threshold during flight.
func (s *ConservativeRefuelStrategy) ShouldRefuelBeforeDeparture(ship *navigation.Ship, segment *navigation.RouteSegment) bool {
	return s.fuelService.ShouldTopOffBeforeDeparture(
		ship.Fuel(),
		ship.FuelCapacity(),
		segment.FlightMode,
		segment.FromWaypoint.DistanceTo(segment.ToWaypoint),
		segment.FromWaypoint.HasFuel,
		s.threshold,
	)
}

// ShouldRefuelAfterArrival checks if at a fuel station with fuel below threshold.
func (s *ConservativeRefuelStrategy) ShouldRefuelAfterArrival(ship *navigation.Ship, segment *navigation.RouteSegment) bool {
	// Only opportunistically refuel if not already planned
	if segment.RequiresRefuel {
		return false
	}

	// Check if ship is at the waypoint before checking opportunistic refuel
	if ship.CurrentLocation().Symbol != segment.ToWaypoint.Symbol {
		return false
	}

	return s.fuelService.ShouldRefuelOpportunistically(
		ship.Fuel(),
		ship.FuelCapacity(),
		segment.ToWaypoint,
		s.threshold,
	)
}

// GetStrategyName returns the strategy name for logging.
func (s *ConservativeRefuelStrategy) GetStrategyName() string {
	return "conservative"
}

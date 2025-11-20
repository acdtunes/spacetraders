package fleet

import (
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// SelectionResult contains the result of ship selection
type SelectionResult struct {
	Ship     *navigation.Ship
	Distance float64
	Reason   string // Why this ship was selected (e.g., "has cargo", "closest")
}

// Selector implements fleet ship selection business logic
type Selector struct{}

// NewSelector creates a new fleet selector
func NewSelector() *Selector {
	return &Selector{}
}

// SelectOptimalShip selects the best ship from a fleet for a target location.
//
// Business Rules:
// 1. Ships with required cargo have absolute priority (even if in transit)
// 2. Ships in transit are excluded (unless they have cargo)
// 3. Select closest ship by Euclidean distance as fallback
//
// Parameters:
//   - ships: Available ships to choose from
//   - targetWaypoint: Destination waypoint
//   - requiredCargoSymbol: Optional cargo type for priority selection
//
// Returns:
//   - SelectionResult with selected ship, distance, and reason
//   - Error if no suitable ship found
func (s *Selector) SelectOptimalShip(
	ships []*navigation.Ship,
	targetWaypoint *shared.Waypoint,
	requiredCargoSymbol string,
) (*SelectionResult, error) {
	if len(ships) == 0 {
		return nil, fmt.Errorf("no ships available for selection")
	}

	if targetWaypoint == nil {
		return nil, fmt.Errorf("target waypoint cannot be nil")
	}

	var closestShip *navigation.Ship
	minDistance := math.MaxFloat64
	var shipWithCargo *navigation.Ship

	for _, ship := range ships {
		// PRIORITY CHECK: Does ship already have the required cargo?
		// This takes absolute priority - even over transit status
		if requiredCargoSymbol != "" {
			cargoUnits := ship.Cargo().GetItemUnits(requiredCargoSymbol)
			if cargoUnits > 0 {
				// Ship has cargo - select immediately (priority selection)
				shipWithCargo = ship
				// Continue checking all ships to find truly closest as tiebreaker
			}
		}

		// Skip ships in transit UNLESS they have the required cargo
		// (cargo ships might be mid-delivery after daemon restart)
		if ship.NavStatus() == navigation.NavStatusInTransit && shipWithCargo != ship {
			continue
		}

		// Calculate distance to target
		currentLocation := ship.CurrentLocation()
		distance := currentLocation.DistanceTo(targetWaypoint)

		// Track closest ship as fallback
		if distance < minDistance {
			minDistance = distance
			closestShip = ship
		}
	}

	// Priority: Ship with cargo > Closest ship by distance
	if shipWithCargo != nil {
		// Return with 0 distance since cargo presence is more important than distance
		return &SelectionResult{
			Ship:     shipWithCargo,
			Distance: 0,
			Reason:   fmt.Sprintf("has %s in cargo (priority)", requiredCargoSymbol),
		}, nil
	}

	// Fallback to closest ship
	if closestShip == nil {
		return nil, fmt.Errorf("no available ships found (all are in transit)")
	}

	return &SelectionResult{
		Ship:     closestShip,
		Distance: minDistance,
		Reason:   fmt.Sprintf("closest by distance (%.2f units)", minDistance),
	}, nil
}

// SelectClosestShipByDistance selects the closest ship to a target waypoint
// without any cargo priority logic. Useful for simple rebalancing operations.
//
// Parameters:
//   - ships: Available ships to choose from
//   - targetWaypoint: Destination waypoint
//   - excludeInTransit: If true, skip ships currently in transit
//
// Returns:
//   - SelectionResult with selected ship and distance
//   - Error if no suitable ship found
func (s *Selector) SelectClosestShipByDistance(
	ships []*navigation.Ship,
	targetWaypoint *shared.Waypoint,
	excludeInTransit bool,
) (*SelectionResult, error) {
	if len(ships) == 0 {
		return nil, fmt.Errorf("no ships available for selection")
	}

	if targetWaypoint == nil {
		return nil, fmt.Errorf("target waypoint cannot be nil")
	}

	var closestShip *navigation.Ship
	minDistance := math.MaxFloat64

	for _, ship := range ships {
		// Skip in-transit ships if requested
		if excludeInTransit && ship.NavStatus() == navigation.NavStatusInTransit {
			continue
		}

		// Calculate distance to target
		currentLocation := ship.CurrentLocation()
		distance := currentLocation.DistanceTo(targetWaypoint)

		if distance < minDistance {
			minDistance = distance
			closestShip = ship
		}
	}

	if closestShip == nil {
		return nil, fmt.Errorf("no available ships found")
	}

	return &SelectionResult{
		Ship:     closestShip,
		Distance: minDistance,
		Reason:   "closest by distance",
	}, nil
}

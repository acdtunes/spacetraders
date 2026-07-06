package contract

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

// ShipSelector implements ship selection business logic for contract deliveries
type ShipSelector struct{}

// NewShipSelector creates a new ship selector
func NewShipSelector() *ShipSelector {
	return &ShipSelector{}
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
func (s *ShipSelector) SelectOptimalShip(
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
		if s.hasRequiredCargo(ship, requiredCargoSymbol) {
			shipWithCargo = ship
		}

		if s.shouldSkipShipInTransit(ship, shipWithCargo) {
			continue
		}

		closestShip, minDistance = s.updateClosestShip(ship, targetWaypoint, closestShip, minDistance)
	}

	if shipWithCargo != nil {
		return s.buildCargoSelectionResult(shipWithCargo, requiredCargoSymbol), nil
	}

	if closestShip == nil {
		return nil, fmt.Errorf("no available ships found (all are in transit)")
	}

	return s.buildDistanceSelectionResult(closestShip, minDistance), nil
}

func (s *ShipSelector) hasRequiredCargo(ship *navigation.Ship, requiredCargoSymbol string) bool {
	if requiredCargoSymbol == "" {
		return false
	}
	cargoUnits := ship.Cargo().GetItemUnits(requiredCargoSymbol)
	return cargoUnits > 0
}

func (s *ShipSelector) shouldSkipShipInTransit(ship *navigation.Ship, shipWithCargo *navigation.Ship) bool {
	return ship.NavStatus() == navigation.NavStatusInTransit && shipWithCargo != ship
}

func (s *ShipSelector) updateClosestShip(
	ship *navigation.Ship,
	targetWaypoint *shared.Waypoint,
	currentClosest *navigation.Ship,
	currentMinDistance float64,
) (*navigation.Ship, float64) {
	currentLocation := ship.CurrentLocation()
	distance := currentLocation.DistanceTo(targetWaypoint)

	if distance < currentMinDistance {
		return ship, distance
	}

	return currentClosest, currentMinDistance
}

func (s *ShipSelector) buildCargoSelectionResult(ship *navigation.Ship, requiredCargoSymbol string) *SelectionResult {
	return &SelectionResult{
		Ship:     ship,
		Distance: 0,
		Reason:   fmt.Sprintf("has %s in cargo (priority)", requiredCargoSymbol),
	}
}

func (s *ShipSelector) buildDistanceSelectionResult(ship *navigation.Ship, distance float64) *SelectionResult {
	return &SelectionResult{
		Ship:     ship,
		Distance: distance,
		Reason:   fmt.Sprintf("closest by distance (%.2f units)", distance),
	}
}

package contract

import (
	"fmt"

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
//  1. Ships with required cargo have absolute priority (even if in transit)
//  2. Ships in transit are excluded (unless they have cargo)
//  3. Fallback: cargo-fit hull selection via SelectHullForCargo (l7h2 Phase 3):
//     the smallest hull whose hold fits the load, the command frigate strictly
//     last-resort, fewest-round-trips when nothing fits in one trip. This
//     right-sizes the hull for the job (sp-snmb's goal) with a deterministic
//     fit ladder instead of the earlier completion-time estimate, so a fitting
//     light hull wins even when a heavy is idle and closer.
//
// Parameters:
//   - ships: Available ships to choose from
//   - targetWaypoint: Destination waypoint
//   - requiredCargoSymbol: Optional cargo type for priority selection
//   - unitsNeeded: Units still required for the delivery, used to judge which
//     hulls fit the load (and trip counts when none do)
//
// Returns:
//   - SelectionResult with selected ship, distance, and reason
//   - Error if no suitable ship found
func (s *ShipSelector) SelectOptimalShip(
	ships []*navigation.Ship,
	targetWaypoint *shared.Waypoint,
	requiredCargoSymbol string,
	unitsNeeded int,
) (*SelectionResult, error) {
	if len(ships) == 0 {
		return nil, fmt.Errorf("no ships available for selection")
	}

	if targetWaypoint == nil {
		return nil, fmt.Errorf("target waypoint cannot be nil")
	}

	var available []*navigation.Ship
	var shipWithCargo *navigation.Ship

	for _, ship := range ships {
		if s.hasRequiredCargo(ship, requiredCargoSymbol) {
			shipWithCargo = ship
		}

		if s.shouldSkipShipInTransit(ship, shipWithCargo) {
			continue
		}

		available = append(available, ship)
	}

	if shipWithCargo != nil {
		return s.buildCargoSelectionResult(shipWithCargo, requiredCargoSymbol), nil
	}

	if len(available) == 0 {
		return nil, fmt.Errorf("no available ships found (all are in transit)")
	}

	return SelectHullForCargo(available, targetWaypoint, unitsNeeded)
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

func (s *ShipSelector) buildCargoSelectionResult(ship *navigation.Ship, requiredCargoSymbol string) *SelectionResult {
	return &SelectionResult{
		Ship:     ship,
		Distance: 0,
		Reason:   fmt.Sprintf("has %s in cargo (priority)", requiredCargoSymbol),
	}
}

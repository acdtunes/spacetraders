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
//  2. An in-transit ship claimed by another controller is excluded; an
//     unclaimed one is a normal candidate.
//  3. Fallback: cargo-fit hull selection via SelectHullForCargo - nearest by
//     travel time (a supplied ETA where one covers the hull), command frigate
//     last-resort; see that function's doc for the full tiebreak ladder.
//
// requiredCargoSymbol is optional and drives the priority rule above.
// unitsNeeded is the quantity still outstanding, used to judge which hulls fit
// the load (and trip counts when none do). etas/deliveryFleet/standbySlots
// pass through to SelectHullForCargo's ranking and ownership tiebreak.
func (s *ShipSelector) SelectOptimalShip(
	ships []*navigation.Ship,
	targetWaypoint *shared.Waypoint,
	requiredCargoSymbol string,
	unitsNeeded int,
	etas map[string]float64,
	deliveryFleet []string,
	standbySlots []string,
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

	return SelectHullForCargo(available, targetWaypoint, unitsNeeded, etas, deliveryFleet, standbySlots)
}

func (s *ShipSelector) hasRequiredCargo(ship *navigation.Ship, requiredCargoSymbol string) bool {
	if requiredCargoSymbol == "" {
		return false
	}
	cargoUnits := ship.Cargo().GetItemUnits(requiredCargoSymbol)
	return cargoUnits > 0
}

// shouldSkipShipInTransit drops a mid-flight hull only when another controller
// owns it: an unclaimed in-transit hull is a legitimate candidate whose ETA
// already counts its remaining flight, and interrupting an ownerless
// repositioning for paying work is the point of ranking it.
func (s *ShipSelector) shouldSkipShipInTransit(ship *navigation.Ship, shipWithCargo *navigation.Ship) bool {
	if ship.NavStatus() != navigation.NavStatusInTransit || shipWithCargo == ship {
		return false
	}
	return !ship.IsIdle()
}

func (s *ShipSelector) buildCargoSelectionResult(ship *navigation.Ship, requiredCargoSymbol string) *SelectionResult {
	return &SelectionResult{
		Ship:     ship,
		Distance: 0,
		Reason:   fmt.Sprintf("has %s in cargo (priority)", requiredCargoSymbol),
	}
}

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
// 3. Fallback: select the hull with the lowest estimated completion time -
//    right-sized for the job, not just the closest (sp-snmb). A fast,
//    adequately-sized ship can beat a closer slow one for a small delivery;
//    a big-cargo hull is reserved for deliveries that actually need the hold,
//    since an undersized hull pays for extra distance in extra round trips.
//
// Parameters:
//   - ships: Available ships to choose from
//   - targetWaypoint: Destination waypoint
//   - requiredCargoSymbol: Optional cargo type for priority selection
//   - unitsNeeded: Units still required for the delivery, used to estimate
//     how many round trips each candidate hull would need
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

	var fastestShip *navigation.Ship
	var fastestDistance float64
	minTotalTime := math.MaxFloat64
	var shipWithCargo *navigation.Ship

	for _, ship := range ships {
		if s.hasRequiredCargo(ship, requiredCargoSymbol) {
			shipWithCargo = ship
		}

		if s.shouldSkipShipInTransit(ship, shipWithCargo) {
			continue
		}

		fastestShip, fastestDistance, minTotalTime = s.updateFastestAdequateShip(ship, targetWaypoint, unitsNeeded, fastestShip, fastestDistance, minTotalTime)
	}

	if shipWithCargo != nil {
		return s.buildCargoSelectionResult(shipWithCargo, requiredCargoSymbol), nil
	}

	if fastestShip == nil {
		return nil, fmt.Errorf("no available ships found (all are in transit)")
	}

	return s.buildDistanceSelectionResult(fastestShip, fastestDistance), nil
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

// updateFastestAdequateShip ranks a candidate by estimated total completion
// time (round trips required x travel time per trip) rather than raw
// distance, so hull right-sizing (sp-snmb) can prefer a fast, adequately
// sized ship over a closer but slower or over/under-sized one.
func (s *ShipSelector) updateFastestAdequateShip(
	ship *navigation.Ship,
	targetWaypoint *shared.Waypoint,
	unitsNeeded int,
	currentFastest *navigation.Ship,
	currentDistance float64,
	currentMinTotalTime float64,
) (*navigation.Ship, float64, float64) {
	currentLocation := ship.CurrentLocation()
	distance := currentLocation.DistanceTo(targetWaypoint)
	totalTime := s.estimatedCompletionTime(ship, distance, unitsNeeded)

	if totalTime < currentMinTotalTime {
		return ship, distance, totalTime
	}

	return currentFastest, currentDistance, currentMinTotalTime
}

// estimatedCompletionTime estimates how long it would take a ship, at the
// given distance from the target, to deliver unitsNeeded units - factoring in
// both travel speed and cargo capacity. A small-cargo ship needs more round
// trips for a large delivery, so a fast hull only wins when its speed
// advantage isn't erased by extra trips (sp-snmb hull right-sizing).
func (s *ShipSelector) estimatedCompletionTime(ship *navigation.Ship, distance float64, unitsNeeded int) float64 {
	units := unitsNeeded
	if units < 1 {
		units = 1
	}
	capacity := ship.CargoCapacity()
	if capacity < 1 {
		capacity = 1
	}
	// ceil(units/capacity) with units>=1 and capacity>=1 is always >= 1, so no
	// extra floor guard is needed here.
	trips := int(math.Ceil(float64(units) / float64(capacity)))
	travelTime := shared.FlightModeCruise.TravelTime(distance, ship.EngineSpeed())
	return float64(trips) * float64(travelTime)
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
		Reason:   fmt.Sprintf("fastest adequate hull (%.2f units away)", distance),
	}
}

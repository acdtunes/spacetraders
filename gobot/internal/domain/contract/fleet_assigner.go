package contract

import (
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// MaxShipsPerWaypoint defines the maximum allowed ships at a single waypoint
	// to prevent clustering. Beyond this threshold, rebalancing is triggered.
	MaxShipsPerWaypoint = 2

	// MaxShipsPerMarket defines the maximum ships assigned to a single market
	// during distribution to ensure balanced fleet coverage.
	MaxShipsPerMarket = 2
)

// DistributionMetrics contains statistics about fleet distribution
type DistributionMetrics struct {
	AverageDistance float64
	IsClustered     bool
	ClusteredAt     string // Waypoint symbol where clustering detected
}

// Assignment represents a ship-to-waypoint assignment
type Assignment struct {
	ShipSymbol     string
	TargetWaypoint string
	Distance       float64
}

// FleetAssigner implements fleet assignment business logic for contract operations
type FleetAssigner struct{}

// NewFleetAssigner creates a new fleet assigner
func NewFleetAssigner() *FleetAssigner {
	return &FleetAssigner{}
}

// IsRebalancingNeeded evaluates if fleet rebalancing is required based on:
// 1. Clustering: More than MaxShipsPerWaypoint ships at same location
// 2. Distance: Average distance to nearest target exceeds threshold
//
// Business Rules:
//   - Clustering takes priority over distance (immediate rebalance needed)
//   - Distance threshold allows tuning of rebalancing sensitivity
//
// Parameters:
//   - ships: Current fleet
//   - targetWaypoints: Destination waypoints (markets, fuel stations, etc.)
//   - distanceThreshold: Maximum acceptable average distance
//
// Returns:
//   - needsRebalancing: True if rebalancing should be triggered
//   - metrics: Distribution statistics for decision-making
func (fa *FleetAssigner) IsRebalancingNeeded(
	ships []*navigation.Ship,
	targetWaypoints []*shared.Waypoint,
	distanceThreshold float64,
) (bool, *DistributionMetrics, error) {
	if len(ships) == 0 || len(targetWaypoints) == 0 {
		return false, &DistributionMetrics{}, nil
	}

	// Check for clustering: count ships at each waypoint
	waypointCounts := make(map[string]int)
	for _, ship := range ships {
		waypointCounts[ship.CurrentLocation().Symbol]++
	}

	// If any waypoint has more than allowed, clustering detected
	for waypoint, count := range waypointCounts {
		if count > MaxShipsPerWaypoint {
			// Clustering detected - immediate rebalancing needed
			return true, &DistributionMetrics{
				AverageDistance: 0, // Not relevant when clustering
				IsClustered:     true,
				ClusteredAt:     waypoint,
			}, nil
		}
	}

	// Calculate average distance from each ship to its nearest target
	totalDistance := 0.0
	for _, ship := range ships {
		currentLocation := ship.CurrentLocation()
		_, minDistance := shared.FindNearestWaypoint(currentLocation, targetWaypoints)
		totalDistance += minDistance
	}

	avgDistance := totalDistance / float64(len(ships))
	needsRebalancing := avgDistance > distanceThreshold

	return needsRebalancing, &DistributionMetrics{
		AverageDistance: avgDistance,
		IsClustered:     false,
		ClusteredAt:     "",
	}, nil
}

// PrePositionHint biases one idle hull toward a predicted next-source market during
// assignment (sp-1ef0 contract pre-position). It is honored only when TargetWaypoint is
// set AND Confidence clears Threshold — the confidence guard that bounds wasted-move
// risk. A zero-value hint (or any sub-threshold one) leaves distance-based round-robin
// exactly as it was, so the no-signal path is untouched.
type PrePositionHint struct {
	// TargetWaypoint is the predicted next-source market symbol. Empty disables the hint.
	TargetWaypoint string
	// Confidence is the same-good-remaining signal's near-certainty in [0,1].
	Confidence float64
	// Threshold is the minimum confidence to act on the hint (from live config).
	Threshold float64
}

// active reports whether the hint names a target and clears the confidence guard.
func (h PrePositionHint) active() bool {
	return h.TargetWaypoint != "" && h.Confidence > 0 && h.Confidence >= h.Threshold
}

// AssignShipsToTargets distributes ships across target waypoints using
// balanced round-robin with distance optimization. It is the no-hint entry point;
// see AssignShipsToTargetsWithHint for the contract pre-position variant.
//
// Business Rules:
//   - Maximum MaxShipsPerMarket ships per target (prevents over-concentration)
//   - Assigns each ship to nearest available target
//   - Ensures balanced distribution across all targets
//
// Parameters:
//   - ships: Fleet to distribute
//   - targetWaypoints: Destination waypoints for assignment
//
// Returns:
//   - assignments: List of ship-to-waypoint assignments with distances
func (fa *FleetAssigner) AssignShipsToTargets(
	ships []*navigation.Ship,
	targetWaypoints []*shared.Waypoint,
) ([]Assignment, error) {
	return fa.AssignShipsToTargetsWithHint(ships, targetWaypoints, PrePositionHint{})
}

// AssignShipsToTargetsWithHint is AssignShipsToTargets plus an optional contract
// pre-position hint (sp-1ef0). When the hint clears its confidence guard, the idle hull
// nearest the predicted next-source market is placed onto it FIRST — overriding pure
// distance so it is already close when the contract's next same-good delivery begins —
// and the remaining hulls distribute by distance as before. Below the guard (or with an
// empty/absent hint) the result is identical to the legacy round-robin.
//
// Algorithm:
//  1. Cap ships per target (balanced distribution, MaxShipsPerMarket ceiling).
//  2. If the hint is active, reserve one slot at the predicted market for the nearest hull.
//  3. Distribute the rest to their nearest target with remaining capacity.
func (fa *FleetAssigner) AssignShipsToTargetsWithHint(
	ships []*navigation.Ship,
	targetWaypoints []*shared.Waypoint,
	hint PrePositionHint,
) ([]Assignment, error) {
	if len(ships) == 0 || len(targetWaypoints) == 0 {
		return []Assignment{}, nil
	}

	// Calculate max ships per target for balanced distribution
	// E.g., 5 ships, 3 targets -> max 2 ships per target (ceiling division)
	maxPerTarget := (len(ships) + len(targetWaypoints) - 1) / len(targetWaypoints)

	// Cap at MaxShipsPerMarket - no need to cluster all ships at one location
	if maxPerTarget > MaxShipsPerMarket {
		maxPerTarget = MaxShipsPerMarket
	}

	// Track how many ships assigned to each target
	targetCounts := make(map[string]int)
	for _, waypoint := range targetWaypoints {
		targetCounts[waypoint.Symbol] = 0
	}

	// Assignment list
	var assignments []Assignment
	remaining := ships

	// Pre-position: when the same-good/multi-delivery-remaining signal clears the
	// confidence guard, bias the idle hull nearest the predicted next source onto it so
	// it is closer when the next delivery starts. Below the guard we fall straight
	// through to distance round-robin (the guard — no wasted move on a weak signal).
	if hint.active() {
		if target := findTargetWaypoint(targetWaypoints, hint.TargetWaypoint); target != nil {
			if ship := nearestShipToTarget(remaining, target); ship != nil {
				assignments = append(assignments, Assignment{
					ShipSymbol:     ship.ShipSymbol(),
					TargetWaypoint: target.Symbol,
					Distance:       ship.CurrentLocation().DistanceTo(target),
				})
				targetCounts[target.Symbol]++
				remaining = shipsExcept(remaining, ship.ShipSymbol())
			}
		}
	}

	// For each remaining ship, find nearest target with available capacity
	for _, ship := range remaining {
		var bestTarget *shared.Waypoint
		bestDistance := math.MaxFloat64
		currentLocation := ship.CurrentLocation()

		for _, targetWaypoint := range targetWaypoints {
			// Skip targets at capacity
			if targetCounts[targetWaypoint.Symbol] >= maxPerTarget {
				continue
			}

			distance := currentLocation.DistanceTo(targetWaypoint)

			// Select nearest available target
			if distance < bestDistance {
				bestDistance = distance
				bestTarget = targetWaypoint
			}
		}

		// Assign ship to best target (if found)
		if bestTarget != nil {
			assignments = append(assignments, Assignment{
				ShipSymbol:     ship.ShipSymbol(),
				TargetWaypoint: bestTarget.Symbol,
				Distance:       bestDistance,
			})
			targetCounts[bestTarget.Symbol]++
		}
		// Note: Ships without assignment remain at current location
	}

	return assignments, nil
}

// findTargetWaypoint returns the target waypoint with the given symbol, or nil when the
// predicted market is not among the discovered targets.
func findTargetWaypoint(targets []*shared.Waypoint, symbol string) *shared.Waypoint {
	for _, wp := range targets {
		if wp.Symbol == symbol {
			return wp
		}
	}
	return nil
}

// nearestShipToTarget returns the ship closest to target, or nil when ships is empty.
func nearestShipToTarget(ships []*navigation.Ship, target *shared.Waypoint) *navigation.Ship {
	var best *navigation.Ship
	bestDistance := math.MaxFloat64
	for _, ship := range ships {
		d := ship.CurrentLocation().DistanceTo(target)
		if d < bestDistance {
			bestDistance = d
			best = ship
		}
	}
	return best
}

// shipsExcept returns ships with the named ship removed (preserving order).
func shipsExcept(ships []*navigation.Ship, exclude string) []*navigation.Ship {
	out := make([]*navigation.Ship, 0, len(ships))
	for _, ship := range ships {
		if ship.ShipSymbol() != exclude {
			out = append(out, ship)
		}
	}
	return out
}

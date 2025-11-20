package fleet

import (
	"fmt"
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
	ShipSymbol      string
	TargetWaypoint  string
	Distance        float64
}

// DistributionService implements fleet distribution business logic
type DistributionService struct{}

// NewDistributionService creates a new distribution service
func NewDistributionService() *DistributionService {
	return &DistributionService{}
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
func (ds *DistributionService) IsRebalancingNeeded(
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
		minDistance := math.MaxFloat64
		currentLocation := ship.CurrentLocation()

		for _, targetWaypoint := range targetWaypoints {
			distance := currentLocation.DistanceTo(targetWaypoint)
			if distance < minDistance {
				minDistance = distance
			}
		}

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

// AssignShipsToTargets distributes ships across target waypoints using
// balanced round-robin with distance optimization.
//
// Business Rules:
//   - Maximum MaxShipsPerMarket ships per target (prevents over-concentration)
//   - Assigns each ship to nearest available target
//   - Ensures balanced distribution across all targets
//
// Algorithm:
//   1. Calculate max ships per target (balanced distribution)
//   2. Cap at MaxShipsPerMarket to prevent clustering
//   3. For each ship, select nearest target with capacity
//   4. Track assignments and capacities
//
// Parameters:
//   - ships: Fleet to distribute
//   - targetWaypoints: Destination waypoints for assignment
//
// Returns:
//   - assignments: List of ship-to-waypoint assignments with distances
func (ds *DistributionService) AssignShipsToTargets(
	ships []*navigation.Ship,
	targetWaypoints []*shared.Waypoint,
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

	// For each ship, find nearest target with available capacity
	for _, ship := range ships {
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

// CalculateDistributionQuality evaluates how well a fleet is distributed
// relative to target waypoints. Lower scores indicate better distribution.
//
// Parameters:
//   - ships: Current fleet
//   - targetWaypoints: Target locations
//
// Returns:
//   - quality score: Average distance to nearest target (lower is better)
func (ds *DistributionService) CalculateDistributionQuality(
	ships []*navigation.Ship,
	targetWaypoints []*shared.Waypoint,
) (float64, error) {
	if len(ships) == 0 || len(targetWaypoints) == 0 {
		return 0, fmt.Errorf("ships and targets cannot be empty")
	}

	totalDistance := 0.0
	for _, ship := range ships {
		minDistance := math.MaxFloat64
		currentLocation := ship.CurrentLocation()

		for _, targetWaypoint := range targetWaypoints {
			distance := currentLocation.DistanceTo(targetWaypoint)
			if distance < minDistance {
				minDistance = distance
			}
		}

		totalDistance += minDistance
	}

	return totalDistance / float64(len(ships)), nil
}

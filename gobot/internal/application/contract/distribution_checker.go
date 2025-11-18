package contract

import (
	"context"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// DistributionChecker determines if ship rebalancing is needed
type DistributionChecker struct {
	graphProvider system.ISystemGraphProvider
}

// NewDistributionChecker creates a new distribution checker
func NewDistributionChecker(graphProvider system.ISystemGraphProvider) *DistributionChecker {
	return &DistributionChecker{
		graphProvider: graphProvider,
	}
}

// calculateDistance computes Euclidean distance between two waypoints
func calculateDistance(x1, y1, x2, y2 float64) float64 {
	dx := x2 - x1
	dy := y2 - y1
	return math.Sqrt(dx*dx + dy*dy)
}

// IsRebalancingNeeded checks if ships are poorly distributed relative to target markets
// Returns true if average distance from ships to nearest target exceeds threshold
func (dc *DistributionChecker) IsRebalancingNeeded(
	ctx context.Context,
	ships []*navigation.Ship,
	targetMarkets []string,
	systemSymbol string,
	playerID int,
	distanceThreshold float64,
) (bool, float64, error) {
	if len(ships) == 0 || len(targetMarkets) == 0 {
		return false, 0, nil
	}

	// Get system graph for coordinate lookup
	graphResult, err := dc.graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return false, 0, fmt.Errorf("failed to get system graph: %w", err)
	}

	// Extract waypoints map
	waypointsRaw, ok := graphResult.Graph["waypoints"].(map[string]interface{})
	if !ok {
		return false, 0, fmt.Errorf("invalid graph format: missing waypoints")
	}

	// Calculate distance from each ship to its nearest target market
	totalDistance := 0.0
	for _, ship := range ships {
		minDistance := math.MaxFloat64

		// Get ship's current waypoint coordinates
		shipWp, ok := waypointsRaw[ship.CurrentLocation().Symbol].(map[string]interface{})
		if !ok {
			continue // Skip ships at unknown waypoints
		}
		shipX := shipWp["x"].(float64)
		shipY := shipWp["y"].(float64)

		for _, marketWaypoint := range targetMarkets {
			// Get market waypoint coordinates
			marketWp, ok := waypointsRaw[marketWaypoint].(map[string]interface{})
			if !ok {
				continue // Skip unknown market waypoints
			}
			marketX := marketWp["x"].(float64)
			marketY := marketWp["y"].(float64)

			distance := calculateDistance(shipX, shipY, marketX, marketY)
			if distance < minDistance {
				minDistance = distance
			}
		}

		totalDistance += minDistance
	}

	// Calculate average distance
	avgDistance := totalDistance / float64(len(ships))

	// Compare against threshold
	needsRebalancing := avgDistance > distanceThreshold

	return needsRebalancing, avgDistance, nil
}

// AssignShipsToMarkets distributes ships across target markets using balanced round-robin
// Returns a map of ship symbol to assigned market waypoint
func (dc *DistributionChecker) AssignShipsToMarkets(
	ctx context.Context,
	ships []*navigation.Ship,
	targetMarkets []string,
	systemSymbol string,
	playerID int,
) (map[string]string, error) {
	if len(ships) == 0 || len(targetMarkets) == 0 {
		return make(map[string]string), nil
	}

	// Get system graph for coordinate lookup
	graphResult, err := dc.graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get system graph: %w", err)
	}

	// Extract waypoints map
	waypointsRaw, ok := graphResult.Graph["waypoints"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid graph format: missing waypoints")
	}

	// Calculate max ships per market to ensure balanced distribution
	// E.g., 5 ships, 3 markets -> max 2 ships per market (some get 2, some get 1)
	maxPerMarket := (len(ships) + len(targetMarkets) - 1) / len(targetMarkets) // Ceiling division

	// Cap at 2 ships per market - no need to cluster all ships at one location
	// With 5 ships and 1 market, only 2 go there; others stay put
	if maxPerMarket > 2 {
		maxPerMarket = 2
	}

	// Track how many ships assigned to each market
	marketCounts := make(map[string]int)
	for _, market := range targetMarkets {
		marketCounts[market] = 0
	}

	// Assignment map: ship symbol -> market waypoint
	assignments := make(map[string]string)

	// Round-robin assignment: for each ship, find the nearest market that isn't full
	for _, ship := range ships {
		bestMarket := ""
		bestDistance := math.MaxFloat64

		// Get ship's current waypoint coordinates
		shipWp, ok := waypointsRaw[ship.CurrentLocation().Symbol].(map[string]interface{})
		if !ok {
			continue // Skip ships at unknown waypoints
		}
		shipX := shipWp["x"].(float64)
		shipY := shipWp["y"].(float64)

		for _, marketWaypoint := range targetMarkets {
			// Skip markets that have reached their capacity
			if marketCounts[marketWaypoint] >= maxPerMarket {
				continue
			}

			// Get market waypoint coordinates
			marketWp, ok := waypointsRaw[marketWaypoint].(map[string]interface{})
			if !ok {
				continue // Skip unknown market waypoints
			}
			marketX := marketWp["x"].(float64)
			marketY := marketWp["y"].(float64)

			distance := calculateDistance(shipX, shipY, marketX, marketY)

			// Simple distance-based selection among available markets
			if distance < bestDistance {
				bestDistance = distance
				bestMarket = marketWaypoint
			}
		}

		if bestMarket != "" {
			assignments[ship.ShipSymbol()] = bestMarket
			marketCounts[bestMarket]++
		}
	}

	return assignments, nil
}

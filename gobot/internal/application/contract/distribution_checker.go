package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/fleet"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// DistributionChecker is a thin application layer wrapper for fleet distribution logic.
// It fetches waypoint data and delegates business logic to domain FleetDistributionService.
type DistributionChecker struct {
	graphProvider       system.ISystemGraphProvider
	distributionService *fleet.DistributionService
}

// NewDistributionChecker creates a new distribution checker
func NewDistributionChecker(graphProvider system.ISystemGraphProvider) *DistributionChecker {
	return &DistributionChecker{
		graphProvider:       graphProvider,
		distributionService: fleet.NewDistributionService(),
	}
}

// IsRebalancingNeeded checks if ships are poorly distributed relative to target markets.
// This is a thin wrapper that fetches waypoint data and delegates to domain service.
//
// Returns:
//   - needsRebalancing: true if rebalancing should be triggered
//   - avgDistance: average distance from ships to nearest target
//   - error: any error encountered
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

	// 1. Fetch waypoint coordinates from graph
	targetWaypoints, err := dc.fetchWaypoints(ctx, targetMarkets, systemSymbol, playerID)
	if err != nil {
		return false, 0, err
	}

	// 2. Delegate to domain service for business logic
	needsRebalancing, metrics, err := dc.distributionService.IsRebalancingNeeded(
		ships,
		targetWaypoints,
		distanceThreshold,
	)
	if err != nil {
		return false, 0, fmt.Errorf("rebalancing check failed: %w", err)
	}

	return needsRebalancing, metrics.AverageDistance, nil
}

// AssignShipsToMarkets distributes ships across target markets using balanced round-robin.
// This is a thin wrapper that fetches waypoint data and delegates to domain service.
//
// Returns:
//   - assignments: map of ship symbol to assigned market waypoint
//   - error: any error encountered
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

	// 1. Fetch waypoint coordinates from graph
	targetWaypoints, err := dc.fetchWaypoints(ctx, targetMarkets, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}

	// 2. Delegate to domain service for assignment logic
	domainAssignments, err := dc.distributionService.AssignShipsToTargets(ships, targetWaypoints)
	if err != nil {
		return nil, fmt.Errorf("ship assignment failed: %w", err)
	}

	// 3. Convert domain assignments to application DTO (map[string]string)
	assignments := make(map[string]string)
	for _, assignment := range domainAssignments {
		assignments[assignment.ShipSymbol] = assignment.TargetWaypoint
	}

	return assignments, nil
}

// fetchWaypoints fetches waypoint objects from the graph provider.
// This is infrastructure coordination - converting from graph provider format to domain Waypoint objects.
func (dc *DistributionChecker) fetchWaypoints(
	ctx context.Context,
	waypointSymbols []string,
	systemSymbol string,
	playerID int,
) ([]*shared.Waypoint, error) {
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

	// Build waypoint objects
	var waypoints []*shared.Waypoint
	for _, symbol := range waypointSymbols {
		wpRaw, ok := waypointsRaw[symbol].(map[string]interface{})
		if !ok {
			continue // Skip unknown waypoints
		}

		x := wpRaw["x"].(float64)
		y := wpRaw["y"].(float64)

		waypoint, err := shared.NewWaypoint(symbol, x, y)
		if err != nil {
			return nil, fmt.Errorf("failed to create waypoint %s: %w", symbol, err)
		}

		waypoints = append(waypoints, waypoint)
	}

	return waypoints, nil
}

package contract

import (
	"context"
	"fmt"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// DistributionChecker is a thin application layer wrapper for fleet distribution logic.
// It fetches waypoint data and delegates business logic to domain FleetAssigner.
type DistributionChecker struct {
	graphProvider system.ISystemGraphProvider
	fleetAssigner *domainContract.FleetAssigner
}

// NewDistributionChecker creates a new distribution checker
func NewDistributionChecker(graphProvider system.ISystemGraphProvider) *DistributionChecker {
	return &DistributionChecker{
		graphProvider: graphProvider,
		fleetAssigner: domainContract.NewFleetAssigner(),
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

	targetWaypoints, err := dc.fetchWaypoints(ctx, targetMarkets, systemSymbol, playerID)
	if err != nil {
		return false, 0, err
	}

	return dc.checkDistribution(ships, targetWaypoints, distanceThreshold)
}

func (dc *DistributionChecker) checkDistribution(
	ships []*navigation.Ship,
	targetWaypoints []*shared.Waypoint,
	distanceThreshold float64,
) (bool, float64, error) {
	needsRebalancing, metrics, err := dc.fleetAssigner.IsRebalancingNeeded(
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

	targetWaypoints, err := dc.fetchWaypoints(ctx, targetMarkets, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}

	domainAssignments, err := dc.delegateShipAssignment(ships, targetWaypoints)
	if err != nil {
		return nil, err
	}

	return dc.convertAssignmentsToDTO(domainAssignments), nil
}

func (dc *DistributionChecker) delegateShipAssignment(
	ships []*navigation.Ship,
	targetWaypoints []*shared.Waypoint,
) ([]domainContract.Assignment, error) {
	domainAssignments, err := dc.fleetAssigner.AssignShipsToTargets(ships, targetWaypoints)
	if err != nil {
		return nil, fmt.Errorf("ship assignment failed: %w", err)
	}
	return domainAssignments, nil
}

func (dc *DistributionChecker) convertAssignmentsToDTO(domainAssignments []domainContract.Assignment) map[string]string {
	assignments := make(map[string]string)
	for _, assignment := range domainAssignments {
		assignments[assignment.ShipSymbol] = assignment.TargetWaypoint
	}
	return assignments
}

// fetchWaypoints fetches waypoint objects from the graph provider.
// This is infrastructure coordination - converting from graph provider format to domain Waypoint objects.
func (dc *DistributionChecker) fetchWaypoints(
	ctx context.Context,
	waypointSymbols []string,
	systemSymbol string,
	playerID int,
) ([]*shared.Waypoint, error) {
	graphResult, err := dc.getSystemGraph(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}

	waypointsRaw, err := dc.extractWaypointsMap(graphResult)
	if err != nil {
		return nil, err
	}

	return dc.buildWaypointObjects(waypointSymbols, waypointsRaw)
}

func (dc *DistributionChecker) getSystemGraph(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) (*system.GraphLoadResult, error) {
	graphResult, err := dc.graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get system graph: %w", err)
	}
	return graphResult, nil
}

func (dc *DistributionChecker) extractWaypointsMap(graphResult *system.GraphLoadResult) (map[string]interface{}, error) {
	waypointsRaw, ok := graphResult.Graph["waypoints"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid graph format: missing waypoints")
	}
	return waypointsRaw, nil
}

func (dc *DistributionChecker) buildWaypointObjects(
	waypointSymbols []string,
	waypointsRaw map[string]interface{},
) ([]*shared.Waypoint, error) {
	var waypoints []*shared.Waypoint
	for _, symbol := range waypointSymbols {
		wpRaw, ok := waypointsRaw[symbol].(map[string]interface{})
		if !ok {
			continue
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

package contract

import (
	"context"
	"fmt"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// SelectClosestShip selects the ship closest to the target waypoint from a list of ship symbols.
// This is a thin application layer wrapper that:
// 1. Fetches ships from repository
// 2. Fetches target waypoint coordinates
// 3. Delegates selection logic to domain FleetSelector service
//
// Parameters:
//   - shipSymbols: List of ship symbols to consider
//   - shipRepo: Repository to fetch ship details
//   - graphProvider: For loading waypoint coordinates
//   - targetWaypointSymbol: The destination waypoint symbol
//   - requiredCargoSymbol: The cargo needed for delivery (optional, for prioritization)
//   - playerID: Player ID for ship lookups
//
// Returns:
//   - shipSymbol: The symbol of the selected ship
//   - distance: The distance from the selected ship to the target
//   - error: Any error encountered
func SelectClosestShip(
	ctx context.Context,
	shipSymbols []string,
	shipRepo navigation.ShipRepository,
	graphProvider system.ISystemGraphProvider,
	targetWaypointSymbol string,
	requiredCargoSymbol string,
	playerID int,
) (string, float64, error) {
	if len(shipSymbols) == 0 {
		return "", 0, fmt.Errorf("no ships available for selection")
	}

	// 1. Fetch all ships from repository
	var ships []*navigation.Ship
	for _, shipSymbol := range shipSymbols {
		ship, err := shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return "", 0, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
		}
		ships = append(ships, ship)
	}

	// 2. Fetch target waypoint coordinates from graph
	systemSymbol := shared.ExtractSystemSymbol(targetWaypointSymbol)
	graphResult, err := graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to load system graph: %w", err)
	}

	// Extract waypoints map
	waypointsRaw, ok := graphResult.Graph["waypoints"].(map[string]interface{})
	if !ok {
		return "", 0, fmt.Errorf("invalid graph format: missing waypoints")
	}

	// Get target waypoint coordinates
	targetWpRaw, ok := waypointsRaw[targetWaypointSymbol].(map[string]interface{})
	if !ok {
		return "", 0, fmt.Errorf("target waypoint %s not found in graph", targetWaypointSymbol)
	}
	targetX := targetWpRaw["x"].(float64)
	targetY := targetWpRaw["y"].(float64)

	// Create target waypoint
	targetWaypoint, err := shared.NewWaypoint(targetWaypointSymbol, targetX, targetY)
	if err != nil {
		return "", 0, fmt.Errorf("failed to create target waypoint: %w", err)
	}

	// 3. Delegate to domain service for selection logic
	selector := domainContract.NewShipSelector()
	result, err := selector.SelectOptimalShip(ships, targetWaypoint, requiredCargoSymbol)
	if err != nil {
		return "", 0, err
	}

	// 4. Log selection for debugging
	fmt.Printf("[SHIP_SELECTOR] Selected %s - %s\n", result.Ship.ShipSymbol(), result.Reason)

	return result.Ship.ShipSymbol(), result.Distance, nil
}

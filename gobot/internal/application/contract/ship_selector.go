package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
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
//   - converter: For converting graph waypoint data to domain objects
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
	converter system.IWaypointConverter,
	targetWaypointSymbol string,
	requiredCargoSymbol string,
	playerID int,
) (string, float64, error) {
	logger := common.LoggerFromContext(ctx)

	if len(shipSymbols) == 0 {
		return "", 0, fmt.Errorf("no ships available for selection")
	}

	logger.Log("INFO", "Ship selection initiated", map[string]interface{}{
		"action":          "select_ship",
		"candidate_count": len(shipSymbols),
		"target_waypoint": targetWaypointSymbol,
		"required_cargo":  requiredCargoSymbol,
	})

	// 1. OPTIMIZATION: Fetch all ships from cached list (1 API call instead of N)
	// The ship list is cached for 15 seconds in ShipRepository.FindAllByPlayer
	allShips, err := shipRepo.FindAllByPlayer(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return "", 0, fmt.Errorf("failed to load ships: %w", err)
	}

	// Build lookup set for efficient filtering
	symbolSet := make(map[string]bool, len(shipSymbols))
	for _, s := range shipSymbols {
		symbolSet[s] = true
	}

	// Filter to only requested ships
	var ships []*navigation.Ship
	for _, ship := range allShips {
		if symbolSet[ship.ShipSymbol()] {
			ships = append(ships, ship)
		}
	}

	if len(ships) == 0 {
		return "", 0, fmt.Errorf("none of the requested ships found in fleet")
	}

	// 2. Fetch target waypoint coordinates from graph
	systemSymbol := shared.ExtractSystemSymbol(targetWaypointSymbol)
	graphResult, err := graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to load system graph: %w", err)
	}

	// Get target waypoint from navigation graph
	targetWaypoint, ok := graphResult.Graph.Waypoints[targetWaypointSymbol]
	if !ok {
		return "", 0, fmt.Errorf("target waypoint %s not found in graph", targetWaypointSymbol)
	}

	// 3. Delegate to domain service for selection logic
	selector := domainContract.NewShipSelector()
	result, err := selector.SelectOptimalShip(ships, targetWaypoint, requiredCargoSymbol)
	if err != nil {
		return "", 0, err
	}

	// 4. Log selection decision
	logger.Log("INFO", "Ship selection completed", map[string]interface{}{
		"action":          "ship_selected",
		"selected_ship":   result.Ship.ShipSymbol(),
		"distance":        result.Distance,
		"reason":          result.Reason,
		"target_waypoint": targetWaypointSymbol,
	})

	return result.Ship.ShipSymbol(), result.Distance, nil
}

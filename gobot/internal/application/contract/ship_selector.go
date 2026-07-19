package contract

import (
	"context"
	"fmt"
	"strings"

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
//   - unitsNeeded: Units still required for the delivery - used for hull
//     right-sizing, estimating round trips per candidate hull
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
	unitsNeeded int,
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

	// Fetches all ships once (cached ~15s in ShipRepository.FindAllByPlayer)
	// instead of one API call per candidate.
	allShips, err := shipRepo.FindAllByPlayer(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return "", 0, fmt.Errorf("failed to load ships: %w", err)
	}

	symbolSet := make(map[string]bool, len(shipSymbols))
	for _, s := range shipSymbols {
		symbolSet[s] = true
	}

	var ships []*navigation.Ship
	for _, ship := range allShips {
		if symbolSet[ship.ShipSymbol()] {
			ships = append(ships, ship)
		}
	}

	if len(ships) == 0 {
		return "", 0, fmt.Errorf("none of the requested ships found in fleet")
	}

	systemSymbol := shared.ExtractSystemSymbol(targetWaypointSymbol)
	graphResult, err := graphProvider.GetGraph(ctx, systemSymbol, false, playerID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to load system graph: %w", err)
	}

	targetWaypoint, ok := graphResult.Graph.Waypoints[targetWaypointSymbol]
	if !ok {
		return "", 0, fmt.Errorf("target waypoint %s not found in graph", targetWaypointSymbol)
	}

	selector := domainContract.NewShipSelector()
	result, err := selector.SelectOptimalShip(ships, targetWaypoint, requiredCargoSymbol, unitsNeeded)
	if err != nil {
		return "", 0, err
	}

	// Enumerates every candidate with its distance to the target (command ships
	// marked) so the pick is auditable - not just the winning symbol.
	logger.Log("INFO", "Ship selection completed", map[string]interface{}{
		"action":          "ship_selected",
		"selected_ship":   result.Ship.ShipSymbol(),
		"distance":        result.Distance,
		"reason":          result.Reason,
		"target_waypoint": targetWaypointSymbol,
		"candidates":      summarizeCandidates(ships, targetWaypoint),
	})

	return result.Ship.ShipSymbol(), result.Distance, nil
}

// summarizeCandidates renders every candidate ship with its distance to the
// selection target, marking command ships, so the selection log shows the full
// set behind a decision - not just the winner. Example:
// "TORWIND-3@0.00, TORWIND-1@52.10(command)".
func summarizeCandidates(ships []*navigation.Ship, target *shared.Waypoint) string {
	entries := make([]string, 0, len(ships))
	for _, ship := range ships {
		distance := ship.CurrentLocation().DistanceTo(target)
		entry := fmt.Sprintf("%s@%.2f", ship.ShipSymbol(), distance)
		if isCommandHull(ship) {
			entry += "(command)"
		}
		entries = append(entries, entry)
	}
	return strings.Join(entries, ", ")
}

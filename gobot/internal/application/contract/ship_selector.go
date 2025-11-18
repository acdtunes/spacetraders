package contract

import (
	"context"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// SelectClosestShip selects the ship closest to the target waypoint
// from a list of ship symbols. Prioritizes ships that already have the required cargo.
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
//   - shipSymbol: The symbol of the closest ship
//   - distance: The distance from the closest ship to the target
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

	// Extract system symbol from target
	systemSymbol := extractSystem(targetWaypointSymbol)

	// Load system graph to get waypoint coordinates
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

	var closestShip string
	minDistance := math.MaxFloat64
	var shipWithCargo string

	for _, shipSymbol := range shipSymbols {
		// Load ship to get current location and cargo
		ship, err := shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return "", 0, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
		}

		// PRIORITY CHECK: Does ship already have the required cargo?
		// Check this BEFORE transit status - a ship with cargo should be selected
		// even if in transit (it's likely mid-delivery after daemon restart)
		if requiredCargoSymbol != "" {
			cargoUnits := ship.Cargo().GetItemUnits(requiredCargoSymbol)
			if cargoUnits > 0 {
				fmt.Printf("[SHIP_SELECTOR] %s has %d units of %s in cargo - PRIORITY SELECTION\n",
					shipSymbol, cargoUnits, requiredCargoSymbol)
				shipWithCargo = shipSymbol
				// Don't break - continue checking all ships to log distances
			}
		}

		// Skip ships that are currently in transit (e.g., rebalancing)
		// But only if they don't have the required cargo (checked above)
		if ship.NavStatus() == navigation.NavStatusInTransit {
			fmt.Printf("[SHIP_SELECTOR] Skipping %s (IN_TRANSIT)\n", shipSymbol)
			continue
		}

		currentLocation := ship.CurrentLocation()

		// Calculate Euclidean distance
		var distance float64
		if currentLocation.Symbol == targetWaypointSymbol {
			distance = 0
		} else {
			// Get current location coordinates
			currentWpRaw, ok := waypointsRaw[currentLocation.Symbol].(map[string]interface{})
			if !ok {
				fmt.Printf("[SHIP_SELECTOR] Warning: Waypoint %s not in graph, using heuristic distance\n", currentLocation.Symbol)
				distance = 100
			} else {
				currentX := currentWpRaw["x"].(float64)
				currentY := currentWpRaw["y"].(float64)

				// Calculate Euclidean distance
				dx := targetX - currentX
				dy := targetY - currentY
				distance = math.Sqrt(dx*dx + dy*dy)
			}
		}

		// Track closest ship (as fallback if no ship has cargo)
		if distance < minDistance {
			minDistance = distance
			closestShip = shipSymbol
		}

		fmt.Printf("[SHIP_SELECTOR] %s at %s, distance to %s: %.2f units\n",
			shipSymbol, currentLocation.Symbol, targetWaypointSymbol, distance)
	}

	// Priority: Ship with cargo > Closest ship by distance
	if shipWithCargo != "" {
		fmt.Printf("[SHIP_SELECTOR] Selected %s (has required cargo)\n", shipWithCargo)
		return shipWithCargo, 0, nil
	}

	// Check if any ship was found (all might have been filtered out)
	if closestShip == "" {
		return "", 0, fmt.Errorf("no available ships found (all are in transit)")
	}

	fmt.Printf("[SHIP_SELECTOR] Selected %s (closest by distance: %.2f units)\n", closestShip, minDistance)
	return closestShip, minDistance, nil
}

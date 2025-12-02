package contract

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FindCoordinatorShips returns the list of ship symbols currently owned by the coordinator.
// These are ships that are assigned to the coordinator container and haven't been transferred to workers.
//
// Parameters:
//   - coordinatorID: The container ID of the coordinator
//   - playerID: Player ID for ship lookups
//   - shipRepo: Repository to query ships with assignments
//
// Returns:
//   - shipSymbols: List of ship symbols owned by the coordinator
//   - error: Any error encountered
func FindCoordinatorShips(
	ctx context.Context,
	coordinatorID string,
	playerID int,
	shipRepo navigation.ShipRepository,
) ([]string, error) {
	// Find all ships assigned to this coordinator
	ships, err := shipRepo.FindByContainer(ctx, coordinatorID, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to find coordinator ships: %w", err)
	}

	// Extract ship symbols
	shipSymbols := make([]string, 0, len(ships))
	for _, ship := range ships {
		shipSymbols = append(shipSymbols, ship.ShipSymbol())
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Coordinator ships retrieved", map[string]interface{}{
		"action":         "find_coordinator_ships",
		"coordinator_id": coordinatorID,
		"ship_count":     len(shipSymbols),
		"ships":          shipSymbols,
	})

	return shipSymbols, nil
}

// FindIdleLightHaulers finds all idle light hauler ships for a player.
//
// A ship is considered an "idle light hauler" if:
//   1. Ship role is "HAULER"
//   2. Ship has no active assignment (Ship.IsIdle() returns true)
//
// This provides a dynamic pool of available haulers without requiring pre-assignment.
// Ship assignment status is now embedded in the Ship aggregate and enriched by the repository.
//
// Parameters:
//   - ctx: Context for cancellation and logging
//   - playerID: Player ID to find ships for
//   - shipRepo: Repository to query ships (enriches assignment data automatically)
//
// Returns:
//   - ships: List of idle hauler ship entities
//   - shipSymbols: List of idle hauler ship symbols (for convenience)
//   - error: Any error encountered
func FindIdleLightHaulers(
	ctx context.Context,
	playerID shared.PlayerID,
	shipRepo navigation.ShipRepository,
) ([]*navigation.Ship, []string, error) {
	logger := common.LoggerFromContext(ctx)

	// Fetch all ships for player (includes assignment data via hybrid repo)
	allShips, err := shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch ships: %w", err)
	}

	var idleHaulers []*navigation.Ship
	var idleHaulerSymbols []string

	for _, ship := range allShips {
		// Filter 1: Only haulers
		if ship.Role() != "HAULER" {
			continue
		}

		// Filter 2: Must have cargo capacity (excludes probes/satellites tagged as haulers)
		if ship.CargoCapacity() == 0 {
			continue
		}

		// Filter 3: Exclude ships in transit (even without assignment)
		// Ships being balanced or navigating are not available for new contracts
		if ship.NavStatus() == navigation.NavStatusInTransit {
			continue
		}

		// Filter 4: Exclude command ship (ship #1 - the first ship, used for purchasing)
		// Command ships end in "-1" (e.g., "TORWIND-1", "AGENT-1")
		// These should remain available for manual operations
		if isCommandShip(ship.ShipSymbol()) {
			continue
		}

		// Filter 5: Only idle ships (no active assignment)
		// Ship.IsIdle() checks the embedded assignment state
		if ship.IsIdle() {
			idleHaulers = append(idleHaulers, ship)
			idleHaulerSymbols = append(idleHaulerSymbols, ship.ShipSymbol())
		}
	}

	logger.Log("INFO", "Idle light haulers discovered", map[string]interface{}{
		"action":         "find_idle_haulers",
		"total_ships":    len(allShips),
		"idle_haulers":   len(idleHaulers),
		"hauler_symbols": idleHaulerSymbols,
	})

	// Fallback: If no haulers found, use the first available idle ship with cargo capacity
	if len(idleHaulers) == 0 && len(allShips) > 0 {
		for _, ship := range allShips {
			// Skip ships in transit
			if ship.NavStatus() == navigation.NavStatusInTransit {
				continue
			}

			// Skip ships with no cargo capacity (probes/satellites)
			if ship.CargoCapacity() == 0 {
				continue
			}

			// Skip command ship (even in fallback mode)
			if isCommandShip(ship.ShipSymbol()) {
				continue
			}

			// Check if ship is idle
			if ship.IsIdle() {
				idleHaulers = append(idleHaulers, ship)
				idleHaulerSymbols = append(idleHaulerSymbols, ship.ShipSymbol())

				logger.Log("INFO", "Using fallback ship (no haulers available)", map[string]interface{}{
					"action":         "fallback_ship",
					"ship_symbol":    ship.ShipSymbol(),
					"ship_role":      ship.Role(),
					"cargo_capacity": ship.CargoCapacity(),
				})
				break // Only use first available ship as fallback
			}
		}
	}

	return idleHaulers, idleHaulerSymbols, nil
}

// isCommandShip checks if a ship symbol represents the command ship (ship #1).
// Command ships are reserved for manual operations like purchasing and should not
// be automatically assigned to manufacturing or other automated tasks.
//
// Ship symbols ending in "-1" are considered command ships (e.g., "TORWIND-1", "AGENT-1").
func isCommandShip(shipSymbol string) bool {
	return strings.HasSuffix(shipSymbol, "-1")
}

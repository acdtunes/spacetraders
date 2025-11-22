package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FindCoordinatorShips returns the list of ship symbols currently owned by the coordinator.
// These are ships that are assigned to the coordinator container and haven't been transferred to workers.
//
// Parameters:
//   - coordinatorID: The container ID of the coordinator
//   - playerID: Player ID for ship assignment lookups
//   - shipAssignmentRepo: Repository to query ship assignments
//
// Returns:
//   - shipSymbols: List of ship symbols owned by the coordinator
//   - error: Any error encountered
func FindCoordinatorShips(
	ctx context.Context,
	coordinatorID string,
	playerID int,
	shipAssignmentRepo container.ShipAssignmentRepository,
) ([]string, error) {
	// Find all ship assignments for this coordinator
	assignments, err := shipAssignmentRepo.FindByContainer(ctx, coordinatorID, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find coordinator ships: %w", err)
	}

	// Extract ship symbols
	shipSymbols := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		shipSymbols = append(shipSymbols, assignment.ShipSymbol())
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
//   2. Ship has no active assignment (no ShipAssignment record, or status is "idle")
//
// This provides a dynamic pool of available haulers without requiring pre-assignment.
//
// Parameters:
//   - ctx: Context for cancellation and logging
//   - playerID: Player ID to find ships for
//   - shipRepo: Repository to query ships
//   - shipAssignmentRepo: Repository to check assignment status
//
// Returns:
//   - ships: List of idle hauler ship entities
//   - shipSymbols: List of idle hauler ship symbols (for convenience)
//   - error: Any error encountered
func FindIdleLightHaulers(
	ctx context.Context,
	playerID shared.PlayerID,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
) ([]*navigation.Ship, []string, error) {
	logger := common.LoggerFromContext(ctx)

	// Fetch all ships for player
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

		// Filter 2: Exclude ships in transit (even without assignment)
		// Ships being balanced or navigating are not available for new contracts
		if ship.NavStatus() == navigation.NavStatusInTransit {
			continue
		}

		// Filter 3: Only idle ships (no active assignment)
		assignment, err := shipAssignmentRepo.FindByShip(ctx, ship.ShipSymbol(), playerID.Value())
		if err != nil {
			// If error fetching assignment, assume ship is not assigned
			idleHaulers = append(idleHaulers, ship)
			idleHaulerSymbols = append(idleHaulerSymbols, ship.ShipSymbol())
			continue
		}

		// Include ships with no assignment or idle status
		if assignment == nil || assignment.Status() == "idle" {
			idleHaulers = append(idleHaulers, ship)
			idleHaulerSymbols = append(idleHaulerSymbols, ship.ShipSymbol())
		}
	}

	logger.Log("INFO", "Idle light haulers discovered", map[string]interface{}{
		"action":           "find_idle_haulers",
		"total_ships":      len(allShips),
		"idle_haulers":     len(idleHaulers),
		"hauler_symbols":   idleHaulerSymbols,
	})

	return idleHaulers, idleHaulerSymbols, nil
}

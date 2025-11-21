package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
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

// CreatePoolAssignments creates ship assignments for all ships in the pool,
// assigning them to the coordinator container.
//
// Parameters:
//   - shipSymbols: List of ship symbols to assign to the pool
//   - coordinatorID: The container ID of the coordinator
//   - playerID: Player ID for ship assignments
//   - shipAssignmentRepo: Repository to create assignments
//
// Returns:
//   - error: Any error encountered
func CreatePoolAssignments(
	ctx context.Context,
	shipSymbols []string,
	coordinatorID string,
	playerID int,
	shipAssignmentRepo container.ShipAssignmentRepository,
) error {
	logger := common.LoggerFromContext(ctx)

	for _, shipSymbol := range shipSymbols {
		assignment := container.NewShipAssignment(
			shipSymbol,
			playerID,
			coordinatorID,
			nil, // Use nil for clock - the entity will use default
		)

		if err := shipAssignmentRepo.Assign(ctx, assignment); err != nil {
			return fmt.Errorf("failed to assign ship %s to coordinator: %w", shipSymbol, err)
		}

		logger.Log("INFO", "Ship assigned to coordinator", map[string]interface{}{
			"action":         "assign_ship",
			"ship_symbol":    shipSymbol,
			"coordinator_id": coordinatorID,
		})
	}

	logger.Log("INFO", "Ship pool created", map[string]interface{}{
		"action":     "create_pool",
		"ship_count": len(shipSymbols),
	})

	return nil
}

// ReleasePoolAssignments releases all ship assignments owned by the coordinator.
//
// Parameters:
//   - coordinatorID: The container ID of the coordinator
//   - playerID: Player ID for ship assignments
//   - shipAssignmentRepo: Repository to release assignments
//   - reason: Reason for release (e.g., "coordinator_stopped")
//
// Returns:
//   - error: Any error encountered
func ReleasePoolAssignments(
	ctx context.Context,
	coordinatorID string,
	playerID int,
	shipAssignmentRepo container.ShipAssignmentRepository,
	reason string,
) error {
	if err := shipAssignmentRepo.ReleaseByContainer(ctx, coordinatorID, playerID, reason); err != nil {
		return fmt.Errorf("failed to release pool assignments: %w", err)
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Ship pool released", map[string]interface{}{
		"action":         "release_pool",
		"coordinator_id": coordinatorID,
		"reason":         reason,
	})

	return nil
}

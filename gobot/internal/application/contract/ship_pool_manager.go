package contract

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
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
	shipAssignmentRepo daemon.ShipAssignmentRepository,
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

	fmt.Printf("[SHIP_POOL] Coordinator %s owns %d ships: %v\n",
		coordinatorID, len(shipSymbols), shipSymbols)

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
	shipAssignmentRepo daemon.ShipAssignmentRepository,
) error {
	for _, shipSymbol := range shipSymbols {
		assignment := daemon.NewShipAssignment(
			shipSymbol,
			playerID,
			coordinatorID,
			nil, // Use nil for clock - the entity will use default
		)

		if err := shipAssignmentRepo.Insert(ctx, assignment); err != nil {
			return fmt.Errorf("failed to assign ship %s to coordinator: %w", shipSymbol, err)
		}

		fmt.Printf("[SHIP_POOL] Assigned %s to coordinator %s\n", shipSymbol, coordinatorID)
	}

	fmt.Printf("[SHIP_POOL] Created pool with %d ships\n", len(shipSymbols))

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
	shipAssignmentRepo daemon.ShipAssignmentRepository,
	reason string,
) error {
	if err := shipAssignmentRepo.ReleaseByContainer(ctx, coordinatorID, playerID, reason); err != nil {
		return fmt.Errorf("failed to release pool assignments: %w", err)
	}

	fmt.Printf("[SHIP_POOL] Released all ships for coordinator %s (reason: %s)\n",
		coordinatorID, reason)

	return nil
}

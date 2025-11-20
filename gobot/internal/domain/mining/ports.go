package mining

import "context"

// MiningOperationRepository defines the interface for mining operation persistence
type MiningOperationRepository interface {
	// Add creates a new mining operation in the database
	Add(ctx context.Context, op *MiningOperation) error

	// FindByID retrieves a mining operation by its ID and player ID
	FindByID(ctx context.Context, id string, playerID int) (*MiningOperation, error)

	// Save persists changes to an existing mining operation
	Save(ctx context.Context, op *MiningOperation) error

	// Remove removes a mining operation from the database
	Remove(ctx context.Context, id string, playerID int) error

	// FindActive retrieves all active (RUNNING) operations for a player
	FindActive(ctx context.Context, playerID int) ([]*MiningOperation, error)

	// FindByStatus retrieves all operations with a given status for a player
	FindByStatus(ctx context.Context, playerID int, status OperationStatus) ([]*MiningOperation, error)
}

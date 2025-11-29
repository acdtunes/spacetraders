package gas

import "context"

// OperationRepository defines the interface for gas operation persistence
type OperationRepository interface {
	// Add creates a new gas operation in the database
	Add(ctx context.Context, op *Operation) error

	// FindByID retrieves a gas operation by its ID and player ID
	FindByID(ctx context.Context, id string, playerID int) (*Operation, error)

	// Save persists changes to an existing gas operation
	Save(ctx context.Context, op *Operation) error

	// Remove removes a gas operation from the database
	Remove(ctx context.Context, id string, playerID int) error

	// FindActive retrieves all active (RUNNING) operations for a player
	FindActive(ctx context.Context, playerID int) ([]*Operation, error)

	// FindByStatus retrieves all operations with a given status for a player
	FindByStatus(ctx context.Context, playerID int, status OperationStatus) ([]*Operation, error)
}

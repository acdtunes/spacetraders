package mining

import "context"

// MiningOperationRepository defines the interface for mining operation persistence
type MiningOperationRepository interface {
	// Insert creates a new mining operation in the database
	Insert(ctx context.Context, op *MiningOperation) error

	// FindByID retrieves a mining operation by its ID and player ID
	FindByID(ctx context.Context, id string, playerID int) (*MiningOperation, error)

	// Update persists changes to an existing mining operation
	Update(ctx context.Context, op *MiningOperation) error

	// Delete removes a mining operation from the database
	Delete(ctx context.Context, id string, playerID int) error

	// FindActive retrieves all active (RUNNING) operations for a player
	FindActive(ctx context.Context, playerID int) ([]*MiningOperation, error)

	// FindByStatus retrieves all operations with a given status for a player
	FindByStatus(ctx context.Context, playerID int, status OperationStatus) ([]*MiningOperation, error)
}

// CargoTransferQueueRepository defines the interface for cargo transfer queue persistence
type CargoTransferQueueRepository interface {
	// Enqueue adds a new cargo transfer request to the queue
	Enqueue(ctx context.Context, transfer *CargoTransferRequest) error

	// FindPendingForMiner retrieves a pending transfer request for a specific miner
	FindPendingForMiner(ctx context.Context, minerShip string) (*CargoTransferRequest, error)

	// FindPendingForOperation retrieves all pending transfers for an operation
	FindPendingForOperation(ctx context.Context, operationID string) ([]*CargoTransferRequest, error)

	// MarkInProgress updates a transfer to IN_PROGRESS and assigns the transport ship
	MarkInProgress(ctx context.Context, transferID string, transportShip string) error

	// MarkCompleted updates a transfer to COMPLETED
	MarkCompleted(ctx context.Context, transferID string) error

	// DeleteByOperation removes all transfers for a specific operation
	DeleteByOperation(ctx context.Context, operationID string) error
}

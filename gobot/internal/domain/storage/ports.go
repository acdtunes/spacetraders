package storage

import "context"

// StorageCoordinator manages all storage ships across all operations.
// It provides thread-safe access to cargo with reservation tracking and
// blocking wait capabilities for haulers.
//
// Key responsibilities:
// - Register/unregister storage ships
// - Track cargo levels with reservation system
// - Provide blocking WaitForCargo for haulers
// - Notify waiting haulers when extractors deposit cargo
// - Filter by operation ID for multiple simultaneous operations
type StorageCoordinator interface {
	// RegisterStorageShip adds a storage ship to the coordinator
	RegisterStorageShip(ship *StorageShip) error

	// UnregisterStorageShip removes a storage ship from the coordinator
	UnregisterStorageShip(shipSymbol string)

	// WaitForCargo blocks until cargo is available and reserved.
	// Returns the storage ship with reserved cargo and the amount reserved.
	// The caller MUST call ConfirmTransfer or CancelReservation after this returns.
	// If context is cancelled, returns error and no reservation is held.
	//
	// Parameters:
	// - ctx: Context for cancellation
	// - operationID: Filter by specific operation
	// - goodSymbol: The cargo type to wait for
	// - minUnits: Minimum units required (will reserve up to hauler capacity)
	//
	// Returns:
	// - StorageShip with reserved cargo
	// - Units reserved (>= minUnits)
	// - Error if context cancelled or operation not found
	WaitForCargo(ctx context.Context, operationID, goodSymbol string, minUnits int) (*StorageShip, int, error)

	// NotifyCargoDeposited is called by extractors after depositing cargo.
	// This wakes any waiting haulers if they can now be satisfied.
	NotifyCargoDeposited(storageShipSymbol, goodSymbol string, units int)

	// GetTotalCargoAvailable returns total unreserved cargo for a good across all
	// storage ships in the specified operation.
	GetTotalCargoAvailable(operationID, goodSymbol string) int

	// FindStorageShipWithSpace finds a storage ship in the operation with available space.
	// Used by extractors to find where to deposit cargo.
	FindStorageShipWithSpace(operationID string, minSpace int) (*StorageShip, bool)

	// GetStorageShipBySymbol retrieves a storage ship by its symbol
	GetStorageShipBySymbol(shipSymbol string) (*StorageShip, bool)

	// GetStorageShipsForOperation returns all storage ships for an operation
	GetStorageShipsForOperation(operationID string) []*StorageShip

	// SubscribeToDeposits returns a channel that receives notifications when
	// cargo is deposited to a specific storage ship. Used by storage ship workers
	// to react to deposits (e.g., jettison HYDROCARBON) without polling.
	// The returned unsubscribe function MUST be called to clean up.
	SubscribeToDeposits(shipSymbol string) (notifications <-chan CargoDepositNotification, unsubscribe func())
}

// CargoDepositNotification contains details about a cargo deposit event
type CargoDepositNotification struct {
	GoodSymbol string
	Units      int
}

// StorageOperationRepository handles persistence of storage operations
type StorageOperationRepository interface {
	// Create persists a new storage operation
	Create(ctx context.Context, operation *StorageOperation) error

	// Update saves changes to an existing storage operation
	Update(ctx context.Context, operation *StorageOperation) error

	// FindByID retrieves a storage operation by its ID
	FindByID(ctx context.Context, id string) (*StorageOperation, error)

	// FindByPlayerID retrieves all storage operations for a player
	FindByPlayerID(ctx context.Context, playerID int) ([]*StorageOperation, error)

	// FindByStatus retrieves storage operations by status for a player
	FindByStatus(ctx context.Context, playerID int, statuses []OperationStatus) ([]*StorageOperation, error)

	// FindByGood retrieves storage operations that support a specific good
	FindByGood(ctx context.Context, playerID int, goodSymbol string) ([]*StorageOperation, error)

	// FindRunning retrieves all running storage operations for a player
	FindRunning(ctx context.Context, playerID int) ([]*StorageOperation, error)

	// FindRunningByWaypoint retrieves the first running storage operation for a specific waypoint (e.g., gas giant)
	// Returns nil if no operation exists for that waypoint
	// NOTE: Use FindAllRunningByWaypoint to get ALL running operations (to stop duplicates)
	FindRunningByWaypoint(ctx context.Context, playerID int, waypointSymbol string) (*StorageOperation, error)

	// FindAllRunningByWaypoint retrieves ALL running storage operations for a specific waypoint
	// Used to stop all old operations when starting a new one (prevents duplicate operations)
	FindAllRunningByWaypoint(ctx context.Context, playerID int, waypointSymbol string) ([]*StorageOperation, error)

	// Delete removes a storage operation
	Delete(ctx context.Context, id string) error
}

// CargoWaiter represents a hauler waiting for cargo from storage ships.
// Used internally by StorageCoordinator for FIFO queue management.
type CargoWaiter struct {
	OperationID string
	GoodSymbol  string
	MinUnits    int
	ResultChan  chan *CargoWaiterResult
}

// CargoWaiterResult contains the result of a cargo wait operation
type CargoWaiterResult struct {
	StorageShip   *StorageShip
	UnitsReserved int
	Error         error
}

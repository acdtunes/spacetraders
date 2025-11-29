package storage

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// OperationType represents the type of extraction operation
type OperationType string

const (
	// OperationTypeGasSiphon extracts gas from gas giants
	OperationTypeGasSiphon OperationType = "GAS_SIPHON"

	// OperationTypeMining extracts ore from asteroids
	OperationTypeMining OperationType = "MINING"

	// OperationTypeCustom for future resource types
	OperationTypeCustom OperationType = "CUSTOM"
)

// OperationStatus represents the lifecycle state of a storage operation
type OperationStatus string

const (
	OperationStatusPending   OperationStatus = "PENDING"
	OperationStatusRunning   OperationStatus = "RUNNING"
	OperationStatusCompleted OperationStatus = "COMPLETED"
	OperationStatusStopped   OperationStatus = "STOPPED"
	OperationStatusFailed    OperationStatus = "FAILED"
)

// StorageOperation represents a cargo extraction and buffering operation.
// It coordinates extractor ships that extract resources and storage ships that buffer
// cargo until haulers (from the manufacturing pool) collect and deliver to destinations.
//
// This is the aggregate root for storage operations.
//
// Invariants:
// - Must have at least one extractor ship
// - Must have at least one storage ship
// - Waypoint must be specified
// - Supported goods must be specified
type StorageOperation struct {
	id             string
	playerID       int
	waypointSymbol string        // Extraction location (gas giant, asteroid field)
	operationType  OperationType // GAS_SIPHON, MINING, CUSTOM
	extractorShips []string      // Ships that extract resources
	storageShips   []string      // Ships that buffer cargo (stay in orbit)
	supportedGoods []string      // Goods this operation produces
	lifecycle      *shared.LifecycleStateMachine
}

// NewStorageOperation creates a new storage operation instance
func NewStorageOperation(
	id string,
	playerID int,
	waypointSymbol string,
	operationType OperationType,
	extractorShips []string,
	storageShips []string,
	supportedGoods []string,
	clock shared.Clock,
) (*StorageOperation, error) {
	if id == "" {
		return nil, fmt.Errorf("operation ID cannot be empty")
	}
	if playerID <= 0 {
		return nil, fmt.Errorf("player ID must be positive")
	}
	if waypointSymbol == "" {
		return nil, fmt.Errorf("waypoint symbol cannot be empty")
	}
	if len(extractorShips) == 0 {
		return nil, fmt.Errorf("operation must have at least 1 extractor ship")
	}
	if len(storageShips) == 0 {
		return nil, fmt.Errorf("operation must have at least 1 storage ship")
	}
	if len(supportedGoods) == 0 {
		return nil, fmt.Errorf("operation must specify supported goods")
	}

	// Copy slices to avoid external mutation
	extractors := make([]string, len(extractorShips))
	copy(extractors, extractorShips)

	storage := make([]string, len(storageShips))
	copy(storage, storageShips)

	goods := make([]string, len(supportedGoods))
	copy(goods, supportedGoods)

	return &StorageOperation{
		id:             id,
		playerID:       playerID,
		waypointSymbol: waypointSymbol,
		operationType:  operationType,
		extractorShips: extractors,
		storageShips:   storage,
		supportedGoods: goods,
		lifecycle:      shared.NewLifecycleStateMachine(clock),
	}, nil
}

// Getters

func (op *StorageOperation) ID() string                { return op.id }
func (op *StorageOperation) PlayerID() int             { return op.playerID }
func (op *StorageOperation) WaypointSymbol() string    { return op.waypointSymbol }
func (op *StorageOperation) OperationType() OperationType { return op.operationType }
func (op *StorageOperation) ExtractorShips() []string  { return op.extractorShips }
func (op *StorageOperation) StorageShips() []string    { return op.storageShips }
func (op *StorageOperation) SupportedGoods() []string  { return op.supportedGoods }

// Lifecycle getters delegated to state machine
func (op *StorageOperation) LastError() error      { return op.lifecycle.LastError() }
func (op *StorageOperation) CreatedAt() time.Time  { return op.lifecycle.CreatedAt() }
func (op *StorageOperation) UpdatedAt() time.Time  { return op.lifecycle.UpdatedAt() }
func (op *StorageOperation) StartedAt() *time.Time { return op.lifecycle.StartedAt() }
func (op *StorageOperation) StoppedAt() *time.Time { return op.lifecycle.StoppedAt() }

// Status converts from LifecycleStatus to OperationStatus
func (op *StorageOperation) Status() OperationStatus {
	switch op.lifecycle.Status() {
	case shared.LifecycleStatusPending:
		return OperationStatusPending
	case shared.LifecycleStatusRunning:
		return OperationStatusRunning
	case shared.LifecycleStatusCompleted:
		return OperationStatusCompleted
	case shared.LifecycleStatusStopped:
		return OperationStatusStopped
	case shared.LifecycleStatusFailed:
		return OperationStatusFailed
	default:
		return OperationStatusPending
	}
}

// State transition methods

// Start transitions the operation from PENDING to RUNNING
func (op *StorageOperation) Start() error {
	status := op.lifecycle.Status()
	if status != shared.LifecycleStatusPending {
		return fmt.Errorf("cannot start operation in %s state", op.Status())
	}
	return op.lifecycle.Start()
}

// Stop transitions the operation to STOPPED state
func (op *StorageOperation) Stop() error {
	status := op.lifecycle.Status()
	if status == shared.LifecycleStatusCompleted || status == shared.LifecycleStatusStopped {
		return fmt.Errorf("cannot stop operation in %s state", op.Status())
	}
	return op.lifecycle.Stop()
}

// Complete transitions the operation to COMPLETED state
func (op *StorageOperation) Complete() error {
	status := op.lifecycle.Status()
	if status != shared.LifecycleStatusRunning {
		return fmt.Errorf("cannot complete operation in %s state", op.Status())
	}
	return op.lifecycle.Complete()
}

// Fail transitions the operation to FAILED state with error
func (op *StorageOperation) Fail(err error) error {
	status := op.lifecycle.Status()
	if status == shared.LifecycleStatusCompleted || status == shared.LifecycleStatusStopped {
		return fmt.Errorf("cannot fail operation in %s state", op.Status())
	}
	return op.lifecycle.Fail(err)
}

// Query methods

// IsRunning returns true if the operation is currently executing
func (op *StorageOperation) IsRunning() bool {
	return op.lifecycle.IsRunning()
}

// IsFinished returns true if the operation has completed, failed, or stopped
func (op *StorageOperation) IsFinished() bool {
	return op.lifecycle.IsFinished()
}

// IsPending returns true if the operation hasn't started yet
func (op *StorageOperation) IsPending() bool {
	return op.lifecycle.IsPending()
}

// SupportsGood checks if operation produces the specified good
func (op *StorageOperation) SupportsGood(goodSymbol string) bool {
	for _, g := range op.supportedGoods {
		if g == goodSymbol {
			return true
		}
	}
	return false
}

// RuntimeDuration calculates how long the operation has been running
func (op *StorageOperation) RuntimeDuration() time.Duration {
	return op.lifecycle.RuntimeDuration()
}

// String provides human-readable representation
func (op *StorageOperation) String() string {
	return fmt.Sprintf("StorageOperation[%s, type=%s, status=%s, waypoint=%s, extractors=%d, storage=%d, goods=%v]",
		op.id, op.operationType, op.Status(), op.waypointSymbol,
		len(op.extractorShips), len(op.storageShips), op.supportedGoods)
}

// StorageOperationData is the DTO for persisting storage operations
type StorageOperationData struct {
	ID             string
	PlayerID       int
	WaypointSymbol string
	OperationType  string
	Status         string
	ExtractorShips []string
	StorageShips   []string
	SupportedGoods []string
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      *time.Time
	StoppedAt      *time.Time
}

// ToData converts the entity to a DTO for persistence
func (op *StorageOperation) ToData() *StorageOperationData {
	var lastErr string
	if op.lifecycle.LastError() != nil {
		lastErr = op.lifecycle.LastError().Error()
	}

	return &StorageOperationData{
		ID:             op.id,
		PlayerID:       op.playerID,
		WaypointSymbol: op.waypointSymbol,
		OperationType:  string(op.operationType),
		Status:         string(op.Status()),
		ExtractorShips: op.extractorShips,
		StorageShips:   op.storageShips,
		SupportedGoods: op.supportedGoods,
		LastError:      lastErr,
		CreatedAt:      op.lifecycle.CreatedAt(),
		UpdatedAt:      op.lifecycle.UpdatedAt(),
		StartedAt:      op.lifecycle.StartedAt(),
		StoppedAt:      op.lifecycle.StoppedAt(),
	}
}

// StorageOperationFromData creates a StorageOperation entity from a DTO
func StorageOperationFromData(data *StorageOperationData, clock shared.Clock) *StorageOperation {
	lifecycle := shared.NewLifecycleStateMachine(clock)

	// Convert OperationStatus to LifecycleStatus
	var lifecycleStatus shared.LifecycleStatus
	switch OperationStatus(data.Status) {
	case OperationStatusPending:
		lifecycleStatus = shared.LifecycleStatusPending
	case OperationStatusRunning:
		lifecycleStatus = shared.LifecycleStatusRunning
	case OperationStatusCompleted:
		lifecycleStatus = shared.LifecycleStatusCompleted
	case OperationStatusStopped:
		lifecycleStatus = shared.LifecycleStatusStopped
	case OperationStatusFailed:
		lifecycleStatus = shared.LifecycleStatusFailed
	default:
		lifecycleStatus = shared.LifecycleStatusPending
	}

	// Parse last error
	var lastErr error
	if data.LastError != "" {
		lastErr = fmt.Errorf("%s", data.LastError)
	}

	// Recover lifecycle state from persistence
	lifecycle.RecoverFromPersistence(
		lifecycleStatus,
		data.CreatedAt,
		data.UpdatedAt,
		data.StartedAt,
		data.StoppedAt,
		lastErr,
	)

	return &StorageOperation{
		id:             data.ID,
		playerID:       data.PlayerID,
		waypointSymbol: data.WaypointSymbol,
		operationType:  OperationType(data.OperationType),
		extractorShips: data.ExtractorShips,
		storageShips:   data.StorageShips,
		supportedGoods: data.SupportedGoods,
		lifecycle:      lifecycle,
	}
}

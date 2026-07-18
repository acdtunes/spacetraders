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

	// OperationTypeWarehouse buffers arbitrary contract goods at a home waypoint.
	// Unlike the extractor-fed types above, a warehouse has ZERO extractor ships:
	// cargo arrives from HAULERS (tour/trade deposit legs), not from extraction.
	// It reuses the StorageShip deposit/withdraw protocols and StorageCoordinator
	// unchanged — see NewWarehouseOperation.
	OperationTypeWarehouse OperationType = "WAREHOUSE"
)

// AllOperationTypes returns every OperationType constant. sp-cu42: the
// schema_enum_drift_test.go gate derives its valid_operation_type
// migration-coverage check from this list instead of a hand-copied constant
// list, so a new operation type here is checked against the storage_operations
// CHECK constraint automatically. Keep this in sync with the const block above
// — it is the ONE place that must be updated when adding a type (versus the
// two-hop miss that shipped WAREHOUSE without a migration: a new constant here
// plus a separate, easy-to-forget copy in a distant test file).
func AllOperationTypes() []OperationType {
	return []OperationType{
		OperationTypeGasSiphon,
		OperationTypeMining,
		OperationTypeCustom,
		OperationTypeWarehouse,
	}
}

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
//   - Extractor-fed types (GAS_SIPHON, MINING) must have at least one extractor
//     ship; a WAREHOUSE (NewWarehouseOperation) has zero — cargo arrives from
//     haulers, not extractors.
//   - Must have at least one storage ship
//   - Waypoint must be specified
//   - Supported goods must be specified
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
	if err := validateOperationIdentity(id, playerID, waypointSymbol); err != nil {
		return nil, err
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

	return &StorageOperation{
		id:             id,
		playerID:       playerID,
		waypointSymbol: waypointSymbol,
		operationType:  operationType,
		extractorShips: copyStrings(extractorShips),
		storageShips:   copyStrings(storageShips),
		supportedGoods: copyStrings(supportedGoods),
		lifecycle:      shared.NewLifecycleStateMachine(clock),
	}, nil
}

// validateOperationIdentity guards the identity fields every storage-operation
// constructor requires, with the exact messages both constructors share.
func validateOperationIdentity(id string, playerID int, waypointSymbol string) error {
	if id == "" {
		return fmt.Errorf("operation ID cannot be empty")
	}
	if playerID <= 0 {
		return fmt.Errorf("player ID must be positive")
	}
	if waypointSymbol == "" {
		return fmt.Errorf("waypoint symbol cannot be empty")
	}
	return nil
}

// copyStrings copies a slice so external mutation of the caller's slice can
// never reach the aggregate.
func copyStrings(values []string) []string {
	copied := make([]string, len(values))
	copy(copied, values)
	return copied
}

// NewWarehouseOperation creates a passive warehouse storage operation: a
// dedicated hull parked at a home waypoint that BUFFERS arbitrary contract
// goods deposited by haulers. It is the extractor-free sibling of
// NewStorageOperation (sp-dchv Lane B): a warehouse has ZERO extractor ships
// because cargo arrives from tour/trade deposit legs, not from extraction, so
// the >=1-extractor invariant is inapplicable and replaced by >=1 storage ship
// plus a non-empty supported-goods whitelist.
//
// Everything downstream is shared UNCHANGED with extractor-fed operations: the
// StorageShip deposit (ReserveSpace/ConfirmDeposit) and withdrawal
// (TryReserveCargo/ConfirmTransfer) protocols, the StorageCoordinator, the
// storage_operations persistence, and the StorageRecoveryService live-cargo
// rebuild on daemon restart (RULINGS #2) — recovery reads only StorageShips()
// and WaypointSymbol(), never extractors, so a zero-extractor warehouse
// reconstructs identically.
func NewWarehouseOperation(
	id string,
	playerID int,
	waypointSymbol string,
	storageShips []string,
	supportedGoods []string,
	clock shared.Clock,
) (*StorageOperation, error) {
	if err := validateOperationIdentity(id, playerID, waypointSymbol); err != nil {
		return nil, err
	}
	if len(storageShips) == 0 {
		return nil, fmt.Errorf("warehouse operation must have at least 1 storage ship")
	}
	if len(supportedGoods) == 0 {
		return nil, fmt.Errorf("warehouse operation must specify supported goods")
	}

	// Extractors are deliberately empty: a warehouse is fed by haulers, not
	// extractors.
	return &StorageOperation{
		id:             id,
		playerID:       playerID,
		waypointSymbol: waypointSymbol,
		operationType:  OperationTypeWarehouse,
		extractorShips: []string{},
		storageShips:   copyStrings(storageShips),
		supportedGoods: copyStrings(supportedGoods),
		lifecycle:      shared.NewLifecycleStateMachine(clock),
	}, nil
}

// Getters

func (op *StorageOperation) ID() string                   { return op.id }
func (op *StorageOperation) PlayerID() int                { return op.playerID }
func (op *StorageOperation) WaypointSymbol() string       { return op.waypointSymbol }
func (op *StorageOperation) OperationType() OperationType { return op.operationType }
func (op *StorageOperation) ExtractorShips() []string     { return op.extractorShips }
func (op *StorageOperation) StorageShips() []string       { return op.storageShips }
func (op *StorageOperation) SupportedGoods() []string     { return op.supportedGoods }

// Lifecycle getters delegated to state machine
func (op *StorageOperation) LastError() error      { return op.lifecycle.LastError() }
func (op *StorageOperation) CreatedAt() time.Time  { return op.lifecycle.CreatedAt() }
func (op *StorageOperation) UpdatedAt() time.Time  { return op.lifecycle.UpdatedAt() }
func (op *StorageOperation) StartedAt() *time.Time { return op.lifecycle.StartedAt() }
func (op *StorageOperation) StoppedAt() *time.Time { return op.lifecycle.StoppedAt() }

// operationStatusByLifecycle projects each shared lifecycle state onto the
// storage-operation status. A lifecycle state absent here falls back to
// OperationStatusPending (the former switch's default).
var operationStatusByLifecycle = map[shared.LifecycleStatus]OperationStatus{
	shared.LifecycleStatusPending:   OperationStatusPending,
	shared.LifecycleStatusRunning:   OperationStatusRunning,
	shared.LifecycleStatusCompleted: OperationStatusCompleted,
	shared.LifecycleStatusStopped:   OperationStatusStopped,
	shared.LifecycleStatusFailed:    OperationStatusFailed,
}

// Status converts from LifecycleStatus to OperationStatus
func (op *StorageOperation) Status() OperationStatus {
	return shared.ProjectStatus(op.lifecycle, operationStatusByLifecycle, OperationStatusPending)
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

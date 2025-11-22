package mining

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// OperationStatus represents the lifecycle state of a mining operation
type OperationStatus string

const (
	OperationStatusPending   OperationStatus = "PENDING"
	OperationStatusRunning   OperationStatus = "RUNNING"
	OperationStatusCompleted OperationStatus = "COMPLETED"
	OperationStatusStopped   OperationStatus = "STOPPED"
	OperationStatusFailed    OperationStatus = "FAILED"
)

// Operation represents a complete mining operation aggregate root
// It orchestrates mining ships extracting from asteroids and transport ships
// selling the cargo at optimal markets.
type Operation struct {
	id             string
	playerID       int
	asteroidField  string // Waypoint symbol of the asteroid field
	topNOres       int      // Number of ore types to keep (jettison rest)
	minerShips     []string // Ship symbols for mining
	transportShips []string // Ship symbols for transport

	// Batch collection configuration
	batchThreshold int // Number of miners to accumulate before spawning transport
	batchTimeout   int // Seconds to wait before spawning transport anyway

	// Iteration control
	maxIterations int // -1 for infinite

	// Lifecycle state machine handles status, timestamps, and errors
	lifecycle *shared.LifecycleStateMachine
}

// NewOperation creates a new mining operation instance
func NewOperation(
	id string,
	playerID int,
	asteroidField string,
	minerShips []string,
	transportShips []string,
	topNOres int,
	batchThreshold int,
	batchTimeout int,
	maxIterations int,
	clock shared.Clock,
) *Operation {
	// Copy slices to avoid external mutation
	miners := make([]string, len(minerShips))
	copy(miners, minerShips)

	transports := make([]string, len(transportShips))
	copy(transports, transportShips)

	return &Operation{
		id:             id,
		playerID:       playerID,
		asteroidField:  asteroidField,
		topNOres:       topNOres,
		minerShips:     miners,
		transportShips: transports,
		batchThreshold: batchThreshold,
		batchTimeout:   batchTimeout,
		maxIterations:  maxIterations,
		lifecycle:      shared.NewLifecycleStateMachine(clock),
	}
}

// Getters

func (op *Operation) ID() string               { return op.id }
func (op *Operation) PlayerID() int            { return op.playerID }
func (op *Operation) AsteroidField() string    { return op.asteroidField }
func (op *Operation) TopNOres() int            { return op.topNOres }
func (op *Operation) MinerShips() []string     { return op.minerShips }
func (op *Operation) TransportShips() []string { return op.transportShips }
func (op *Operation) BatchThreshold() int      { return op.batchThreshold }
func (op *Operation) BatchTimeout() int        { return op.batchTimeout }
func (op *Operation) MaxIterations() int       { return op.maxIterations }

// Lifecycle getters delegated to state machine
func (op *Operation) LastError() error      { return op.lifecycle.LastError() }
func (op *Operation) CreatedAt() time.Time  { return op.lifecycle.CreatedAt() }
func (op *Operation) UpdatedAt() time.Time  { return op.lifecycle.UpdatedAt() }
func (op *Operation) StartedAt() *time.Time { return op.lifecycle.StartedAt() }
func (op *Operation) StoppedAt() *time.Time { return op.lifecycle.StoppedAt() }

// Status converts from LifecycleStatus to OperationStatus
func (op *Operation) Status() OperationStatus {
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
		return OperationStatusPending // Safe default
	}
}

// State transition methods

// Start transitions the operation from PENDING to RUNNING
func (op *Operation) Start() error {
	// Check if we can start from current state
	status := op.lifecycle.Status()
	if status != shared.LifecycleStatusPending {
		return fmt.Errorf("cannot start operation in %s state", op.Status())
	}

	// Validate before starting
	if err := op.Validate(); err != nil {
		return fmt.Errorf("operation validation failed: %w", err)
	}

	// Delegate to lifecycle state machine
	return op.lifecycle.Start()
}

// Stop transitions the operation to STOPPED state
func (op *Operation) Stop() error {
	// Check if we can stop from current state
	status := op.lifecycle.Status()
	if status == shared.LifecycleStatusCompleted || status == shared.LifecycleStatusStopped {
		return fmt.Errorf("cannot stop operation in %s state", op.Status())
	}

	return op.lifecycle.Stop()
}

// Complete transitions the operation to COMPLETED state
func (op *Operation) Complete() error {
	// Check if we can complete from current state
	status := op.lifecycle.Status()
	if status != shared.LifecycleStatusRunning {
		return fmt.Errorf("cannot complete operation in %s state", op.Status())
	}

	return op.lifecycle.Complete()
}

// Fail transitions the operation to FAILED state with error
func (op *Operation) Fail(err error) error {
	// Check if we can fail from current state
	status := op.lifecycle.Status()
	if status == shared.LifecycleStatusCompleted || status == shared.LifecycleStatusStopped {
		return fmt.Errorf("cannot fail operation in %s state", op.Status())
	}

	return op.lifecycle.Fail(err)
}

// Validation methods

// Validate checks all invariants for the mining operation
func (op *Operation) Validate() error {
	if !op.HasMiners() {
		return fmt.Errorf("operation must have at least 1 miner ship")
	}

	if !op.HasTransports() {
		return fmt.Errorf("operation must have at least 1 transport ship")
	}

	if op.topNOres < 1 {
		return fmt.Errorf("topNOres must be >= 1, got %d", op.topNOres)
	}

	if op.asteroidField == "" {
		return fmt.Errorf("asteroid field waypoint must be specified")
	}

	// BatchThreshold and BatchTimeout are optional (0 means not used in Transport-as-Sink pattern)
	// Only validate if they're provided (non-zero)
	if op.batchThreshold < 0 {
		return fmt.Errorf("batchThreshold must be >= 0, got %d", op.batchThreshold)
	}

	if op.batchTimeout < 0 {
		return fmt.Errorf("batchTimeout must be >= 0, got %d", op.batchTimeout)
	}

	return nil
}

// HasMiners returns true if the operation has at least one miner ship
func (op *Operation) HasMiners() bool {
	return len(op.minerShips) > 0
}

// HasTransports returns true if the operation has at least one transport ship
func (op *Operation) HasTransports() bool {
	return len(op.transportShips) > 0
}

// State queries

// IsRunning returns true if the operation is currently executing
func (op *Operation) IsRunning() bool {
	return op.lifecycle.IsRunning()
}

// IsFinished returns true if the operation has completed, failed, or stopped
func (op *Operation) IsFinished() bool {
	return op.lifecycle.IsFinished()
}

// IsPending returns true if the operation hasn't started yet
func (op *Operation) IsPending() bool {
	return op.lifecycle.IsPending()
}

// Runtime calculation

// RuntimeDuration calculates how long the operation has been running
func (op *Operation) RuntimeDuration() time.Duration {
	return op.lifecycle.RuntimeDuration()
}

// String provides human-readable representation
func (op *Operation) String() string {
	return fmt.Sprintf("Operation[%s, status=%s, asteroid=%s, miners=%d, transports=%d]",
		op.id, op.Status(), op.asteroidField, len(op.minerShips), len(op.transportShips))
}

// OperationData is the DTO for persisting mining operations
type OperationData struct {
	ID             string
	PlayerID       int
	AsteroidField  string
	Status         string
	TopNOres       int
	MinerShips     []string
	TransportShips []string
	BatchThreshold int
	BatchTimeout   int
	MaxIterations  int
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      *time.Time
	StoppedAt      *time.Time
}

// ToData converts the entity to a DTO for persistence
func (op *Operation) ToData() *OperationData {
	var lastErr string
	if op.lifecycle.LastError() != nil {
		lastErr = op.lifecycle.LastError().Error()
	}

	return &OperationData{
		ID:             op.id,
		PlayerID:       op.playerID,
		AsteroidField:  op.asteroidField,
		Status:         string(op.Status()), // Convert via Status() method
		TopNOres:       op.topNOres,
		MinerShips:     op.minerShips,
		TransportShips: op.transportShips,
		BatchThreshold: op.batchThreshold,
		BatchTimeout:   op.batchTimeout,
		MaxIterations:  op.maxIterations,
		LastError:      lastErr,
		CreatedAt:      op.lifecycle.CreatedAt(),
		UpdatedAt:      op.lifecycle.UpdatedAt(),
		StartedAt:      op.lifecycle.StartedAt(),
		StoppedAt:      op.lifecycle.StoppedAt(),
	}
}

// FromData creates a Operation entity from a DTO
func FromData(data *OperationData, clock shared.Clock) *Operation {
	// Create lifecycle state machine
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

	return &Operation{
		id:             data.ID,
		playerID:       data.PlayerID,
		asteroidField:  data.AsteroidField,
		topNOres:       data.TopNOres,
		minerShips:     data.MinerShips,
		transportShips: data.TransportShips,
		batchThreshold: data.BatchThreshold,
		batchTimeout:   data.BatchTimeout,
		maxIterations:  data.MaxIterations,
		lifecycle:      lifecycle,
	}
}

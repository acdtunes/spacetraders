package gas

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// OperationStatus represents the lifecycle state of a gas extraction operation
type OperationStatus string

const (
	OperationStatusPending   OperationStatus = "PENDING"
	OperationStatusRunning   OperationStatus = "RUNNING"
	OperationStatusCompleted OperationStatus = "COMPLETED"
	OperationStatusStopped   OperationStatus = "STOPPED"
	OperationStatusFailed    OperationStatus = "FAILED"
)

// Operation represents a gas extraction operation aggregate root
// It orchestrates siphon ships extracting from gas giants and transport ships
// delivering the cargo to manufacturing factories.
type Operation struct {
	id             string
	playerID       int
	gasGiant       string   // Waypoint symbol of the gas giant
	siphonShips    []string // Ships performing siphoning (need siphon mounts + gas processor)
	transportShips []string // Ships delivering to factories
	maxIterations  int      // -1 for infinite
	lifecycle      *shared.LifecycleStateMachine
}

// NewOperation creates a new gas extraction operation instance
func NewOperation(
	id string,
	playerID int,
	gasGiant string,
	siphonShips []string,
	transportShips []string,
	maxIterations int,
	clock shared.Clock,
) *Operation {
	// Copy slices to avoid external mutation
	siphoners := make([]string, len(siphonShips))
	copy(siphoners, siphonShips)

	transports := make([]string, len(transportShips))
	copy(transports, transportShips)

	return &Operation{
		id:             id,
		playerID:       playerID,
		gasGiant:       gasGiant,
		siphonShips:    siphoners,
		transportShips: transports,
		maxIterations:  maxIterations,
		lifecycle:      shared.NewLifecycleStateMachine(clock),
	}
}

// Getters

func (op *Operation) ID() string               { return op.id }
func (op *Operation) PlayerID() int            { return op.playerID }
func (op *Operation) GasGiant() string         { return op.gasGiant }
func (op *Operation) SiphonShips() []string    { return op.siphonShips }
func (op *Operation) TransportShips() []string { return op.transportShips }
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

// Validate checks all invariants for the gas operation
func (op *Operation) Validate() error {
	if !op.HasSiphonShips() {
		return fmt.Errorf("operation must have at least 1 siphon ship")
	}

	if !op.HasTransportShips() {
		return fmt.Errorf("operation must have at least 1 transport ship")
	}

	if op.gasGiant == "" {
		return fmt.Errorf("gas giant waypoint must be specified")
	}

	return nil
}

// HasSiphonShips returns true if the operation has at least one siphon ship
func (op *Operation) HasSiphonShips() bool {
	return len(op.siphonShips) > 0
}

// HasTransportShips returns true if the operation has at least one transport ship
func (op *Operation) HasTransportShips() bool {
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
	return fmt.Sprintf("GasOperation[%s, status=%s, gasGiant=%s, siphoners=%d, transports=%d]",
		op.id, op.Status(), op.gasGiant, len(op.siphonShips), len(op.transportShips))
}

// OperationData is the DTO for persisting gas operations
type OperationData struct {
	ID             string
	PlayerID       int
	GasGiant       string
	Status         string
	SiphonShips    []string
	TransportShips []string
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
		GasGiant:       op.gasGiant,
		Status:         string(op.Status()), // Convert via Status() method
		SiphonShips:    op.siphonShips,
		TransportShips: op.transportShips,
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
		gasGiant:       data.GasGiant,
		siphonShips:    data.SiphonShips,
		transportShips: data.TransportShips,
		maxIterations:  data.MaxIterations,
		lifecycle:      lifecycle,
	}
}

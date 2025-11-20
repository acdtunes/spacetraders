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
	status         OperationStatus
	topNOres       int      // Number of ore types to keep (jettison rest)
	minerShips     []string // Ship symbols for mining
	transportShips []string // Ship symbols for transport

	// Batch collection configuration
	batchThreshold int // Number of miners to accumulate before spawning transport
	batchTimeout   int // Seconds to wait before spawning transport anyway

	// Iteration control
	maxIterations int // -1 for infinite

	// Error tracking
	lastError error

	// Lifecycle timestamps
	createdAt time.Time
	updatedAt time.Time
	startedAt *time.Time
	stoppedAt *time.Time

	// Time provider for testability
	clock shared.Clock
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
	if clock == nil {
		clock = shared.NewRealClock()
	}

	// Copy slices to avoid external mutation
	miners := make([]string, len(minerShips))
	copy(miners, minerShips)

	transports := make([]string, len(transportShips))
	copy(transports, transportShips)

	now := clock.Now()
	return &Operation{
		id:             id,
		playerID:       playerID,
		asteroidField:  asteroidField,
		status:         OperationStatusPending,
		topNOres:       topNOres,
		minerShips:     miners,
		transportShips: transports,
		batchThreshold: batchThreshold,
		batchTimeout:   batchTimeout,
		maxIterations:  maxIterations,
		createdAt:      now,
		updatedAt:      now,
		clock:          clock,
	}
}

// Getters

func (op *Operation) ID() string                    { return op.id }
func (op *Operation) PlayerID() int                 { return op.playerID }
func (op *Operation) AsteroidField() string         { return op.asteroidField }
func (op *Operation) Status() OperationStatus       { return op.status }
func (op *Operation) TopNOres() int                 { return op.topNOres }
func (op *Operation) MinerShips() []string          { return op.minerShips }
func (op *Operation) TransportShips() []string      { return op.transportShips }
func (op *Operation) BatchThreshold() int           { return op.batchThreshold }
func (op *Operation) BatchTimeout() int             { return op.batchTimeout }
func (op *Operation) MaxIterations() int            { return op.maxIterations }
func (op *Operation) LastError() error              { return op.lastError }
func (op *Operation) CreatedAt() time.Time          { return op.createdAt }
func (op *Operation) UpdatedAt() time.Time          { return op.updatedAt }
func (op *Operation) StartedAt() *time.Time         { return op.startedAt }
func (op *Operation) StoppedAt() *time.Time         { return op.stoppedAt }

// State transition methods

// Start transitions the operation from PENDING to RUNNING
func (op *Operation) Start() error {
	if op.status != OperationStatusPending {
		return fmt.Errorf("cannot start operation in %s state", op.status)
	}

	if err := op.Validate(); err != nil {
		return fmt.Errorf("operation validation failed: %w", err)
	}

	now := op.clock.Now()
	op.status = OperationStatusRunning
	op.startedAt = &now
	op.updatedAt = now
	return nil
}

// Stop transitions the operation to STOPPED state
func (op *Operation) Stop() error {
	if op.status == OperationStatusCompleted || op.status == OperationStatusStopped {
		return fmt.Errorf("cannot stop operation in %s state", op.status)
	}

	now := op.clock.Now()
	op.status = OperationStatusStopped
	op.stoppedAt = &now
	op.updatedAt = now
	return nil
}

// Complete transitions the operation to COMPLETED state
func (op *Operation) Complete() error {
	if op.status != OperationStatusRunning {
		return fmt.Errorf("cannot complete operation in %s state", op.status)
	}

	now := op.clock.Now()
	op.status = OperationStatusCompleted
	op.stoppedAt = &now
	op.updatedAt = now
	return nil
}

// Fail transitions the operation to FAILED state with error
func (op *Operation) Fail(err error) error {
	if op.status == OperationStatusCompleted || op.status == OperationStatusStopped {
		return fmt.Errorf("cannot fail operation in %s state", op.status)
	}

	now := op.clock.Now()
	op.status = OperationStatusFailed
	op.lastError = err
	op.stoppedAt = &now
	op.updatedAt = now
	return nil
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
	return op.status == OperationStatusRunning
}

// IsFinished returns true if the operation has completed, failed, or stopped
func (op *Operation) IsFinished() bool {
	return op.status == OperationStatusCompleted ||
		op.status == OperationStatusFailed ||
		op.status == OperationStatusStopped
}

// IsPending returns true if the operation hasn't started yet
func (op *Operation) IsPending() bool {
	return op.status == OperationStatusPending
}

// Runtime calculation

// RuntimeDuration calculates how long the operation has been running
func (op *Operation) RuntimeDuration() time.Duration {
	if op.startedAt == nil {
		return 0
	}

	endTime := op.clock.Now()
	if op.stoppedAt != nil {
		endTime = *op.stoppedAt
	}

	return endTime.Sub(*op.startedAt)
}

// String provides human-readable representation
func (op *Operation) String() string {
	return fmt.Sprintf("Operation[%s, status=%s, asteroid=%s, miners=%d, transports=%d]",
		op.id, op.status, op.asteroidField, len(op.minerShips), len(op.transportShips))
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
	if op.lastError != nil {
		lastErr = op.lastError.Error()
	}

	return &OperationData{
		ID:             op.id,
		PlayerID:       op.playerID,
		AsteroidField:  op.asteroidField,
		Status:         string(op.status),
		TopNOres:       op.topNOres,
		MinerShips:     op.minerShips,
		TransportShips: op.transportShips,
		BatchThreshold: op.batchThreshold,
		BatchTimeout:   op.batchTimeout,
		MaxIterations:  op.maxIterations,
		LastError:      lastErr,
		CreatedAt:      op.createdAt,
		UpdatedAt:      op.updatedAt,
		StartedAt:      op.startedAt,
		StoppedAt:      op.stoppedAt,
	}
}

// FromData creates a Operation entity from a DTO
func FromData(data *OperationData, clock shared.Clock) *Operation {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	var lastErr error
	if data.LastError != "" {
		lastErr = fmt.Errorf("%s", data.LastError)
	}

	return &Operation{
		id:             data.ID,
		playerID:       data.PlayerID,
		asteroidField:  data.AsteroidField,
		status:         OperationStatus(data.Status),
		topNOres:       data.TopNOres,
		minerShips:     data.MinerShips,
		transportShips: data.TransportShips,
		batchThreshold: data.BatchThreshold,
		batchTimeout:   data.BatchTimeout,
		maxIterations:  data.MaxIterations,
		lastError:      lastErr,
		createdAt:      data.CreatedAt,
		updatedAt:      data.UpdatedAt,
		startedAt:      data.StartedAt,
		stoppedAt:      data.StoppedAt,
		clock:          clock,
	}
}

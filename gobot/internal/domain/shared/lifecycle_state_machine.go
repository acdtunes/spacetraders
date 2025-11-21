package shared

import (
	"fmt"
	"time"
)

// LifecycleStatus represents the state of an entity in its lifecycle
type LifecycleStatus string

const (
	// LifecycleStatusPending indicates the entity is queued but not started
	LifecycleStatusPending LifecycleStatus = "PENDING"

	// LifecycleStatusRunning indicates the entity is actively executing
	LifecycleStatusRunning LifecycleStatus = "RUNNING"

	// LifecycleStatusCompleted indicates the entity finished successfully
	LifecycleStatusCompleted LifecycleStatus = "COMPLETED"

	// LifecycleStatusFailed indicates the entity encountered an error
	LifecycleStatusFailed LifecycleStatus = "FAILED"

	// LifecycleStatusStopped indicates the entity was stopped by user
	LifecycleStatusStopped LifecycleStatus = "STOPPED"
)

// LifecycleStateMachine manages the common lifecycle state transitions
// for entities that follow the PENDING → RUNNING → COMPLETED/FAILED/STOPPED pattern.
//
// This component uses composition to provide reusable state management behavior
// across different domain entities (Container, MiningOperation, Route).
//
// Invariants:
// - State transitions must follow valid paths
// - Timestamps are automatically managed
// - Clock is injected for testability
type LifecycleStateMachine struct {
	status    LifecycleStatus
	createdAt time.Time
	updatedAt time.Time
	startedAt *time.Time
	stoppedAt *time.Time
	lastError error
	clock     Clock
}

// NewLifecycleStateMachine creates a new lifecycle state machine in PENDING state
func NewLifecycleStateMachine(clock Clock) *LifecycleStateMachine {
	if clock == nil {
		clock = NewRealClock()
	}

	now := clock.Now()
	return &LifecycleStateMachine{
		status:    LifecycleStatusPending,
		createdAt: now,
		updatedAt: now,
		clock:     clock,
	}
}

// Getters

// Status returns the current lifecycle status
func (sm *LifecycleStateMachine) Status() LifecycleStatus {
	return sm.status
}

// CreatedAt returns when the entity was created
func (sm *LifecycleStateMachine) CreatedAt() time.Time {
	return sm.createdAt
}

// UpdatedAt returns when the entity was last updated
func (sm *LifecycleStateMachine) UpdatedAt() time.Time {
	return sm.updatedAt
}

// StartedAt returns when the entity started execution (nil if not started)
func (sm *LifecycleStateMachine) StartedAt() *time.Time {
	return sm.startedAt
}

// StoppedAt returns when the entity stopped execution (nil if still running)
func (sm *LifecycleStateMachine) StoppedAt() *time.Time {
	return sm.stoppedAt
}

// LastError returns the last error encountered (nil if no error)
func (sm *LifecycleStateMachine) LastError() error {
	return sm.lastError
}

// State transition methods

// Start transitions from PENDING or STOPPED to RUNNING state
func (sm *LifecycleStateMachine) Start() error {
	if sm.status != LifecycleStatusPending && sm.status != LifecycleStatusStopped {
		return fmt.Errorf("cannot start from %s state", sm.status)
	}

	now := sm.clock.Now()
	sm.status = LifecycleStatusRunning
	sm.startedAt = &now
	sm.updatedAt = now
	return nil
}

// Complete transitions from RUNNING to COMPLETED state
func (sm *LifecycleStateMachine) Complete() error {
	if sm.status != LifecycleStatusRunning {
		return fmt.Errorf("cannot complete from %s state", sm.status)
	}

	now := sm.clock.Now()
	sm.status = LifecycleStatusCompleted
	sm.stoppedAt = &now
	sm.updatedAt = now
	return nil
}

// Fail transitions to FAILED state with an error
// Can fail from any non-terminal state (not COMPLETED or STOPPED)
func (sm *LifecycleStateMachine) Fail(err error) error {
	if sm.status == LifecycleStatusCompleted || sm.status == LifecycleStatusStopped {
		return fmt.Errorf("cannot fail from %s state", sm.status)
	}

	now := sm.clock.Now()
	sm.status = LifecycleStatusFailed
	sm.lastError = err
	sm.stoppedAt = &now
	sm.updatedAt = now
	return nil
}

// Stop transitions to STOPPED state
// Can stop from any non-terminal state (not COMPLETED or STOPPED)
func (sm *LifecycleStateMachine) Stop() error {
	if sm.status == LifecycleStatusCompleted || sm.status == LifecycleStatusStopped {
		return fmt.Errorf("cannot stop from %s state", sm.status)
	}

	now := sm.clock.Now()
	sm.status = LifecycleStatusStopped
	sm.stoppedAt = &now
	sm.updatedAt = now
	return nil
}

// State query methods

// IsRunning returns true if the entity is currently executing
func (sm *LifecycleStateMachine) IsRunning() bool {
	return sm.status == LifecycleStatusRunning
}

// IsFinished returns true if the entity has completed, failed, or stopped
func (sm *LifecycleStateMachine) IsFinished() bool {
	return sm.status == LifecycleStatusCompleted ||
		sm.status == LifecycleStatusFailed ||
		sm.status == LifecycleStatusStopped
}

// IsPending returns true if the entity hasn't started yet
func (sm *LifecycleStateMachine) IsPending() bool {
	return sm.status == LifecycleStatusPending
}

// Runtime calculation

// RuntimeDuration calculates how long the entity has been/was running
// Returns 0 if not started yet
func (sm *LifecycleStateMachine) RuntimeDuration() time.Duration {
	if sm.startedAt == nil {
		return 0
	}

	endTime := sm.clock.Now()
	if sm.stoppedAt != nil {
		endTime = *sm.stoppedAt
	}

	return endTime.Sub(*sm.startedAt)
}

// Internal state management for advanced use cases

// SetStatusForRecovery allows setting status during entity reconstruction (e.g., from database)
// This should only be used by entity constructors, not during normal operation
func (sm *LifecycleStateMachine) SetStatusForRecovery(status LifecycleStatus) {
	sm.status = status
}

// UpdateTimestamp updates the updatedAt timestamp
// Useful when entity performs operations that don't change lifecycle state
func (sm *LifecycleStateMachine) UpdateTimestamp() {
	sm.updatedAt = sm.clock.Now()
}

// ResetForRestart clears error state and resets timestamps for restart scenario
// Used when an entity needs to be restarted after failure
func (sm *LifecycleStateMachine) ResetForRestart() {
	sm.status = LifecycleStatusPending
	sm.lastError = nil
	sm.startedAt = nil
	sm.stoppedAt = nil
	sm.updatedAt = sm.clock.Now()
}

// RecoverFromPersistence restores the complete lifecycle state from persisted data
// This should only be used when reconstructing entities from storage
func (sm *LifecycleStateMachine) RecoverFromPersistence(
	status LifecycleStatus,
	createdAt, updatedAt time.Time,
	startedAt, stoppedAt *time.Time,
	lastError error,
) {
	sm.status = status
	sm.createdAt = createdAt
	sm.updatedAt = updatedAt
	sm.startedAt = startedAt
	sm.stoppedAt = stoppedAt
	sm.lastError = lastError
}

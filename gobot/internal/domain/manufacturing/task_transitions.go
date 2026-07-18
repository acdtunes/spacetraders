package manufacturing

import "fmt"

// State-machine transitions for ManufacturingTask. The entity type, its
// constructors, and read accessors live in task.go; every method that moves a
// task through PENDING -> READY -> ASSIGNED -> EXECUTING -> COMPLETED/FAILED
// (or re-sources it back to PENDING) lives here.

// State transitions

// MarkReady transitions task from PENDING to READY
func (t *ManufacturingTask) MarkReady() error {
	if t.status != TaskStatusPending {
		return &ErrInvalidTaskTransition{
			TaskID: t.id,
			From:   t.status,
			To:     TaskStatusReady,
		}
	}
	t.status = TaskStatusReady
	t.readyAt = nowPtr()
	return nil
}

// AssignShip assigns a ship to execute this task
func (t *ManufacturingTask) AssignShip(shipSymbol string) error {
	if t.status != TaskStatusReady {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusAssigned,
			Description: "can only assign ship to READY tasks",
		}
	}
	if t.assignedShip != "" && t.assignedShip != shipSymbol {
		return &ErrTaskAlreadyAssigned{
			TaskID:       t.id,
			AssignedShip: t.assignedShip,
		}
	}
	t.status = TaskStatusAssigned
	t.assignedShip = shipSymbol
	return nil
}

// StartExecution marks the task as executing
func (t *ManufacturingTask) StartExecution() error {
	if t.status != TaskStatusAssigned {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusExecuting,
			Description: "can only start execution from ASSIGNED state",
		}
	}
	t.status = TaskStatusExecuting
	t.startedAt = nowPtr()
	return nil
}

// Complete marks the task as successfully completed
// NOTE: assignedShip is preserved for ship affinity - downstream tasks (like SELL)
// need to know which ship executed upstream tasks (like COLLECT) that have the cargo
func (t *ManufacturingTask) Complete() error {
	if t.status != TaskStatusExecuting {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusCompleted,
			Description: "can only complete from EXECUTING state",
		}
	}
	t.status = TaskStatusCompleted
	t.completedAt = nowPtr()
	// DO NOT clear assignedShip - downstream tasks need this for ship affinity
	// Ship assignment release is handled by the container runner, not the task state
	return nil
}

// Fail marks the task as failed
func (t *ManufacturingTask) Fail(errorMsg string) error {
	if t.status != TaskStatusExecuting && t.status != TaskStatusAssigned {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusFailed,
			Description: "can only fail from EXECUTING or ASSIGNED state",
		}
	}
	t.status = TaskStatusFailed
	t.errorMessage = errorMsg
	t.completedAt = nowPtr()
	t.retryCount++
	// DO NOT clear assignedShip - FindByAssignedShip needs it to find the task
	// Ship assignment release is handled by ResetForRetry or coordinator
	return nil
}

// ResetForRetry prepares the task for a retry attempt
func (t *ManufacturingTask) ResetForRetry() error {
	if t.status != TaskStatusFailed {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusPending,
			Description: "can only retry FAILED tasks",
		}
	}
	if t.retryCount >= t.maxRetries {
		return &ErrMaxRetriesExceeded{
			TaskID:     t.id,
			RetryCount: t.retryCount,
			MaxRetries: t.maxRetries,
		}
	}
	t.status = TaskStatusPending
	t.errorMessage = ""
	t.startedAt = nil
	t.completedAt = nil
	t.readyAt = nil     // Reset so MarkReady() sets fresh timestamp for fair aging
	t.assignedShip = "" // Release ship so it can be reassigned
	// BUG FIX #3: Reset phase tracking for fresh retry
	t.ResetPhaseTracking()
	return nil
}

// RollbackAssignment returns task to READY state (used on assignment failure)
func (t *ManufacturingTask) RollbackAssignment() error {
	if t.status != TaskStatusAssigned {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusReady,
			Description: "can only rollback from ASSIGNED state",
		}
	}
	t.status = TaskStatusReady
	t.assignedShip = ""
	// Reset readyAt for fair aging - assignment failure shouldn't give priority bonus
	t.readyAt = nowPtr()
	return nil
}

// RollbackExecution returns task to READY state (used on execution interruption, e.g., daemon restart)
// IMPORTANT: We preserve assignedShip because for SELL tasks, the ship still has cargo that needs to be sold.
// The ship assignment in the database will be released separately, but we need to remember which ship
// was executing so we can re-assign the same ship when the task is retried.
func (t *ManufacturingTask) RollbackExecution() error {
	if t.status != TaskStatusExecuting {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusReady,
			Description: "can only rollback from EXECUTING state",
		}
	}
	t.status = TaskStatusReady
	// NOTE: We intentionally DO NOT clear assignedShip here.
	// For SELL tasks, the ship has cargo from the COLLECT task and must complete the sell.
	// The coordinator will use this to re-assign the same ship.
	// t.assignedShip = "" // REMOVED - preserve ship for recovery
	t.startedAt = nil
	// Reset readyAt to prevent accumulating aging priority after recovery
	// Without this, recovered tasks would have artificially high priority
	t.readyAt = nowPtr()
	return nil
}

// ResetToPending returns task from READY back to PENDING state.
// Used when market conditions change (e.g., sell market becomes saturated) and we want
// the SupplyMonitor to re-evaluate the task later when conditions improve.
func (t *ManufacturingTask) ResetToPending() error {
	if t.status != TaskStatusReady {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusPending,
			Description: "can only reset to pending from READY state",
		}
	}
	t.status = TaskStatusPending
	t.readyAt = nil
	return nil
}

// ParkForResupply returns an in-flight (EXECUTING or ASSIGNED) task to PENDING
// without counting a retry, so it can be re-sourced and re-dispatched once supply
// recovers. Used when a construction/acquire task reaches execution with no buy
// source - a transient supply gap that must be treated as a pending-supply state
// rather than a permanent failure (sp-hs2j). The ship is released so any ship can
// take the task after it is re-sourced, and the retry budget is left untouched
// because no delivery was attempted. For a construction delivery whose source and
// factory are both empty this leaves it IsDeferredConstruction(), so the existing
// SupplyMonitor re-sourcing path (sp-r900) picks it up when supply regenerates.
func (t *ManufacturingTask) ParkForResupply() error {
	if t.status != TaskStatusExecuting && t.status != TaskStatusAssigned {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusPending,
			Description: "can only park EXECUTING or ASSIGNED tasks for resupply",
		}
	}
	t.status = TaskStatusPending
	t.errorMessage = ""
	t.startedAt = nil
	t.completedAt = nil
	t.readyAt = nil     // reset so MarkReady() sets a fresh timestamp for fair aging
	t.assignedShip = "" // release ship so any ship can take the task once re-sourced
	t.ResetPhaseTracking()
	return nil
}

// ClearSourceForResupply drops the resolved buy source (both source market and
// factory) from a DELIVER_TO_CONSTRUCTION task whose source turned out to be DRY at
// execution time - the market was reachable but sold nothing. This reverts the task
// to the deferred/unsourceable signature, so that once it is parked back to PENDING
// (ParkForResupply) it reads as IsDeferredConstruction() and the SupplyMonitor
// re-sources it when the market refills (sp-r900). Without dropping the dry source the
// task would keep the same empty market and retry to permanent death, dead-stalling
// its leg. This is the execution-time twin of the planner's no-source deferral: the
// dry-market self-heal (sp-izh8), mirroring the no-source park path (sp-hs2j).
// Construction-only: no other task type has the deferred-construction re-sourcing path.
func (t *ManufacturingTask) ClearSourceForResupply() error {
	if t.taskType != TaskTypeDeliverToConstruction {
		return fmt.Errorf("can only clear source for resupply on DELIVER_TO_CONSTRUCTION tasks, got %s", t.taskType)
	}
	t.sourceMarket = ""
	t.factorySymbol = ""
	return nil
}

// Cancel marks the task as failed with a cancellation reason (used when pipeline is recycled).
// Can cancel PENDING, READY, or ASSIGNED tasks - tasks that are executing should complete or fail.
// Uses FAILED status since the database constraint doesn't include CANCELLED.
func (t *ManufacturingTask) Cancel(reason string) error {
	if t.status != TaskStatusPending && t.status != TaskStatusReady && t.status != TaskStatusAssigned {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          TaskStatusFailed,
			Description: "can only cancel PENDING, READY, or ASSIGNED tasks",
		}
	}
	t.status = TaskStatusFailed
	t.errorMessage = reason
	t.completedAt = nowPtr()
	return nil
}

// UpdateSourceMarket changes the source market for ACQUIRE_DELIVER tasks or for
// DELIVER_TO_CONSTRUCTION tasks that were deferred at planning time.
// This is used by SupplyMonitor to re-source PENDING tasks when the original
// source market's supply degrades (ACQUIRE_DELIVER) or when a deferred
// construction material becomes sourceable again (DELIVER_TO_CONSTRUCTION).
// Only allowed for PENDING tasks to prevent disrupting in-flight work.
func (t *ManufacturingTask) UpdateSourceMarket(newSource string) error {
	if t.status != TaskStatusPending {
		return &ErrInvalidTaskTransition{
			TaskID:      t.id,
			From:        t.status,
			To:          t.status,
			Description: "can only update source market for PENDING tasks",
		}
	}
	if t.taskType != TaskTypeAcquireDeliver && t.taskType != TaskTypeDeliverToConstruction {
		return fmt.Errorf("can only update source market for ACQUIRE_DELIVER or DELIVER_TO_CONSTRUCTION tasks, got %s", t.taskType)
	}
	t.sourceMarket = newSource
	return nil
}

// BUG FIX #3: Phase completion methods

// MarkCollectPhaseComplete marks the collect phase as completed for COLLECT_SELL tasks.
// Called after successfully purchasing goods from the factory.
func (t *ManufacturingTask) MarkCollectPhaseComplete() {
	t.collectPhaseCompleted = true
	t.phaseCompletedAt = nowPtr()
}

// MarkAcquirePhaseComplete marks the acquire phase as completed for ACQUIRE_DELIVER tasks.
// Called after successfully purchasing goods from the market.
func (t *ManufacturingTask) MarkAcquirePhaseComplete() {
	t.acquirePhaseCompleted = true
	t.phaseCompletedAt = nowPtr()
}

// ResetPhaseTracking clears phase completion flags.
// Used when retrying a failed task to start fresh.
func (t *ManufacturingTask) ResetPhaseTracking() {
	t.collectPhaseCompleted = false
	t.acquirePhaseCompleted = false
	t.phaseCompletedAt = nil
}

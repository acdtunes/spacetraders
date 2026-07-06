package manufacturing

// TaskTypeAllocations tracks current worker assignments by task type.
type TaskTypeAllocations struct {
	CollectSellCount       int
	AcquireDeliverCount    int
	TotalWorkers           int
	HasReadyCollectSell    bool
	HasReadyAcquireDeliver bool
}

// WorkerReservationPolicy prevents task type starvation in manufacturing.
// Without reservation, one task type could monopolize all workers,
// causing deadlock (e.g., all workers doing ACQUIRE_DELIVER, nothing collecting).
type WorkerReservationPolicy struct{}

// NewWorkerReservationPolicy creates a new reservation policy.
func NewWorkerReservationPolicy() *WorkerReservationPolicy {
	return &WorkerReservationPolicy{}
}

// ShouldAssign determines if a task type should be assigned given current allocations.
// Returns true if assignment won't starve the other task type.
//
// Logic:
//   - If BOTH types are below minimum AND BOTH have ready tasks -> allow either (break deadlock)
//   - If assigning ACQUIRE_DELIVER: Skip if COLLECT_SELL is below minimum AND has ready tasks
//   - If assigning COLLECT_SELL: Skip if ACQUIRE_DELIVER is below minimum AND has ready tasks
//
// This ensures both task types always get their minimum workers when tasks are available,
// while preventing a deadlock where both types block each other when starting from zero.
func (p *WorkerReservationPolicy) ShouldAssign(taskType TaskType, alloc TaskTypeAllocations) bool {
	// Deadlock prevention: If BOTH counts are below minimum and BOTH have ready tasks,
	// allow either type to be assigned (first come, first served based on priority queue).
	// This breaks the chicken-and-egg problem where each type blocks the other.
	bothBelowMinimum := alloc.CollectSellCount < MinCollectSellWorkers &&
		alloc.AcquireDeliverCount < MinAcquireDeliverWorkers
	bothHaveReady := alloc.HasReadyCollectSell && alloc.HasReadyAcquireDeliver

	if bothBelowMinimum && bothHaveReady {
		// Allow any task type to break the deadlock
		return true
	}

	switch taskType {
	case TaskTypeCollectSell:
		// Don't assign if it would starve ACQUIRE_DELIVER tasks
		if alloc.AcquireDeliverCount < MinAcquireDeliverWorkers && alloc.HasReadyAcquireDeliver {
			return false
		}
		return true

	case TaskTypeAcquireDeliver:
		// Don't assign if it would starve COLLECT_SELL tasks
		if alloc.CollectSellCount < MinCollectSellWorkers && alloc.HasReadyCollectSell {
			return false
		}
		return true

	case TaskTypeLiquidate:
		// Liquidation is high priority, always allow
		return true

	default:
		return true
	}
}

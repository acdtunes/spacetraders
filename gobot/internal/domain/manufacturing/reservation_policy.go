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

// CalculateReservedCapacity returns how many workers should be reserved for each type.
// Returns (collectSellReserve, acquireDeliverReserve)
func (p *WorkerReservationPolicy) CalculateReservedCapacity(totalWorkers int) (int, int) {
	// Reserve minimum or 20% of total, whichever is greater
	reserve := totalWorkers / 5
	if reserve < MinCollectSellWorkers {
		reserve = MinCollectSellWorkers
	}
	return reserve, reserve
}

// GetMinimumReservations returns the minimum worker reservations for each task type.
func (p *WorkerReservationPolicy) GetMinimumReservations() (collectSell, acquireDeliver int) {
	return MinCollectSellWorkers, MinAcquireDeliverWorkers
}

// IsBalanced returns true if both task types have at least their minimum worker allocation.
func (p *WorkerReservationPolicy) IsBalanced(alloc TaskTypeAllocations) bool {
	// Only check balance if both types have ready tasks
	if !alloc.HasReadyCollectSell || !alloc.HasReadyAcquireDeliver {
		return true // Can't be imbalanced if one type has no ready tasks
	}
	return alloc.CollectSellCount >= MinCollectSellWorkers &&
		alloc.AcquireDeliverCount >= MinAcquireDeliverWorkers
}

// GetStarvedTaskType returns the task type that is below minimum allocation,
// or empty string if neither is starved.
func (p *WorkerReservationPolicy) GetStarvedTaskType(alloc TaskTypeAllocations) TaskType {
	if alloc.CollectSellCount < MinCollectSellWorkers && alloc.HasReadyCollectSell {
		return TaskTypeCollectSell
	}
	if alloc.AcquireDeliverCount < MinAcquireDeliverWorkers && alloc.HasReadyAcquireDeliver {
		return TaskTypeAcquireDeliver
	}
	return ""
}

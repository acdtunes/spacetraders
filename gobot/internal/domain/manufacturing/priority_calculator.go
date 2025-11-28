package manufacturing

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TaskPriorityCalculator computes effective task priority using
// base priority, aging bonus, and supply-based adjustments.
type TaskPriorityCalculator struct {
	clock shared.Clock
}

// NewTaskPriorityCalculator creates a calculator with the given clock.
func NewTaskPriorityCalculator(clock shared.Clock) *TaskPriorityCalculator {
	return &TaskPriorityCalculator{clock: clock}
}

// CalculateEffectivePriority returns the task's priority including aging bonus.
// Aging prevents task starvation by gradually increasing priority over time.
func (c *TaskPriorityCalculator) CalculateEffectivePriority(task *ManufacturingTask) int {
	basePriority := task.Priority()
	agingBonus := c.calculateAgingBonus(task.CreatedAt())
	return basePriority + agingBonus
}

// CalculateEffectivePriorityWithReadyAt uses readyAt timestamp for aging calculation.
// This is preferred when the task has been made ready, as it better reflects
// how long the task has been waiting for assignment.
func (c *TaskPriorityCalculator) CalculateEffectivePriorityWithReadyAt(task *ManufacturingTask) int {
	basePriority := task.Priority()

	// Use readyAt if available, otherwise fall back to createdAt
	var referenceTime time.Time
	if task.ReadyAt() != nil {
		referenceTime = *task.ReadyAt()
	} else {
		referenceTime = task.CreatedAt()
	}

	agingBonus := c.calculateAgingBonus(referenceTime)
	return basePriority + agingBonus
}

// calculateAgingBonus computes the priority bonus based on task age.
// Returns a capped bonus to prevent runaway priority accumulation.
func (c *TaskPriorityCalculator) calculateAgingBonus(referenceTime time.Time) int {
	ageMinutes := int(c.clock.Now().Sub(referenceTime).Minutes())
	bonus := ageMinutes * AgingRatePerMinute
	if bonus > MaxAgingBonus {
		return MaxAgingBonus
	}
	if bonus < 0 {
		return 0
	}
	return bonus
}

// PriorityFromSupply returns priority boost based on source market supply.
// Higher supply = better prices = higher priority to buy now.
func PriorityFromSupply(supply SupplyLevel) int {
	switch supply {
	case SupplyLevelAbundant:
		return SupplyPriorityAbundant
	case SupplyLevelHigh:
		return SupplyPriorityHigh
	default:
		return SupplyPriorityModerate
	}
}

// BasePriorityForTaskType returns the base priority for a task type.
func BasePriorityForTaskType(taskType TaskType) int {
	switch taskType {
	case TaskTypeAcquireDeliver:
		return PriorityAcquireDeliver
	case TaskTypeCollectSell:
		return PriorityCollectSell
	case TaskTypeLiquidate:
		return PriorityLiquidate
	default:
		return PriorityAcquireDeliver
	}
}

// ComparePriority returns -1 if task1 has higher priority (should be processed first),
// 1 if task2 has higher priority, and 0 if they have equal priority.
func (c *TaskPriorityCalculator) ComparePriority(task1, task2 *ManufacturingTask) int {
	p1 := c.CalculateEffectivePriorityWithReadyAt(task1)
	p2 := c.CalculateEffectivePriorityWithReadyAt(task2)

	if p1 > p2 {
		return -1 // task1 has higher priority
	}
	if p2 > p1 {
		return 1 // task2 has higher priority
	}
	return 0 // equal priority
}

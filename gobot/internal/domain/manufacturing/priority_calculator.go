package manufacturing

import (
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

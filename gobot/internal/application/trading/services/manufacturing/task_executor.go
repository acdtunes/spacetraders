package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TaskExecutor executes a specific type of manufacturing task.
// Implements the Strategy pattern for task type dispatch.
// Each task type has its own executor that knows how to handle its specific workflow.
type TaskExecutor interface {
	Execute(ctx context.Context, params TaskExecutionParams) error
	TaskType() manufacturing.TaskType
}

// TaskExecutionParams contains parameters for task execution.
type TaskExecutionParams struct {
	Task        *manufacturing.ManufacturingTask
	ShipSymbol  string
	PlayerID    shared.PlayerID
	ContainerID string
}

// TaskExecutorRegistry maps task types to their executors.
// This implements the Open/Closed Principle - add new executors
// without modifying existing code.
type TaskExecutorRegistry struct {
	executors map[manufacturing.TaskType]TaskExecutor
}

// NewTaskExecutorRegistry creates a new registry for task executors.
func NewTaskExecutorRegistry() *TaskExecutorRegistry {
	return &TaskExecutorRegistry{
		executors: make(map[manufacturing.TaskType]TaskExecutor),
	}
}

// Register adds an executor to the registry.
// If an executor for this task type already exists, it will be replaced.
func (r *TaskExecutorRegistry) Register(executor TaskExecutor) {
	r.executors[executor.TaskType()] = executor
}

// GetExecutor retrieves the executor for a specific task type.
func (r *TaskExecutorRegistry) GetExecutor(taskType manufacturing.TaskType) (TaskExecutor, error) {
	executor, ok := r.executors[taskType]
	if !ok {
		return nil, fmt.Errorf("no executor registered for task type: %s", taskType)
	}
	return executor, nil
}

// HasExecutor checks if an executor is registered for the given task type.
func (r *TaskExecutorRegistry) HasExecutor(taskType manufacturing.TaskType) bool {
	_, ok := r.executors[taskType]
	return ok
}

// RegisteredTypes returns all task types that have registered executors.
func (r *TaskExecutorRegistry) RegisteredTypes() []manufacturing.TaskType {
	types := make([]manufacturing.TaskType, 0, len(r.executors))
	for taskType := range r.executors {
		types = append(types, taskType)
	}
	return types
}

// NewDefaultTaskExecutorRegistry creates a registry with all standard executors registered.
// This is a convenience function for production use.
func NewDefaultTaskExecutorRegistry(
	navigator Navigator,
	purchaser *ManufacturingPurchaser,
	seller *ManufacturingSeller,
) *TaskExecutorRegistry {
	registry := NewTaskExecutorRegistry()

	// Register all standard executors
	registry.Register(NewAcquireDeliverExecutor(navigator, purchaser, seller))
	registry.Register(NewCollectSellExecutor(navigator, purchaser, seller))
	registry.Register(NewLiquidateExecutor(navigator, seller))

	return registry
}

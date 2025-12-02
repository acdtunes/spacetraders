package manufacturing

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// =============================================================================
// Interface Segregation: Focused interfaces following ISP
// =============================================================================

// -----------------------------------------------------------------------------
// Task Assignment Interfaces (split from TaskAssigner)
// -----------------------------------------------------------------------------

// TaskAssignmentExecutor assigns tasks to ships.
// This is the core task assignment interface.
type TaskAssignmentExecutor interface {
	AssignTasks(ctx context.Context, params AssignParams) (int, error)
}

// ShipLocator finds ships and task locations.
// Used by assignment and other ship-related operations.
type ShipLocator interface {
	// FindClosestShip finds the ship closest to target waypoint.
	FindClosestShip(ships map[string]*navigation.Ship, target *shared.Waypoint) (*navigation.Ship, string)

	// GetTaskSourceLocation returns the waypoint where task starts.
	GetTaskSourceLocation(ctx context.Context, task *manufacturing.ManufacturingTask, playerID int) *shared.Waypoint
}

// MarketConditionEvaluator evaluates market conditions for task readiness.
type MarketConditionEvaluator interface {
	// IsSellMarketSaturated checks if market has HIGH/ABUNDANT supply.
	IsSellMarketSaturated(ctx context.Context, sellMarket, good string, playerID int) bool

	// IsFactoryOutputReady checks if factory has HIGH/ABUNDANT supply for collection.
	IsFactoryOutputReady(ctx context.Context, factorySymbol, good string, playerID int) bool

	// IsFactoryInputSaturated checks if factory input is already saturated.
	IsFactoryInputSaturated(ctx context.Context, factorySymbol, inputGood string, playerID int) bool
}

// AssignmentStateTracker tracks task assignment state in memory.
type AssignmentStateTracker interface {
	// Track records a task assignment.
	Track(taskID, shipSymbol, containerID string, taskType manufacturing.TaskType)

	// Untrack removes a task assignment.
	Untrack(taskID string)

	// IsTaskTracked returns true if the task is currently tracked.
	IsTaskTracked(taskID string) bool

	// IsShipAssigned returns true if the ship is currently assigned.
	IsShipAssigned(shipSymbol string) bool

	// GetCount returns the number of tracked assignments.
	GetCount() int

	// GetAllocations returns current allocations for reservation policy.
	GetAllocations() manufacturing.TaskTypeAllocations
}

// AssignmentReconciliator syncs assignment state with database.
type AssignmentReconciliator interface {
	// Reconcile syncs in-memory state with database.
	Reconcile(ctx context.Context, playerID int)

	// CountAssignedByType returns counts of assigned tasks by type.
	CountAssignedByType(ctx context.Context, playerID int) map[manufacturing.TaskType]int
}

// -----------------------------------------------------------------------------
// Pipeline Management Interfaces (split from PipelineManager)
// -----------------------------------------------------------------------------

// PipelineCreator creates new manufacturing pipelines.
type PipelineCreator interface {
	// ScanAndCreatePipelines finds opportunities and creates new pipelines.
	ScanAndCreatePipelines(ctx context.Context, params PipelineScanParams) (int, error)
}

// PipelineCompletionDetector detects and marks complete pipelines.
type PipelineCompletionDetector interface {
	// CheckPipelineCompletion checks if pipeline is complete and updates status.
	CheckPipelineCompletion(ctx context.Context, pipelineID string) (bool, error)

	// CheckAllPipelinesForCompletion checks all active pipelines.
	CheckAllPipelinesForCompletion(ctx context.Context) int
}

// PipelineRecoveryHandler handles stuck pipeline recovery.
type PipelineRecoveryHandler interface {
	// DetectStuckPipelines finds pipelines that are stuck.
	DetectStuckPipelines(ctx context.Context, playerID int) []string

	// RecyclePipeline cancels a stuck pipeline and frees its slot.
	RecyclePipeline(ctx context.Context, pipelineID string, playerID int) error

	// DetectAndRecycleStuckPipelines finds and recycles stuck pipelines.
	DetectAndRecycleStuckPipelines(ctx context.Context, playerID int) int
}

// PipelineRegistry tracks active pipelines.
type PipelineRegistry interface {
	// Register adds a pipeline to the registry.
	Register(pipeline *manufacturing.ManufacturingPipeline)

	// Unregister removes a pipeline from the registry.
	Unregister(pipelineID string)

	// Get returns a pipeline by ID.
	Get(pipelineID string) *manufacturing.ManufacturingPipeline

	// GetAll returns all active pipelines.
	GetAll() map[string]*manufacturing.ManufacturingPipeline

	// HasPipelineForGood checks if active pipeline exists for this good.
	HasPipelineForGood(good string) bool

	// Count returns the number of active pipelines.
	Count() int
}

// TaskRescueHandler rescues stale tasks from database.
type TaskRescueHandler interface {
	// RescueReadyTasks loads READY tasks from DB and re-enqueues them.
	RescueReadyTasks(ctx context.Context, playerID int) RescueResult

	// RescueFailedTasks rescues FAILED tasks that can be retried.
	RescueFailedTasks(ctx context.Context, playerID int) int
}

// -----------------------------------------------------------------------------
// Task Queue Interfaces
// -----------------------------------------------------------------------------

// TaskQueueManager manages the task queue.
type TaskQueueManager interface {
	// Enqueue adds a task to the queue.
	Enqueue(task *manufacturing.ManufacturingTask)

	// Dequeue removes and returns the highest priority task.
	Dequeue() *manufacturing.ManufacturingTask

	// Remove removes a specific task from the queue.
	Remove(taskID string)

	// GetReadyTasks returns all ready tasks sorted by priority.
	GetReadyTasks() []*manufacturing.ManufacturingTask

	// HasReadyTasksByType checks if there are ready tasks of a specific type.
	HasReadyTasksByType(taskType manufacturing.TaskType) bool
}

// -----------------------------------------------------------------------------
// Composite Interfaces (for backward compatibility)
// -----------------------------------------------------------------------------

// Note: The original TaskAssigner and PipelineManager interfaces are kept in
// their respective files for backward compatibility. New code should prefer
// the focused interfaces above.

package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// PipelineRecycler handles stuck pipeline detection and recovery.
// Uses StuckPipelineFailedTaskThreshold from pipeline_lifecycle_manager.go
type PipelineRecycler struct {
	pipelineRepo       manufacturing.PipelineRepository
	taskRepo           manufacturing.TaskRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	taskQueue          services.ManufacturingTaskQueue
	factoryTracker     *manufacturing.FactoryStateTracker
	registry           *ActivePipelineRegistry
}

// NewPipelineRecycler creates a new pipeline recycler.
func NewPipelineRecycler(
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	taskQueue services.ManufacturingTaskQueue,
	factoryTracker *manufacturing.FactoryStateTracker,
	registry *ActivePipelineRegistry,
) *PipelineRecycler {
	return &PipelineRecycler{
		pipelineRepo:       pipelineRepo,
		taskRepo:           taskRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		taskQueue:          taskQueue,
		factoryTracker:     factoryTracker,
		registry:           registry,
	}
}

// DetectStuckPipelines finds pipelines that are stuck based on failed task count.
// Returns the IDs of stuck pipelines.
func (r *PipelineRecycler) DetectStuckPipelines(ctx context.Context, playerID int) []string {
	if r.taskRepo == nil || r.registry == nil {
		return nil
	}

	pipelines := r.registry.GetAll()
	stuckPipelines := make([]string, 0)

	for pipelineID := range pipelines {
		tasks, err := r.taskRepo.FindByPipelineID(ctx, pipelineID)
		if err != nil {
			continue
		}

		// Count failed tasks
		failedTasks := 0
		for _, task := range tasks {
			if task.Status() == manufacturing.TaskStatusFailed {
				failedTasks++
			}
		}

		// Only recycle if threshold exceeded
		if failedTasks >= StuckPipelineFailedTaskThreshold {
			stuckPipelines = append(stuckPipelines, pipelineID)
		}
	}

	return stuckPipelines
}

// DetectAndRecycleStuckPipelines finds and recycles stuck pipelines.
// Returns the count of pipelines recycled.
func (r *PipelineRecycler) DetectAndRecycleStuckPipelines(ctx context.Context, playerID int) int {
	logger := common.LoggerFromContext(ctx)

	stuckPipelines := r.DetectStuckPipelines(ctx, playerID)
	if len(stuckPipelines) == 0 {
		return 0
	}

	for _, pipelineID := range stuckPipelines {
		pipeline := r.registry.Get(pipelineID)
		if pipeline != nil {
			logger.Log("WARN", "Detected stuck pipeline", map[string]interface{}{
				"pipeline_id": pipelineID[:8],
				"good":        pipeline.ProductGood(),
			})
		}
		_ = r.RecyclePipeline(ctx, pipelineID, playerID)
	}

	logger.Log("INFO", fmt.Sprintf("Recycled %d stuck pipelines", len(stuckPipelines)), nil)
	return len(stuckPipelines)
}

// RecyclePipeline cancels a stuck pipeline and frees its slot.
func (r *PipelineRecycler) RecyclePipeline(ctx context.Context, pipelineID string, playerID int) error {
	logger := common.LoggerFromContext(ctx)

	// Cancel all incomplete tasks
	if r.taskRepo != nil {
		tasks, err := r.taskRepo.FindByPipelineID(ctx, pipelineID)
		if err == nil {
			for _, task := range tasks {
				if r.shouldCancelTask(task) {
					// Release ship assignment
					if task.AssignedShip() != "" && r.shipAssignmentRepo != nil {
						_ = r.shipAssignmentRepo.Release(ctx, task.AssignedShip(), playerID, "pipeline_recycled")
					}
					if err := task.Cancel("pipeline recycled"); err == nil {
						_ = r.taskRepo.Update(ctx, task)
					}
				}
				// Remove from queue
				if r.taskQueue != nil {
					r.taskQueue.Remove(task.ID())
				}
			}
		}
	}

	// Remove factory states
	if r.factoryTracker != nil {
		r.factoryTracker.RemovePipeline(pipelineID)
	}

	// Mark pipeline as cancelled
	pipeline := r.registry.Get(pipelineID)
	if pipeline != nil {
		if err := pipeline.Cancel(); err == nil {
			if r.pipelineRepo != nil {
				_ = r.pipelineRepo.Update(ctx, pipeline)
			}
			metrics.RecordManufacturingPipelineCompletion(
				pipeline.PlayerID(),
				pipeline.ProductGood(),
				"cancelled",
				pipeline.RuntimeDuration(),
				0,
			)
		}
		r.registry.Unregister(pipelineID)
	}

	logger.Log("INFO", "Recycled stuck pipeline", map[string]interface{}{
		"pipeline_id": pipelineID[:8],
	})

	return nil
}

// shouldCancelTask returns true if the task should be cancelled during recycling.
func (r *PipelineRecycler) shouldCancelTask(task *manufacturing.ManufacturingTask) bool {
	switch task.Status() {
	case manufacturing.TaskStatusPending,
		manufacturing.TaskStatusReady,
		manufacturing.TaskStatusAssigned:
		return true
	default:
		return false
	}
}

// GetStuckPipelineThreshold returns the threshold for stuck pipeline detection.
func (r *PipelineRecycler) GetStuckPipelineThreshold() int {
	return StuckPipelineFailedTaskThreshold
}

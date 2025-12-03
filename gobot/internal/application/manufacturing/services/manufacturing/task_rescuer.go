package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// TaskRescuer loads stale READY tasks from DB and re-enqueues them.
// Handles tasks that may have been left in READY status after restart or crash.
type TaskRescuer struct {
	taskRepo         manufacturing.TaskRepository
	taskQueue        services.ManufacturingTaskQueue
	conditionChecker *MarketConditionChecker
}

// NewTaskRescuer creates a new task rescuer.
func NewTaskRescuer(
	taskRepo manufacturing.TaskRepository,
	taskQueue services.ManufacturingTaskQueue,
	conditionChecker *MarketConditionChecker,
) *TaskRescuer {
	return &TaskRescuer{
		taskRepo:         taskRepo,
		taskQueue:        taskQueue,
		conditionChecker: conditionChecker,
	}
}

// RescueReadyTasks loads READY tasks from EXECUTING pipelines and enqueues them.
// Validates task state against current market conditions before enqueuing.
// Only rescues tasks from active (PLANNING/EXECUTING) pipelines - tasks from
// FAILED/CANCELLED/COMPLETED pipelines are skipped to prevent endless rescue loops.
func (r *TaskRescuer) RescueReadyTasks(ctx context.Context, playerID int) RescueResult {
	if r.taskRepo == nil {
		return RescueResult{}
	}

	logger := common.LoggerFromContext(ctx)
	result := RescueResult{}

	// Load READY tasks from active pipelines only (excludes FAILED/CANCELLED/COMPLETED)
	readyTasks, err := r.taskRepo.FindReadyWithActivePipeline(ctx, playerID)
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("Failed to find ready tasks: %v", err), nil)
		return result
	}

	for _, task := range readyTasks {
		switch task.TaskType() {
		case manufacturing.TaskTypeCollectSell:
			rescued := r.rescueCollectSellTask(ctx, task, playerID)
			if rescued {
				result.CollectSellRescued++
			} else {
				result.ResetToPending++
			}

		case manufacturing.TaskTypeAcquireDeliver:
			rescued := r.rescueAcquireDeliverTask(ctx, task, playerID)
			if rescued {
				result.AcquireDeliverRescued++
			} else {
				result.ResetToPending++
			}

		case manufacturing.TaskTypeStorageAcquireDeliver:
			// STORAGE_ACQUIRE_DELIVER tasks pick up from storage ships and deliver to factory
			// Same validation as ACQUIRE_DELIVER - check factory input not saturated
			rescued := r.rescueStorageAcquireDeliverTask(ctx, task, playerID)
			if rescued {
				result.StorageAcquireDeliverRescued++
			} else {
				result.ResetToPending++
			}
		}
	}

	// Log results
	if result.CollectSellRescued > 0 {
		logger.Log("DEBUG", fmt.Sprintf("Rescued %d COLLECT_SELL tasks to queue", result.CollectSellRescued), nil)
	}
	if result.AcquireDeliverRescued > 0 {
		logger.Log("DEBUG", fmt.Sprintf("Rescued %d ACQUIRE_DELIVER tasks to queue", result.AcquireDeliverRescued), nil)
	}
	if result.StorageAcquireDeliverRescued > 0 {
		logger.Log("DEBUG", fmt.Sprintf("Rescued %d STORAGE_ACQUIRE_DELIVER tasks to queue", result.StorageAcquireDeliverRescued), nil)
	}
	if result.ResetToPending > 0 {
		logger.Log("DEBUG", fmt.Sprintf("Reset %d tasks to PENDING due to supply conditions", result.ResetToPending), nil)
	}

	return result
}

// rescueCollectSellTask attempts to rescue a COLLECT_SELL task.
// Returns true if rescued, false if reset to PENDING.
func (r *TaskRescuer) rescueCollectSellTask(
	ctx context.Context,
	task *manufacturing.ManufacturingTask,
	playerID int,
) bool {
	if r.conditionChecker != nil {
		// Storage-based collection tasks don't need factory supply checks
		// They collect from storage ships, not factory markets
		if !task.IsStorageBasedCollection() {
			// Check 1: Factory must have HIGH/ABUNDANT supply to collect
			if !r.conditionChecker.IsFactoryOutputReady(ctx, task.FactorySymbol(), task.Good(), playerID) {
				r.resetToPending(ctx, task)
				return false
			}
		}

		// Check 2: Sell market must not be saturated (applies to all COLLECT_SELL)
		if r.conditionChecker.IsSellMarketSaturated(ctx, task.TargetMarket(), task.Good(), playerID) {
			r.resetToPending(ctx, task)
			return false
		}
	}

	// All checks passed - enqueue
	if r.taskQueue != nil {
		r.taskQueue.Enqueue(task)
	}
	return true
}

// rescueAcquireDeliverTask attempts to rescue an ACQUIRE_DELIVER task.
// Returns true if rescued, false if reset to PENDING.
func (r *TaskRescuer) rescueAcquireDeliverTask(
	ctx context.Context,
	task *manufacturing.ManufacturingTask,
	playerID int,
) bool {
	if r.conditionChecker != nil {
		// Check: Factory input is not already saturated
		if r.conditionChecker.IsFactoryInputSaturated(ctx, task.FactorySymbol(), task.Good(), playerID) {
			r.resetToPending(ctx, task)
			return false
		}
	}

	// All checks passed - enqueue
	if r.taskQueue != nil {
		r.taskQueue.Enqueue(task)
	}
	return true
}

// rescueStorageAcquireDeliverTask attempts to rescue a STORAGE_ACQUIRE_DELIVER task.
// These tasks pick up cargo from storage ships and deliver to factories.
// Returns true if rescued, false if reset to PENDING.
func (r *TaskRescuer) rescueStorageAcquireDeliverTask(
	ctx context.Context,
	task *manufacturing.ManufacturingTask,
	playerID int,
) bool {
	if r.conditionChecker != nil {
		// Check: Factory input is not already saturated
		if r.conditionChecker.IsFactoryInputSaturated(ctx, task.FactorySymbol(), task.Good(), playerID) {
			r.resetToPending(ctx, task)
			return false
		}
	}

	// All checks passed - enqueue
	if r.taskQueue != nil {
		r.taskQueue.Enqueue(task)
	}
	return true
}

// resetToPending resets a task to PENDING status and removes it from the queue.
// This prevents stale queue entries from being assigned when conditions change.
func (r *TaskRescuer) resetToPending(ctx context.Context, task *manufacturing.ManufacturingTask) {
	if err := task.ResetToPending(); err == nil {
		if r.taskRepo != nil {
			_ = r.taskRepo.Update(ctx, task)
		}
		// Remove from queue to prevent stale task from being assigned
		// The queue may have an old copy from a previous rescue cycle
		if r.taskQueue != nil {
			r.taskQueue.Remove(task.ID())
		}
	}
}

// RescueResult contains the results of a rescue operation.
type RescueResult struct {
	CollectSellRescued           int
	AcquireDeliverRescued        int
	StorageAcquireDeliverRescued int
	ResetToPending               int
}

// TotalRescued returns the total number of tasks rescued.
func (r RescueResult) TotalRescued() int {
	return r.CollectSellRescued + r.AcquireDeliverRescued + r.StorageAcquireDeliverRescued
}

// RescueFailedTasks rescues FAILED tasks that can be retried.
func (r *TaskRescuer) RescueFailedTasks(ctx context.Context, playerID int) int {
	if r.taskRepo == nil {
		return 0
	}

	logger := common.LoggerFromContext(ctx)

	failedTasks, err := r.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusFailed)
	if err != nil {
		return 0
	}

	rescued := 0
	for _, task := range failedTasks {
		// LIQUIDATE tasks should NOT be rescued - if they failed with "no cargo",
		// the cargo is gone and there's nothing to liquidate. Rescuing them creates
		// an infinite loop of failed LIQUIDATE tasks.
		if task.TaskType() == manufacturing.TaskTypeLiquidate {
			continue
		}

		if !task.CanRetry() {
			continue
		}

		// Reset for retry
		if err := task.ResetForRetry(); err != nil {
			continue
		}

		// Mark ready
		if err := task.MarkReady(); err != nil {
			continue
		}

		if r.taskRepo != nil {
			_ = r.taskRepo.Update(ctx, task)
		}

		if r.taskQueue != nil {
			r.taskQueue.Enqueue(task)
		}

		rescued++
	}

	if rescued > 0 {
		logger.Log("DEBUG", fmt.Sprintf("Rescued %d failed tasks for retry", rescued), nil)
	}

	return rescued
}

package services

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// ActivateCollectionPipelineTasks activates PENDING and enqueues READY COLLECT_SELL tasks from COLLECTION pipelines.
// COLLECTION pipelines have no factory states, so they're not handled by the normal factory polling.
// This method ensures tasks (from recovery, retry, or un-saturated markets) get activated and enqueued.
func (a *TaskActivator) ActivateCollectionPipelineTasks(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	if a.taskRepo == nil || a.pipelineRepo == nil {
		return 0
	}

	pipelineCache := make(map[string]*manufacturing.ManufacturingPipeline)

	activated, lastActivatedPipelineID := a.promotePendingCollectTasks(ctx, pipelineCache)

	enqueued, lastEnqueuedPipelineID, ok := a.enqueueReadyCollectTasks(ctx, pipelineCache)
	activated += enqueued
	if enqueued > 0 {
		lastActivatedPipelineID = lastEnqueuedPipelineID
	}
	if !ok {
		return activated
	}

	if activated > 0 {
		logger.Log("INFO", "COLLECTION pipeline task activation summary", map[string]interface{}{
			"activated": activated,
		})
		a.notifier.notifyTasksReady(lastActivatedPipelineID)
	}

	return activated
}

// promotePendingCollectTasks marks PENDING COLLECT_SELL tasks READY once their factory is ABUNDANT
// and their sell market is not saturated. ABUNDANT rather than HIGH is deliberate: it buffers the
// supply drop that can happen while the ship is still navigating, and the executor still collects
// if supply has only fallen to HIGH on arrival.
func (a *TaskActivator) promotePendingCollectTasks(ctx context.Context, pipelineCache map[string]*manufacturing.ManufacturingPipeline) (int, string) {
	logger := common.LoggerFromContext(ctx)

	pendingTasks, err := a.taskRepo.FindByStatus(ctx, a.playerID, manufacturing.TaskStatusPending)
	if err != nil {
		return 0, ""
	}

	activated := 0
	lastActivatedPipelineID := ""
	for _, task := range pendingTasks {
		if task.TaskType() != manufacturing.TaskTypeCollectSell {
			continue
		}

		pipelineID := task.PipelineID()
		if a.executingCollectionPipeline(ctx, pipelineCache, pipelineID) == nil {
			continue
		}

		factorySupply := a.supply.sourceMarketSupply(ctx, task.FactorySymbol(), task.Good())
		if factorySupply != supplyAbundant {
			continue
		}

		if a.supply.sellMarketSaturated(ctx, task.TargetMarket(), task.Good()) {
			continue
		}

		if err := task.MarkReady(); err != nil {
			continue
		}
		if err := a.taskRepo.Update(ctx, task); err != nil {
			continue
		}
		a.taskQueue.Enqueue(task)
		activated++
		lastActivatedPipelineID = pipelineID

		logger.Log("INFO", "Activated PENDING COLLECTION task", map[string]interface{}{
			"task":           shortID(task.ID()),
			"good":           task.Good(),
			"factory":        task.FactorySymbol(),
			"factory_supply": factorySupply,
		})
	}
	return activated, lastActivatedPipelineID
}

// enqueueReadyCollectTasks queues COLLECT_SELL tasks already marked READY, sending any whose sell
// market has since saturated back to PENDING rather than dispatching into a flooded market.
// ok is false when the tasks could not be read at all, which suppresses the caller's summary.
func (a *TaskActivator) enqueueReadyCollectTasks(ctx context.Context, pipelineCache map[string]*manufacturing.ManufacturingPipeline) (int, string, bool) {
	logger := common.LoggerFromContext(ctx)

	readyTasks, err := a.taskRepo.FindByStatus(ctx, a.playerID, manufacturing.TaskStatusReady)
	if err != nil {
		logger.Log("WARN", "Failed to find ready tasks for COLLECTION pipeline check", map[string]interface{}{
			"error": err.Error(),
		})
		return 0, "", false
	}

	activated := 0
	lastActivatedPipelineID := ""
	for _, task := range readyTasks {
		if task.TaskType() != manufacturing.TaskTypeCollectSell {
			continue
		}

		pipelineID := task.PipelineID()
		if a.executingCollectionPipeline(ctx, pipelineCache, pipelineID) == nil {
			continue
		}

		if a.supply.sellMarketSaturated(ctx, task.TargetMarket(), task.Good()) {
			task.ResetToPending()
			_ = a.taskRepo.Update(ctx, task)
			logger.Log("DEBUG", "COLLECTION task sell market saturated - reset to PENDING", map[string]interface{}{
				"task":        shortID(task.ID()),
				"good":        task.Good(),
				"sell_market": task.TargetMarket(),
			})
			continue
		}

		a.taskQueue.Enqueue(task)
		activated++
		lastActivatedPipelineID = pipelineID

		logger.Log("INFO", "Enqueued READY COLLECTION task", map[string]interface{}{
			"task":        shortID(task.ID()),
			"good":        task.Good(),
			"sell_market": task.TargetMarket(),
		})
	}
	return activated, lastActivatedPipelineID, true
}

func (a *TaskActivator) executingCollectionPipeline(ctx context.Context, cache map[string]*manufacturing.ManufacturingPipeline, pipelineID string) *manufacturing.ManufacturingPipeline {
	pipeline, cached := cache[pipelineID]
	if !cached {
		pipeline, _ = a.pipelineRepo.FindByID(ctx, pipelineID)
		cache[pipelineID] = pipeline
	}
	if pipeline == nil || pipeline.PipelineType() != manufacturing.PipelineTypeCollection {
		return nil
	}
	if pipeline.Status() != manufacturing.PipelineStatusExecuting {
		return nil
	}
	return pipeline
}

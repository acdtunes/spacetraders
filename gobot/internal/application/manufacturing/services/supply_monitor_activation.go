package services

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// checkDependenciesComplete checks if all task dependencies are complete
func (m *SupplyMonitor) checkDependenciesComplete(ctx context.Context, task *manufacturing.ManufacturingTask) bool {
	if m.taskRepo == nil {
		return true // Assume complete if no repo
	}

	for _, depID := range task.DependsOn() {
		depTask, err := m.taskRepo.FindByID(ctx, depID)
		if err != nil {
			return false
		}
		if depTask == nil || depTask.Status() != manufacturing.TaskStatusCompleted {
			return false
		}
	}

	return true
}

// ActivateSupplyGatedTasks checks all PENDING ACQUIRE_DELIVER tasks and activates
// those whose source market now has HIGH/ABUNDANT supply.
// Raw materials (ores, crystals, gases) are activated immediately since they bypass supply-gating.
// This is called during each poll cycle to process supply-gated tasks.
func (m *SupplyMonitor) ActivateSupplyGatedTasks(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	if m.taskRepo == nil {
		return 0
	}

	// Find all PENDING ACQUIRE_DELIVER tasks for this player
	pendingTasks, err := m.taskRepo.FindByStatus(ctx, m.playerID, manufacturing.TaskStatusPending)
	if err != nil {
		logger.Log("WARN", "Failed to find pending tasks for supply-gate check", map[string]interface{}{
			"error": err.Error(),
		})
		return 0
	}

	// Cache pipeline status lookups to avoid repeated DB queries
	pipelineStatusCache := make(map[string]manufacturing.PipelineStatus)

	activated := 0
	lastActivatedPipelineID := ""
	for _, task := range pendingTasks {
		// Only process ACQUIRE_DELIVER tasks
		if task.TaskType() != manufacturing.TaskTypeAcquireDeliver {
			continue
		}

		// CRITICAL: Verify pipeline is still EXECUTING before activating task
		// Tasks from CANCELLED/FAILED/COMPLETED pipelines should not be activated
		pipelineID := task.PipelineID()
		pipelineStatus, found := m.cachedPipelineStatus(ctx, pipelineStatusCache, pipelineID)
		if !found {
			logger.Log("DEBUG", "Skipping task - pipeline not found", map[string]interface{}{
				"task_id":     shortID(task.ID()),
				"pipeline_id": shortID(pipelineID),
			})
			continue
		}

		if pipelineStatus != manufacturing.PipelineStatusExecuting {
			logger.Log("DEBUG", "Skipping task from non-executing pipeline", map[string]interface{}{
				"task_id":         shortID(task.ID()),
				"pipeline_id":     shortID(pipelineID),
				"pipeline_status": pipelineStatus,
			})
			continue
		}

		// FACTORY INPUT SATURATION CHECK: Skip activation if factory already has HIGH/ABUNDANT supply
		// This prevents acquiring more goods for markets that don't need them
		factoryInputSupply := m.getSourceMarketSupply(ctx, task.FactorySymbol(), task.Good())
		if isHighOrAbundant(factoryInputSupply) {
			logger.Log("DEBUG", "Skipping task - factory input already saturated", map[string]interface{}{
				"task_id":        shortID(task.ID()),
				"good":           task.Good(),
				"factory":        task.FactorySymbol(),
				"factory_supply": factoryInputSupply,
			})
			continue
		}

		isRawMaterial := goods.IsMineableRawMaterial(task.Good())
		sourceSupply := m.getSourceMarketSupply(ctx, task.SourceMarket(), task.Good())

		var reason string
		if acceptableSourceSupply(sourceSupply, isRawMaterial) {
			if isRawMaterial {
				reason = "raw_material_good_supply"
			} else {
				reason = "acceptable_supply"
			}
		} else {
			// RE-SOURCING: Current source has bad supply, try to find a better market
			// This prevents tasks from being stuck forever when their original source degrades
			betterSupply, resourced := m.resourcePendingTask(ctx, task, isRawMaterial)
			if !resourced {
				continue
			}
			sourceSupply = betterSupply
			reason = "re-sourced"
		}

		applySourceSupplyPriority(task, sourceSupply)

		// Activate the task
		if err := task.MarkReady(); err != nil {
			logger.Log("WARN", "Failed to mark supply-gated task ready", map[string]interface{}{
				"task_id": shortID(task.ID()),
				"error":   err.Error(),
			})
			continue
		}

		// Persist the change
		if err := m.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARN", "Failed to persist activated task", map[string]interface{}{
				"task_id": shortID(task.ID()),
				"error":   err.Error(),
			})
			continue
		}

		// Add to queue
		m.taskQueue.Enqueue(task)
		activated++
		lastActivatedPipelineID = pipelineID

		logger.Log("INFO", "Activated supply-gated ACQUIRE_DELIVER task", map[string]interface{}{
			"task_id":       shortID(task.ID()),
			"good":          task.Good(),
			"source":        task.SourceMarket(),
			"source_supply": sourceSupply,
			"factory":       task.FactorySymbol(),
			"reason":        reason,
		})
	}

	if activated > 0 {
		logger.Log("INFO", "Supply-gated task activation summary", map[string]interface{}{
			"activated": activated,
		})
		m.notifyTaskReady(lastActivatedPipelineID)
	}

	return activated
}

func (m *SupplyMonitor) cachedPipelineStatus(ctx context.Context, cache map[string]manufacturing.PipelineStatus, pipelineID string) (manufacturing.PipelineStatus, bool) {
	if status, cached := cache[pipelineID]; cached {
		return status, true
	}
	if m.pipelineRepo == nil {
		return "", true
	}
	pipeline, err := m.pipelineRepo.FindByID(ctx, pipelineID)
	if err != nil || pipeline == nil {
		return "", false
	}
	cache[pipelineID] = pipeline.Status()
	return pipeline.Status(), true
}

func (m *SupplyMonitor) resourcePendingTask(ctx context.Context, task *manufacturing.ManufacturingTask, isRawMaterial bool) (string, bool) {
	logger := common.LoggerFromContext(ctx)

	systemSymbol := extractSystem(task.FactorySymbol())

	var betterSource *MarketLocatorResult
	var err error
	if isRawMaterial {
		betterSource, err = m.marketLocator.FindExportMarketWithGoodSupply(ctx, task.Good(), systemSymbol, m.playerID)
	} else {
		betterSource, err = m.marketLocator.FindExportMarketBySupplyPriority(ctx, task.Good(), systemSymbol, m.playerID)
	}
	if err != nil || betterSource == nil {
		return "", false
	}

	betterSupply := m.getSourceMarketSupply(ctx, betterSource.WaypointSymbol, task.Good())
	if !acceptableSourceSupply(betterSupply, isRawMaterial) {
		return "", false
	}

	oldSource := task.SourceMarket()
	if err := task.UpdateSourceMarket(betterSource.WaypointSymbol); err != nil {
		logger.Log("WARN", "Failed to update task source market", map[string]interface{}{
			"task_id": shortID(task.ID()),
			"error":   err.Error(),
		})
		return "", false
	}

	logger.Log("INFO", "Re-sourced PENDING task to better market", map[string]interface{}{
		"task_id":    shortID(task.ID()),
		"good":       task.Good(),
		"old_source": oldSource,
		"new_source": betterSource.WaypointSymbol,
		"new_supply": betterSupply,
	})

	return betterSupply, true
}

// ActivateCollectionPipelineTasks activates PENDING and enqueues READY COLLECT_SELL tasks from COLLECTION pipelines.
// COLLECTION pipelines have no factory states, so they're not handled by the normal factory polling.
// This method ensures tasks (from recovery, retry, or un-saturated markets) get activated and enqueued.
func (m *SupplyMonitor) ActivateCollectionPipelineTasks(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	if m.taskRepo == nil || m.pipelineRepo == nil {
		return 0
	}

	// Cache pipeline lookups
	pipelineCache := make(map[string]*manufacturing.ManufacturingPipeline)
	activated := 0
	lastActivatedPipelineID := ""

	// Step 1: Activate PENDING COLLECTION pipeline tasks if conditions are favorable
	pendingTasks, err := m.taskRepo.FindByStatus(ctx, m.playerID, manufacturing.TaskStatusPending)
	if err == nil {
		for _, task := range pendingTasks {
			if task.TaskType() != manufacturing.TaskTypeCollectSell {
				continue
			}

			pipelineID := task.PipelineID()
			if m.executingCollectionPipeline(ctx, pipelineCache, pipelineID) == nil {
				continue
			}

			// Check factory supply - need ABUNDANT to START (buffer for supply drops during navigation)
			// Executor will still collect if supply is HIGH when ship arrives
			factorySupply := m.getSourceMarketSupply(ctx, task.FactorySymbol(), task.Good())
			if factorySupply != supplyAbundant {
				continue
			}

			// Check sell market - should not be saturated
			if m.isSellMarketSaturated(ctx, task.TargetMarket(), task.Good()) {
				continue
			}

			// Activate the task
			if err := task.MarkReady(); err != nil {
				continue
			}
			if err := m.taskRepo.Update(ctx, task); err != nil {
				continue
			}
			m.taskQueue.Enqueue(task)
			activated++
			lastActivatedPipelineID = pipelineID

			logger.Log("INFO", "Activated PENDING COLLECTION task", map[string]interface{}{
				"task":           shortID(task.ID()),
				"good":           task.Good(),
				"factory":        task.FactorySymbol(),
				"factory_supply": factorySupply,
			})
		}
	}

	// Step 2: Enqueue READY COLLECTION pipeline tasks that aren't in queue
	readyTasks, err := m.taskRepo.FindByStatus(ctx, m.playerID, manufacturing.TaskStatusReady)
	if err != nil {
		logger.Log("WARN", "Failed to find ready tasks for COLLECTION pipeline check", map[string]interface{}{
			"error": err.Error(),
		})
		return activated
	}

	for _, task := range readyTasks {
		if task.TaskType() != manufacturing.TaskTypeCollectSell {
			continue
		}

		pipelineID := task.PipelineID()
		if m.executingCollectionPipeline(ctx, pipelineCache, pipelineID) == nil {
			continue
		}

		// Check sell market saturation before enqueueing
		if m.isSellMarketSaturated(ctx, task.TargetMarket(), task.Good()) {
			task.ResetToPending()
			_ = m.taskRepo.Update(ctx, task)
			logger.Log("DEBUG", "COLLECTION task sell market saturated - reset to PENDING", map[string]interface{}{
				"task":        shortID(task.ID()),
				"good":        task.Good(),
				"sell_market": task.TargetMarket(),
			})
			continue
		}

		// Enqueue the task
		m.taskQueue.Enqueue(task)
		activated++
		lastActivatedPipelineID = pipelineID

		logger.Log("INFO", "Enqueued READY COLLECTION task", map[string]interface{}{
			"task":        shortID(task.ID()),
			"good":        task.Good(),
			"sell_market": task.TargetMarket(),
		})
	}

	if activated > 0 {
		logger.Log("INFO", "COLLECTION pipeline task activation summary", map[string]interface{}{
			"activated": activated,
		})
		m.notifyTaskReady(lastActivatedPipelineID)
	}

	return activated
}

func (m *SupplyMonitor) executingCollectionPipeline(ctx context.Context, cache map[string]*manufacturing.ManufacturingPipeline, pipelineID string) *manufacturing.ManufacturingPipeline {
	pipeline, cached := cache[pipelineID]
	if !cached {
		pipeline, _ = m.pipelineRepo.FindByID(ctx, pipelineID)
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

// ActivateConstructionTasks checks all PENDING DELIVER_TO_CONSTRUCTION tasks and
// activates those whose dependencies are complete. Construction deliveries have a
// fixed bill at the construction site, so no supply gating is applied beyond
// requiring the pipeline to be EXECUTING and dependencies to be COMPLETED.
func (m *SupplyMonitor) ActivateConstructionTasks(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	if m.taskRepo == nil {
		return 0
	}

	pendingTasks, err := m.taskRepo.FindByStatus(ctx, m.playerID, manufacturing.TaskStatusPending)
	if err != nil {
		logger.Log("WARN", "Failed to find pending tasks for construction activation", map[string]interface{}{
			"error": err.Error(),
		})
		return 0
	}

	// Cache pipeline status lookups to avoid repeated DB queries
	pipelineStatusCache := make(map[string]manufacturing.PipelineStatus)

	activated := 0
	lastActivatedPipelineID := ""
	for _, task := range pendingTasks {
		if task.TaskType() != manufacturing.TaskTypeDeliverToConstruction {
			continue
		}

		// Verify pipeline is still EXECUTING before activating task
		pipelineID := task.PipelineID()
		pipelineStatus, found := m.cachedPipelineStatus(ctx, pipelineStatusCache, pipelineID)
		if !found || pipelineStatus != manufacturing.PipelineStatusExecuting {
			continue
		}

		// Wait for input deliveries (e.g., factory inputs) to complete first
		if !m.checkDependenciesComplete(ctx, task) {
			continue
		}

		if err := task.MarkReady(); err != nil {
			logger.Log("WARN", "Failed to mark construction task ready", map[string]interface{}{
				"task_id": shortID(task.ID()),
				"error":   err.Error(),
			})
			continue
		}

		if err := m.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARN", "Failed to persist activated construction task", map[string]interface{}{
				"task_id": shortID(task.ID()),
				"error":   err.Error(),
			})
			continue
		}

		m.taskQueue.Enqueue(task)
		activated++
		lastActivatedPipelineID = pipelineID

		logger.Log("INFO", "Activated DELIVER_TO_CONSTRUCTION task", map[string]interface{}{
			"task_id":           shortID(task.ID()),
			"good":              task.Good(),
			"source":            task.SourceMarket(),
			"factory":           task.FactorySymbol(),
			"construction_site": task.ConstructionSite(),
		})
	}

	if activated > 0 {
		logger.Log("INFO", "Construction task activation summary", map[string]interface{}{
			"activated": activated,
		})
		m.notifyTaskReady(lastActivatedPipelineID)
	}

	return activated
}

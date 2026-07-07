package services

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// taskReadyNotifier publishes task ready notifications via the event bus.
type taskReadyNotifier struct {
	publisher navigation.ShipEventPublisher
	playerID  int
}

func (n *taskReadyNotifier) notifyTasksReady(pipelineID string) {
	if n.publisher == nil {
		return
	}
	n.publisher.PublishTasksBecameReady(navigation.TasksBecameReadyEvent{
		PlayerID:   n.playerID,
		PipelineID: pipelineID,
	})
}

// marketSupplyReader answers supply-level questions against market data.
type marketSupplyReader struct {
	marketRepo market.MarketRepository
	playerID   int
}

// sourceMarketSupply returns the supply level of a good at a specific market
func (r marketSupplyReader) sourceMarketSupply(ctx context.Context, waypointSymbol string, good string) string {
	marketData, err := r.marketRepo.GetMarketData(ctx, waypointSymbol, r.playerID)
	if err != nil || marketData == nil {
		return supplyModerate // Default if we can't check
	}

	tradeGood := marketData.FindGood(good)
	if tradeGood == nil {
		return supplyModerate
	}
	return supplyOrModerate(tradeGood)
}

// sellMarketSaturated checks if the sell market has HIGH or ABUNDANT supply
// Returns true if we should NOT sell to this market (would crash prices)
func (r marketSupplyReader) sellMarketSaturated(ctx context.Context, sellMarket string, good string) bool {
	marketData, err := r.marketRepo.GetMarketData(ctx, sellMarket, r.playerID)
	if err != nil || marketData == nil {
		return false // Can't check, assume not saturated
	}

	tradeGood := marketData.FindGood(good)
	if tradeGood == nil || tradeGood.Supply() == nil {
		return false
	}

	supply := *tradeGood.Supply()
	return isHighOrAbundant(supply)
}

// TaskActivator flips gated manufacturing tasks between PENDING and READY as
// market conditions and dependencies evolve.
type TaskActivator struct {
	taskRepo      manufacturing.TaskRepository
	pipelineRepo  manufacturing.PipelineRepository
	taskQueue     ManufacturingTaskQueue
	marketLocator *MarketLocator
	supply        marketSupplyReader
	playerID      int
	notifier      *taskReadyNotifier
}

// checkDependenciesComplete checks if all task dependencies are complete
func (a *TaskActivator) checkDependenciesComplete(ctx context.Context, task *manufacturing.ManufacturingTask) bool {
	if a.taskRepo == nil {
		return true // Assume complete if no repo
	}

	for _, depID := range task.DependsOn() {
		depTask, err := a.taskRepo.FindByID(ctx, depID)
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
func (a *TaskActivator) ActivateSupplyGatedTasks(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	if a.taskRepo == nil {
		return 0
	}

	// Find all PENDING ACQUIRE_DELIVER tasks for this player
	pendingTasks, err := a.taskRepo.FindByStatus(ctx, a.playerID, manufacturing.TaskStatusPending)
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
		pipelineStatus, found := a.cachedPipelineStatus(ctx, pipelineStatusCache, pipelineID)
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
		factoryInputSupply := a.supply.sourceMarketSupply(ctx, task.FactorySymbol(), task.Good())
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
		sourceSupply := a.supply.sourceMarketSupply(ctx, task.SourceMarket(), task.Good())

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
			betterSupply, resourced := a.resourcePendingTask(ctx, task, isRawMaterial)
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
		if err := a.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARN", "Failed to persist activated task", map[string]interface{}{
				"task_id": shortID(task.ID()),
				"error":   err.Error(),
			})
			continue
		}

		// Add to queue
		a.taskQueue.Enqueue(task)
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
		a.notifier.notifyTasksReady(lastActivatedPipelineID)
	}

	return activated
}

func (a *TaskActivator) cachedPipelineStatus(ctx context.Context, cache map[string]manufacturing.PipelineStatus, pipelineID string) (manufacturing.PipelineStatus, bool) {
	if status, cached := cache[pipelineID]; cached {
		return status, true
	}
	if a.pipelineRepo == nil {
		return "", true
	}
	pipeline, err := a.pipelineRepo.FindByID(ctx, pipelineID)
	if err != nil || pipeline == nil {
		return "", false
	}
	cache[pipelineID] = pipeline.Status()
	return pipeline.Status(), true
}

func (a *TaskActivator) resourcePendingTask(ctx context.Context, task *manufacturing.ManufacturingTask, isRawMaterial bool) (string, bool) {
	logger := common.LoggerFromContext(ctx)

	systemSymbol := extractSystem(task.FactorySymbol())

	var betterSource *MarketLocatorResult
	var err error
	if isRawMaterial {
		betterSource, err = a.marketLocator.FindExportMarketWithGoodSupply(ctx, task.Good(), systemSymbol, a.playerID)
	} else {
		betterSource, err = a.marketLocator.FindExportMarketBySupplyPriority(ctx, task.Good(), systemSymbol, a.playerID)
	}
	if err != nil || betterSource == nil {
		return "", false
	}

	betterSupply := a.supply.sourceMarketSupply(ctx, betterSource.WaypointSymbol, task.Good())
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

// resourceDeferredConstructionTask attempts to locate a buy source for a
// construction material that was deferred at planning time (no source found
// then). It reuses the construction source locator (EXPORT MODERATE+, with the
// IMPORT/EXCHANGE ABUNDANT/HIGH fallback). On success it assigns the source to
// the task (keeping it PENDING) so the caller can mark it READY; on failure it
// returns false and the task stays deferred for a later poll.
func (a *TaskActivator) resourceDeferredConstructionTask(ctx context.Context, task *manufacturing.ManufacturingTask) bool {
	logger := common.LoggerFromContext(ctx)

	if a.marketLocator == nil {
		return false
	}

	systemSymbol := extractSystem(task.ConstructionSite())
	source, err := a.marketLocator.FindConstructionSource(ctx, task.Good(), systemSymbol, a.playerID)
	if err != nil || source == nil {
		return false
	}

	if err := task.UpdateSourceMarket(source.WaypointSymbol); err != nil {
		logger.Log("WARN", "Failed to assign source to deferred construction task", map[string]interface{}{
			"task_id": shortID(task.ID()),
			"good":    task.Good(),
			"error":   err.Error(),
		})
		return false
	}

	logger.Log("INFO", "Re-sourced deferred construction material - supply recovered", map[string]interface{}{
		"task_id":           shortID(task.ID()),
		"good":              task.Good(),
		"source":            source.WaypointSymbol,
		"supply":            source.Supply,
		"construction_site": task.ConstructionSite(),
	})
	return true
}

// ActivateCollectionPipelineTasks activates PENDING and enqueues READY COLLECT_SELL tasks from COLLECTION pipelines.
// COLLECTION pipelines have no factory states, so they're not handled by the normal factory polling.
// This method ensures tasks (from recovery, retry, or un-saturated markets) get activated and enqueued.
func (a *TaskActivator) ActivateCollectionPipelineTasks(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	if a.taskRepo == nil || a.pipelineRepo == nil {
		return 0
	}

	// Cache pipeline lookups
	pipelineCache := make(map[string]*manufacturing.ManufacturingPipeline)
	activated := 0
	lastActivatedPipelineID := ""

	// Step 1: Activate PENDING COLLECTION pipeline tasks if conditions are favorable
	pendingTasks, err := a.taskRepo.FindByStatus(ctx, a.playerID, manufacturing.TaskStatusPending)
	if err == nil {
		for _, task := range pendingTasks {
			if task.TaskType() != manufacturing.TaskTypeCollectSell {
				continue
			}

			pipelineID := task.PipelineID()
			if a.executingCollectionPipeline(ctx, pipelineCache, pipelineID) == nil {
				continue
			}

			// Check factory supply - need ABUNDANT to START (buffer for supply drops during navigation)
			// Executor will still collect if supply is HIGH when ship arrives
			factorySupply := a.supply.sourceMarketSupply(ctx, task.FactorySymbol(), task.Good())
			if factorySupply != supplyAbundant {
				continue
			}

			// Check sell market - should not be saturated
			if a.supply.sellMarketSaturated(ctx, task.TargetMarket(), task.Good()) {
				continue
			}

			// Activate the task
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
	}

	// Step 2: Enqueue READY COLLECTION pipeline tasks that aren't in queue
	readyTasks, err := a.taskRepo.FindByStatus(ctx, a.playerID, manufacturing.TaskStatusReady)
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
		if a.executingCollectionPipeline(ctx, pipelineCache, pipelineID) == nil {
			continue
		}

		// Check sell market saturation before enqueueing
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

		// Enqueue the task
		a.taskQueue.Enqueue(task)
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
		a.notifier.notifyTasksReady(lastActivatedPipelineID)
	}

	return activated
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

// ActivateConstructionTasks checks all PENDING DELIVER_TO_CONSTRUCTION tasks and
// activates those whose dependencies are complete. Construction deliveries have a
// fixed bill at the construction site, so no supply gating is applied beyond
// requiring the pipeline to be EXECUTING and dependencies to be COMPLETED.
func (a *TaskActivator) ActivateConstructionTasks(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	if a.taskRepo == nil {
		return 0
	}

	pendingTasks, err := a.taskRepo.FindByStatus(ctx, a.playerID, manufacturing.TaskStatusPending)
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
		pipelineStatus, found := a.cachedPipelineStatus(ctx, pipelineStatusCache, pipelineID)
		if !found || pipelineStatus != manufacturing.PipelineStatusExecuting {
			continue
		}

		// Wait for input deliveries (e.g., factory inputs) to complete first
		if !a.checkDependenciesComplete(ctx, task) {
			continue
		}

		// DEFERRED material recovery: a task planned with no buy source (supply was
		// too low at planning time) must be re-sourced before it can go READY -
		// dispatching it with an empty source would fail at execution. This mirrors
		// the supply-gated re-sourcing of PENDING ACQUIRE_DELIVER tasks. If still
		// unsourceable, the task stays PENDING (deferred, visible) for a later poll.
		if task.IsDeferredConstruction() {
			if !a.resourceDeferredConstructionTask(ctx, task) {
				continue
			}
		}

		if err := task.MarkReady(); err != nil {
			logger.Log("WARN", "Failed to mark construction task ready", map[string]interface{}{
				"task_id": shortID(task.ID()),
				"error":   err.Error(),
			})
			continue
		}

		if err := a.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARN", "Failed to persist activated construction task", map[string]interface{}{
				"task_id": shortID(task.ID()),
				"error":   err.Error(),
			})
			continue
		}

		a.taskQueue.Enqueue(task)
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
		a.notifier.notifyTasksReady(lastActivatedPipelineID)
	}

	return activated
}

// DeactivateSaturatedAcquireDeliverTasks resets READY ACQUIRE_DELIVER tasks to PENDING
// when the factory's input supply has become HIGH/ABUNDANT since the task was marked ready.
// This prevents wasted trips when factory already has enough supply.
func (a *TaskActivator) DeactivateSaturatedAcquireDeliverTasks(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	if a.taskRepo == nil {
		return 0
	}

	// Find all READY ACQUIRE_DELIVER tasks for this player
	readyTasks, err := a.taskRepo.FindByStatus(ctx, a.playerID, manufacturing.TaskStatusReady)
	if err != nil {
		logger.Log("WARN", "Failed to find ready tasks for saturation check", map[string]interface{}{
			"error": err.Error(),
		})
		return 0
	}

	deactivated := 0
	for _, task := range readyTasks {
		// Only process ACQUIRE_DELIVER tasks
		if task.TaskType() != manufacturing.TaskTypeAcquireDeliver {
			continue
		}

		// Check factory input supply level
		factoryInputSupply := a.supply.sourceMarketSupply(ctx, task.FactorySymbol(), task.Good())
		if !isHighOrAbundant(factoryInputSupply) {
			continue // Factory still needs this input
		}

		// Factory input is saturated - reset task to PENDING
		task.ResetToPending()
		if err := a.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARN", "Failed to deactivate saturated task", map[string]interface{}{
				"task_id": shortID(task.ID()),
				"error":   err.Error(),
			})
			continue
		}

		deactivated++
		logger.Log("INFO", "Deactivated READY task - factory input saturated", map[string]interface{}{
			"task_id":        shortID(task.ID()),
			"good":           task.Good(),
			"factory":        task.FactorySymbol(),
			"factory_supply": factoryInputSupply,
		})
	}

	if deactivated > 0 {
		logger.Log("INFO", "Deactivated saturated ACQUIRE_DELIVER tasks", map[string]interface{}{
			"deactivated": deactivated,
		})
	}

	return deactivated
}

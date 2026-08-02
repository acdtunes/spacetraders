package services

import (
	"context"
	"time"

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
		if task.TaskType() != manufacturing.TaskTypeAcquireDeliver {
			continue
		}

		pipelineID := task.PipelineID()
		if !a.pipelineAcceptsActivation(ctx, pipelineStatusCache, task, pipelineID) {
			continue
		}
		if a.factoryInputSaturated(ctx, task) {
			continue
		}

		sourceSupply, reason, ok := a.admissibleSourceSupply(ctx, task)
		if !ok {
			continue
		}

		applySourceSupplyPriority(task, sourceSupply)
		if !a.publishReadyTask(ctx, task, "Failed to mark supply-gated task ready", "Failed to persist activated task") {
			continue
		}
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

// pipelineAcceptsActivation rejects a task whose pipeline is missing or no longer EXECUTING:
// CANCELLED/FAILED/COMPLETED pipelines must never have their tasks activated.
func (a *TaskActivator) pipelineAcceptsActivation(ctx context.Context, cache map[string]manufacturing.PipelineStatus, task *manufacturing.ManufacturingTask, pipelineID string) bool {
	logger := common.LoggerFromContext(ctx)
	pipelineStatus, found := a.cachedPipelineStatus(ctx, cache, pipelineID)
	if !found {
		logger.Log("DEBUG", "Skipping task - pipeline not found", map[string]interface{}{
			"task_id":     shortID(task.ID()),
			"pipeline_id": shortID(pipelineID),
		})
		return false
	}
	if pipelineStatus != manufacturing.PipelineStatusExecuting {
		logger.Log("DEBUG", "Skipping task from non-executing pipeline", map[string]interface{}{
			"task_id":         shortID(task.ID()),
			"pipeline_id":     shortID(pipelineID),
			"pipeline_status": pipelineStatus,
		})
		return false
	}
	return true
}

// factoryInputSaturated reports whether the destination factory already holds HIGH/ABUNDANT supply
// of this good, so acquiring more would deliver into a market that does not need it.
func (a *TaskActivator) factoryInputSaturated(ctx context.Context, task *manufacturing.ManufacturingTask) bool {
	factoryInputSupply := a.supply.sourceMarketSupply(ctx, task.FactorySymbol(), task.Good())
	if !isHighOrAbundant(factoryInputSupply) {
		return false
	}
	common.LoggerFromContext(ctx).Log("DEBUG", "Skipping task - factory input already saturated", map[string]interface{}{
		"task_id":        shortID(task.ID()),
		"good":           task.Good(),
		"factory":        task.FactorySymbol(),
		"factory_supply": factoryInputSupply,
	})
	return true
}

// admissibleSourceSupply returns the supply level to activate against and the reason to log, after
// RE-SOURCING to a better market when the task's own source has degraded — without which a task
// stays stuck forever behind its original source. ok is false when nothing can be sourced.
func (a *TaskActivator) admissibleSourceSupply(ctx context.Context, task *manufacturing.ManufacturingTask) (string, string, bool) {
	isRawMaterial := goods.IsMineableRawMaterial(task.Good())
	sourceSupply := a.supply.sourceMarketSupply(ctx, task.SourceMarket(), task.Good())

	if acceptableSourceSupply(sourceSupply, isRawMaterial) {
		if isRawMaterial {
			return sourceSupply, "raw_material_good_supply", true
		}
		return sourceSupply, "acceptable_supply", true
	}

	betterSupply, resourced := a.resourcePendingTask(ctx, task, isRawMaterial)
	if !resourced {
		return "", "", false
	}
	return betterSupply, "re-sourced", true
}

// publishReadyTask marks a task READY, persists it and enqueues it, reporting false if any step
// failed so the caller skips it without counting an activation. The two failure messages differ per
// task family and are supplied by the caller.
func (a *TaskActivator) publishReadyTask(ctx context.Context, task *manufacturing.ManufacturingTask, markFailureMessage, persistFailureMessage string) bool {
	logger := common.LoggerFromContext(ctx)
	if err := task.MarkReady(); err != nil {
		logger.Log("WARN", markFailureMessage, map[string]interface{}{
			"task_id": shortID(task.ID()),
			"error":   err.Error(),
		})
		return false
	}
	if err := a.taskRepo.Update(ctx, task); err != nil {
		logger.Log("WARN", persistFailureMessage, map[string]interface{}{
			"task_id": shortID(task.ID()),
			"error":   err.Error(),
		})
		return false
	}
	a.taskQueue.Enqueue(task)
	return true
}

// constructionRetryBackoff is the minimum age of a task's last failure before the
// retry sweep re-queues it: long enough that the transient cause (a
// phantom-cargo resync, a bad surplus-sell market pick) has had a tick to clear,
// short enough that a gate leg doesn't sit dead for an hour.
const constructionRetryBackoff = 2 * time.Minute

// DeactivateSaturatedAcquireDeliverTasks resets READY ACQUIRE_DELIVER tasks to PENDING
// when the factory's input supply has become HIGH/ABUNDANT since the task was marked ready.
// This prevents wasted trips when factory already has enough supply.
func (a *TaskActivator) DeactivateSaturatedAcquireDeliverTasks(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	if a.taskRepo == nil {
		return 0
	}

	readyTasks, err := a.taskRepo.FindByStatus(ctx, a.playerID, manufacturing.TaskStatusReady)
	if err != nil {
		logger.Log("WARN", "Failed to find ready tasks for saturation check", map[string]interface{}{
			"error": err.Error(),
		})
		return 0
	}

	deactivated := 0
	for _, task := range readyTasks {
		if task.TaskType() != manufacturing.TaskTypeAcquireDeliver {
			continue
		}

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

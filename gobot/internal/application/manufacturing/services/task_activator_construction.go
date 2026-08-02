package services

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// sweepRetryableConstructionFailures flips FAILED DELIVER_TO_CONSTRUCTION tasks that
// still have retry budget back to PENDING via the domain's ResetForRetry — the retry
// seam Fail() already charges (retryCount increments on each failure) but which
// nothing drove for construction tasks, so transient-class failures (a phantom-cargo
// 4219 resync, a bad surplus-sell market pick) sat FAILED forever and froze the gate
// leg. The retry re-runs the whole chain: the existing resync machinery
// and a fresh market pick handle the transient causes, so no error-code special
// cases here. Bounded three ways:
//   - task.CanRetry(): after maxRetries the task stays FAILED (visible, existing
//     semantics)
//   - a failure-age backoff (constructionRetryBackoff, read off the completedAt
//     stamp Fail() sets), so a hot failure is not re-queued the same tick
//   - the pipeline must still be EXECUTING — a recycled pipeline's Cancel()ed tasks
//     are FAILED without a retry charge and must not resurrect
//
// Flipped tasks re-enter the PENDING pass in this same activation call, so
// promotion to READY happens as soon as dependencies allow.
func (a *TaskActivator) sweepRetryableConstructionFailures(ctx context.Context, pipelineStatusCache map[string]manufacturing.PipelineStatus) {
	logger := common.LoggerFromContext(ctx)

	failedTasks, err := a.taskRepo.FindByStatus(ctx, a.playerID, manufacturing.TaskStatusFailed)
	if err != nil {
		logger.Log("WARN", "Failed to find failed tasks for the construction retry sweep", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	for _, task := range failedTasks {
		if task.TaskType() != manufacturing.TaskTypeDeliverToConstruction {
			continue
		}
		if !task.CanRetry() {
			continue
		}
		failedAt := task.CompletedAt()
		if failedAt == nil || time.Since(*failedAt) < constructionRetryBackoff {
			continue
		}
		pipelineStatus, found := a.cachedPipelineStatus(ctx, pipelineStatusCache, task.PipelineID())
		if !found || pipelineStatus != manufacturing.PipelineStatusExecuting {
			continue
		}

		lastError := task.ErrorMessage() // ResetForRetry clears it — capture for the log
		if err := task.ResetForRetry(); err != nil {
			logger.Log("WARN", "Failed to reset construction task for retry", map[string]interface{}{
				"task_id": shortID(task.ID()),
				"error":   err.Error(),
			})
			continue
		}
		if err := a.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARN", "Failed to persist construction task retry", map[string]interface{}{
				"task_id": shortID(task.ID()),
				"error":   err.Error(),
			})
			continue
		}

		logger.Log("INFO", "Swept FAILED construction task back to PENDING for retry", map[string]interface{}{
			"task_id":     shortID(task.ID()),
			"good":        task.Good(),
			"retry_count": task.RetryCount(),
			"max_retries": task.MaxRetries(),
			"last_error":  lastError,
		})
	}
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

	systemSymbol := extractSystemSymbol(task.FactorySymbol())

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

// resourceDeferredConstructionTask un-sticks a construction material that was deferred at planning
// time (neither a buy source nor a factory found then). It recovers in two ways, in precedence order:
//
//  1. FABRICATE (primary): resolve a FACTORY that exports the good and set it on the task,
//     so the task goes READY and the drain fabricates it (buying inputs, feeding the factory,
//     harvesting) instead of buying the good's own export cold. This breaks the buy-only deadlock:
//     the drain buying a manufactured export without feeding it depletes that export MODERATE->SCARCE,
//     after which the buy-only recovery below can NEVER clear the floor, so the task sat PENDING
//     forever. Only for MANUFACTURABLE goods — a raw/mined good is never fabricated.
//  2. BUY (fallback): re-source against the pipeline's persisted --min-supply floor — the recovery
//     for raw/mined goods with no factory. Stays deferred (returns false) if no buy source clears.
//
// On success it assigns the factory/source (keeping the task PENDING) so the caller marks it READY;
// on failure it returns false and the task stays deferred for a later poll. It never spends — input
// buys still flow through the drain's produce path and its money-guard stack (RULINGS #4).
func (a *TaskActivator) resourceDeferredConstructionTask(ctx context.Context, task *manufacturing.ManufacturingTask) bool {
	if a.marketLocator == nil {
		return false
	}
	systemSymbol := extractSystemSymbol(task.ConstructionSite())
	if a.resolveDeferredViaFactory(ctx, task, systemSymbol) {
		return true
	}
	return a.resolveDeferredViaBuySource(ctx, task, systemSymbol)
}

// resolveDeferredViaFactory sets a fabrication factory on a deferred construction task so it recovers
// as a FABRICATE. It mirrors the planner's fabricate-eligibility (planFabrication): a good
// with a recipe (GetRequiredInputs non-empty) for which a factory EXPORTS it while IMPORTING its
// inputs. Returns false (leaving the task for the buy fallback) when the good has no recipe or no such
// factory exists — so a good with only a plain buy market falls through to the buy path.
func (a *TaskActivator) resolveDeferredViaFactory(ctx context.Context, task *manufacturing.ManufacturingTask, systemSymbol string) bool {
	logger := common.LoggerFromContext(ctx)
	good := task.Good()
	inputs := goods.GetRequiredInputs(good)
	if len(inputs) == 0 {
		return false // no recipe — not fabricable; use the buy fallback
	}
	factory, err := a.marketLocator.FindFactoryForProduction(ctx, good, inputs, systemSymbol, a.playerID)
	if err != nil || factory == nil {
		return false
	}
	if err := task.UpdateFactorySymbol(factory.WaypointSymbol); err != nil {
		logger.Log("WARN", "Failed to assign factory to deferred construction task", map[string]interface{}{
			"task_id": shortID(task.ID()),
			"good":    good,
			"error":   err.Error(),
		})
		return false
	}
	logger.Log("INFO", "Recovered deferred construction material via fabrication - factory resolved", map[string]interface{}{
		"task_id":           shortID(task.ID()),
		"good":              good,
		"factory":           factory.WaypointSymbol,
		"construction_site": task.ConstructionSite(),
	})
	return true
}

// resolveDeferredViaBuySource re-sources a deferred construction material from a BUY market against
// the pipeline's persisted --min-supply floor. sp-j2hq: read the caller-set floor back off the
// persisted pipeline, so a floor set at planning time (or updated later via a resumed
// `construction start --min-supply X` call) is honored here too, not just during the initial planning
// pass. An unset/unreadable floor resolves to "" and FindConstructionSource treats that as MODERATE.
// This is the fallback for raw/mined goods with no fabrication factory.
func (a *TaskActivator) resolveDeferredViaBuySource(ctx context.Context, task *manufacturing.ManufacturingTask, systemSymbol string) bool {
	logger := common.LoggerFromContext(ctx)
	minSupply := a.pipelineMinSupply(ctx, task.PipelineID(), task.Good())
	source, err := a.marketLocator.FindConstructionSource(ctx, task.Good(), systemSymbol, a.playerID, manufacturing.SupplyLevel(minSupply))
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

// pipelineMinSupply reads the EXPORT sourcing floor for a specific good on a construction
// pipeline: the pipeline's persisted global --min-supply floor, or the good's per-good
// override when one is set (sp-sdyo). Returns "" (unset) if the pipeline repo is unavailable or
// the pipeline can't be loaded, which FindConstructionSource treats as the default MODERATE floor.
// Reading the override off the SAME persisted pipeline the global floor lives on is what makes a
// per-good override survive a restart and apply to the deferred-material recovery path, not just
// the initial planning pass (RULINGS #2).
func (a *TaskActivator) pipelineMinSupply(ctx context.Context, pipelineID, good string) string {
	if a.pipelineRepo == nil {
		return ""
	}
	pipeline, err := a.pipelineRepo.FindByID(ctx, pipelineID)
	if err != nil || pipeline == nil {
		return ""
	}
	return pipeline.GoodOverrides().MinSupplyFor(good, pipeline.MinSupply())
}

// ActivateConstructionTasks first sweeps retryable FAILED DELIVER_TO_CONSTRUCTION
// tasks back to PENDING, then checks all PENDING DELIVER_TO_CONSTRUCTION
// tasks and activates those whose dependencies are complete — so a swept task is
// promoted READY within the same pass when dependencies allow. Construction
// deliveries have a fixed bill at the construction site, so no supply gating is
// applied beyond requiring the pipeline to be EXECUTING and dependencies COMPLETED.
func (a *TaskActivator) ActivateConstructionTasks(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	if a.taskRepo == nil {
		return 0
	}

	// Cache pipeline status lookups to avoid repeated DB queries (shared by the
	// retry sweep and the activation pass below)
	pipelineStatusCache := make(map[string]manufacturing.PipelineStatus)

	a.sweepRetryableConstructionFailures(ctx, pipelineStatusCache)

	pendingTasks, err := a.taskRepo.FindByStatus(ctx, a.playerID, manufacturing.TaskStatusPending)
	if err != nil {
		logger.Log("WARN", "Failed to find pending tasks for construction activation", map[string]interface{}{
			"error": err.Error(),
		})
		return 0
	}

	activated := 0
	lastActivatedPipelineID := ""
	for _, task := range pendingTasks {
		if task.TaskType() != manufacturing.TaskTypeDeliverToConstruction {
			continue
		}

		pipelineID := task.PipelineID()
		pipelineStatus, found := a.cachedPipelineStatus(ctx, pipelineStatusCache, pipelineID)
		if !found || pipelineStatus != manufacturing.PipelineStatusExecuting {
			continue
		}

		// Input deliveries (e.g. factory inputs) must land first.
		if !a.checkDependenciesComplete(ctx, task) {
			continue
		}

		// A task planned with no buy source (supply was too low at planning time) must be
		// re-sourced before going READY — dispatching it with an empty source fails at execution.
		// Still unsourceable leaves it PENDING and visible for a later poll.
		if task.IsDeferredConstruction() && !a.resourceDeferredConstructionTask(ctx, task) {
			continue
		}

		if !a.publishReadyTask(ctx, task, "Failed to mark construction task ready", "Failed to persist activated construction task") {
			continue
		}
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

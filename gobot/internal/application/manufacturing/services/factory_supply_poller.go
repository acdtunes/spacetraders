package services

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// FactorySupplyPoller owns the supply monitor poll cycle: it observes factory
// market supply, resets/readies factory collection state, triggers replenishment
// planning, drives task activation, and publishes task-ready events.
type FactorySupplyPoller struct {
	marketRepo        market.MarketRepository
	factoryTracker    *manufacturing.FactoryStateTracker
	factoryStateRepo  manufacturing.FactoryStateRepository
	pipelineRepo      manufacturing.PipelineRepository
	taskQueue         ManufacturingTaskQueue
	taskRepo          manufacturing.TaskRepository
	sellMarketDistrib *SellMarketDistributor
	replenisher       *ReplenishmentPlanner
	activator         *TaskActivator
	supply            marketSupplyReader
	notifier          *taskReadyNotifier
	pollInterval      time.Duration
	playerID          int
}

// Run starts the poll loop until the context is cancelled.
func (p *FactorySupplyPoller) Run(ctx context.Context) {
	logger := common.LoggerFromContext(ctx)
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	logger.Log("INFO", "Supply monitor started", map[string]interface{}{
		"poll_interval": p.pollInterval.String(),
	})

	for {
		select {
		case <-ticker.C:
			p.PollOnce(ctx)
		case <-ctx.Done():
			logger.Log("INFO", "Supply monitor stopped", nil)
			return
		}
	}
}

// PollOnce checks ALL factories (including ready ones)
// This is necessary to detect supply drops and reset ready flags
func (p *FactorySupplyPoller) PollOnce(ctx context.Context) {
	logger := common.LoggerFromContext(ctx)

	allFactories := p.factoryTracker.GetAllFactories()
	if len(allFactories) == 0 {
		// Even without factories, check supply-gated and construction tasks
		// (CONSTRUCTION pipelines at depth 3 have no factory states at all)
		p.activator.ActivateSupplyGatedTasks(ctx)
		p.activator.ActivateConstructionTasks(ctx)
		return
	}

	logger.Log("DEBUG", "Polling factories for supply updates", map[string]interface{}{
		"factory_count": len(allFactories),
	})

	for _, factory := range allFactories {
		p.checkFactorySupply(ctx, factory)
	}

	// The gate these tasks were waiting on reads supply at the SOURCE market.
	p.activator.ActivateSupplyGatedTasks(ctx)

	// Prevents wasted trips when the factory already has enough supply.
	p.activator.DeactivateSaturatedAcquireDeliverTasks(ctx)

	// COLLECTION pipelines have no factory states, so we must poll them separately
	p.activator.ActivateCollectionPipelineTasks(ctx)

	// CONSTRUCTION pipelines are not covered by the acquire/collect activators above
	p.activator.ActivateConstructionTasks(ctx)
}

// checkFactorySupply checks a single factory's supply level
func (p *FactorySupplyPoller) checkFactorySupply(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	supply, ok := p.readFactorySupply(ctx, factory)
	if !ok {
		return
	}

	previousSupply := factory.CurrentSupply()
	factory.UpdateSupply(supply)
	p.persistFactoryState(ctx, factory)

	if previousSupply != supply {
		logger.Log("INFO", "Factory supply changed", map[string]interface{}{
			"factory":  factory.FactorySymbol(),
			"output":   factory.OutputGood(),
			"previous": previousSupply,
			"current":  supply,
		})

		metrics.RecordManufacturingSupplyTransition(factory.PlayerID(), factory.OutputGood(), previousSupply, supply)

		// DEMAND-DRIVEN SUPPLY: a factory that has just fallen below HIGH is replenished with the
		// raw materials it needs to keep producing.
		if isHighOrAbundant(previousSupply) && !isHighOrAbundant(supply) {
			p.replenisher.createTasksForFactory(ctx, factory)
		}
	}

	// CONTINUOUS DELIVERY: supply that never reached HIGH in the first place produces no
	// transition above, so a fed factory still short of target re-stages here or the pipeline stalls.
	if !isHighOrAbundant(supply) && factory.HasReceivedAnyDelivery() {
		if !p.replenisher.hasPendingAcquireDeliverTasks(ctx, factory) {
			logger.Log("INFO", "Factory supply still below target with no pending deliveries - creating more tasks", map[string]interface{}{
				"factory": factory.FactorySymbol(),
				"output":  factory.OutputGood(),
				"supply":  supply,
			})
			p.replenisher.createTasksForFactory(ctx, factory)
		}
	}

	if factory.IsReadyForCollection() {
		logger.Log("INFO", "Factory ready for collection", map[string]interface{}{
			"factory":  factory.FactorySymbol(),
			"output":   factory.OutputGood(),
			"supply":   supply,
			"pipeline": factory.PipelineID(),
		})

		metrics.RecordManufacturingFactoryCycle(factory.PlayerID(), factory.FactorySymbol(), factory.OutputGood())

		p.markCollectTasksReady(ctx, factory)
	}
}

// markCollectTasksReady marks COLLECT tasks for this factory as ready.
// If no pending COLLECT task exists (e.g., previous one completed), creates new COLLECT + SELL tasks.
//
// STREAMING EXECUTION MODEL: Collection is gated by TWO conditions:
//  1. Factory supply is HIGH/ABUNDANT (checked by caller via IsReadyForCollection)
//  2. At least one delivery has been recorded (prevents premature collection)
//
// This allows ACQUIRE_DELIVER and COLLECT_SELL to run in parallel within a pipeline,
// dramatically improving ship utilization from ~30% to potentially 80%+.
func (p *FactorySupplyPoller) markCollectTasksReady(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	if !pipelineExecutingForFactory(ctx, p.pipelineRepo, factory, "Skipping task activation") {
		return
	}

	if !p.collectionGateOpen(ctx, factory) {
		return
	}

	if p.taskRepo == nil {
		// Fall back to in-memory queue only
		marked := p.taskQueue.MarkCollectTasksReady(factory.FactorySymbol(), factory.OutputGood())
		logger.Log("DEBUG", "Marked COLLECT tasks ready (in-memory)", map[string]interface{}{
			"factory":      factory.FactorySymbol(),
			"output":       factory.OutputGood(),
			"tasks_marked": marked,
		})
		return
	}

	tasks, err := p.taskRepo.FindByPipelineID(ctx, factory.PipelineID())
	if err != nil {
		logger.Log("WARN", "Failed to find tasks for pipeline", map[string]interface{}{
			"pipeline": factory.PipelineID(),
			"error":    err.Error(),
		})
		return
	}

	marked, hasPendingCollect, hasCompletedCollect := p.readyCollectTasks(ctx, tasks, factory)

	// No pending COLLECT but a completed one means supply became ABUNDANT again after the previous
	// collection, so the follow-up pair is staged instead.
	if !hasPendingCollect && hasCompletedCollect {
		logger.Log("INFO", "No pending COLLECT task but factory is ready - creating new tasks", map[string]interface{}{
			"factory":  factory.FactorySymbol(),
			"output":   factory.OutputGood(),
			"pipeline": factory.PipelineID(),
		})

		p.createNewCollectSellTasks(ctx, factory)
		return
	}

	logger.Log("INFO", "Marked COLLECT tasks ready", map[string]interface{}{
		"factory":      factory.FactorySymbol(),
		"output":       factory.OutputGood(),
		"tasks_marked": marked,
	})

	if marked > 0 {
		p.notifier.notifyTasksReady(factory.PipelineID())
	}
}

func isCollectTaskForFactory(task *manufacturing.ManufacturingTask, factory *manufacturing.FactoryState) bool {
	return task.TaskType() == manufacturing.TaskTypeCollectSell &&
		task.FactorySymbol() == factory.FactorySymbol() &&
		task.Good() == factory.OutputGood()
}

func (p *FactorySupplyPoller) readyAndEnqueueCollectTask(ctx context.Context, task *manufacturing.ManufacturingTask) bool {
	logger := common.LoggerFromContext(ctx)

	if p.supply.sellMarketSaturated(ctx, task.TargetMarket(), task.Good()) {
		logger.Log("DEBUG", "Sell market saturated - keeping COLLECT_SELL task pending", map[string]interface{}{
			"task":        shortID(task.ID()),
			"sell_market": task.TargetMarket(),
			"good":        task.Good(),
		})
		return false
	}

	if err := task.MarkReady(); err != nil {
		logger.Log("WARN", "Failed to mark task ready", map[string]interface{}{
			"task":  task.ID(),
			"error": err.Error(),
		})
		return false
	}

	if err := p.taskRepo.Update(ctx, task); err != nil {
		logger.Log("WARN", "Failed to persist task state", map[string]interface{}{
			"task":  task.ID(),
			"error": err.Error(),
		})
		return false
	}

	p.taskQueue.Enqueue(task)
	return true
}

func (p *FactorySupplyPoller) requeueRecoveredCollectTask(ctx context.Context, task *manufacturing.ManufacturingTask) bool {
	if p.supply.sellMarketSaturated(ctx, task.TargetMarket(), task.Good()) {
		task.ResetToPending()
		if p.taskRepo != nil {
			_ = p.taskRepo.Update(ctx, task)
		}
		return false
	}
	p.taskQueue.Enqueue(task)
	return true
}

// readFactorySupply reads the factory's live supply level for its own output good. ok is false when
// the market has not been scanned yet, or does not list the good at all.
func (p *FactorySupplyPoller) readFactorySupply(ctx context.Context, factory *manufacturing.FactoryState) (string, bool) {
	logger := common.LoggerFromContext(ctx)

	marketData, err := p.marketRepo.GetMarketData(ctx, factory.FactorySymbol(), factory.PlayerID())
	if err != nil {
		logger.Log("WARN", "Failed to get market data for factory", map[string]interface{}{
			"factory": factory.FactorySymbol(),
			"output":  factory.OutputGood(),
			"error":   err.Error(),
		})
		return "", false
	}
	if marketData == nil {
		return "", false // scouts may not have scanned this waypoint yet
	}

	tradeGood := marketData.FindGood(factory.OutputGood())
	if tradeGood == nil {
		logger.Log("WARN", "Output good not found in factory market", map[string]interface{}{
			"factory": factory.FactorySymbol(),
			"output":  factory.OutputGood(),
		})
		return "", false
	}
	return supplyOrModerate(tradeGood), true
}

// persistFactoryState is best-effort: a write failure is logged, never propagated, so a poll cycle
// still acts on the supply it just read.
func (p *FactorySupplyPoller) persistFactoryState(ctx context.Context, factory *manufacturing.FactoryState) {
	if p.factoryStateRepo == nil {
		return
	}
	if err := p.factoryStateRepo.Update(ctx, factory); err != nil {
		common.LoggerFromContext(ctx).Log("WARN", "Failed to persist factory state update", map[string]interface{}{
			"factory": factory.FactorySymbol(),
			"output":  factory.OutputGood(),
			"error":   err.Error(),
		})
	}
}

// collectionGateOpen holds collection back until the factory has received at least one delivery,
// so an opportunistic factory that merely happens to hold stock is not collected before we have
// started feeding it. A SOURCE factory (no required inputs) produces without deliveries, so it is
// exempt and collectable as soon as supply is HIGH/ABUNDANT.
func (p *FactorySupplyPoller) collectionGateOpen(ctx context.Context, factory *manufacturing.FactoryState) bool {
	if len(factory.RequiredInputs()) == 0 || factory.HasReceivedAnyDelivery() {
		return true
	}
	common.LoggerFromContext(ctx).Log("DEBUG", "Factory ready but no deliveries yet - waiting", map[string]interface{}{
		"factory": factory.FactorySymbol(),
		"output":  factory.OutputGood(),
		"supply":  factory.CurrentSupply(),
	})
	return false
}

// readyCollectTasks promotes this factory's PENDING collect tasks and re-queues ones left READY by
// a daemon restart, reporting how many were marked plus whether any pending or completed collect
// task exists at all — the two facts that decide whether a follow-up pair must be staged.
func (p *FactorySupplyPoller) readyCollectTasks(ctx context.Context, tasks []*manufacturing.ManufacturingTask, factory *manufacturing.FactoryState) (marked int, hasPendingCollect, hasCompletedCollect bool) {
	for _, task := range tasks {
		if !isCollectTaskForFactory(task, factory) {
			continue
		}
		switch task.Status() {
		case manufacturing.TaskStatusPending:
			hasPendingCollect = true
			if p.readyAndEnqueueCollectTask(ctx, task) {
				marked++
			}
		case manufacturing.TaskStatusReady:
			if p.requeueRecoveredCollectTask(ctx, task) {
				marked++
			}
		case manufacturing.TaskStatusCompleted:
			hasCompletedCollect = true
		}
	}
	return marked, hasPendingCollect, hasCompletedCollect
}

// createNewCollectSellTasks creates a new atomic COLLECT_SELL task for a factory that's ready
// but has no pending COLLECT task (previous one completed and supply is ABUNDANT again).
// Uses atomic task to prevent "orphaned cargo" bug where one ship collects and another sells.
//
// IMPORTANT: This only creates follow-up tasks for FINAL GOODS factories.
// Intermediate factories (that produce goods used by other factories) should not get
// follow-up tasks here because we don't know their downstream destination.
// Intermediate goods flow is handled by the initial pipeline task creation only.
//
// MARKET DISTRIBUTION: For final goods, uses SellMarketDistributor to select from ALL
// eligible SCARCE/LIMITED markets, preferring markets with the fewest pending tasks.
func (p *FactorySupplyPoller) createNewCollectSellTasks(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	fallbackMarket, eligible := p.followUpSellFallback(ctx, factory)
	if !eligible {
		return
	}

	// This distributes sales across multiple SCARCE/LIMITED markets to avoid flooding
	systemSymbol := extractSystemSymbol(factory.FactorySymbol())
	sellMarket, err := p.sellMarketDistrib.SelectSellMarket(
		ctx,
		factory.OutputGood(),
		factory.FactorySymbol(),
		systemSymbol,
		factory.PlayerID(),
		fallbackMarket,
	)
	if err != nil {
		logger.Log("WARN", "Failed to select sell market from distributor, using fallback", map[string]interface{}{
			"factory":  factory.FactorySymbol(),
			"output":   factory.OutputGood(),
			"fallback": fallbackMarket,
			"error":    err.Error(),
		})
		sellMarket = fallbackMarket
	}

	// Check sell market supply BEFORE creating task
	// If saturated, don't create a new task yet
	if p.supply.sellMarketSaturated(ctx, sellMarket, factory.OutputGood()) {
		logger.Log("DEBUG", "Sell market saturated - skipping new COLLECT_SELL task creation", map[string]interface{}{
			"factory":     factory.FactorySymbol(),
			"output":      factory.OutputGood(),
			"sell_market": sellMarket,
		})
		return
	}

	p.stageCollectSellTask(ctx, factory, sellMarket)
}

// followUpSellFallback reports whether this factory may get a follow-up collection at all, and the
// pipeline's own sell market to fall back on. Only FINAL-GOODS factories of an EXECUTING pipeline
// qualify: an intermediate factory's output goes to another factory, and without knowing that
// downstream factory the goods cannot be routed.
func (p *FactorySupplyPoller) followUpSellFallback(ctx context.Context, factory *manufacturing.FactoryState) (string, bool) {
	logger := common.LoggerFromContext(ctx)

	var pipeline *manufacturing.ManufacturingPipeline
	var fallbackMarket string
	if p.pipelineRepo != nil {
		var err error
		pipeline, err = p.pipelineRepo.FindByID(ctx, factory.PipelineID())
		if err != nil {
			logger.Log("WARN", "Failed to load pipeline for sell market lookup", map[string]interface{}{
				"factory":  factory.FactorySymbol(),
				"output":   factory.OutputGood(),
				"pipeline": factory.PipelineID(),
				"error":    err.Error(),
			})
			return "", false
		}
		if pipeline == nil {
			logger.Log("DEBUG", "Skipping new task creation - pipeline not found", map[string]interface{}{
				"factory":     factory.FactorySymbol(),
				"pipeline_id": shortID(factory.PipelineID()),
			})
			return "", false
		}
		if pipeline.Status() != manufacturing.PipelineStatusExecuting {
			logger.Log("DEBUG", "Skipping new task creation - pipeline not executing", map[string]interface{}{
				"factory":         factory.FactorySymbol(),
				"pipeline_id":     shortID(factory.PipelineID()),
				"pipeline_status": pipeline.Status(),
			})
			return "", false
		}

		fallbackMarket = pipeline.SellMarket()
	}

	if pipeline != nil && factory.OutputGood() != pipeline.ProductGood() {
		logger.Log("DEBUG", "Skipping follow-up task for intermediate factory", map[string]interface{}{
			"factory":       factory.FactorySymbol(),
			"output":        factory.OutputGood(),
			"final_product": pipeline.ProductGood(),
			"reason":        "intermediate goods need specific factory destination",
		})
		return "", false
	}

	if fallbackMarket == "" {
		var err error
		fallbackMarket, err = p.findBestSellMarket(ctx, factory.FactorySymbol(), factory.OutputGood())
		if err != nil {
			logger.Log("WARN", "Failed to find sell market for new collection", map[string]interface{}{
				"factory": factory.FactorySymbol(),
				"output":  factory.OutputGood(),
				"error":   err.Error(),
			})
			return "", false
		}
	}
	return fallbackMarket, true
}

// stageCollectSellTask creates, persists and enqueues the atomic COLLECT_SELL task — one ship both
// collects and sells. It is immediately READY because supply at the factory is already HIGH/ABUNDANT.
func (p *FactorySupplyPoller) stageCollectSellTask(ctx context.Context, factory *manufacturing.FactoryState, sellMarket string) {
	logger := common.LoggerFromContext(ctx)

	collectSellTask := manufacturing.NewCollectSellTask(
		factory.PipelineID(),
		factory.PlayerID(),
		factory.OutputGood(),
		factory.FactorySymbol(), // Where to collect from
		sellMarket,              // Where to sell to
		nil,                     // No dependencies - this is a follow-up collection
	)
	if err := collectSellTask.MarkReady(); err != nil {
		logger.Log("WARN", "Failed to mark new COLLECT_SELL task ready", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	if err := p.taskRepo.Create(ctx, collectSellTask); err != nil {
		logger.Log("WARN", "Failed to persist new COLLECT_SELL task", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	p.taskQueue.Enqueue(collectSellTask)

	logger.Log("INFO", "Created new COLLECT_SELL task for repeated collection (atomic)", map[string]interface{}{
		"factory":     factory.FactorySymbol(),
		"output":      factory.OutputGood(),
		"sell_market": sellMarket,
		"task_id":     collectSellTask.ID(),
	})

	p.notifier.notifyTasksReady(factory.PipelineID())
}

// findBestSellMarket finds the best market to sell the collected good.
// Uses the existing FindBestMarketBuying which considers both price and activity.
// If waypointProvider is available, it will prefer closer markets when prices are similar.
func (p *FactorySupplyPoller) findBestSellMarket(ctx context.Context, factorySymbol string, good string) (string, error) {
	system := extractSystemSymbol(factorySymbol)

	result, err := p.marketRepo.FindBestMarketBuying(ctx, good, system, p.playerID)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", manufacturing.NewErrNoValidSellMarket(good)
	}

	return result.WaypointSymbol, nil
}

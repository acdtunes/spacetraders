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

	// Get ALL factories - we need to poll ready ones too in case supply dropped
	// This allows us to reset ready flags when supply drops below HIGH
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

	// Check and activate any supply-gated ACQUIRE_DELIVER tasks
	// This activates tasks that were waiting for HIGH/ABUNDANT supply at source market
	p.activator.ActivateSupplyGatedTasks(ctx)

	// Deactivate READY ACQUIRE_DELIVER tasks whose factory input became saturated
	// This prevents wasted trips when factory already has enough supply
	p.activator.DeactivateSaturatedAcquireDeliverTasks(ctx)

	// Enqueue READY COLLECTION pipeline tasks that aren't in the queue
	// COLLECTION pipelines have no factory states, so we must poll them separately
	p.activator.ActivateCollectionPipelineTasks(ctx)

	// Activate PENDING DELIVER_TO_CONSTRUCTION tasks whose dependencies completed
	// CONSTRUCTION pipelines are not covered by the acquire/collect activators above
	p.activator.ActivateConstructionTasks(ctx)
}

// checkFactorySupply checks a single factory's supply level
func (p *FactorySupplyPoller) checkFactorySupply(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	// Get current market data
	marketData, err := p.marketRepo.GetMarketData(ctx, factory.FactorySymbol(), factory.PlayerID())
	if err != nil {
		logger.Log("WARN", "Failed to get market data for factory", map[string]interface{}{
			"factory": factory.FactorySymbol(),
			"output":  factory.OutputGood(),
			"error":   err.Error(),
		})
		return
	}
	if marketData == nil {
		// No market data available yet - scouts may not have scanned this waypoint
		return
	}

	// Find the output good
	tradeGood := marketData.FindGood(factory.OutputGood())
	if tradeGood == nil {
		logger.Log("WARN", "Output good not found in factory market", map[string]interface{}{
			"factory": factory.FactorySymbol(),
			"output":  factory.OutputGood(),
		})
		return
	}

	supply := supplyOrModerate(tradeGood)

	// Update factory state
	previousSupply := factory.CurrentSupply()
	factory.UpdateSupply(supply)

	// Persist the change to database
	if p.factoryStateRepo != nil {
		if err := p.factoryStateRepo.Update(ctx, factory); err != nil {
			logger.Log("WARN", "Failed to persist factory state update", map[string]interface{}{
				"factory": factory.FactorySymbol(),
				"output":  factory.OutputGood(),
				"error":   err.Error(),
			})
		}
	}

	// Log supply change and record metrics
	if previousSupply != supply {
		logger.Log("INFO", "Factory supply changed", map[string]interface{}{
			"factory":  factory.FactorySymbol(),
			"output":   factory.OutputGood(),
			"previous": previousSupply,
			"current":  supply,
		})

		// Record supply transition metric
		metrics.RecordManufacturingSupplyTransition(factory.PlayerID(), factory.OutputGood(), previousSupply, supply)

		// DEMAND-DRIVEN SUPPLY: When supply drops below HIGH, create ACQUIRE_DELIVER tasks
		// This replenishes the factory with raw materials so it can produce more output
		wasHighOrAbundant := isHighOrAbundant(previousSupply)
		isNowLower := !isHighOrAbundant(supply)
		if wasHighOrAbundant && isNowLower {
			p.replenisher.createTasksForFactory(ctx, factory)
		}
	}

	// CONTINUOUS DELIVERY: If supply is STILL below HIGH/ABUNDANT and factory is active,
	// create more ACQUIRE_DELIVER tasks if none are pending.
	// This fixes the bug where pipeline stalls because supply never reached HIGH to begin with.
	supplyBelowTarget := !isHighOrAbundant(supply)
	if supplyBelowTarget && factory.HasReceivedAnyDelivery() {
		if !p.replenisher.hasPendingAcquireDeliverTasks(ctx, factory) {
			logger.Log("INFO", "Factory supply still below target with no pending deliveries - creating more tasks", map[string]interface{}{
				"factory": factory.FactorySymbol(),
				"output":  factory.OutputGood(),
				"supply":  supply,
			})
			p.replenisher.createTasksForFactory(ctx, factory)
		}
	}

	// Check if now ready for collection
	if factory.IsReadyForCollection() {
		logger.Log("INFO", "Factory ready for collection", map[string]interface{}{
			"factory":  factory.FactorySymbol(),
			"output":   factory.OutputGood(),
			"supply":   supply,
			"pipeline": factory.PipelineID(),
		})

		// Record factory cycle completion metric
		metrics.RecordManufacturingFactoryCycle(factory.PlayerID(), factory.FactorySymbol(), factory.OutputGood())

		// Mark related COLLECT tasks as ready
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

	// CRITICAL: Verify pipeline is still EXECUTING before activating tasks
	// Tasks from CANCELLED/FAILED/COMPLETED pipelines should not be activated
	if !pipelineExecutingForFactory(ctx, p.pipelineRepo, factory, "Skipping task activation") {
		return
	}

	// STREAMING GATE: Ensure at least one delivery before allowing collection
	// This prevents premature collection when factory has HIGH supply but
	// we haven't started feeding it yet (opportunistic factory with existing stock)
	//
	// EXCEPTION: Skip this gate if factory has NO required inputs (source factory).
	// Source factories produce goods without needing any deliveries, so they
	// should be collected as soon as supply is HIGH/ABUNDANT.
	hasRequiredInputs := len(factory.RequiredInputs()) > 0
	if hasRequiredInputs && !factory.HasReceivedAnyDelivery() {
		logger.Log("DEBUG", "Factory ready but no deliveries yet - waiting", map[string]interface{}{
			"factory": factory.FactorySymbol(),
			"output":  factory.OutputGood(),
			"supply":  factory.CurrentSupply(),
		})
		return
	}

	// Get all tasks for this pipeline
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

	// Find COLLECT tasks for this factory
	tasks, err := p.taskRepo.FindByPipelineID(ctx, factory.PipelineID())
	if err != nil {
		logger.Log("WARN", "Failed to find tasks for pipeline", map[string]interface{}{
			"pipeline": factory.PipelineID(),
			"error":    err.Error(),
		})
		return
	}

	// Check if there's a pending COLLECT task
	var hasPendingCollect bool
	var hasCompletedCollect bool
	marked := 0

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
			// Task is already READY (e.g., from DB recovery after daemon restart)
			// Re-check saturation and add to queue if still valid
			if p.requeueRecoveredCollectTask(ctx, task) {
				marked++
			}
		case manufacturing.TaskStatusCompleted:
			hasCompletedCollect = true
		}
	}

	// If no pending COLLECT but there was a completed one, create new COLLECT + SELL tasks
	// This handles the case where factory supply became ABUNDANT again after previous collection
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

	// Notify coordinator that tasks are ready for assignment
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

	// Load the pipeline to check if this is the final product factory AND if it's still EXECUTING
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
			return
		}
		if pipeline == nil {
			logger.Log("DEBUG", "Skipping new task creation - pipeline not found", map[string]interface{}{
				"factory":     factory.FactorySymbol(),
				"pipeline_id": shortID(factory.PipelineID()),
			})
			return
		}

		// CRITICAL: Don't create new tasks for non-executing pipelines
		if pipeline.Status() != manufacturing.PipelineStatusExecuting {
			logger.Log("DEBUG", "Skipping new task creation - pipeline not executing", map[string]interface{}{
				"factory":         factory.FactorySymbol(),
				"pipeline_id":     shortID(factory.PipelineID()),
				"pipeline_status": pipeline.Status(),
			})
			return
		}

		fallbackMarket = pipeline.SellMarket()
	}

	// CRITICAL: Only create follow-up tasks for FINAL GOODS factories
	// Intermediate factories (EQUIPMENT, ELECTRONICS, etc.) should not get new tasks here
	// because their output goes to another factory, not to a sell market.
	// Without knowing the downstream factory, we can't correctly route intermediate goods.
	if pipeline != nil && factory.OutputGood() != pipeline.ProductGood() {
		logger.Log("DEBUG", "Skipping follow-up task for intermediate factory", map[string]interface{}{
			"factory":       factory.FactorySymbol(),
			"output":        factory.OutputGood(),
			"final_product": pipeline.ProductGood(),
			"reason":        "intermediate goods need specific factory destination",
		})
		return
	}

	// Fallback to finding best market if pipeline lookup failed
	if fallbackMarket == "" {
		var err error
		fallbackMarket, err = p.findBestSellMarket(ctx, factory.FactorySymbol(), factory.OutputGood())
		if err != nil {
			logger.Log("WARN", "Failed to find sell market for new collection", map[string]interface{}{
				"factory": factory.FactorySymbol(),
				"output":  factory.OutputGood(),
				"error":   err.Error(),
			})
			return
		}
	}

	// Use the distributor to select from ALL eligible markets, not just the pipeline's market
	// This distributes sales across multiple SCARCE/LIMITED markets to avoid flooding
	systemSymbol := extractSystem(factory.FactorySymbol())
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

	// Create atomic COLLECT_SELL task (same ship collects AND sells)
	// Immediately ready since supply is HIGH/ABUNDANT
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

	// Persist task
	if err := p.taskRepo.Create(ctx, collectSellTask); err != nil {
		logger.Log("WARN", "Failed to persist new COLLECT_SELL task", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Add to queue
	p.taskQueue.Enqueue(collectSellTask)

	logger.Log("INFO", "Created new COLLECT_SELL task for repeated collection (atomic)", map[string]interface{}{
		"factory":     factory.FactorySymbol(),
		"output":      factory.OutputGood(),
		"sell_market": sellMarket,
		"task_id":     collectSellTask.ID(),
	})

	// Notify coordinator that tasks are ready for assignment
	p.notifier.notifyTasksReady(factory.PipelineID())
}

// findBestSellMarket finds the best market to sell the collected good.
// Uses the existing FindBestMarketBuying which considers both price and activity.
// If waypointProvider is available, it will prefer closer markets when prices are similar.
func (p *FactorySupplyPoller) findBestSellMarket(ctx context.Context, factorySymbol string, good string) (string, error) {
	// Extract system from factory symbol (e.g., X1-YZ19-K84 -> X1-YZ19)
	system := extractSystem(factorySymbol)

	// Use existing market repo method to find best buying market
	result, err := p.marketRepo.FindBestMarketBuying(ctx, good, system, p.playerID)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", manufacturing.NewErrNoValidSellMarket(good)
	}

	return result.WaypointSymbol, nil
}

// extractSystem extracts the system symbol from a waypoint symbol
// e.g., X1-YZ19-K84 -> X1-YZ19
func extractSystem(waypointSymbol string) string {
	// Waypoint format: SECTOR-SYSTEM-WAYPOINT (e.g., X1-YZ19-K84)
	// System is first two parts
	parts := 0
	for i, c := range waypointSymbol {
		if c == '-' {
			parts++
			if parts == 2 {
				return waypointSymbol[:i]
			}
		}
	}
	return waypointSymbol
}

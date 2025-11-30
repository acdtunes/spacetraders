package services

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// SupplyMonitor polls factories and marks COLLECT tasks as ready when supply reaches HIGH.
// It runs as a background service, periodically checking factory supply levels.
// When a factory is ready but no COLLECT task exists, it creates new COLLECT + SELL tasks.
// When factory supply drops below HIGH, creates ACQUIRE_DELIVER tasks to replenish.
// When a required input is available from a running storage operation (e.g., gas siphoning),
// it creates STORAGE_ACQUIRE_DELIVER tasks instead of regular ACQUIRE_DELIVER tasks.
type SupplyMonitor struct {
	marketRepo          market.MarketRepository
	factoryTracker      *manufacturing.FactoryStateTracker
	factoryStateRepo    manufacturing.FactoryStateRepository
	pipelineRepo        manufacturing.PipelineRepository // For looking up pipeline sell_market
	taskQueue           ManufacturingTaskQueue
	taskRepo            manufacturing.TaskRepository
	sellMarketDistrib   *SellMarketDistributor // Distributes sales across multiple markets
	marketLocator       *goodsServices.MarketLocator // For finding export markets for inputs
	storageOpRepo       storage.StorageOperationRepository // For finding storage operations that provide goods
	pollInterval        time.Duration
	playerID            int
	taskReadyChan       chan<- struct{} // Optional: notifies coordinator when tasks become ready
}

// NewSupplyMonitor creates a new supply monitor
func NewSupplyMonitor(
	marketRepo market.MarketRepository,
	factoryTracker *manufacturing.FactoryStateTracker,
	factoryStateRepo manufacturing.FactoryStateRepository,
	pipelineRepo manufacturing.PipelineRepository,
	taskQueue ManufacturingTaskQueue,
	taskRepo manufacturing.TaskRepository,
	marketLocator *goodsServices.MarketLocator,
	storageOpRepo storage.StorageOperationRepository,
	pollInterval time.Duration,
	playerID int,
) *SupplyMonitor {
	// Create sell market distributor to avoid flooding single markets
	sellMarketDistrib := NewSellMarketDistributor(marketRepo, taskRepo)

	return &SupplyMonitor{
		marketRepo:          marketRepo,
		factoryTracker:      factoryTracker,
		factoryStateRepo:    factoryStateRepo,
		pipelineRepo:        pipelineRepo,
		taskQueue:           taskQueue,
		taskRepo:            taskRepo,
		sellMarketDistrib:   sellMarketDistrib,
		marketLocator:       marketLocator,
		storageOpRepo:       storageOpRepo,
		pollInterval:        pollInterval,
		playerID:            playerID,
	}
}

// SetTaskReadyChannel sets the channel for notifying when tasks become ready.
// This enables event-driven task assignment instead of polling.
func (m *SupplyMonitor) SetTaskReadyChannel(ch chan<- struct{}) {
	m.taskReadyChan = ch
}

// notifyTaskReady sends a non-blocking notification that tasks are ready
func (m *SupplyMonitor) notifyTaskReady() {
	if m.taskReadyChan == nil {
		return
	}
	// Non-blocking send - if coordinator is busy, it will pick up tasks on next event
	select {
	case m.taskReadyChan <- struct{}{}:
	default:
	}
}

// Run starts the supply monitor background loop
func (m *SupplyMonitor) Run(ctx context.Context) {
	logger := common.LoggerFromContext(ctx)
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	logger.Log("INFO", "Supply monitor started", map[string]interface{}{
		"poll_interval": m.pollInterval.String(),
	})

	for {
		select {
		case <-ticker.C:
			m.pollFactories(ctx)
		case <-ctx.Done():
			logger.Log("INFO", "Supply monitor stopped", nil)
			return
		}
	}
}

// pollFactories checks ALL factories (including ready ones)
// This is necessary to detect supply drops and reset ready flags
func (m *SupplyMonitor) pollFactories(ctx context.Context) {
	logger := common.LoggerFromContext(ctx)

	// Get ALL factories - we need to poll ready ones too in case supply dropped
	// This allows us to reset ready flags when supply drops below HIGH
	allFactories := m.factoryTracker.GetAllFactories()
	if len(allFactories) == 0 {
		// Even without factories, check supply-gated tasks
		m.ActivateSupplyGatedTasks(ctx)
		return
	}

	logger.Log("DEBUG", "Polling factories for supply updates", map[string]interface{}{
		"factory_count": len(allFactories),
	})

	for _, factory := range allFactories {
		m.checkFactorySupply(ctx, factory)
	}

	// Check and activate any supply-gated ACQUIRE_DELIVER tasks
	// This activates tasks that were waiting for HIGH/ABUNDANT supply at source market
	m.ActivateSupplyGatedTasks(ctx)

	// Enqueue READY COLLECTION pipeline tasks that aren't in the queue
	// COLLECTION pipelines have no factory states, so we must poll them separately
	m.ActivateCollectionPipelineTasks(ctx)
}

// checkFactorySupply checks a single factory's supply level
func (m *SupplyMonitor) checkFactorySupply(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	// Get current market data
	marketData, err := m.marketRepo.GetMarketData(ctx, factory.FactorySymbol(), factory.PlayerID())
	if err != nil {
		logger.Log("WARN", "Failed to get market data for factory", map[string]interface{}{
			"factory":    factory.FactorySymbol(),
			"output":     factory.OutputGood(),
			"error":      err.Error(),
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

	// Get supply level
	supply := "MODERATE"
	if tradeGood.Supply() != nil {
		supply = *tradeGood.Supply()
	}

	// Update factory state
	previousSupply := factory.CurrentSupply()
	factory.UpdateSupply(supply)

	// Persist the change to database
	if m.factoryStateRepo != nil {
		if err := m.factoryStateRepo.Update(ctx, factory); err != nil {
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
		wasHighOrAbundant := previousSupply == "HIGH" || previousSupply == "ABUNDANT"
		isNowLower := supply != "HIGH" && supply != "ABUNDANT"
		if wasHighOrAbundant && isNowLower {
			m.createAcquireDeliverTasksForFactory(ctx, factory)
		}
	}

	// CONTINUOUS DELIVERY: If supply is STILL below HIGH/ABUNDANT and factory is active,
	// create more ACQUIRE_DELIVER tasks if none are pending.
	// This fixes the bug where pipeline stalls because supply never reached HIGH to begin with.
	supplyBelowTarget := supply != "HIGH" && supply != "ABUNDANT"
	if supplyBelowTarget && factory.HasReceivedAnyDelivery() {
		if !m.hasPendingAcquireDeliverTasks(ctx, factory) {
			logger.Log("INFO", "Factory supply still below target with no pending deliveries - creating more tasks", map[string]interface{}{
				"factory": factory.FactorySymbol(),
				"output":  factory.OutputGood(),
				"supply":  supply,
			})
			m.createAcquireDeliverTasksForFactory(ctx, factory)
		}
	}

	// Check if now ready for collection
	if factory.IsReadyForCollection() {
		logger.Log("INFO", "Factory ready for collection", map[string]interface{}{
			"factory":    factory.FactorySymbol(),
			"output":     factory.OutputGood(),
			"supply":     supply,
			"pipeline":   factory.PipelineID(),
		})

		// Record factory cycle completion metric
		metrics.RecordManufacturingFactoryCycle(factory.PlayerID(), factory.FactorySymbol(), factory.OutputGood())

		// Mark related COLLECT tasks as ready
		m.markCollectTasksReady(ctx, factory)
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
func (m *SupplyMonitor) markCollectTasksReady(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	// CRITICAL: Verify pipeline is still EXECUTING before activating tasks
	// Tasks from CANCELLED/FAILED/COMPLETED pipelines should not be activated
	if m.pipelineRepo != nil {
		pipeline, err := m.pipelineRepo.FindByID(ctx, factory.PipelineID())
		if err != nil || pipeline == nil {
			logger.Log("DEBUG", "Skipping task activation - pipeline not found", map[string]interface{}{
				"factory":     factory.FactorySymbol(),
				"pipeline_id": factory.PipelineID()[:8],
			})
			return
		}
		if pipeline.Status() != manufacturing.PipelineStatusExecuting {
			logger.Log("DEBUG", "Skipping task activation - pipeline not executing", map[string]interface{}{
				"factory":         factory.FactorySymbol(),
				"pipeline_id":     factory.PipelineID()[:8],
				"pipeline_status": pipeline.Status(),
			})
			return
		}
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
	if m.taskRepo == nil {
		// Fall back to in-memory queue only
		marked := m.taskQueue.MarkCollectTasksReady(factory.FactorySymbol(), factory.OutputGood())
		logger.Log("DEBUG", "Marked COLLECT tasks ready (in-memory)", map[string]interface{}{
			"factory":      factory.FactorySymbol(),
			"output":       factory.OutputGood(),
			"tasks_marked": marked,
		})
		return
	}

	// Find COLLECT tasks for this factory
	tasks, err := m.taskRepo.FindByPipelineID(ctx, factory.PipelineID())
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
		// Find COLLECT_SELL tasks for this factory output
		isCollectTask := task.TaskType() == manufacturing.TaskTypeCollectSell &&
			task.FactorySymbol() == factory.FactorySymbol() &&
			task.Good() == factory.OutputGood()

		if isCollectTask {
			if task.Status() == manufacturing.TaskStatusPending {
				hasPendingCollect = true

				// Check sell market supply BEFORE marking ready
				// If saturated, keep task PENDING to avoid wasted trips
				if m.isSellMarketSaturated(ctx, task.TargetMarket(), task.Good()) {
					logger.Log("DEBUG", "Sell market saturated - keeping COLLECT_SELL task pending", map[string]interface{}{
						"task":        task.ID()[:8],
						"sell_market": task.TargetMarket(),
						"good":        task.Good(),
					})
					continue
				}

				// Mark as ready
				if err := task.MarkReady(); err != nil {
					logger.Log("WARN", "Failed to mark task ready", map[string]interface{}{
						"task":  task.ID(),
						"error": err.Error(),
					})
					continue
				}

				// Persist the change
				if err := m.taskRepo.Update(ctx, task); err != nil {
					logger.Log("WARN", "Failed to persist task state", map[string]interface{}{
						"task":  task.ID(),
						"error": err.Error(),
					})
					continue
				}

				// CRITICAL: Add to in-memory queue so assignTasks can find it
				// Without this, the task is READY in DB but invisible to the coordinator
				m.taskQueue.Enqueue(task)
				marked++
			} else if task.Status() == manufacturing.TaskStatusReady {
				// Task is already READY (e.g., from DB recovery after daemon restart)
				// Re-check saturation and add to queue if still valid
				if m.isSellMarketSaturated(ctx, task.TargetMarket(), task.Good()) {
					// Reset to PENDING since market is now saturated
					task.ResetToPending()
					if m.taskRepo != nil {
						_ = m.taskRepo.Update(ctx, task)
					}
					continue
				}
				// Add to in-memory queue so it can be assigned
				m.taskQueue.Enqueue(task)
				marked++
			} else if task.Status() == manufacturing.TaskStatusCompleted {
				hasCompletedCollect = true
			}
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

		m.createNewCollectSellTasks(ctx, factory)
		return
	}

	logger.Log("INFO", "Marked COLLECT tasks ready", map[string]interface{}{
		"factory":      factory.FactorySymbol(),
		"output":       factory.OutputGood(),
		"tasks_marked": marked,
	})

	// Notify coordinator that tasks are ready for assignment
	if marked > 0 {
		m.notifyTaskReady()
	}
}

// isSellMarketSaturated checks if the sell market has HIGH or ABUNDANT supply
// Returns true if we should NOT sell to this market (would crash prices)
func (m *SupplyMonitor) isSellMarketSaturated(ctx context.Context, sellMarket string, good string) bool {
	marketData, err := m.marketRepo.GetMarketData(ctx, sellMarket, m.playerID)
	if err != nil || marketData == nil {
		return false // Can't check, assume not saturated
	}

	tradeGood := marketData.FindGood(good)
	if tradeGood == nil || tradeGood.Supply() == nil {
		return false
	}

	supply := *tradeGood.Supply()
	return supply == "HIGH" || supply == "ABUNDANT"
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
func (m *SupplyMonitor) createNewCollectSellTasks(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	// Load the pipeline to check if this is the final product factory AND if it's still EXECUTING
	var pipeline *manufacturing.ManufacturingPipeline
	var fallbackMarket string
	if m.pipelineRepo != nil {
		var err error
		pipeline, err = m.pipelineRepo.FindByID(ctx, factory.PipelineID())
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
				"pipeline_id": factory.PipelineID()[:8],
			})
			return
		}

		// CRITICAL: Don't create new tasks for non-executing pipelines
		if pipeline.Status() != manufacturing.PipelineStatusExecuting {
			logger.Log("DEBUG", "Skipping new task creation - pipeline not executing", map[string]interface{}{
				"factory":         factory.FactorySymbol(),
				"pipeline_id":     factory.PipelineID()[:8],
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
		fallbackMarket, err = m.findBestSellMarket(ctx, factory.FactorySymbol(), factory.OutputGood())
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
	sellMarket, err := m.sellMarketDistrib.SelectSellMarket(
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
	if m.isSellMarketSaturated(ctx, sellMarket, factory.OutputGood()) {
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
	if err := m.taskRepo.Create(ctx, collectSellTask); err != nil {
		logger.Log("WARN", "Failed to persist new COLLECT_SELL task", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	// Add to queue
	m.taskQueue.Enqueue(collectSellTask)

	logger.Log("INFO", "Created new COLLECT_SELL task for repeated collection (atomic)", map[string]interface{}{
		"factory":     factory.FactorySymbol(),
		"output":      factory.OutputGood(),
		"sell_market": sellMarket,
		"task_id":     collectSellTask.ID(),
	})

	// Notify coordinator that tasks are ready for assignment
	m.notifyTaskReady()
}

// findBestSellMarket finds the best market to sell the collected good.
// Uses the existing FindBestMarketBuying which considers both price and activity.
// If waypointProvider is available, it will prefer closer markets when prices are similar.
func (m *SupplyMonitor) findBestSellMarket(ctx context.Context, factorySymbol string, good string) (string, error) {
	// Extract system from factory symbol (e.g., X1-YZ19-K84 -> X1-YZ19)
	system := extractSystem(factorySymbol)

	// Use existing market repo method to find best buying market
	result, err := m.marketRepo.FindBestMarketBuying(ctx, good, system, m.playerID)
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

// PollOnce performs a single poll of factories (for testing/manual triggering)
func (m *SupplyMonitor) PollOnce(ctx context.Context) {
	m.pollFactories(ctx)
}

// SetPollInterval updates the polling interval
func (m *SupplyMonitor) SetPollInterval(interval time.Duration) {
	m.pollInterval = interval
}

// createAcquireDeliverTasksForFactory creates ACQUIRE_DELIVER tasks when factory supply drops.
// This is the DEMAND-DRIVEN supply chain model: only acquire raw materials when needed.
//
// Algorithm:
//  1. Get market data for the factory to find IMPORT goods (factory inputs)
//  2. Check if there are already pending ACQUIRE_DELIVER tasks for these inputs
//  3. Check each input's supply level at the factory (INPUT BALANCING)
//  4. For each input that is LOW and without a pending task, find an EXPORT market and create task
func (m *SupplyMonitor) createAcquireDeliverTasksForFactory(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	if m.marketLocator == nil {
		logger.Log("WARN", "MarketLocator not available - cannot create ACQUIRE_DELIVER tasks", map[string]interface{}{
			"factory": factory.FactorySymbol(),
		})
		return
	}

	// CRITICAL: Verify pipeline is still EXECUTING before creating new tasks
	if m.pipelineRepo != nil {
		pipeline, err := m.pipelineRepo.FindByID(ctx, factory.PipelineID())
		if err != nil || pipeline == nil {
			logger.Log("DEBUG", "Skipping ACQUIRE_DELIVER task creation - pipeline not found", map[string]interface{}{
				"factory":     factory.FactorySymbol(),
				"pipeline_id": factory.PipelineID()[:8],
			})
			return
		}
		if pipeline.Status() != manufacturing.PipelineStatusExecuting {
			logger.Log("DEBUG", "Skipping ACQUIRE_DELIVER task creation - pipeline not executing", map[string]interface{}{
				"factory":         factory.FactorySymbol(),
				"pipeline_id":     factory.PipelineID()[:8],
				"pipeline_status": pipeline.Status(),
			})
			return
		}
	}

	// Get factory market data to find required inputs (IMPORT goods)
	marketData, err := m.marketRepo.GetMarketData(ctx, factory.FactorySymbol(), factory.PlayerID())
	if err != nil {
		logger.Log("WARN", "Failed to get market data for factory", map[string]interface{}{
			"factory": factory.FactorySymbol(),
			"error":   err.Error(),
		})
		return
	}

	// Build set of required inputs for this factory
	requiredInputsSet := make(map[string]bool)
	for _, input := range factory.RequiredInputs() {
		requiredInputsSet[input] = true
	}

	if len(requiredInputsSet) == 0 {
		logger.Log("DEBUG", "Factory has no required inputs - may be a source factory", map[string]interface{}{
			"factory": factory.FactorySymbol(),
			"output":  factory.OutputGood(),
		})
		return
	}

	// Find IMPORT goods that are ALSO in the factory's required inputs
	// This prevents creating tasks for unrelated goods the market happens to import
	type inputWithSupply struct {
		good   string
		supply string
	}
	var factoryInputs []inputWithSupply
	for _, tradeGood := range marketData.TradeGoods() {
		// Only consider goods that are both IMPORT AND required by this factory
		if tradeGood.TradeType() == market.TradeTypeImport && requiredInputsSet[tradeGood.Symbol()] {
			supply := "MODERATE" // Default if not available
			if tradeGood.Supply() != nil {
				supply = *tradeGood.Supply()
			}
			factoryInputs = append(factoryInputs, inputWithSupply{
				good:   tradeGood.Symbol(),
				supply: supply,
			})
		}
	}

	if len(factoryInputs) == 0 {
		logger.Log("DEBUG", "Factory required inputs not available as IMPORT at market", map[string]interface{}{
			"factory":         factory.FactorySymbol(),
			"output":          factory.OutputGood(),
			"required_inputs": factory.RequiredInputs(),
		})
		return
	}

	// Get existing tasks for this pipeline to check what's already pending
	existingTasks, err := m.taskRepo.FindByPipelineID(ctx, factory.PipelineID())
	if err != nil {
		logger.Log("WARN", "Failed to find existing tasks", map[string]interface{}{
			"pipeline": factory.PipelineID(),
			"error":    err.Error(),
		})
		return
	}

	// Build set of goods with pending/ready/executing ACQUIRE_DELIVER or STORAGE_ACQUIRE_DELIVER tasks
	// CRITICAL: Must include both task types to prevent duplicate task creation
	pendingInputs := make(map[string]bool)
	for _, task := range existingTasks {
		isDeliveryTask := task.TaskType() == manufacturing.TaskTypeAcquireDeliver ||
			task.TaskType() == manufacturing.TaskTypeStorageAcquireDeliver
		if isDeliveryTask &&
			task.FactorySymbol() == factory.FactorySymbol() &&
			(task.Status() == manufacturing.TaskStatusPending ||
				task.Status() == manufacturing.TaskStatusReady ||
				task.Status() == manufacturing.TaskStatusAssigned ||
				task.Status() == manufacturing.TaskStatusExecuting) {
			pendingInputs[task.Good()] = true
		}
	}

	systemSymbol := extractSystem(factory.FactorySymbol())
	tasksCreated := 0
	skippedHighSupply := 0

	// Create ACQUIRE_DELIVER tasks for inputs that don't have pending tasks
	// INPUT BALANCING: Only deliver inputs that are actually needed (not already HIGH/ABUNDANT)
	for _, input := range factoryInputs {
		if pendingInputs[input.good] {
			continue // Already has a pending task
		}

		// INPUT BALANCING OPTIMIZATION: Skip inputs that already have HIGH/ABUNDANT supply at factory
		// This prevents over-delivering one input while another starves
		if input.supply == "HIGH" || input.supply == "ABUNDANT" {
			logger.Log("DEBUG", "Input already abundant at factory, skipping delivery", map[string]interface{}{
				"factory": factory.FactorySymbol(),
				"input":   input.good,
				"supply":  input.supply,
			})
			skippedHighSupply++
			continue
		}

		// STORAGE OPERATION INTEGRATION: Check if this input is produced by a running storage operation
		// (e.g., gas siphoning produces LIQUID_HYDROGEN, LIQUID_NITROGEN, HYDROCARBON)
		// If so, create STORAGE_ACQUIRE_DELIVER task instead of regular ACQUIRE_DELIVER
		if storageOp := m.findRunningStorageOperationForGood(ctx, input.good); storageOp != nil {
			task := manufacturing.NewStorageAcquireDeliverTask(
				factory.PipelineID(),
				factory.PlayerID(),
				input.good,
				storageOp.ID(),             // Storage operation to acquire from
				storageOp.WaypointSymbol(), // Where storage ships are located
				factory.FactorySymbol(),    // Where to deliver
				nil,                        // No dependencies
			)

			// Storage tasks are always ready since they don't depend on market supply
			if err := task.MarkReady(); err != nil {
				logger.Log("WARN", "Failed to mark STORAGE_ACQUIRE_DELIVER task ready", map[string]interface{}{
					"error": err.Error(),
				})
				continue
			}

			// Persist task
			if err := m.taskRepo.Create(ctx, task); err != nil {
				logger.Log("WARN", "Failed to persist STORAGE_ACQUIRE_DELIVER task", map[string]interface{}{
					"error": err.Error(),
				})
				continue
			}

			m.taskQueue.Enqueue(task)
			tasksCreated++

			logger.Log("INFO", "Created STORAGE_ACQUIRE_DELIVER task (from gas operation)", map[string]interface{}{
				"factory":       factory.FactorySymbol(),
				"input":         input.good,
				"storage_op":    storageOp.ID()[:8],
				"storage_wp":    storageOp.WaypointSymbol(),
				"task_id":       task.ID()[:8],
			})
			continue // Move to next input - this one is handled by storage
		}

		// Find export market to buy this input from
		// Use supply-priority method: ABUNDANT → HIGH → MODERATE (skip SCARCE/LIMITED)
		// This prevents buying from expensive SCARCE markets (e.g., C44 at 650 credits)
		// when ABUNDANT markets are available (e.g., G52 at 18 credits)
		exportMarket, err := m.marketLocator.FindExportMarketBySupplyPriority(ctx, input.good, systemSymbol, factory.PlayerID())
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("No export market for %s: %v", input.good, err), map[string]interface{}{
				"factory": factory.FactorySymbol(),
				"input":   input.good,
			})
			continue
		}

		// SUPPLY-GATED TASK CREATION: Check source market supply before marking task ready
		// This prevents buying from SCARCE/LIMITED markets (highest prices) and losing 50%+ on spread
		sourceSupply := m.getSourceMarketSupply(ctx, exportMarket.WaypointSymbol, input.good)
		isAcceptableSupply := sourceSupply == "HIGH" || sourceSupply == "ABUNDANT" || sourceSupply == "MODERATE"
		isRawMaterial := goods.IsMineableRawMaterial(input.good)

		// Create ACQUIRE_DELIVER task: buy from export market, deliver to factory
		task := manufacturing.NewAcquireDeliverTask(
			factory.PipelineID(),
			factory.PlayerID(),
			input.good,
			exportMarket.WaypointSymbol, // Where to buy
			factory.FactorySymbol(),     // Where to deliver
			nil,                         // No dependencies
		)

		// SUPPLY-BASED PRIORITY: Higher supply = better prices = higher priority
		// This ensures we buy from ABUNDANT markets before HIGH before MODERATE
		switch sourceSupply {
		case "ABUNDANT":
			task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityAbundant)
		case "HIGH":
			task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityHigh)
		case "MODERATE":
			task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityModerate)
		// SCARCE/LIMITED stay at default priority but won't be marked READY anyway
		}

		// SUPPLY GATING DECISION:
		// - Raw materials (ores, crystals, gases): Always mark ready (can't be fabricated)
		// - Goods with MODERATE/HIGH/ABUNDANT supply: Mark ready (acceptable prices)
		// - Goods with SCARCE/LIMITED supply: Stay PENDING (too expensive, wait for better prices)
		// MODERATE is allowed to bootstrap supply chains - without acquiring intermediate goods,
		// factories can't produce and supply will never improve.
		shouldEnqueue := false
		if isRawMaterial || isAcceptableSupply {
			// Mark immediately ready
			if err := task.MarkReady(); err != nil {
				logger.Log("WARN", "Failed to mark ACQUIRE_DELIVER task ready", map[string]interface{}{
					"error": err.Error(),
				})
				continue
			}
			shouldEnqueue = true

			logger.Log("INFO", "Created ACQUIRE_DELIVER task (READY)", map[string]interface{}{
				"factory":       factory.FactorySymbol(),
				"input":         input.good,
				"source":        exportMarket.WaypointSymbol,
				"source_supply": sourceSupply,
				"is_raw":        isRawMaterial,
				"task_id":       task.ID()[:8],
			})
		} else {
			// Stay PENDING - will be activated when source market supply improves
			logger.Log("INFO", "Created ACQUIRE_DELIVER task (PENDING - supply gated)", map[string]interface{}{
				"factory":       factory.FactorySymbol(),
				"input":         input.good,
				"source":        exportMarket.WaypointSymbol,
				"source_supply": sourceSupply,
				"waiting_for":   "HIGH/ABUNDANT",
				"task_id":       task.ID()[:8],
			})
		}

		// Persist task BEFORE enqueueing to prevent orphaned queue entries
		if err := m.taskRepo.Create(ctx, task); err != nil {
			logger.Log("WARN", "Failed to persist ACQUIRE_DELIVER task", map[string]interface{}{
				"error": err.Error(),
			})
			continue
		}

		// Only enqueue after successful persistence
		if shouldEnqueue {
			m.taskQueue.Enqueue(task)
		}

		tasksCreated++
	}

	if tasksCreated > 0 || skippedHighSupply > 0 {
		logger.Log("INFO", "ACQUIRE_DELIVER task creation summary", map[string]interface{}{
			"factory":             factory.FactorySymbol(),
			"output":              factory.OutputGood(),
			"tasks_created":       tasksCreated,
			"skipped_high_supply": skippedHighSupply,
			"total_inputs":        len(factoryInputs),
		})
	}
	if tasksCreated > 0 {
		m.notifyTaskReady()
	}
}

// hasPendingAcquireDeliverTasks checks if there are any pending/ready/assigned/executing
// ACQUIRE_DELIVER tasks for this factory. Used to avoid creating duplicate delivery tasks.
func (m *SupplyMonitor) hasPendingAcquireDeliverTasks(ctx context.Context, factory *manufacturing.FactoryState) bool {
	if m.taskRepo == nil {
		return false
	}

	tasks, err := m.taskRepo.FindByPipelineID(ctx, factory.PipelineID())
	if err != nil {
		return false // Assume no pending if we can't check
	}

	for _, task := range tasks {
		if task.TaskType() == manufacturing.TaskTypeAcquireDeliver &&
			task.FactorySymbol() == factory.FactorySymbol() &&
			(task.Status() == manufacturing.TaskStatusPending ||
				task.Status() == manufacturing.TaskStatusReady ||
				task.Status() == manufacturing.TaskStatusAssigned ||
				task.Status() == manufacturing.TaskStatusExecuting) {
			return true
		}
	}

	return false
}

// getSourceMarketSupply returns the supply level of a good at a specific market
func (m *SupplyMonitor) getSourceMarketSupply(ctx context.Context, waypointSymbol string, good string) string {
	marketData, err := m.marketRepo.GetMarketData(ctx, waypointSymbol, m.playerID)
	if err != nil || marketData == nil {
		return "MODERATE" // Default if we can't check
	}

	tradeGood := marketData.FindGood(good)
	if tradeGood == nil || tradeGood.Supply() == nil {
		return "MODERATE"
	}

	return *tradeGood.Supply()
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
	for _, task := range pendingTasks {
		// Only process ACQUIRE_DELIVER tasks
		if task.TaskType() != manufacturing.TaskTypeAcquireDeliver {
			continue
		}

		// CRITICAL: Verify pipeline is still EXECUTING before activating task
		// Tasks from CANCELLED/FAILED/COMPLETED pipelines should not be activated
		pipelineID := task.PipelineID()
		pipelineStatus, cached := pipelineStatusCache[pipelineID]
		if !cached {
			if m.pipelineRepo != nil {
				pipeline, err := m.pipelineRepo.FindByID(ctx, pipelineID)
				if err != nil || pipeline == nil {
					// Pipeline not found - skip this task
					logger.Log("DEBUG", "Skipping task - pipeline not found", map[string]interface{}{
						"task_id":     task.ID()[:8],
						"pipeline_id": pipelineID[:8],
					})
					continue
				}
				pipelineStatus = pipeline.Status()
				pipelineStatusCache[pipelineID] = pipelineStatus
			}
		}

		if pipelineStatus != manufacturing.PipelineStatusExecuting {
			logger.Log("DEBUG", "Skipping task from non-executing pipeline", map[string]interface{}{
				"task_id":         task.ID()[:8],
				"pipeline_id":     pipelineID[:8],
				"pipeline_status": pipelineStatus,
			})
			continue
		}

		// Determine if this task should be activated
		var shouldActivate bool
		var reason string
		var sourceSupply string

		if goods.IsMineableRawMaterial(task.Good()) {
			// Raw materials bypass supply-gating - they can only be mined/purchased
			// Activate immediately since waiting for HIGH supply may never happen
			shouldActivate = true
			reason = "raw_material"
			sourceSupply = "N/A"
		} else {
			// Check source market supply for fabricated goods
			// MODERATE is allowed to bootstrap supply chains - without acquiring intermediate goods,
			// factories can't produce and supply will never improve.
			// Only SCARCE/LIMITED are blocked (too expensive, 50%+ loss on spread).
			sourceSupply = m.getSourceMarketSupply(ctx, task.SourceMarket(), task.Good())
			if sourceSupply == "HIGH" || sourceSupply == "ABUNDANT" || sourceSupply == "MODERATE" {
				shouldActivate = true
				reason = "acceptable_supply"
			}
		}

		if !shouldActivate {
			// RE-SOURCING: Current source has bad supply, try to find a better market
			// This prevents tasks from being stuck forever when their original source degrades
			systemSymbol := extractSystem(task.FactorySymbol())
			betterSource, err := m.marketLocator.FindExportMarketBySupplyPriority(ctx, task.Good(), systemSymbol, m.playerID)
			if err != nil {
				// No better source available - keep waiting
				continue
			}

			// Check if the better source actually has acceptable supply
			betterSupply := m.getSourceMarketSupply(ctx, betterSource.WaypointSymbol, task.Good())
			if betterSupply != "HIGH" && betterSupply != "ABUNDANT" && betterSupply != "MODERATE" {
				// Better source also has bad supply - keep waiting
				continue
			}

			// Found a better source! Update the task
			oldSource := task.SourceMarket()
			if err := task.UpdateSourceMarket(betterSource.WaypointSymbol); err != nil {
				logger.Log("WARN", "Failed to update task source market", map[string]interface{}{
					"task_id": task.ID()[:8],
					"error":   err.Error(),
				})
				continue
			}

			logger.Log("INFO", "Re-sourced PENDING task to better market", map[string]interface{}{
				"task_id":     task.ID()[:8],
				"good":        task.Good(),
				"old_source":  oldSource,
				"new_source":  betterSource.WaypointSymbol,
				"new_supply":  betterSupply,
			})

			// Update supply info for priority setting and activation
			sourceSupply = betterSupply
			shouldActivate = true
			reason = "re-sourced"
		}

		// SUPPLY-BASED PRIORITY: Higher supply = better prices = higher priority
		// This ensures we buy from ABUNDANT markets before HIGH before MODERATE
		switch sourceSupply {
		case "ABUNDANT":
			task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityAbundant)
		case "HIGH":
			task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityHigh)
		case "MODERATE":
			task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityModerate)
		}

		// Activate the task
		if err := task.MarkReady(); err != nil {
			logger.Log("WARN", "Failed to mark supply-gated task ready", map[string]interface{}{
				"task_id": task.ID()[:8],
				"error":   err.Error(),
			})
			continue
		}

		// Persist the change
		if err := m.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARN", "Failed to persist activated task", map[string]interface{}{
				"task_id": task.ID()[:8],
				"error":   err.Error(),
			})
			continue
		}

		// Add to queue
		m.taskQueue.Enqueue(task)
		activated++

		logger.Log("INFO", "Activated supply-gated ACQUIRE_DELIVER task", map[string]interface{}{
			"task_id":       task.ID()[:8],
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
		m.notifyTaskReady()
	}

	return activated
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

	// Step 1: Activate PENDING COLLECTION pipeline tasks if conditions are favorable
	pendingTasks, err := m.taskRepo.FindByStatus(ctx, m.playerID, manufacturing.TaskStatusPending)
	if err == nil {
		for _, task := range pendingTasks {
			if task.TaskType() != manufacturing.TaskTypeCollectSell {
				continue
			}

			pipelineID := task.PipelineID()
			pipeline, cached := pipelineCache[pipelineID]
			if !cached {
				pipeline, _ = m.pipelineRepo.FindByID(ctx, pipelineID)
				pipelineCache[pipelineID] = pipeline
			}

			if pipeline == nil || pipeline.PipelineType() != manufacturing.PipelineTypeCollection {
				continue
			}
			if pipeline.Status() != manufacturing.PipelineStatusExecuting {
				continue
			}

			// Check factory supply - need ABUNDANT to START (buffer for supply drops during navigation)
			// Executor will still collect if supply is HIGH when ship arrives
			factorySupply := m.getSourceMarketSupply(ctx, task.FactorySymbol(), task.Good())
			if factorySupply != "ABUNDANT" {
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

			logger.Log("INFO", "Activated PENDING COLLECTION task", map[string]interface{}{
				"task":          task.ID()[:8],
				"good":          task.Good(),
				"factory":       task.FactorySymbol(),
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
		pipeline, cached := pipelineCache[pipelineID]
		if !cached {
			pipeline, _ = m.pipelineRepo.FindByID(ctx, pipelineID)
			pipelineCache[pipelineID] = pipeline
		}

		if pipeline == nil || pipeline.PipelineType() != manufacturing.PipelineTypeCollection {
			continue
		}
		if pipeline.Status() != manufacturing.PipelineStatusExecuting {
			continue
		}

		// Check sell market saturation before enqueueing
		if m.isSellMarketSaturated(ctx, task.TargetMarket(), task.Good()) {
			task.ResetToPending()
			_ = m.taskRepo.Update(ctx, task)
			logger.Log("DEBUG", "COLLECTION task sell market saturated - reset to PENDING", map[string]interface{}{
				"task":        task.ID()[:8],
				"good":        task.Good(),
				"sell_market": task.TargetMarket(),
			})
			continue
		}

		// Enqueue the task
		m.taskQueue.Enqueue(task)
		activated++

		logger.Log("INFO", "Enqueued READY COLLECTION task", map[string]interface{}{
			"task":        task.ID()[:8],
			"good":        task.Good(),
			"sell_market": task.TargetMarket(),
		})
	}

	if activated > 0 {
		logger.Log("INFO", "COLLECTION pipeline task activation summary", map[string]interface{}{
			"activated": activated,
		})
		m.notifyTaskReady()
	}

	return activated
}

// findRunningStorageOperationForGood checks if there's a running storage operation
// that produces the specified good. This enables integration between gas siphoning
// operations and the manufacturing pipeline - instead of buying gases from market,
// haulers can pick up cargo directly from storage ships at the extraction site.
//
// Returns the storage operation if found, nil otherwise.
func (m *SupplyMonitor) findRunningStorageOperationForGood(ctx context.Context, good string) *storage.StorageOperation {
	logger := common.LoggerFromContext(ctx)

	if m.storageOpRepo == nil {
		logger.Log("DEBUG", "Storage operation lookup: storageOpRepo is nil", map[string]interface{}{
			"good": good,
		})
		return nil
	}

	// Find storage operations that support this good
	operations, err := m.storageOpRepo.FindByGood(ctx, m.playerID, good)
	if err != nil {
		logger.Log("WARN", "Storage operation lookup: FindByGood failed", map[string]interface{}{
			"good":      good,
			"player_id": m.playerID,
			"error":     err.Error(),
		})
		return nil
	}

	logger.Log("DEBUG", "Storage operation lookup", map[string]interface{}{
		"good":            good,
		"player_id":       m.playerID,
		"operations_found": len(operations),
	})

	// Return the first RUNNING operation that supports this good
	for _, op := range operations {
		isRunning := op.IsRunning()
		supportsGood := op.SupportsGood(good)
		logger.Log("DEBUG", "Storage operation check", map[string]interface{}{
			"op_id":        op.ID()[:8],
			"op_status":    op.Status(),
			"is_running":   isRunning,
			"supports_good": supportsGood,
		})
		if isRunning && supportsGood {
			return op
		}
	}

	return nil
}

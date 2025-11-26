package services

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// SupplyMonitor polls factories and marks COLLECT tasks as ready when supply reaches HIGH.
// It runs as a background service, periodically checking factory supply levels.
// When a factory is ready but no COLLECT task exists, it creates new COLLECT + SELL tasks.
type SupplyMonitor struct {
	marketRepo       market.MarketRepository
	factoryTracker   *manufacturing.FactoryStateTracker
	factoryStateRepo manufacturing.FactoryStateRepository
	pipelineRepo     manufacturing.PipelineRepository // For looking up pipeline sell_market
	taskQueue        *TaskQueue
	taskRepo         manufacturing.TaskRepository
	pollInterval     time.Duration
	playerID         int
	taskReadyChan    chan<- struct{} // Optional: notifies coordinator when tasks become ready
}

// NewSupplyMonitor creates a new supply monitor
func NewSupplyMonitor(
	marketRepo market.MarketRepository,
	factoryTracker *manufacturing.FactoryStateTracker,
	factoryStateRepo manufacturing.FactoryStateRepository,
	pipelineRepo manufacturing.PipelineRepository,
	taskQueue *TaskQueue,
	taskRepo manufacturing.TaskRepository,
	pollInterval time.Duration,
	playerID int,
) *SupplyMonitor {
	return &SupplyMonitor{
		marketRepo:       marketRepo,
		factoryTracker:   factoryTracker,
		factoryStateRepo: factoryStateRepo,
		pipelineRepo:     pipelineRepo,
		taskQueue:        taskQueue,
		taskRepo:         taskRepo,
		pollInterval:     pollInterval,
		playerID:         playerID,
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
		return
	}

	logger.Log("DEBUG", "Polling factories for supply updates", map[string]interface{}{
		"factory_count": len(allFactories),
	})

	for _, factory := range allFactories {
		m.checkFactorySupply(ctx, factory)
	}
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
func (m *SupplyMonitor) markCollectTasksReady(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

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

				// Add to queue
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

// createNewCollectSellTasks creates a new atomic COLLECT_SELL task for a factory that's ready
// but has no pending COLLECT task (previous one completed and supply is ABUNDANT again).
// Uses atomic task to prevent "orphaned cargo" bug where one ship collects and another sells.
func (m *SupplyMonitor) createNewCollectSellTasks(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	// CRITICAL: Use the pipeline's sell_market instead of selecting a new one
	// This ensures we use the closest market selected at pipeline creation time
	// and avoids opportunity cost of ships travelling far
	var sellMarket string
	if m.pipelineRepo != nil {
		pipeline, err := m.pipelineRepo.FindByID(ctx, factory.PipelineID())
		if err != nil {
			logger.Log("WARN", "Failed to load pipeline for sell market lookup", map[string]interface{}{
				"factory":  factory.FactorySymbol(),
				"output":   factory.OutputGood(),
				"pipeline": factory.PipelineID(),
				"error":    err.Error(),
			})
			return
		}
		if pipeline != nil {
			sellMarket = pipeline.SellMarket()
		}
	}

	// Fallback to finding best market if pipeline lookup failed
	if sellMarket == "" {
		var err error
		sellMarket, err = m.findBestSellMarket(ctx, factory.FactorySymbol(), factory.OutputGood())
		if err != nil {
			logger.Log("WARN", "Failed to find sell market for new collection", map[string]interface{}{
				"factory": factory.FactorySymbol(),
				"output":  factory.OutputGood(),
				"error":   err.Error(),
			})
			return
		}
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

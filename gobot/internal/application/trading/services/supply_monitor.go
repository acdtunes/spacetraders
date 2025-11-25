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
type SupplyMonitor struct {
	marketRepo     market.MarketRepository
	factoryTracker *manufacturing.FactoryStateTracker
	taskQueue      *TaskQueue
	taskRepo       manufacturing.TaskRepository
	pollInterval   time.Duration
	playerID       int
}

// NewSupplyMonitor creates a new supply monitor
func NewSupplyMonitor(
	marketRepo market.MarketRepository,
	factoryTracker *manufacturing.FactoryStateTracker,
	taskQueue *TaskQueue,
	taskRepo manufacturing.TaskRepository,
	pollInterval time.Duration,
	playerID int,
) *SupplyMonitor {
	return &SupplyMonitor{
		marketRepo:     marketRepo,
		factoryTracker: factoryTracker,
		taskQueue:      taskQueue,
		taskRepo:       taskRepo,
		pollInterval:   pollInterval,
		playerID:       playerID,
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

// pollFactories checks all factories awaiting production
func (m *SupplyMonitor) pollFactories(ctx context.Context) {
	logger := common.LoggerFromContext(ctx)

	// Get all factories with inputs delivered but not ready
	pendingFactories := m.factoryTracker.GetFactoriesAwaitingProduction()
	if len(pendingFactories) == 0 {
		return
	}

	logger.Log("DEBUG", "Polling factories for supply updates", map[string]interface{}{
		"factory_count": len(pendingFactories),
	})

	for _, factory := range pendingFactories {
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

// markCollectTasksReady marks COLLECT tasks for this factory as ready
func (m *SupplyMonitor) markCollectTasksReady(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	// Get all tasks for this pipeline
	if m.taskRepo == nil {
		// Fall back to in-memory queue only
		marked := m.taskQueue.MarkCollectTasksReady(factory.FactorySymbol(), factory.OutputGood())
		logger.Log("DEBUG", "Marked COLLECT tasks ready (in-memory)", map[string]interface{}{
			"factory":     factory.FactorySymbol(),
			"output":      factory.OutputGood(),
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

	marked := 0
	for _, task := range tasks {
		// Find COLLECT tasks for this factory output
		if task.TaskType() == manufacturing.TaskTypeCollect &&
			task.FactorySymbol() == factory.FactorySymbol() &&
			task.Good() == factory.OutputGood() &&
			task.Status() == manufacturing.TaskStatusPending {

			// Check if all dependencies are met
			depsComplete := m.checkDependenciesComplete(ctx, task)
			if !depsComplete {
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

			// Add to queue
			m.taskQueue.Enqueue(task)
			marked++
		}
	}

	logger.Log("INFO", "Marked COLLECT tasks ready", map[string]interface{}{
		"factory":      factory.FactorySymbol(),
		"output":       factory.OutputGood(),
		"tasks_marked": marked,
	})
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

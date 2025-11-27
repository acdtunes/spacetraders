package manufacturing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Constants for pipeline lifecycle
const (
	// MaxFinalCollections is the number of successful COLLECT_SELL cycles for the final product
	// before a pipeline is considered complete.
	MaxFinalCollections = 3

	// StuckPipelineThreshold is how long a pipeline can run without progress before being recycled.
	StuckPipelineThreshold = 30 * time.Minute

	// StuckPipelineFailedTaskThreshold is the max failed tasks before a pipeline is considered stuck.
	StuckPipelineFailedTaskThreshold = 5
)

// PipelineManager manages pipeline lifecycle (create, complete, cancel, recycle).
type PipelineManager interface {
	// ScanAndCreatePipelines finds opportunities and creates new pipelines
	ScanAndCreatePipelines(ctx context.Context, params PipelineScanParams) (int, error)

	// CheckPipelineCompletion checks if pipeline is complete and updates status
	CheckPipelineCompletion(ctx context.Context, pipelineID string) (bool, error)

	// RecyclePipeline cancels a stuck pipeline and frees its slot
	RecyclePipeline(ctx context.Context, pipelineID string, playerID int) error

	// DetectAndRecycleStuckPipelines finds and recycles stuck pipelines, returns count
	DetectAndRecycleStuckPipelines(ctx context.Context, playerID int) int

	// RescueReadyCollectSellTasks loads READY COLLECT_SELL tasks from DB and enqueues them
	RescueReadyCollectSellTasks(ctx context.Context, playerID int)

	// HasPipelineForGood checks if active pipeline exists for this good
	HasPipelineForGood(good string) bool

	// GetActivePipelines returns all active pipelines
	GetActivePipelines() map[string]*manufacturing.ManufacturingPipeline

	// AddActivePipeline adds a pipeline to active pipelines (for state recovery)
	AddActivePipeline(id string, pipeline *manufacturing.ManufacturingPipeline)

	// SetActivePipelines sets the active pipelines map (for initialization)
	SetActivePipelines(pipelines map[string]*manufacturing.ManufacturingPipeline)

	// OnPipelineCompleted is called when a pipeline completes/fails (for triggering rescans)
	SetPipelineCompletedCallback(callback func(ctx context.Context))
}

// PipelineScanParams contains parameters for pipeline scanning
type PipelineScanParams struct {
	SystemSymbol     string
	PlayerID         int
	MinPurchasePrice int
	MaxPipelines     int
}

// PipelineLifecycleManager implements PipelineManager
type PipelineLifecycleManager struct {
	// Services
	demandFinder    *services.ManufacturingDemandFinder
	pipelinePlanner *services.PipelinePlanner
	taskQueue       *services.TaskQueue
	factoryTracker  *manufacturing.FactoryStateTracker

	// Repositories
	pipelineRepo     manufacturing.PipelineRepository
	taskRepo         manufacturing.TaskRepository
	factoryStateRepo manufacturing.FactoryStateRepository
	marketRepo       market.MarketRepository

	// Runtime state
	mu              sync.RWMutex
	activePipelines map[string]*manufacturing.ManufacturingPipeline

	// Clock for time operations
	clock shared.Clock

	// Callback when pipeline completes
	onPipelineCompleted func(ctx context.Context)
}

// NewPipelineLifecycleManager creates a new pipeline lifecycle manager
func NewPipelineLifecycleManager(
	demandFinder *services.ManufacturingDemandFinder,
	pipelinePlanner *services.PipelinePlanner,
	taskQueue *services.TaskQueue,
	factoryTracker *manufacturing.FactoryStateTracker,
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
	factoryStateRepo manufacturing.FactoryStateRepository,
	marketRepo market.MarketRepository,
	clock shared.Clock,
) *PipelineLifecycleManager {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &PipelineLifecycleManager{
		demandFinder:     demandFinder,
		pipelinePlanner:  pipelinePlanner,
		taskQueue:        taskQueue,
		factoryTracker:   factoryTracker,
		pipelineRepo:     pipelineRepo,
		taskRepo:         taskRepo,
		factoryStateRepo: factoryStateRepo,
		marketRepo:       marketRepo,
		activePipelines:  make(map[string]*manufacturing.ManufacturingPipeline),
		clock:            clock,
	}
}

// SetPipelineCompletedCallback sets the callback for pipeline completion
func (m *PipelineLifecycleManager) SetPipelineCompletedCallback(callback func(ctx context.Context)) {
	m.onPipelineCompleted = callback
}

// GetActivePipelines returns all active pipelines
func (m *PipelineLifecycleManager) GetActivePipelines() map[string]*manufacturing.ManufacturingPipeline {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return a copy to prevent external modification
	result := make(map[string]*manufacturing.ManufacturingPipeline, len(m.activePipelines))
	for k, v := range m.activePipelines {
		result[k] = v
	}
	return result
}

// SetActivePipelines sets the active pipelines map
func (m *PipelineLifecycleManager) SetActivePipelines(pipelines map[string]*manufacturing.ManufacturingPipeline) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activePipelines = pipelines
}

// HasPipelineForGood checks if we already have an active pipeline for this good
func (m *PipelineLifecycleManager) HasPipelineForGood(good string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, pipeline := range m.activePipelines {
		if pipeline.ProductGood() == good {
			return true
		}
	}
	return false
}

// AddActivePipeline adds a pipeline to active pipelines (for state recovery)
func (m *PipelineLifecycleManager) AddActivePipeline(id string, pipeline *manufacturing.ManufacturingPipeline) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activePipelines[id] = pipeline
}

// ScanAndCreatePipelines scans for opportunities and creates new pipelines
func (m *PipelineLifecycleManager) ScanAndCreatePipelines(ctx context.Context, params PipelineScanParams) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Check if we have room for more pipelines
	m.mu.RLock()
	activePipelineCount := len(m.activePipelines)
	m.mu.RUnlock()

	if activePipelineCount >= params.MaxPipelines {
		logger.Log("DEBUG", "Max pipelines reached, skipping opportunity scan", map[string]interface{}{
			"active_pipelines": activePipelineCount,
			"max_pipelines":    params.MaxPipelines,
		})
		return 0, nil
	}

	// Find opportunities
	config := services.DemandFinderConfig{
		MinPurchasePrice: params.MinPurchasePrice,
		MaxOpportunities: params.MaxPipelines * 2,
	}

	opportunities, err := m.demandFinder.FindHighDemandManufacturables(
		ctx,
		params.SystemSymbol,
		params.PlayerID,
		config,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to scan opportunities: %w", err)
	}

	logger.Log("INFO", fmt.Sprintf("Found %d manufacturing opportunities", len(opportunities)), nil)

	pipelinesCreated := 0

	// Create pipelines for top opportunities
	for _, opp := range opportunities {
		if activePipelineCount >= params.MaxPipelines {
			break
		}

		if m.HasPipelineForGood(opp.Good()) {
			continue
		}

		// Create pipeline
		pipeline, tasks, factoryStates, err := m.pipelinePlanner.CreatePipeline(
			ctx, opp, params.SystemSymbol, params.PlayerID,
		)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to create pipeline for %s: %v", opp.Good(), err), nil)
			continue
		}

		// Start the pipeline
		if err := pipeline.Start(); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to start pipeline for %s: %v", opp.Good(), err), nil)
			continue
		}

		// Persist pipeline and tasks
		if m.pipelineRepo != nil {
			if err := m.pipelineRepo.Create(ctx, pipeline); err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to persist pipeline: %v", err), nil)
				continue
			}

			for _, task := range tasks {
				if err := m.taskRepo.Create(ctx, task); err != nil {
					logger.Log("ERROR", fmt.Sprintf("Failed to persist task: %v", err), nil)
				}
			}

			for _, state := range factoryStates {
				if err := m.factoryStateRepo.Create(ctx, state); err != nil {
					logger.Log("ERROR", fmt.Sprintf("Failed to persist factory state: %v", err), nil)
				}
				m.factoryTracker.LoadState(state)
			}
		}

		// Add to active pipelines
		m.mu.Lock()
		m.activePipelines[pipeline.ID()] = pipeline
		m.mu.Unlock()

		// Enqueue ready tasks (tasks with no dependencies)
		// COLLECT_SELL tasks are gated by SupplyMonitor
		for _, task := range tasks {
			if task.TaskType() == manufacturing.TaskTypeCollectSell {
				continue
			}

			if len(task.DependsOn()) == 0 {
				if err := task.MarkReady(); err == nil {
					m.taskQueue.Enqueue(task)
					if m.taskRepo != nil {
						_ = m.taskRepo.Update(ctx, task)
					}
				}
			}
		}

		activePipelineCount++
		pipelinesCreated++

		logger.Log("INFO", fmt.Sprintf("Created pipeline for %s with %d tasks", opp.Good(), len(tasks)), map[string]interface{}{
			"pipeline_id":   pipeline.ID(),
			"good":          opp.Good(),
			"sell_market":   opp.SellMarket().Symbol,
			"task_count":    len(tasks),
			"factory_count": len(factoryStates),
		})
	}

	return pipelinesCreated, nil
}

// CheckPipelineCompletion checks if a pipeline is complete and updates status
func (m *PipelineLifecycleManager) CheckPipelineCompletion(ctx context.Context, pipelineID string) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	m.mu.Lock()
	pipeline, exists := m.activePipelines[pipelineID]
	if !exists {
		m.mu.Unlock()
		return false, nil
	}

	if m.taskRepo == nil {
		m.mu.Unlock()
		return false, nil
	}

	tasks, err := m.taskRepo.FindByPipelineID(ctx, pipelineID)
	if err != nil {
		m.mu.Unlock()
		return false, err
	}

	// Count completed COLLECT_SELL tasks for the final product
	finalCollections := 0
	for _, task := range tasks {
		if task.TaskType() == manufacturing.TaskTypeCollectSell &&
			task.Good() == pipeline.ProductGood() &&
			task.Status() == manufacturing.TaskStatusCompleted {
			finalCollections++
		}
	}

	// Check if sell market is saturated
	sellMarketSaturated := m.isSellMarketSaturated(ctx, pipeline.SellMarket(), pipeline.ProductGood(), pipeline.PlayerID())

	// Determine completion conditions
	allCompleted := true
	anyFailed := false
	for _, task := range tasks {
		if task.Status() != manufacturing.TaskStatusCompleted {
			allCompleted = false
		}
		if task.Status() == manufacturing.TaskStatusFailed && !task.CanRetry() {
			anyFailed = true
		}
	}

	shouldComplete := allCompleted ||
		(finalCollections >= MaxFinalCollections) ||
		(finalCollections >= 1 && sellMarketSaturated)

	pipelineCompleted := false
	pipelineFailed := false

	if shouldComplete && finalCollections > 0 {
		completionReason := "all_tasks_done"
		if finalCollections >= MaxFinalCollections {
			completionReason = fmt.Sprintf("reached_%d_collections", MaxFinalCollections)
		} else if sellMarketSaturated {
			completionReason = "sell_market_saturated"
		}

		if err := pipeline.Complete(); err == nil {
			if m.pipelineRepo != nil {
				_ = m.pipelineRepo.Update(ctx, pipeline)
			}
			delete(m.activePipelines, pipelineID)
			pipelineCompleted = true

			netProfit := pipeline.TotalRevenue() - pipeline.TotalCost()
			metrics.RecordManufacturingPipelineCompletion(
				pipeline.PlayerID(),
				pipeline.ProductGood(),
				"completed",
				pipeline.RuntimeDuration(),
				netProfit,
			)

			logger.Log("INFO", fmt.Sprintf("Pipeline %s completed: %s", pipelineID[:8], completionReason), map[string]interface{}{
				"final_collections": finalCollections,
				"net_profit":        netProfit,
			})
		}
	} else if anyFailed {
		if err := pipeline.Fail("One or more tasks failed"); err == nil {
			if m.pipelineRepo != nil {
				_ = m.pipelineRepo.Update(ctx, pipeline)
			}
			delete(m.activePipelines, pipelineID)
			pipelineFailed = true

			metrics.RecordManufacturingPipelineCompletion(
				pipeline.PlayerID(),
				pipeline.ProductGood(),
				"failed",
				pipeline.RuntimeDuration(),
				0,
			)

			logger.Log("WARN", fmt.Sprintf("Pipeline %s failed", pipelineID[:8]), nil)
		}
	}

	m.mu.Unlock()

	// Trigger callback if pipeline finished
	if (pipelineCompleted || pipelineFailed) && m.onPipelineCompleted != nil {
		m.onPipelineCompleted(ctx)
	}

	return pipelineCompleted || pipelineFailed, nil
}

// DetectAndRecycleStuckPipelines finds and recycles stuck pipelines, returns count
func (m *PipelineLifecycleManager) DetectAndRecycleStuckPipelines(ctx context.Context, playerID int) int {
	logger := common.LoggerFromContext(ctx)

	if m.taskRepo == nil || m.pipelineRepo == nil {
		return 0
	}

	m.mu.RLock()
	pipelinesCopy := make(map[string]*manufacturing.ManufacturingPipeline, len(m.activePipelines))
	for id, p := range m.activePipelines {
		pipelinesCopy[id] = p
	}
	m.mu.RUnlock()

	now := m.clock.Now()
	stuckPipelines := make([]string, 0)

	for pipelineID, pipeline := range pipelinesCopy {
		age := now.Sub(pipeline.CreatedAt())
		if age < StuckPipelineThreshold {
			continue
		}

		tasks, err := m.taskRepo.FindByPipelineID(ctx, pipelineID)
		if err != nil {
			continue
		}

		var finalCollections, failedTasks, activeTasks int
		for _, task := range tasks {
			if task.TaskType() == manufacturing.TaskTypeCollectSell &&
				task.Good() == pipeline.ProductGood() &&
				task.Status() == manufacturing.TaskStatusCompleted {
				finalCollections++
			}
			if task.Status() == manufacturing.TaskStatusFailed {
				failedTasks++
			}
			if task.Status() == manufacturing.TaskStatusAssigned ||
				task.Status() == manufacturing.TaskStatusExecuting {
				activeTasks++
			}
		}

		if finalCollections > 0 {
			continue
		}

		isStuck := false
		stuckReason := ""

		if failedTasks >= StuckPipelineFailedTaskThreshold {
			isStuck = true
			stuckReason = fmt.Sprintf("%d failed tasks", failedTasks)
		} else if activeTasks == 0 {
			isStuck = true
			stuckReason = "no active tasks"
		}

		if isStuck {
			logger.Log("WARN", "Detected stuck pipeline", map[string]interface{}{
				"pipeline_id": pipelineID[:8],
				"good":        pipeline.ProductGood(),
				"age_minutes": int(age.Minutes()),
				"reason":      stuckReason,
			})
			stuckPipelines = append(stuckPipelines, pipelineID)
		}
	}

	// Recycle stuck pipelines
	for _, pipelineID := range stuckPipelines {
		_ = m.RecyclePipeline(ctx, pipelineID, playerID)
	}

	if len(stuckPipelines) > 0 {
		logger.Log("INFO", fmt.Sprintf("Recycled %d stuck pipelines", len(stuckPipelines)), nil)
	}

	return len(stuckPipelines)
}

// RescueReadyCollectSellTasks loads READY COLLECT_SELL tasks from DB and enqueues them
func (m *PipelineLifecycleManager) RescueReadyCollectSellTasks(ctx context.Context, playerID int) {
	if m.taskRepo == nil {
		return
	}

	logger := common.LoggerFromContext(ctx)

	// Load READY COLLECT_SELL tasks from DB
	readyTasks, err := m.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusReady)
	if err != nil {
		return
	}

	rescued := 0
	for _, task := range readyTasks {
		if task.TaskType() != manufacturing.TaskTypeCollectSell {
			continue
		}

		// Skip if sell market is saturated
		if m.isSellMarketSaturated(ctx, task.TargetMarket(), task.Good(), playerID) {
			continue
		}

		// Enqueue (will be skipped if already in queue)
		m.taskQueue.Enqueue(task)
		rescued++
	}

	if rescued > 0 {
		logger.Log("DEBUG", fmt.Sprintf("Rescued %d COLLECT_SELL tasks to queue", rescued), nil)
	}
}

// RecyclePipeline cancels a stuck pipeline and frees its slot
func (m *PipelineLifecycleManager) RecyclePipeline(ctx context.Context, pipelineID string, playerID int) error {
	logger := common.LoggerFromContext(ctx)

	// Cancel pending tasks
	tasks, err := m.taskRepo.FindByPipelineID(ctx, pipelineID)
	if err == nil {
		for _, task := range tasks {
			if task.Status() == manufacturing.TaskStatusPending ||
				task.Status() == manufacturing.TaskStatusReady {
				if err := task.Cancel("pipeline recycled"); err == nil {
					_ = m.taskRepo.Update(ctx, task)
				}
			}
			m.taskQueue.Remove(task.ID())
		}
	}

	// Remove factory states
	if m.factoryTracker != nil {
		m.factoryTracker.RemovePipeline(pipelineID)
	}

	// Mark pipeline as cancelled
	m.mu.Lock()
	pipeline, exists := m.activePipelines[pipelineID]
	if exists {
		if err := pipeline.Cancel(); err == nil {
			if m.pipelineRepo != nil {
				_ = m.pipelineRepo.Update(ctx, pipeline)
			}
			metrics.RecordManufacturingPipelineCompletion(
				pipeline.PlayerID(),
				pipeline.ProductGood(),
				"cancelled",
				pipeline.RuntimeDuration(),
				0,
			)
		}
		delete(m.activePipelines, pipelineID)
	}
	m.mu.Unlock()

	logger.Log("INFO", "Recycled stuck pipeline", map[string]interface{}{
		"pipeline_id": pipelineID[:8],
	})

	return nil
}

// isSellMarketSaturated checks if sell market has HIGH or ABUNDANT supply
func (m *PipelineLifecycleManager) isSellMarketSaturated(ctx context.Context, sellMarket string, good string, playerID int) bool {
	if m.marketRepo == nil {
		return false
	}

	marketData, err := m.marketRepo.GetMarketData(ctx, sellMarket, playerID)
	if err != nil || marketData == nil {
		return false
	}

	tradeGood := marketData.FindGood(good)
	if tradeGood == nil || tradeGood.Supply() == nil {
		return false
	}

	supply := *tradeGood.Supply()
	return supply == "HIGH" || supply == "ABUNDANT"
}

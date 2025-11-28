package manufacturing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Constants for pipeline lifecycle
const (
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

	// CheckAllPipelinesForCompletion checks all active pipelines and marks completed ones
	CheckAllPipelinesForCompletion(ctx context.Context) int

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
	demandFinder               *services.ManufacturingDemandFinder
	collectionOpportunityFinder *services.CollectionOpportunityFinder
	pipelinePlanner            *services.PipelinePlanner
	taskQueue                  *services.TaskQueue
	factoryTracker             *manufacturing.FactoryStateTracker

	// Repositories
	pipelineRepo       manufacturing.PipelineRepository
	taskRepo           manufacturing.TaskRepository
	factoryStateRepo   manufacturing.FactoryStateRepository
	marketRepo         market.MarketRepository
	shipAssignmentRepo container.ShipAssignmentRepository

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
	collectionOpportunityFinder *services.CollectionOpportunityFinder,
	pipelinePlanner *services.PipelinePlanner,
	taskQueue *services.TaskQueue,
	factoryTracker *manufacturing.FactoryStateTracker,
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
	factoryStateRepo manufacturing.FactoryStateRepository,
	marketRepo market.MarketRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	clock shared.Clock,
) *PipelineLifecycleManager {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &PipelineLifecycleManager{
		demandFinder:                demandFinder,
		collectionOpportunityFinder: collectionOpportunityFinder,
		pipelinePlanner:             pipelinePlanner,
		taskQueue:                   taskQueue,
		factoryTracker:              factoryTracker,
		pipelineRepo:                pipelineRepo,
		taskRepo:                    taskRepo,
		factoryStateRepo:            factoryStateRepo,
		marketRepo:                  marketRepo,
		shipAssignmentRepo:          shipAssignmentRepo,
		activePipelines:             make(map[string]*manufacturing.ManufacturingPipeline),
		clock:                       clock,
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

// ScanAndCreatePipelines scans for opportunities and creates new pipelines.
// FABRICATION pipelines are limited by MaxPipelines.
// COLLECTION pipelines are unlimited and scanned separately.
func (m *PipelineLifecycleManager) ScanAndCreatePipelines(ctx context.Context, params PipelineScanParams) (int, error) {
	logger := common.LoggerFromContext(ctx)

	totalCreated := 0

	// 1. Count only FABRICATION pipelines toward the limit
	fabricationCount, err := m.pipelineRepo.CountActiveFabricationPipelines(ctx, params.PlayerID)
	if err != nil {
		// Fallback to in-memory count if DB fails
		m.mu.RLock()
		fabricationCount = 0
		for _, p := range m.activePipelines {
			if p.PipelineType() == manufacturing.PipelineTypeFabrication {
				fabricationCount++
			}
		}
		m.mu.RUnlock()
	}

	// 2. Create FABRICATION pipelines if under limit
	if fabricationCount < params.MaxPipelines {
		created, err := m.scanForFabricationOpportunities(ctx, params, fabricationCount)
		if err != nil {
			logger.Log("WARN", "Failed to scan for fabrication opportunities", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			totalCreated += created
		}
	} else {
		logger.Log("DEBUG", "Max fabrication pipelines reached", map[string]interface{}{
			"active_fabrication": fabricationCount,
			"max_pipelines":      params.MaxPipelines,
		})
	}

	// 3. ALWAYS scan for collection opportunities (unlimited)
	if m.collectionOpportunityFinder != nil {
		collectionCreated := m.scanForCollectionOpportunities(ctx, params)
		totalCreated += collectionCreated
	}

	return totalCreated, nil
}

// scanForFabricationOpportunities scans for manufacturing opportunities and creates pipelines.
// These are FABRICATION pipelines that count toward max_pipelines.
func (m *PipelineLifecycleManager) scanForFabricationOpportunities(ctx context.Context, params PipelineScanParams, currentFabricationCount int) (int, error) {
	logger := common.LoggerFromContext(ctx)

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

	logger.Log("INFO", fmt.Sprintf("Found %d fabrication opportunities", len(opportunities)), nil)

	pipelinesCreated := 0
	fabricationCount := currentFabricationCount

	for _, opp := range opportunities {
		if fabricationCount >= params.MaxPipelines {
			break
		}

		if m.HasPipelineForGood(opp.Good()) {
			continue
		}

		// Create FABRICATION pipeline
		pipeline, tasks, factoryStates, err := m.pipelinePlanner.CreatePipeline(
			ctx, opp, params.SystemSymbol, params.PlayerID,
		)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to create pipeline for %s: %v", opp.Good(), err), nil)
			continue
		}

		if err := pipeline.Start(); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to start pipeline for %s: %v", opp.Good(), err), nil)
			continue
		}

		// Persist
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

		m.mu.Lock()
		m.activePipelines[pipeline.ID()] = pipeline
		m.mu.Unlock()

		// Enqueue ready tasks
		isDirectArbitrage := len(factoryStates) == 0
		for _, task := range tasks {
			if task.TaskType() == manufacturing.TaskTypeCollectSell {
				if isDirectArbitrage {
					if err := task.MarkReady(); err == nil {
						m.taskQueue.Enqueue(task)
						if m.taskRepo != nil {
							_ = m.taskRepo.Update(ctx, task)
						}
					}
				}
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

		fabricationCount++
		pipelinesCreated++

		logger.Log("INFO", fmt.Sprintf("Created FABRICATION pipeline for %s", opp.Good()), map[string]interface{}{
			"pipeline_id":   pipeline.ID(),
			"pipeline_type": "FABRICATION",
			"good":          opp.Good(),
			"sell_market":   opp.SellMarket().Symbol,
			"task_count":    len(tasks),
			"factory_count": len(factoryStates),
		})
	}

	return pipelinesCreated, nil
}

// scanForCollectionOpportunities scans for collection opportunities and creates pipelines.
// These are COLLECTION pipelines that are unlimited.
func (m *PipelineLifecycleManager) scanForCollectionOpportunities(ctx context.Context, params PipelineScanParams) int {
	logger := common.LoggerFromContext(ctx)

	config := services.DefaultCollectionFinderConfig()

	opportunities, err := m.collectionOpportunityFinder.FindOpportunities(
		ctx,
		params.SystemSymbol,
		params.PlayerID,
		config,
	)
	if err != nil {
		logger.Log("WARN", "Failed to scan for collection opportunities", map[string]interface{}{
			"error": err.Error(),
		})
		return 0
	}

	if len(opportunities) == 0 {
		return 0
	}

	logger.Log("INFO", fmt.Sprintf("Found %d collection opportunities", len(opportunities)), nil)

	pipelinesCreated := 0

	for _, opp := range opportunities {
		// Create COLLECTION pipeline
		pipeline := manufacturing.NewCollectionPipeline(
			opp.Good,
			opp.SellMarket,
			opp.SellPrice,
			params.PlayerID,
		)

		// Create single COLLECT_SELL task
		task := manufacturing.NewCollectSellTask(
			pipeline.ID(),
			params.PlayerID,
			opp.Good,
			opp.FactorySymbol, // Where to collect from
			opp.SellMarket,    // Where to sell to
			nil,               // No dependencies
		)

		// Mark immediately ready (collection opportunities are already validated)
		if err := task.MarkReady(); err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to mark collection task ready: %v", err), nil)
			continue
		}

		// Add task to pipeline BEFORE starting (AddTask only works during PLANNING)
		if err := pipeline.AddTask(task); err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to add task to pipeline: %v", err), nil)
			continue
		}

		// Start pipeline (transitions from PLANNING to EXECUTING)
		if err := pipeline.Start(); err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to start collection pipeline for %s: %v", opp.Good, err), nil)
			continue
		}

		// Persist
		if m.pipelineRepo != nil {
			if err := m.pipelineRepo.Create(ctx, pipeline); err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to persist collection pipeline: %v", err), nil)
				continue
			}

			if err := m.taskRepo.Create(ctx, task); err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to persist collection task: %v", err), nil)
				continue
			}
		}

		// Add to active pipelines and queue
		m.mu.Lock()
		m.activePipelines[pipeline.ID()] = pipeline
		m.mu.Unlock()

		m.taskQueue.Enqueue(task)

		pipelinesCreated++

		logger.Log("INFO", fmt.Sprintf("Created COLLECTION pipeline for %s", opp.Good), map[string]interface{}{
			"pipeline_id":     pipeline.ID(),
			"pipeline_type":   "COLLECTION",
			"good":            opp.Good,
			"factory":         opp.FactorySymbol,
			"sell_market":     opp.SellMarket,
			"expected_profit": opp.ExpectedProfit,
		})
	}

	return pipelinesCreated
}

// CheckPipelineCompletion checks if a pipeline is complete and updates status
func (m *PipelineLifecycleManager) CheckPipelineCompletion(ctx context.Context, pipelineID string) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	m.mu.Lock()
	pipeline, exists := m.activePipelines[pipelineID]
	if !exists {
		// Pipeline not in memory - try to load from database
		// This handles cases where pipeline was created but daemon restarted
		// or pipeline wasn't properly loaded during state recovery
		if m.pipelineRepo != nil {
			dbPipeline, err := m.pipelineRepo.FindByID(ctx, pipelineID)
			if err == nil && dbPipeline != nil && dbPipeline.Status() == manufacturing.PipelineStatusExecuting {
				pipeline = dbPipeline
				m.activePipelines[pipelineID] = pipeline
				logger.Log("DEBUG", fmt.Sprintf("Loaded pipeline %s from database for completion check", pipelineID[:8]), nil)
			} else {
				m.mu.Unlock()
				return false, nil
			}
		} else {
			m.mu.Unlock()
			return false, nil
		}
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
	// Each pipeline = one trade cycle. Complete when ANY COLLECT_SELL finishes.
	finalCollections := 0
	collectSellCount := 0
	for _, task := range tasks {
		if task.TaskType() == manufacturing.TaskTypeCollectSell {
			collectSellCount++
			goodMatch := task.Good() == pipeline.ProductGood()
			isCompleted := task.Status() == manufacturing.TaskStatusCompleted
			if goodMatch && isCompleted {
				finalCollections++
			} else {
				// Log why this COLLECT_SELL task didn't count
				logger.Log("DEBUG", fmt.Sprintf("COLLECT_SELL task %s not counted: good_match=%v (task=%s, pipeline=%s), completed=%v (status=%s)",
					task.ID()[:8], goodMatch, task.Good(), pipeline.ProductGood(), isCompleted, task.Status()), nil)
			}
		}
	}

	// Debug logging for recovery diagnostics
	logger.Log("DEBUG", fmt.Sprintf("Pipeline %s completion check: %d tasks, %d collect_sell, %d completed (good=%s)",
		pipelineID[:8], len(tasks), collectSellCount, finalCollections, pipeline.ProductGood()), nil)

	// Determine completion conditions
	anyFailed := false
	for _, task := range tasks {
		if task.Status() == manufacturing.TaskStatusFailed && !task.CanRetry() {
			anyFailed = true
		}
	}

	// Pipeline completes when ANY COLLECT_SELL task completes (one trade cycle done)
	// This allows the slot to be freed for new opportunities
	shouldComplete := finalCollections >= 1

	pipelineCompleted := false
	pipelineFailed := false

	if shouldComplete {
		completionReason := "collect_sell_completed"

		if err := pipeline.Complete(); err == nil {
			if m.pipelineRepo != nil {
				if updateErr := m.pipelineRepo.Update(ctx, pipeline); updateErr != nil {
					logger.Log("ERROR", fmt.Sprintf("Failed to persist pipeline completion: %v", updateErr), nil)
				}
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
		} else {
			logger.Log("ERROR", fmt.Sprintf("Failed to mark pipeline %s as complete: %v (status=%s)",
				pipelineID[:8], err, pipeline.Status()), nil)
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

// RescueReadyCollectSellTasks loads READY tasks from DB and enqueues them
// This rescues both COLLECT_SELL and ACQUIRE_DELIVER tasks that may have been
// left in READY status after a restart or container crash.
// STATE SYNC: Validates task state against current market conditions before enqueuing.
func (m *PipelineLifecycleManager) RescueReadyCollectSellTasks(ctx context.Context, playerID int) {
	if m.taskRepo == nil {
		return
	}

	logger := common.LoggerFromContext(ctx)

	// Load all READY tasks from DB
	readyTasks, err := m.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusReady)
	if err != nil {
		return
	}

	rescuedCollectSell := 0
	rescuedAcquireDeliver := 0
	skippedSaturated := 0
	for _, task := range readyTasks {
		switch task.TaskType() {
		case manufacturing.TaskTypeCollectSell:
			// STATE SYNC: Skip if factory supply is not ready (need HIGH/ABUNDANT)
			if !m.isFactoryOutputReady(ctx, task.FactorySymbol(), task.Good(), playerID) {
				// Reset to PENDING - SupplyMonitor will mark READY when factory is ready
				task.ResetToPending()
				_ = m.taskRepo.Update(ctx, task)
				skippedSaturated++
				continue
			}
			// STATE SYNC: Skip if sell market is saturated
			if m.isSellMarketSaturated(ctx, task.TargetMarket(), task.Good(), playerID) {
				// Reset to PENDING so it can be re-evaluated later
				task.ResetToPending()
				_ = m.taskRepo.Update(ctx, task)
				skippedSaturated++
				continue
			}
			m.taskQueue.Enqueue(task)
			rescuedCollectSell++

		case manufacturing.TaskTypeAcquireDeliver:
			// STATE SYNC: Skip if factory input is already saturated
			if m.isFactoryInputSaturated(ctx, task.FactorySymbol(), task.Good(), playerID) {
				// Reset to PENDING so it can be re-evaluated later
				task.ResetToPending()
				_ = m.taskRepo.Update(ctx, task)
				skippedSaturated++
				continue
			}
			m.taskQueue.Enqueue(task)
			rescuedAcquireDeliver++
		}
	}

	if rescuedCollectSell > 0 {
		logger.Log("DEBUG", fmt.Sprintf("Rescued %d COLLECT_SELL tasks to queue", rescuedCollectSell), nil)
	}
	if rescuedAcquireDeliver > 0 {
		logger.Log("DEBUG", fmt.Sprintf("Rescued %d ACQUIRE_DELIVER tasks to queue", rescuedAcquireDeliver), nil)
	}
	if skippedSaturated > 0 {
		logger.Log("DEBUG", fmt.Sprintf("Reset %d tasks to PENDING due to supply saturation", skippedSaturated), nil)
	}
}

// CheckAllPipelinesForCompletion checks all active pipelines and marks completed ones.
// This should be called after state recovery to handle pipelines whose tasks completed
// before a restart but weren't marked as COMPLETED.
func (m *PipelineLifecycleManager) CheckAllPipelinesForCompletion(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	m.mu.RLock()
	pipelineIDs := make([]string, 0, len(m.activePipelines))
	for id := range m.activePipelines {
		pipelineIDs = append(pipelineIDs, id)
	}
	m.mu.RUnlock()

	completed := 0
	for _, pipelineID := range pipelineIDs {
		wasCompleted, err := m.CheckPipelineCompletion(ctx, pipelineID)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to check pipeline %s completion: %v", pipelineID[:8], err), nil)
			continue
		}
		if wasCompleted {
			completed++
		}
	}

	if completed > 0 {
		logger.Log("INFO", fmt.Sprintf("Completed %d pipelines during state recovery check", completed), nil)
	}

	return completed
}

// RecyclePipeline cancels a stuck pipeline and frees its slot
func (m *PipelineLifecycleManager) RecyclePipeline(ctx context.Context, pipelineID string, playerID int) error {
	logger := common.LoggerFromContext(ctx)

	// Cancel all incomplete tasks (PENDING, READY, ASSIGNED)
	tasks, err := m.taskRepo.FindByPipelineID(ctx, pipelineID)
	if err == nil {
		for _, task := range tasks {
			if task.Status() == manufacturing.TaskStatusPending ||
				task.Status() == manufacturing.TaskStatusReady ||
				task.Status() == manufacturing.TaskStatusAssigned {
				// Release ship assignment if task was assigned
				if task.AssignedShip() != "" && m.shipAssignmentRepo != nil {
					_ = m.shipAssignmentRepo.Release(ctx, task.AssignedShip(), playerID, "pipeline_recycled")
				}
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

// isFactoryInputSaturated checks if factory already has HIGH or ABUNDANT supply of an input good.
// This is used during task rescue to avoid re-queuing ACQUIRE_DELIVER tasks for inputs
// that no longer need replenishment.
func (m *PipelineLifecycleManager) isFactoryInputSaturated(ctx context.Context, factorySymbol string, inputGood string, playerID int) bool {
	if m.marketRepo == nil {
		return false
	}

	marketData, err := m.marketRepo.GetMarketData(ctx, factorySymbol, playerID)
	if err != nil || marketData == nil {
		return false // Can't check, assume not saturated
	}

	tradeGood := marketData.FindGood(inputGood)
	if tradeGood == nil || tradeGood.Supply() == nil {
		return false
	}

	// For factory inputs (IMPORT goods), HIGH/ABUNDANT means we don't need more deliveries
	supply := *tradeGood.Supply()
	return supply == "HIGH" || supply == "ABUNDANT"
}

// isFactoryOutputReady checks if factory has HIGH/ABUNDANT supply of output good
// Returns true if ready for collection, false otherwise
func (m *PipelineLifecycleManager) isFactoryOutputReady(ctx context.Context, factorySymbol string, outputGood string, playerID int) bool {
	if m.marketRepo == nil {
		return true // Can't check, assume ready (optimistic)
	}

	marketData, err := m.marketRepo.GetMarketData(ctx, factorySymbol, playerID)
	if err != nil || marketData == nil {
		return true // Can't check, assume ready
	}

	tradeGood := marketData.FindGood(outputGood)
	if tradeGood == nil || tradeGood.Supply() == nil {
		return true // Can't check supply level
	}

	// For factory outputs (EXPORT goods), need HIGH/ABUNDANT for collection
	supply := *tradeGood.Supply()
	return supply == "HIGH" || supply == "ABUNDANT"
}

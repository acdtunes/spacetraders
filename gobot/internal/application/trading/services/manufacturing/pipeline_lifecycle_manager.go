package manufacturing

import (
	"context"
	"fmt"
	"time"

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

// PipelineLifecycleManager implements PipelineManager by delegating to focused services.
// It coordinates:
// - ActivePipelineRegistry for pipeline tracking
// - PipelineCompletionChecker for completion detection
// - PipelineRecycler for stuck pipeline handling
// - TaskRescuer for task rescue
type PipelineLifecycleManager struct {
	// Services (from plan)
	demandFinder                *services.ManufacturingDemandFinder
	collectionOpportunityFinder *services.CollectionOpportunityFinder
	pipelinePlanner             *services.PipelinePlanner
	taskQueue                   *services.TaskQueue
	factoryTracker              *manufacturing.FactoryStateTracker

	// Repositories
	pipelineRepo       manufacturing.PipelineRepository
	taskRepo           manufacturing.TaskRepository
	factoryStateRepo   manufacturing.FactoryStateRepository
	marketRepo         market.MarketRepository
	shipAssignmentRepo container.ShipAssignmentRepository

	// Focused services (from coordinator)
	registry          *ActivePipelineRegistry
	completionChecker *PipelineCompletionChecker
	recycler          *PipelineRecycler
	taskRescuer       *TaskRescuer

	// Clock for time operations
	clock shared.Clock

	// Callback when pipeline completes
	onPipelineCompleted func(ctx context.Context)
}

// NewPipelineLifecycleManager creates a new pipeline lifecycle manager with all dependencies.
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
	registry *ActivePipelineRegistry,
	completionChecker *PipelineCompletionChecker,
	recycler *PipelineRecycler,
	taskRescuer *TaskRescuer,
	clock shared.Clock,
	onPipelineCompleted func(ctx context.Context),
) *PipelineLifecycleManager {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	m := &PipelineLifecycleManager{
		demandFinder:                demandFinder,
		collectionOpportunityFinder: collectionOpportunityFinder,
		pipelinePlanner:             pipelinePlanner,
		taskQueue:                   taskQueue,
		factoryTracker:              factoryTracker,
		pipelineRepo:                pipelineRepo,
		taskRepo:                    taskRepo,
		factoryStateRepo:            factoryStateRepo,
		marketRepo:                  marketRepo,
		clock:                       clock,
		registry:                    registry,
		completionChecker:           completionChecker,
		recycler:                    recycler,
		taskRescuer:                 taskRescuer,
		onPipelineCompleted:         onPipelineCompleted,
	}

	// Wire the callback to the completion checker
	if m.completionChecker != nil && onPipelineCompleted != nil {
		m.completionChecker.SetCompletedCallback(onPipelineCompleted)
	}

	return m
}

// SetPipelineCompletedCallback sets the callback for pipeline completion
func (m *PipelineLifecycleManager) SetPipelineCompletedCallback(callback func(ctx context.Context)) {
	m.onPipelineCompleted = callback
	if m.completionChecker != nil {
		m.completionChecker.SetCompletedCallback(callback)
	}
}

// GetActivePipelines returns all active pipelines.
// Delegates to registry if available.
func (m *PipelineLifecycleManager) GetActivePipelines() map[string]*manufacturing.ManufacturingPipeline {
	if m.registry != nil {
		return m.registry.GetAll()
	}
	return make(map[string]*manufacturing.ManufacturingPipeline)
}

// SetActivePipelines sets the active pipelines map.
// Delegates to registry if available.
func (m *PipelineLifecycleManager) SetActivePipelines(pipelines map[string]*manufacturing.ManufacturingPipeline) {
	if m.registry != nil {
		m.registry.SetAll(pipelines)
	}
}

// HasPipelineForGood checks if we already have an active pipeline for this good.
// Delegates to registry if available.
func (m *PipelineLifecycleManager) HasPipelineForGood(good string) bool {
	if m.registry != nil {
		return m.registry.HasPipelineForGood(good)
	}
	return false
}

// AddActivePipeline adds a pipeline to active pipelines (for state recovery).
// Delegates to registry if available.
func (m *PipelineLifecycleManager) AddActivePipeline(id string, pipeline *manufacturing.ManufacturingPipeline) {
	if m.registry != nil {
		m.registry.Register(pipeline)
	}
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
		if m.registry != nil {
			fabricationCount = 0
			for _, p := range m.registry.GetAll() {
				if p.PipelineType() == manufacturing.PipelineTypeFabrication {
					fabricationCount++
				}
			}
		}
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

		if m.registry != nil {
			m.registry.Register(pipeline)
		}

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
		if m.registry != nil {
			m.registry.Register(pipeline)
		}

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

// CheckPipelineCompletion checks if a pipeline is complete and updates status.
// Delegates to PipelineCompletionChecker if available.
func (m *PipelineLifecycleManager) CheckPipelineCompletion(ctx context.Context, pipelineID string) (bool, error) {
	if m.completionChecker != nil {
		return m.completionChecker.CheckPipelineCompletion(ctx, pipelineID)
	}
	return false, nil
}

// DetectAndRecycleStuckPipelines finds and recycles stuck pipelines, returns count.
// Delegates to PipelineRecycler if available.
func (m *PipelineLifecycleManager) DetectAndRecycleStuckPipelines(ctx context.Context, playerID int) int {
	if m.recycler != nil {
		return m.recycler.DetectAndRecycleStuckPipelines(ctx, playerID)
	}
	return 0
}

// RescueReadyCollectSellTasks loads READY tasks from DB and enqueues them.
// Delegates to TaskRescuer if available.
func (m *PipelineLifecycleManager) RescueReadyCollectSellTasks(ctx context.Context, playerID int) {
	if m.taskRescuer != nil {
		m.taskRescuer.RescueReadyTasks(ctx, playerID)
	}
}

// CheckAllPipelinesForCompletion checks all active pipelines and marks completed ones.
// Delegates to PipelineCompletionChecker if available.
func (m *PipelineLifecycleManager) CheckAllPipelinesForCompletion(ctx context.Context) int {
	if m.completionChecker != nil {
		return m.completionChecker.CheckAllPipelinesForCompletion(ctx)
	}
	return 0
}

// RecyclePipeline cancels a stuck pipeline and frees its slot.
// Delegates to PipelineRecycler if available.
func (m *PipelineLifecycleManager) RecyclePipeline(ctx context.Context, pipelineID string, playerID int) error {
	if m.recycler != nil {
		return m.recycler.RecyclePipeline(ctx, pipelineID, playerID)
	}
	return nil
}

// GetRegistry returns the active pipeline registry.
func (m *PipelineLifecycleManager) GetRegistry() *ActivePipelineRegistry {
	return m.registry
}

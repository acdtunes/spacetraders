package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/trading/services/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// RunParallelManufacturingCoordinatorCommand orchestrates parallel task-based manufacturing
type RunParallelManufacturingCoordinatorCommand struct {
	SystemSymbol       string        // System to scan for opportunities
	PlayerID           int           // Player identifier
	ContainerID        string        // Container ID for this coordinator
	MinPurchasePrice   int           // Minimum purchase price threshold (default 1000)
	MaxConcurrentTasks int           // Maximum concurrent task executions (default 10)
	MaxPipelines       int           // Maximum active pipelines (default 3)
	SupplyPollInterval time.Duration // How often to poll factory supply (default 30s)
	Strategy           string        // Acquisition strategy: prefer-buy, prefer-fabricate, smart (default: prefer-fabricate)
}

// RunParallelManufacturingCoordinatorResponse is never returned (infinite loop)
type RunParallelManufacturingCoordinatorResponse struct{}

// ContainerRemover is a minimal interface for cleaning up orphaned containers
type ContainerRemover interface {
	Remove(ctx context.Context, containerID string, playerID int) error
}

// RunParallelManufacturingCoordinatorHandler orchestrates parallel manufacturing using task-based pipelines.
//
// This handler is a thin orchestrator that delegates to specialized services:
// - PipelineLifecycleManager: Creates, completes, and recycles pipelines
// - StateRecoveryManager: Recovers state from database after restart
// - TaskAssignmentManager: Assigns ready tasks to idle ships
// - WorkerLifecycleManager: Manages worker container lifecycle
// - OrphanedCargoHandler: Handles ships with cargo from interrupted operations
// - FactoryStateManager: Updates factory state and task dependencies
type RunParallelManufacturingCoordinatorHandler struct {
	// Planning services
	demandFinder               *services.ManufacturingDemandFinder
	collectionOpportunityFinder *services.CollectionOpportunityFinder
	pipelinePlanner            *services.PipelinePlanner
	taskQueue                  *services.TaskQueue
	factoryTracker             *manufacturing.FactoryStateTracker

	// Repositories
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	pipelineRepo       manufacturing.PipelineRepository
	taskRepo           manufacturing.TaskRepository
	factoryStateRepo   manufacturing.FactoryStateRepository
	marketRepo         market.MarketRepository
	containerRemover   ContainerRemover

	// Infrastructure
	mediator         common.Mediator
	daemonClient     daemon.DaemonClient
	clock            shared.Clock
	waypointProvider system.IWaypointProvider

	// Coordinator services (created per Handle call)
	pipelineManager  mfgServices.PipelineManager
	stateRecoverer   mfgServices.StateRecoverer
	taskAssigner     mfgServices.TaskAssigner
	workerManager    mfgServices.WorkerManager
	orphanedHandler  mfgServices.OrphanedCargoManager
	factoryManager   mfgServices.FactoryManager

	// Runtime state
	workerCompletionChan chan string   // Worker container completion signals
	taskReadyChan        chan struct{} // Notified when SupplyMonitor marks tasks ready
}

// NewRunParallelManufacturingCoordinatorHandler creates a new coordinator handler
func NewRunParallelManufacturingCoordinatorHandler(
	demandFinder *services.ManufacturingDemandFinder,
	collectionOpportunityFinder *services.CollectionOpportunityFinder,
	pipelinePlanner *services.PipelinePlanner,
	taskQueue *services.TaskQueue,
	factoryTracker *manufacturing.FactoryStateTracker,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
	factoryStateRepo manufacturing.FactoryStateRepository,
	marketRepo market.MarketRepository,
	containerRemover ContainerRemover,
	mediator common.Mediator,
	daemonClient daemon.DaemonClient,
	clock shared.Clock,
	waypointProvider system.IWaypointProvider,
) *RunParallelManufacturingCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &RunParallelManufacturingCoordinatorHandler{
		demandFinder:               demandFinder,
		collectionOpportunityFinder: collectionOpportunityFinder,
		pipelinePlanner:            pipelinePlanner,
		taskQueue:                  taskQueue,
		factoryTracker:             factoryTracker,
		shipRepo:                   shipRepo,
		shipAssignmentRepo:         shipAssignmentRepo,
		pipelineRepo:               pipelineRepo,
		taskRepo:                   taskRepo,
		factoryStateRepo:           factoryStateRepo,
		marketRepo:                 marketRepo,
		containerRemover:           containerRemover,
		mediator:                   mediator,
		daemonClient:               daemonClient,
		clock:                      clock,
		waypointProvider:           waypointProvider,
		workerCompletionChan:       make(chan string, 100),
		taskReadyChan:              make(chan struct{}, 10),
	}
}

// Handle executes the coordinator command
func (h *RunParallelManufacturingCoordinatorHandler) Handle(
	ctx context.Context,
	request common.Request,
) (common.Response, error) {
	cmd, ok := request.(*RunParallelManufacturingCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)

	// Apply defaults
	config := h.applyDefaults(cmd)

	// Configure acquisition strategy
	h.demandFinder.SetStrategy(config.strategy)

	logger.Log("INFO", "Starting parallel manufacturing coordinator", map[string]interface{}{
		"system":               cmd.SystemSymbol,
		"min_purchase_price":   config.minPurchasePrice,
		"max_concurrent_tasks": config.maxConcurrentTasks,
		"max_pipelines":        config.maxPipelines,
		"supply_poll_interval": config.supplyPollInterval.String(),
		"strategy":             config.strategy,
	})

	// Initialize services for this run
	h.initializeServices(cmd)

	// Recover state from database (handles daemon restart)
	if err := h.recoverState(ctx, cmd.PlayerID); err != nil {
		logger.Log("WARN", fmt.Sprintf("State recovery warning: %v", err), nil)
	}

	// Start supply monitor
	h.startSupplyMonitor(ctx, cmd.PlayerID, config.supplyPollInterval)

	// Set up tickers
	opportunityScanTicker := time.NewTicker(3 * time.Minute)
	stuckPipelineTicker := time.NewTicker(5 * time.Minute)
	idleShipTicker := time.NewTicker(10 * time.Second)
	pipelineCompletionTicker := time.NewTicker(30 * time.Second) // Safety net for lost completion signals
	defer opportunityScanTicker.Stop()
	defer stuckPipelineTicker.Stop()
	defer idleShipTicker.Stop()
	defer pipelineCompletionTicker.Stop()

	// Initial scan and task assignment
	h.pipelineManager.ScanAndCreatePipelines(ctx, mfgServices.PipelineScanParams{
		SystemSymbol:     cmd.SystemSymbol,
		PlayerID:         cmd.PlayerID,
		MinPurchasePrice: config.minPurchasePrice,
		MaxPipelines:     config.maxPipelines,
	})
	h.taskAssigner.AssignTasks(ctx, mfgServices.AssignParams{
		PlayerID:           cmd.PlayerID,
		MaxConcurrentTasks: config.maxConcurrentTasks,
	})

	// Main coordination loop
	for {
		select {
		case <-opportunityScanTicker.C:
			h.pipelineManager.ScanAndCreatePipelines(ctx, mfgServices.PipelineScanParams{
				SystemSymbol:     cmd.SystemSymbol,
				PlayerID:         cmd.PlayerID,
				MinPurchasePrice: config.minPurchasePrice,
				MaxPipelines:     config.maxPipelines,
			})

		case <-stuckPipelineTicker.C:
			recycled := h.pipelineManager.DetectAndRecycleStuckPipelines(ctx, cmd.PlayerID)
			if recycled > 0 {
				h.pipelineManager.ScanAndCreatePipelines(ctx, mfgServices.PipelineScanParams{
					SystemSymbol:     cmd.SystemSymbol,
					PlayerID:         cmd.PlayerID,
					MinPurchasePrice: config.minPurchasePrice,
					MaxPipelines:     config.maxPipelines,
				})
			}

		case <-idleShipTicker.C:
			h.pipelineManager.RescueReadyCollectSellTasks(ctx, cmd.PlayerID)
			h.taskAssigner.AssignTasks(ctx, mfgServices.AssignParams{
				PlayerID:           cmd.PlayerID,
				MaxConcurrentTasks: config.maxConcurrentTasks,
			})

		case <-pipelineCompletionTicker.C:
			// Safety net: Check for pipelines with completed tasks that weren't properly marked complete
			// This handles lost completion signals (non-blocking channel send when coordinator is busy)
			completed := h.pipelineManager.CheckAllPipelinesForCompletion(ctx)
			if completed > 0 {
				logger.Log("INFO", fmt.Sprintf("Safety net: completed %d pipelines with lost signals", completed), nil)
				// Rescan for new opportunities since pipelines completed
				h.pipelineManager.ScanAndCreatePipelines(ctx, mfgServices.PipelineScanParams{
					SystemSymbol:     cmd.SystemSymbol,
					PlayerID:         cmd.PlayerID,
					MinPurchasePrice: config.minPurchasePrice,
					MaxPipelines:     config.maxPipelines,
				})
			}

		case <-h.taskReadyChan:
			h.taskAssigner.AssignTasks(ctx, mfgServices.AssignParams{
				PlayerID:           cmd.PlayerID,
				MaxConcurrentTasks: config.maxConcurrentTasks,
			})

		case shipSymbol := <-h.workerCompletionChan:
			h.handleWorkerCompletion(ctx, cmd, shipSymbol, config.maxConcurrentTasks)

		case <-ctx.Done():
			logger.Log("INFO", "Parallel manufacturing coordinator shutting down", nil)
			return &RunParallelManufacturingCoordinatorResponse{}, nil
		}
	}
}

// coordinatorConfig holds applied configuration
type coordinatorConfig struct {
	minPurchasePrice   int
	maxConcurrentTasks int
	maxPipelines       int
	supplyPollInterval time.Duration
	strategy           string
}

// applyDefaults applies default values to command parameters
func (h *RunParallelManufacturingCoordinatorHandler) applyDefaults(cmd *RunParallelManufacturingCoordinatorCommand) coordinatorConfig {
	config := coordinatorConfig{
		minPurchasePrice:   cmd.MinPurchasePrice,
		maxConcurrentTasks: cmd.MaxConcurrentTasks,
		maxPipelines:       cmd.MaxPipelines,
		supplyPollInterval: cmd.SupplyPollInterval,
		strategy:           cmd.Strategy,
	}

	if config.minPurchasePrice <= 0 {
		config.minPurchasePrice = 1000
	}
	if config.maxConcurrentTasks <= 0 {
		config.maxConcurrentTasks = 10
	}
	if config.maxPipelines <= 0 {
		config.maxPipelines = 3
	}
	if config.supplyPollInterval <= 0 {
		config.supplyPollInterval = 30 * time.Second
	}
	if config.strategy == "" {
		config.strategy = "prefer-fabricate"
	}

	return config
}

// initializeServices creates and wires up all coordinator services
func (h *RunParallelManufacturingCoordinatorHandler) initializeServices(cmd *RunParallelManufacturingCoordinatorCommand) {
	// Create services
	h.pipelineManager = mfgServices.NewPipelineLifecycleManager(
		h.demandFinder,
		h.collectionOpportunityFinder,
		h.pipelinePlanner,
		h.taskQueue,
		h.factoryTracker,
		h.pipelineRepo,
		h.taskRepo,
		h.factoryStateRepo,
		h.marketRepo,
		h.shipAssignmentRepo,
		h.clock,
	)

	h.stateRecoverer = mfgServices.NewStateRecoveryManager(
		h.pipelineRepo,
		h.taskRepo,
		h.factoryStateRepo,
		h.shipAssignmentRepo,
		h.factoryTracker,
		h.taskQueue,
	)

	taskAssignmentMgr := mfgServices.NewTaskAssignmentManager(
		h.taskRepo,
		h.shipRepo,
		h.shipAssignmentRepo,
		h.marketRepo,
		h.waypointProvider,
		h.taskQueue,
	)
	h.taskAssigner = taskAssignmentMgr

	workerLifecycleMgr := mfgServices.NewWorkerLifecycleManager(
		h.taskRepo,
		h.shipAssignmentRepo,
		h.daemonClient,
		h.containerRemover,
		h.taskQueue,
		h.clock,
	)
	h.workerManager = workerLifecycleMgr

	orphanedHandler := mfgServices.NewOrphanedCargoHandler(
		h.taskRepo,
		h.marketRepo,
	)
	h.orphanedHandler = orphanedHandler

	h.factoryManager = mfgServices.NewFactoryStateManager(
		h.taskRepo,
		h.factoryStateRepo,
		h.factoryTracker,
		h.taskQueue,
	)

	// Wire up dependencies (circular references via setters)
	taskAssignmentMgr.SetWorkerManager(h.workerManager)
	taskAssignmentMgr.SetOrphanedHandler(h.orphanedHandler)
	taskAssignmentMgr.SetPipelineManager(h.pipelineManager)
	taskAssignmentMgr.SetActivePipelinesGetter(h.pipelineManager.GetActivePipelines)

	workerLifecycleMgr.SetTaskAssigner(h.taskAssigner)
	workerLifecycleMgr.SetFactoryManager(h.factoryManager)
	workerLifecycleMgr.SetPipelineManager(h.pipelineManager)
	workerLifecycleMgr.SetWorkerCompletionChannel(h.workerCompletionChan)

	orphanedHandler.SetWorkerManager(h.workerManager)
	orphanedHandler.SetTaskAssigner(h.taskAssigner)
	orphanedHandler.SetActivePipelinesGetter(h.pipelineManager.GetActivePipelines)
	orphanedHandler.SetMediator(h.mediator)
}

// recoverState recovers coordinator state from database
func (h *RunParallelManufacturingCoordinatorHandler) recoverState(ctx context.Context, playerID int) error {
	result, err := h.stateRecoverer.RecoverState(ctx, playerID)
	if err != nil {
		return err
	}

	// Load recovered pipelines into pipeline manager
	if pipelineMgr, ok := h.pipelineManager.(*mfgServices.PipelineLifecycleManager); ok {
		for id, pipeline := range result.ActivePipelines {
			pipelineMgr.AddActivePipeline(id, pipeline)
		}
	}

	// Check if any recovered pipelines are already complete (tasks finished before restart)
	h.pipelineManager.CheckAllPipelinesForCompletion(ctx)

	return nil
}

// startSupplyMonitor starts the supply monitor in background
func (h *RunParallelManufacturingCoordinatorHandler) startSupplyMonitor(ctx context.Context, playerID int, pollInterval time.Duration) {
	logger := common.LoggerFromContext(ctx)

	if h.marketRepo == nil {
		return
	}

	supplyMonitor := services.NewSupplyMonitor(
		h.marketRepo,
		h.factoryTracker,
		h.factoryStateRepo,
		h.pipelineRepo,
		h.taskQueue,
		h.taskRepo,
		h.pipelinePlanner.MarketLocator(),
		pollInterval,
		playerID,
	)
	supplyMonitor.SetTaskReadyChannel(h.taskReadyChan)
	go supplyMonitor.Run(ctx)

	logger.Log("INFO", "Supply monitor started", map[string]interface{}{
		"poll_interval": pollInterval.String(),
	})
}

// handleWorkerCompletion handles when a worker container completes
func (h *RunParallelManufacturingCoordinatorHandler) handleWorkerCompletion(
	ctx context.Context,
	cmd *RunParallelManufacturingCoordinatorCommand,
	shipSymbol string,
	maxConcurrentTasks int,
) {
	logger := common.LoggerFromContext(ctx)

	// Get completion details from worker manager
	completion, err := h.workerManager.HandleWorkerCompletion(ctx, shipSymbol)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to handle worker completion: %v", err), nil)
		return
	}

	if completion == nil {
		return
	}

	// Handle success or failure
	if completion.Success {
		logger.Log("INFO", fmt.Sprintf("Task %s completed successfully", completion.TaskID[:8]), nil)

		// Update factory state if this was a delivery task
		if err := h.factoryManager.UpdateFactoryStateOnDelivery(ctx, completion.TaskID, completion.ShipSymbol, completion.PipelineID); err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to update factory state: %v", err), nil)
		}

		// Update dependent tasks
		if err := h.factoryManager.UpdateDependentTasks(ctx, completion.TaskID, completion.PipelineID); err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to update dependent tasks: %v", err), nil)
		}

		// Check pipeline completion
		if completion.PipelineID != "" {
			completed, _ := h.pipelineManager.CheckPipelineCompletion(ctx, completion.PipelineID)
			if completed {
				// Pipeline completed - rescan for new opportunities
				h.pipelineManager.ScanAndCreatePipelines(ctx, mfgServices.PipelineScanParams{
					SystemSymbol:     cmd.SystemSymbol,
					PlayerID:         cmd.PlayerID,
					MinPurchasePrice: 1000, // Use default
					MaxPipelines:     3,    // Use default
				})
			}
		}
	} else {
		logger.Log("ERROR", fmt.Sprintf("Task %s failed: %v", completion.TaskID[:8], completion.Error), nil)
		if err := h.workerManager.HandleTaskFailure(ctx, *completion); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to handle task failure: %v", err), nil)
		}
	}

	// Assign idle ships to ready tasks
	h.taskAssigner.AssignTasks(ctx, mfgServices.AssignParams{
		PlayerID:           cmd.PlayerID,
		MaxConcurrentTasks: maxConcurrentTasks,
	})
}

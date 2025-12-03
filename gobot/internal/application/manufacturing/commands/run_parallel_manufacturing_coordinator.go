package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services/manufacturing"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// RunParallelManufacturingCoordinatorCommand orchestrates parallel task-based manufacturing
type RunParallelManufacturingCoordinatorCommand struct {
	SystemSymbol           string        // System to scan for opportunities
	PlayerID               int           // Player identifier
	ContainerID            string        // Container ID for this coordinator
	MinPurchasePrice       int           // Minimum purchase price threshold (default 1000)
	MaxConcurrentTasks     int           // Maximum concurrent task executions (default 10)
	MaxPipelines           int           // Maximum active fabrication pipelines (default 3)
	MaxCollectionPipelines int           // Maximum active collection pipelines (0 = unlimited)
	SupplyPollInterval     time.Duration // How often to poll factory supply (default 30s)
	Strategy               string        // Acquisition strategy: prefer-buy, prefer-fabricate, smart (default: prefer-fabricate)
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
	taskQueue                  services.ManufacturingTaskQueue
	factoryTracker             *manufacturing.FactoryStateTracker

	// Repositories
	shipRepo         navigation.ShipRepository
	pipelineRepo     manufacturing.PipelineRepository
	taskRepo         manufacturing.TaskRepository
	factoryStateRepo manufacturing.FactoryStateRepository
	marketRepo       market.MarketRepository
	storageOpRepo    storage.StorageOperationRepository
	containerRemover ContainerRemover

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

	// Storage recovery (optional - nil if no storage operations)
	storageRecovery *storageApp.StorageRecoveryService

	// Runtime state
	workerCompletionChan chan string   // Worker container completion signals
	taskReadyChan        chan struct{} // Notified when SupplyMonitor marks tasks ready
}

// NewRunParallelManufacturingCoordinatorHandler creates a new coordinator handler
func NewRunParallelManufacturingCoordinatorHandler(
	demandFinder *services.ManufacturingDemandFinder,
	collectionOpportunityFinder *services.CollectionOpportunityFinder,
	pipelinePlanner *services.PipelinePlanner,
	taskQueue services.ManufacturingTaskQueue,
	factoryTracker *manufacturing.FactoryStateTracker,
	shipRepo navigation.ShipRepository,
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
		demandFinder:                demandFinder,
		collectionOpportunityFinder: collectionOpportunityFinder,
		pipelinePlanner:             pipelinePlanner,
		taskQueue:                   taskQueue,
		factoryTracker:              factoryTracker,
		shipRepo:                    shipRepo,
		pipelineRepo:                pipelineRepo,
		taskRepo:                    taskRepo,
		factoryStateRepo:            factoryStateRepo,
		marketRepo:                  marketRepo,
		containerRemover:            containerRemover,
		mediator:                    mediator,
		daemonClient:                daemonClient,
		clock:                       clock,
		waypointProvider:            waypointProvider,
		workerCompletionChan:        make(chan string, 100),
		taskReadyChan:               make(chan struct{}, 10),
	}
}

// SetStorageRecoveryService sets the optional storage recovery service.
// This enables recovery of storage ship cargo state on daemon restart.
func (h *RunParallelManufacturingCoordinatorHandler) SetStorageRecoveryService(service *storageApp.StorageRecoveryService) {
	h.storageRecovery = service
}

// SetStorageOperationRepository sets the optional storage operation repository.
// This enables the PipelinePlanner to create STORAGE_ACQUIRE_DELIVER tasks when
// factory inputs are available from running storage operations (e.g., gas siphoning).
func (h *RunParallelManufacturingCoordinatorHandler) SetStorageOperationRepository(repo storage.StorageOperationRepository) {
	h.storageOpRepo = repo
	// Propagate to the PipelinePlanner so it can look up storage operations
	// when deciding between ACQUIRE_DELIVER and STORAGE_ACQUIRE_DELIVER tasks
	if h.pipelinePlanner != nil {
		h.pipelinePlanner.SetStorageOperationRepository(repo)
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
		SystemSymbol:           cmd.SystemSymbol,
		PlayerID:               cmd.PlayerID,
		MinPurchasePrice:       config.minPurchasePrice,
		MaxPipelines:           config.maxPipelines,
		MaxCollectionPipelines: config.maxCollectionPipelines,
	})
	h.taskAssigner.AssignTasks(ctx, mfgServices.AssignParams{
		PlayerID:           cmd.PlayerID,
		MaxConcurrentTasks: config.maxConcurrentTasks,
		CoordinatorID:      cmd.ContainerID,
	})

	// Main coordination loop
	for {
		select {
		case <-opportunityScanTicker.C:
			h.pipelineManager.ScanAndCreatePipelines(ctx, mfgServices.PipelineScanParams{
				SystemSymbol:           cmd.SystemSymbol,
				PlayerID:               cmd.PlayerID,
				MinPurchasePrice:       config.minPurchasePrice,
				MaxPipelines:           config.maxPipelines,
				MaxCollectionPipelines: config.maxCollectionPipelines,
			})

		case <-stuckPipelineTicker.C:
			recycled := h.pipelineManager.DetectAndRecycleStuckPipelines(ctx, cmd.PlayerID)
			if recycled > 0 {
				h.pipelineManager.ScanAndCreatePipelines(ctx, mfgServices.PipelineScanParams{
					SystemSymbol:           cmd.SystemSymbol,
					PlayerID:               cmd.PlayerID,
					MinPurchasePrice:       config.minPurchasePrice,
					MaxPipelines:           config.maxPipelines,
					MaxCollectionPipelines: config.maxCollectionPipelines,
				})
			}

		case <-idleShipTicker.C:
			h.pipelineManager.RescueReadyCollectSellTasks(ctx, cmd.PlayerID)
			h.taskAssigner.AssignTasks(ctx, mfgServices.AssignParams{
				PlayerID:           cmd.PlayerID,
				MaxConcurrentTasks: config.maxConcurrentTasks,
				CoordinatorID:      cmd.ContainerID,
			})

		case <-pipelineCompletionTicker.C:
			// Safety net: Check for pipelines with completed tasks that weren't properly marked complete
			// This handles lost completion signals (non-blocking channel send when coordinator is busy)
			completed := h.pipelineManager.CheckAllPipelinesForCompletion(ctx)
			if completed > 0 {
				logger.Log("INFO", fmt.Sprintf("Safety net: completed %d pipelines with lost signals", completed), nil)
				// Rescan for new opportunities since pipelines completed
				h.pipelineManager.ScanAndCreatePipelines(ctx, mfgServices.PipelineScanParams{
					SystemSymbol:           cmd.SystemSymbol,
					PlayerID:               cmd.PlayerID,
					MinPurchasePrice:       config.minPurchasePrice,
					MaxPipelines:           config.maxPipelines,
					MaxCollectionPipelines: config.maxCollectionPipelines,
				})
			}

		case <-h.taskReadyChan:
			h.taskAssigner.AssignTasks(ctx, mfgServices.AssignParams{
				PlayerID:           cmd.PlayerID,
				MaxConcurrentTasks: config.maxConcurrentTasks,
				CoordinatorID:      cmd.ContainerID,
			})

		case shipSymbol := <-h.workerCompletionChan:
			h.handleWorkerCompletion(ctx, cmd, shipSymbol, config)

		case <-ctx.Done():
			logger.Log("INFO", "Parallel manufacturing coordinator shutting down", nil)
			return &RunParallelManufacturingCoordinatorResponse{}, nil
		}
	}
}

// coordinatorConfig holds applied configuration
type coordinatorConfig struct {
	minPurchasePrice       int
	maxConcurrentTasks     int
	maxPipelines           int
	maxCollectionPipelines int // 0 = unlimited
	supplyPollInterval     time.Duration
	strategy               string
}

// applyDefaults applies default values to command parameters
func (h *RunParallelManufacturingCoordinatorHandler) applyDefaults(cmd *RunParallelManufacturingCoordinatorCommand) coordinatorConfig {
	config := coordinatorConfig{
		minPurchasePrice:       cmd.MinPurchasePrice,
		maxConcurrentTasks:     cmd.MaxConcurrentTasks,
		maxPipelines:           cmd.MaxPipelines,
		maxCollectionPipelines: cmd.MaxCollectionPipelines, // 0 = unlimited (no default applied)
		supplyPollInterval:     cmd.SupplyPollInterval,
		strategy:               cmd.Strategy,
	}

	if config.minPurchasePrice <= 0 {
		config.minPurchasePrice = 1000
	}
	if config.maxConcurrentTasks <= 0 {
		config.maxConcurrentTasks = 10
	}
	if config.maxPipelines < 0 {
		config.maxPipelines = 3 // default when unset (-1)
	}
	// Note: maxPipelines = 0 means DISABLED (no fabrication pipelines)
	// Note: maxCollectionPipelines defaults to 0 (unlimited) - no default applied
	if config.supplyPollInterval <= 0 {
		config.supplyPollInterval = 30 * time.Second
	}
	if config.strategy == "" {
		config.strategy = "prefer-fabricate"
	}

	return config
}

// initializeServices creates and wires up all coordinator services using constructor injection
func (h *RunParallelManufacturingCoordinatorHandler) initializeServices(cmd *RunParallelManufacturingCoordinatorCommand) {
	// 1. Create shared state (no dependencies)
	registry := mfgServices.NewActivePipelineRegistry()
	assignmentTracker := mfgServices.NewAssignmentTracker()
	readinessSpec := manufacturing.NewTaskReadinessSpecification()
	reservationPolicy := manufacturing.NewWorkerReservationPolicy()

	// 2. Create focused services
	shipSelector := mfgServices.NewShipSelector(h.waypointProvider)
	conditionChecker := mfgServices.NewMarketConditionChecker(h.marketRepo, readinessSpec)
	reconciler := mfgServices.NewAssignmentReconciler(h.taskRepo, assignmentTracker)
	completionChecker := mfgServices.NewPipelineCompletionChecker(h.pipelineRepo, h.taskRepo, registry)
	recycler := mfgServices.NewPipelineRecycler(h.pipelineRepo, h.taskRepo, h.shipRepo, h.taskQueue, h.factoryTracker, registry, nil)
	taskRescuer := mfgServices.NewTaskRescuer(h.taskRepo, h.taskQueue, conditionChecker)

	// 3. Create factory manager (no circular dependencies)
	h.factoryManager = mfgServices.NewFactoryStateManager(
		h.taskRepo,
		h.factoryStateRepo,
		h.factoryTracker,
		h.taskQueue,
	)

	// 4. Create state recoverer
	h.stateRecoverer = mfgServices.NewStateRecoveryManager(
		h.pipelineRepo,
		h.taskRepo,
		h.factoryStateRepo,
		h.shipRepo,
		h.factoryTracker,
		h.taskQueue,
		nil, // nil = use RealClock
	)

	// 5. Create pipeline manager (callback will trigger rescan)
	onPipelineCompleted := func(ctx context.Context) {
		// This will be called when a pipeline completes
	}
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
		registry,
		completionChecker,
		recycler,
		taskRescuer,
		h.clock,
		onPipelineCompleted,
	)

	// 6. Create worker lifecycle manager (uses assignmentTracker directly to avoid circular dep)
	workerLifecycleMgr := mfgServices.NewWorkerLifecycleManager(
		h.taskRepo,
		h.shipRepo,
		h.daemonClient,
		h.containerRemover,
		h.taskQueue,
		h.clock,
		assignmentTracker,
		h.factoryManager,
		h.pipelineManager,
		h.workerCompletionChan,
	)
	h.workerManager = workerLifecycleMgr

	// 7. Create orphaned cargo handler
	orphanedHandler := mfgServices.NewOrphanedCargoHandler(
		h.taskRepo,
		h.marketRepo,
		h.shipRepo, // For API sync to verify cargo before LIQUIDATE tasks
		h.workerManager,
		nil, // taskAssigner - will be set after creation
		h.mediator,
	)
	h.orphanedHandler = orphanedHandler

	// 8. Create task assignment manager (has all dependencies now)
	taskAssignmentMgr := mfgServices.NewTaskAssignmentManager(
		h.taskRepo,
		h.shipRepo,
		h.marketRepo,
		h.taskQueue,
		shipSelector,
		assignmentTracker,
		conditionChecker,
		reconciler,
		reservationPolicy,
		h.pipelineManager.GetActivePipelines,
		h.workerManager,
		h.orphanedHandler,
	)
	h.taskAssigner = taskAssignmentMgr

	// 9. Final wiring: orphanedHandler needs taskAssigner (use direct assignment to field)
	orphanedHandler.SetTaskAssigner(h.taskAssigner)
}

// recoverState recovers coordinator state from database
func (h *RunParallelManufacturingCoordinatorHandler) recoverState(ctx context.Context, playerID int) error {
	logger := common.LoggerFromContext(ctx)

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

	// Recover storage ship cargo state from API (for STORAGE_ACQUIRE_DELIVER tasks)
	if h.storageRecovery != nil {
		token, err := common.PlayerTokenFromContext(ctx)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Storage recovery skipped: %v", err), nil)
		} else {
			storageResult, err := h.storageRecovery.RecoverStorageOperations(ctx, playerID, token)
			if err != nil {
				logger.Log("WARN", fmt.Sprintf("Storage recovery failed: %v", err), nil)
			} else if storageResult.OperationsRecovered > 0 {
				logger.Log("INFO", fmt.Sprintf("Recovered %d storage operations with %d ships",
					storageResult.OperationsRecovered, storageResult.ShipsRegistered), nil)
			}
		}
	}

	// BUG FIX #1: Restart workers for READY tasks that have assigned ships
	// This handles tasks that were EXECUTING when daemon died and have preserved ship assignments
	if h.workerManager != nil {
		restartedCount, err := h.restartInterruptedWorkers(ctx, playerID)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to restart interrupted workers: %v", err), nil)
		} else if restartedCount > 0 {
			logger.Log("INFO", fmt.Sprintf("BUG FIX #1: Restarted %d interrupted worker(s)", restartedCount), nil)
		}
	}

	// Check if any recovered pipelines are already complete (tasks finished before restart)
	h.pipelineManager.CheckAllPipelinesForCompletion(ctx)

	return nil
}

// restartInterruptedWorkers restarts worker containers for READY tasks that have assigned ships.
// BUG FIX #1: After daemon restart, SELL tasks preserve their ship assignments but their
// worker containers are gone. This function finds these orphaned tasks and restarts workers.
func (h *RunParallelManufacturingCoordinatorHandler) restartInterruptedWorkers(ctx context.Context, playerID int) (int, error) {
	logger := common.LoggerFromContext(ctx)

	if h.taskRepo == nil || h.shipRepo == nil {
		return 0, nil
	}

	// Find READY tasks that have assigned ships (indicating interrupted workers)
	readyTasks, err := h.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusReady)
	if err != nil {
		return 0, fmt.Errorf("failed to find ready tasks: %w", err)
	}

	restartedCount := 0
	for _, task := range readyTasks {
		shipSymbol := task.AssignedShip()
		if shipSymbol == "" {
			continue // No assigned ship - will be handled by normal assignment
		}

		// This task has an assigned ship but no worker - restart it
		ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to load ship %s for task %s: %v",
				shipSymbol, task.ID()[:8], err), nil)
			continue
		}

		// Start worker for this task
		err = h.workerManager.AssignTaskToShip(ctx, mfgServices.AssignTaskParams{
			Task:     task,
			Ship:     ship,
			PlayerID: playerID,
		})
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to restart worker for task %s: %v",
				task.ID()[:8], err), nil)
			continue
		}

		restartedCount++
		logger.Log("INFO", fmt.Sprintf("Restarted worker for task %s (%s) on ship %s",
			task.ID()[:8], task.TaskType(), shipSymbol), nil)
	}

	return restartedCount, nil
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
		h.storageOpRepo, // For creating STORAGE_ACQUIRE_DELIVER tasks
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
	config coordinatorConfig,
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
					SystemSymbol:           cmd.SystemSymbol,
					PlayerID:               cmd.PlayerID,
					MinPurchasePrice:       config.minPurchasePrice,
					MaxPipelines:           config.maxPipelines,
					MaxCollectionPipelines: config.maxCollectionPipelines,
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
		MaxConcurrentTasks: config.maxConcurrentTasks,
	})
}

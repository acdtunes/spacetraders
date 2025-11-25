package commands

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// RunParallelManufacturingCoordinatorCommand orchestrates parallel task-based manufacturing
type RunParallelManufacturingCoordinatorCommand struct {
	SystemSymbol         string // System to scan for opportunities
	PlayerID             int    // Player identifier
	ContainerID          string // Container ID for this coordinator
	MinPurchasePrice     int    // Minimum purchase price threshold (default 1000)
	MaxConcurrentTasks   int    // Maximum concurrent task executions (default 10)
	MaxPipelines         int    // Maximum active pipelines (default 3)
	SupplyPollInterval   time.Duration // How often to poll factory supply (default 30s)
}

// RunParallelManufacturingCoordinatorResponse is never returned (infinite loop)
type RunParallelManufacturingCoordinatorResponse struct{}

// TaskCompletion represents a completed task notification
type TaskCompletion struct {
	TaskID     string
	ShipSymbol string
	PipelineID string
	Success    bool
	Error      error
}

// RunParallelManufacturingCoordinatorHandler orchestrates parallel manufacturing using task-based pipelines.
//
// Key differences from original coordinator:
// - Uses TaskQueue for priority-based task scheduling
// - Creates pipelines with dependency graphs
// - Assigns individual tasks (not entire opportunities) to ships
// - Ships can work on different parts of the same pipeline
// - SupplyMonitor tracks factory supply levels
//
// Workflow:
//  1. Scan for manufacturing opportunities (every 2 minutes)
//  2. Create pipelines with task dependency graphs
//  3. Discover idle ships (every 30 seconds)
//  4. Assign ready tasks to closest idle ships
//  5. Execute tasks via TaskWorker
//  6. Handle completions, update dependencies, mark new tasks ready
//  7. Monitor factory supply levels
//  8. Mark COLLECT tasks ready when supply reaches HIGH
type RunParallelManufacturingCoordinatorHandler struct {
	// Services
	demandFinder    *services.ManufacturingDemandFinder
	pipelinePlanner *services.PipelinePlanner
	taskQueue       *services.TaskQueue
	factoryTracker  *manufacturing.FactoryStateTracker

	// Repositories
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	pipelineRepo       manufacturing.PipelineRepository
	taskRepo           manufacturing.TaskRepository
	factoryStateRepo   manufacturing.FactoryStateRepository
	marketRepo         market.MarketRepository // For SupplyMonitor creation

	// Infrastructure
	mediator     common.Mediator
	daemonClient daemon.DaemonClient
	clock        shared.Clock

	// Runtime state
	mu                    sync.RWMutex
	activePipelines       map[string]*manufacturing.ManufacturingPipeline
	assignedTasks         map[string]string // taskID -> shipSymbol
	taskContainers        map[string]string // taskID -> containerID
	completionChan        chan TaskCompletion
	workerCompletionChan  chan string // Worker container completion signals
}

// NewRunParallelManufacturingCoordinatorHandler creates a new coordinator handler
func NewRunParallelManufacturingCoordinatorHandler(
	demandFinder *services.ManufacturingDemandFinder,
	pipelinePlanner *services.PipelinePlanner,
	taskQueue *services.TaskQueue,
	factoryTracker *manufacturing.FactoryStateTracker,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
	factoryStateRepo manufacturing.FactoryStateRepository,
	marketRepo market.MarketRepository,
	mediator common.Mediator,
	daemonClient daemon.DaemonClient,
	clock shared.Clock,
) *RunParallelManufacturingCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &RunParallelManufacturingCoordinatorHandler{
		demandFinder:        demandFinder,
		pipelinePlanner:     pipelinePlanner,
		taskQueue:           taskQueue,
		factoryTracker:      factoryTracker,
		shipRepo:            shipRepo,
		shipAssignmentRepo:  shipAssignmentRepo,
		pipelineRepo:        pipelineRepo,
		taskRepo:            taskRepo,
		factoryStateRepo:    factoryStateRepo,
		marketRepo:          marketRepo,
		mediator:            mediator,
		daemonClient:        daemonClient,
		clock:               clock,
		activePipelines:     make(map[string]*manufacturing.ManufacturingPipeline),
		assignedTasks:       make(map[string]string),
		taskContainers:      make(map[string]string),
		completionChan:      make(chan TaskCompletion, 100),
		workerCompletionChan: make(chan string, 100),
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

	// Clear stale runtime state from previous runs
	// The handler is a singleton, so we must reset state for each new container
	h.mu.Lock()
	h.activePipelines = make(map[string]*manufacturing.ManufacturingPipeline)
	h.assignedTasks = make(map[string]string)
	h.taskContainers = make(map[string]string)
	h.mu.Unlock()

	// Apply defaults
	minPurchasePrice := cmd.MinPurchasePrice
	if minPurchasePrice <= 0 {
		minPurchasePrice = 1000
	}

	maxConcurrentTasks := cmd.MaxConcurrentTasks
	if maxConcurrentTasks <= 0 {
		maxConcurrentTasks = 10
	}

	maxPipelines := cmd.MaxPipelines
	if maxPipelines <= 0 {
		maxPipelines = 3
	}

	supplyPollInterval := cmd.SupplyPollInterval
	if supplyPollInterval <= 0 {
		supplyPollInterval = 30 * time.Second
	}

	logger.Log("INFO", "Starting parallel manufacturing coordinator", map[string]interface{}{
		"system":               cmd.SystemSymbol,
		"min_purchase_price":   minPurchasePrice,
		"max_concurrent_tasks": maxConcurrentTasks,
		"max_pipelines":        maxPipelines,
		"supply_poll_interval": supplyPollInterval.String(),
	})

	// Recover state from database (handles daemon restart)
	if err := h.recoverState(ctx, cmd.PlayerID); err != nil {
		logger.Log("WARN", fmt.Sprintf("State recovery warning: %v", err), nil)
	}

	// Create and start supply monitor in background
	// We create it here because we need playerID from the command
	if h.marketRepo != nil {
		supplyMonitor := services.NewSupplyMonitor(
			h.marketRepo,
			h.factoryTracker,
			h.taskQueue,
			h.taskRepo,
			supplyPollInterval,
			cmd.PlayerID,
		)
		go supplyMonitor.Run(ctx)
		logger.Log("INFO", "Supply monitor started", map[string]interface{}{
			"poll_interval": supplyPollInterval.String(),
		})
	}

	// Set up tickers
	opportunityScanTicker := time.NewTicker(2 * time.Minute)
	shipDiscoveryTicker := time.NewTicker(30 * time.Second)
	taskAssignmentTicker := time.NewTicker(5 * time.Second)
	defer opportunityScanTicker.Stop()
	defer shipDiscoveryTicker.Stop()
	defer taskAssignmentTicker.Stop()

	// Initial scan
	h.scanAndCreatePipelines(ctx, cmd, minPurchasePrice, maxPipelines)

	// Main coordination loop
	for {
		select {
		case <-opportunityScanTicker.C:
			// Scan for new opportunities and create pipelines
			h.scanAndCreatePipelines(ctx, cmd, minPurchasePrice, maxPipelines)

		case <-shipDiscoveryTicker.C:
			// Discover idle ships (no action needed, just for logging)
			h.logIdleShips(ctx, cmd)

		case <-taskAssignmentTicker.C:
			// Assign ready tasks to idle ships
			h.assignTasks(ctx, cmd, maxConcurrentTasks)

		case completion := <-h.completionChan:
			// Handle task completion
			h.handleTaskCompletion(ctx, cmd, completion)

		case shipSymbol := <-h.workerCompletionChan:
			// Handle worker container completion
			h.handleWorkerContainerCompletion(ctx, cmd, shipSymbol)

		case <-ctx.Done():
			logger.Log("INFO", "Parallel manufacturing coordinator shutting down", nil)
			return &RunParallelManufacturingCoordinatorResponse{}, nil
		}
	}
}

// scanAndCreatePipelines scans for opportunities and creates new pipelines
func (h *RunParallelManufacturingCoordinatorHandler) scanAndCreatePipelines(
	ctx context.Context,
	cmd *RunParallelManufacturingCoordinatorCommand,
	minPurchasePrice int,
	maxPipelines int,
) {
	logger := common.LoggerFromContext(ctx)

	// Check if we have room for more pipelines
	h.mu.RLock()
	activePipelineCount := len(h.activePipelines)
	h.mu.RUnlock()

	if activePipelineCount >= maxPipelines {
		logger.Log("DEBUG", "Max pipelines reached, skipping opportunity scan", map[string]interface{}{
			"active_pipelines": activePipelineCount,
			"max_pipelines":    maxPipelines,
		})
		return
	}

	// Find opportunities
	config := services.DemandFinderConfig{
		MinPurchasePrice: minPurchasePrice,
		MaxOpportunities: maxPipelines * 2, // Get more than we need to filter
	}

	opportunities, err := h.demandFinder.FindHighDemandManufacturables(
		ctx,
		cmd.SystemSymbol,
		cmd.PlayerID,
		config,
	)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to scan opportunities: %v", err), nil)
		return
	}

	logger.Log("INFO", fmt.Sprintf("Found %d manufacturing opportunities", len(opportunities)), nil)

	// Create pipelines for top opportunities (if not already active)
	for _, opp := range opportunities {
		if activePipelineCount >= maxPipelines {
			break
		}

		// Check if we already have a pipeline for this good
		if h.hasPipelineForGood(opp.Good()) {
			continue
		}

		// Create pipeline
		pipeline, tasks, factoryStates, err := h.pipelinePlanner.CreatePipeline(
			ctx,
			opp,
			cmd.SystemSymbol,
			cmd.PlayerID,
		)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to create pipeline for %s: %v", opp.Good(), err), nil)
			continue
		}

		// Persist pipeline and tasks
		if h.pipelineRepo != nil {
			if err := h.pipelineRepo.Create(ctx, pipeline); err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to persist pipeline: %v", err), nil)
				continue
			}

			for _, task := range tasks {
				if err := h.taskRepo.Create(ctx, task); err != nil {
					logger.Log("ERROR", fmt.Sprintf("Failed to persist task: %v", err), nil)
				}
			}

			for _, state := range factoryStates {
				if err := h.factoryStateRepo.Create(ctx, state); err != nil {
					logger.Log("ERROR", fmt.Sprintf("Failed to persist factory state: %v", err), nil)
				}
				h.factoryTracker.LoadState(state)
			}
		}

		// Add to active pipelines
		h.mu.Lock()
		h.activePipelines[pipeline.ID()] = pipeline
		h.mu.Unlock()

		// Enqueue ready tasks (tasks with no dependencies)
		for _, task := range tasks {
			if len(task.DependsOn()) == 0 {
				if err := task.MarkReady(); err == nil {
					h.taskQueue.Enqueue(task)
					if h.taskRepo != nil {
						_ = h.taskRepo.Update(ctx, task)
					}
				}
			}
		}

		activePipelineCount++

		logger.Log("INFO", fmt.Sprintf("Created pipeline for %s with %d tasks", opp.Good(), len(tasks)), map[string]interface{}{
			"pipeline_id":  pipeline.ID(),
			"good":         opp.Good(),
			"sell_market":  opp.SellMarket().Symbol,
			"task_count":   len(tasks),
			"factory_count": len(factoryStates),
		})
	}
}

// hasPipelineForGood checks if we already have an active pipeline for this good
func (h *RunParallelManufacturingCoordinatorHandler) hasPipelineForGood(good string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, pipeline := range h.activePipelines {
		if pipeline.ProductGood() == good {
			return true
		}
	}
	return false
}

// assignTasks assigns ready tasks to idle ships
func (h *RunParallelManufacturingCoordinatorHandler) assignTasks(
	ctx context.Context,
	cmd *RunParallelManufacturingCoordinatorCommand,
	maxConcurrentTasks int,
) {
	logger := common.LoggerFromContext(ctx)

	// Get ready tasks (sorted by priority)
	readyTasks := h.taskQueue.GetReadyTasks()
	if len(readyTasks) == 0 {
		return
	}

	// Check how many tasks are currently assigned
	h.mu.RLock()
	assignedCount := len(h.assignedTasks)
	h.mu.RUnlock()

	if assignedCount >= maxConcurrentTasks {
		logger.Log("DEBUG", "Max concurrent tasks reached", map[string]interface{}{
			"assigned": assignedCount,
			"max":      maxConcurrentTasks,
		})
		return
	}

	// Get idle ships
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	_, idleShipSymbols, err := contract.FindIdleLightHaulers(
		ctx,
		playerID,
		h.shipRepo,
		h.shipAssignmentRepo,
	)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to find idle ships: %v", err), nil)
		return
	}

	if len(idleShipSymbols) == 0 {
		return
	}

	// Load ship entities
	idleShips := make(map[string]*navigation.Ship)
	for _, symbol := range idleShipSymbols {
		ship, err := h.shipRepo.FindBySymbol(ctx, symbol, playerID)
		if err == nil {
			idleShips[symbol] = ship
		}
	}

	// Greedy assignment: for each task, find appropriate ship
	for _, task := range readyTasks {
		if assignedCount >= maxConcurrentTasks {
			break
		}

		if len(idleShips) == 0 {
			break
		}

		var selectedShip *navigation.Ship
		var selectedSymbol string

		// SELL tasks MUST use the ship that executed the dependent COLLECT task
		// (that ship has the cargo to sell)
		if task.TaskType() == manufacturing.TaskTypeSell {
			collectShip := h.findCollectTaskShip(ctx, task, cmd.PlayerID)
			if collectShip != "" {
				if ship, exists := idleShips[collectShip]; exists {
					selectedShip = ship
					selectedSymbol = collectShip
					logger.Log("DEBUG", "SELL task using ship from COLLECT task", map[string]interface{}{
						"task_id":      task.ID(),
						"collect_ship": collectShip,
					})
				} else {
					// Ship not idle yet - skip this task for now
					logger.Log("DEBUG", "SELL task waiting for COLLECT ship to become idle", map[string]interface{}{
						"task_id":      task.ID(),
						"collect_ship": collectShip,
					})
					continue
				}
			}
		}

		// For non-SELL tasks (or if no COLLECT ship found), find closest ship
		if selectedShip == nil {
			sourceLocation := h.getTaskSourceLocation(task)
			selectedShip, selectedSymbol = h.findClosestShip(idleShips, sourceLocation)
		}

		if selectedShip == nil {
			continue
		}

		// Assign task to ship
		err := h.assignTaskToShip(ctx, cmd, task, selectedShip)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to assign task %s to ship %s: %v", task.ID(), selectedSymbol, err), nil)
			continue
		}

		// Remove ship from idle pool
		delete(idleShips, selectedSymbol)
		assignedCount++

		logger.Log("INFO", fmt.Sprintf("Assigned task %s (%s) to ship %s", task.ID(), task.TaskType(), selectedSymbol), map[string]interface{}{
			"task_id":      task.ID(),
			"task_type":    string(task.TaskType()),
			"ship":         selectedSymbol,
			"pipeline_id":  task.PipelineID(),
			"good":         task.Good(),
		})
	}
}

// findCollectTaskShip finds the ship that executed the COLLECT task for a SELL task
func (h *RunParallelManufacturingCoordinatorHandler) findCollectTaskShip(
	ctx context.Context,
	sellTask *manufacturing.ManufacturingTask,
	playerID int,
) string {
	// SELL task depends on COLLECT task - find the COLLECT dependency
	for _, depID := range sellTask.DependsOn() {
		depTask, err := h.taskRepo.FindByID(ctx, depID)
		if err != nil || depTask == nil {
			continue
		}

		// Found the COLLECT task - return its assigned ship
		if depTask.TaskType() == manufacturing.TaskTypeCollect {
			return depTask.AssignedShip()
		}
	}

	return ""
}

// getTaskSourceLocation returns the waypoint where the task starts
func (h *RunParallelManufacturingCoordinatorHandler) getTaskSourceLocation(task *manufacturing.ManufacturingTask) *shared.Waypoint {
	var symbol string

	switch task.TaskType() {
	case manufacturing.TaskTypeAcquire:
		symbol = task.SourceMarket()
	case manufacturing.TaskTypeDeliver:
		// Deliver starts where the ship is (with cargo)
		// Use target market as a reference
		symbol = task.TargetMarket()
	case manufacturing.TaskTypeCollect:
		symbol = task.FactorySymbol()
	case manufacturing.TaskTypeSell, manufacturing.TaskTypeLiquidate:
		symbol = task.TargetMarket()
	}

	if symbol == "" {
		return nil
	}

	// Extract coordinates from symbol (simplified - real implementation would query waypoint repo)
	return &shared.Waypoint{Symbol: symbol, X: 0, Y: 0}
}

// findClosestShip finds the closest ship to a waypoint
func (h *RunParallelManufacturingCoordinatorHandler) findClosestShip(
	ships map[string]*navigation.Ship,
	target *shared.Waypoint,
) (*navigation.Ship, string) {
	if target == nil || len(ships) == 0 {
		// Return first ship if no target
		for symbol, ship := range ships {
			return ship, symbol
		}
		return nil, ""
	}

	var closestShip *navigation.Ship
	var closestSymbol string
	var closestDistance float64 = -1

	for symbol, ship := range ships {
		distance := ship.CurrentLocation().DistanceTo(target)
		if closestDistance < 0 || distance < closestDistance {
			closestDistance = distance
			closestShip = ship
			closestSymbol = symbol
		}
	}

	return closestShip, closestSymbol
}

// assignTaskToShip assigns a task to a ship by spawning a worker container
// This follows the same pattern as contract workflow: persist container -> assign ship -> start container
func (h *RunParallelManufacturingCoordinatorHandler) assignTaskToShip(
	ctx context.Context,
	cmd *RunParallelManufacturingCoordinatorCommand,
	task *manufacturing.ManufacturingTask,
	ship *navigation.Ship,
) error {
	logger := common.LoggerFromContext(ctx)
	shipSymbol := ship.ShipSymbol()

	// Generate worker container ID
	workerContainerID := utils.GenerateContainerID("mfg-task", shipSymbol)

	// Assign ship to task (domain state)
	if err := task.AssignShip(shipSymbol); err != nil {
		return fmt.Errorf("failed to assign ship: %w", err)
	}

	// Persist task assignment
	if h.taskRepo != nil {
		if err := h.taskRepo.Update(ctx, task); err != nil {
			return fmt.Errorf("failed to persist task assignment: %w", err)
		}
	}

	// Create worker command
	workerCmd := &RunManufacturingTaskWorkerCommand{
		ShipSymbol:    shipSymbol,
		Task:          task,
		PlayerID:      cmd.PlayerID,
		ContainerID:   workerContainerID,
		CoordinatorID: cmd.ContainerID,
	}

	// Step 1: Persist worker container to DB (synchronous, no start)
	logger.Log("INFO", fmt.Sprintf("Persisting worker container %s for %s", workerContainerID, shipSymbol), nil)
	if err := h.daemonClient.PersistManufacturingTaskWorkerContainer(ctx, workerContainerID, uint(cmd.PlayerID), workerCmd); err != nil {
		// Rollback task assignment
		_ = task.RollbackAssignment()
		if h.taskRepo != nil {
			_ = h.taskRepo.Update(ctx, task)
		}
		return fmt.Errorf("failed to persist worker container: %w", err)
	}

	// Step 2: Assign ship to worker container (this is the proper FK reference)
	logger.Log("INFO", fmt.Sprintf("Assigning %s to worker container %s", shipSymbol, workerContainerID), nil)
	if h.shipAssignmentRepo != nil {
		assignment := container.NewShipAssignment(
			shipSymbol,
			cmd.PlayerID,
			workerContainerID, // Use worker container ID (proper FK reference)
			h.clock,
		)
		if err := h.shipAssignmentRepo.Assign(ctx, assignment); err != nil {
			// Rollback: stop container and task assignment
			_ = h.daemonClient.StopContainer(ctx, workerContainerID)
			_ = task.RollbackAssignment()
			if h.taskRepo != nil {
				_ = h.taskRepo.Update(ctx, task)
			}
			return fmt.Errorf("failed to assign ship: %w", err)
		}
	}

	// Step 3: Start the worker container (ship is safely assigned)
	logger.Log("INFO", fmt.Sprintf("Starting worker container %s for task %s", workerContainerID, task.ID()[:8]), nil)
	if err := h.daemonClient.StartManufacturingTaskWorkerContainer(ctx, workerContainerID, h.workerCompletionChan); err != nil {
		// Rollback: release ship assignment and rollback task
		if h.shipAssignmentRepo != nil {
			_ = h.shipAssignmentRepo.Release(ctx, shipSymbol, cmd.PlayerID, "worker_start_failed")
		}
		_ = task.RollbackAssignment()
		if h.taskRepo != nil {
			_ = h.taskRepo.Update(ctx, task)
		}
		return fmt.Errorf("failed to start worker container: %w", err)
	}

	// Track assignment in memory
	h.mu.Lock()
	h.assignedTasks[task.ID()] = shipSymbol
	h.taskContainers[task.ID()] = workerContainerID
	h.mu.Unlock()

	// Remove from queue
	h.taskQueue.Remove(task.ID())

	logger.Log("INFO", "Task assigned to worker container", map[string]interface{}{
		"task_id":      task.ID()[:8],
		"task_type":    string(task.TaskType()),
		"ship":         shipSymbol,
		"container_id": workerContainerID,
	})

	return nil
}

// handleWorkerContainerCompletion handles when a worker container completes
func (h *RunParallelManufacturingCoordinatorHandler) handleWorkerContainerCompletion(
	ctx context.Context,
	cmd *RunParallelManufacturingCoordinatorCommand,
	shipSymbol string,
) {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf("Worker container completed for ship %s", shipSymbol), nil)

	// Find the task that was assigned to this ship
	h.mu.Lock()
	var taskID string
	var containerID string
	for tid, symbol := range h.assignedTasks {
		if symbol == shipSymbol {
			taskID = tid
			containerID = h.taskContainers[tid]
			break
		}
	}

	if taskID != "" {
		delete(h.assignedTasks, taskID)
		delete(h.taskContainers, taskID)
	}
	h.mu.Unlock()

	if taskID == "" {
		logger.Log("WARN", fmt.Sprintf("No task found for completed ship %s", shipSymbol), nil)
		return
	}

	// Load the task from repository to get its current state
	var task *manufacturing.ManufacturingTask
	if h.taskRepo != nil {
		var err error
		task, err = h.taskRepo.FindByID(ctx, taskID)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to load task %s: %v", taskID, err), nil)
		}
	}

	if task == nil {
		logger.Log("WARN", fmt.Sprintf("Task %s not found in repository", taskID), nil)
		return
	}

	// Ship assignment is already released by the container runner when it stops
	// We just need to handle the task completion logic

	// Create completion notification based on task status
	success := task.Status() == manufacturing.TaskStatusCompleted
	var taskErr error
	if !success && task.ErrorMessage() != "" {
		taskErr = fmt.Errorf(task.ErrorMessage())
	}

	completion := TaskCompletion{
		TaskID:     taskID,
		ShipSymbol: shipSymbol,
		PipelineID: task.PipelineID(),
		Success:    success,
		Error:      taskErr,
	}

	// Handle the task completion (update dependencies, check pipeline completion, etc.)
	h.handleTaskCompletion(ctx, cmd, completion)

	logger.Log("INFO", fmt.Sprintf("Processed worker completion for task %s (container: %s)", taskID[:8], containerID), nil)
}

// handleTaskCompletion handles a completed task
// Note: Ship assignment is already released by the container runner when it stops
func (h *RunParallelManufacturingCoordinatorHandler) handleTaskCompletion(
	ctx context.Context,
	cmd *RunParallelManufacturingCoordinatorCommand,
	completion TaskCompletion,
) {
	logger := common.LoggerFromContext(ctx)

	// Note: Ship assignment release is handled by container runner (ContainerRunner.Stop())
	// We just need to clean up in-memory tracking

	// Remove from assigned tasks (may have been cleaned up already by handleWorkerContainerCompletion)
	h.mu.Lock()
	delete(h.assignedTasks, completion.TaskID)
	delete(h.taskContainers, completion.TaskID)
	h.mu.Unlock()

	if !completion.Success {
		logger.Log("ERROR", fmt.Sprintf("Task %s failed: %v", completion.TaskID, completion.Error), map[string]interface{}{
			"task_id":     completion.TaskID,
			"ship":        completion.ShipSymbol,
			"pipeline_id": completion.PipelineID,
		})
		// TODO: Handle task failure (retry, cancel pipeline, etc.)
		return
	}

	logger.Log("INFO", fmt.Sprintf("Task %s completed successfully", completion.TaskID), map[string]interface{}{
		"task_id":     completion.TaskID,
		"ship":        completion.ShipSymbol,
		"pipeline_id": completion.PipelineID,
	})

	// Update factory state if this was a DELIVER task
	h.updateFactoryStateOnDelivery(ctx, completion.TaskID, completion.ShipSymbol, completion.PipelineID)

	// Check if dependent tasks can now be marked ready
	h.updateDependentTasks(ctx, completion.TaskID, completion.PipelineID)

	// Check if pipeline is complete
	h.checkPipelineCompletion(ctx, completion.PipelineID)
}

// updateDependentTasks marks tasks as ready if their dependencies are met
func (h *RunParallelManufacturingCoordinatorHandler) updateDependentTasks(
	ctx context.Context,
	completedTaskID string,
	pipelineID string,
) {
	logger := common.LoggerFromContext(ctx)

	if h.taskRepo == nil {
		return
	}

	// Find tasks in this pipeline
	tasks, err := h.taskRepo.FindByPipelineID(ctx, pipelineID)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to find tasks for pipeline: %v", err), nil)
		return
	}

	for _, task := range tasks {
		// Skip if not pending
		if task.Status() != manufacturing.TaskStatusPending {
			continue
		}

		// Check if this task depends on the completed task
		dependsOnCompleted := false
		for _, depID := range task.DependsOn() {
			if depID == completedTaskID {
				dependsOnCompleted = true
				break
			}
		}

		if !dependsOnCompleted {
			continue
		}

		// Check if ALL dependencies are now met
		allDepsMet := true
		for _, depID := range task.DependsOn() {
			depTask, err := h.taskRepo.FindByID(ctx, depID)
			if err != nil || depTask == nil || depTask.Status() != manufacturing.TaskStatusCompleted {
				allDepsMet = false
				break
			}
		}

		if !allDepsMet {
			continue
		}

		// For COLLECT tasks, also check if supply is HIGH
		if task.TaskType() == manufacturing.TaskTypeCollect {
			factory := h.factoryTracker.GetState(task.PipelineID(), task.FactorySymbol(), task.Good())
			if factory == nil || !factory.IsReadyForCollection() {
				logger.Log("DEBUG", fmt.Sprintf("COLLECT task %s waiting for supply HIGH", task.ID()), nil)
				continue
			}
		}

		// Mark task as ready
		if err := task.MarkReady(); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to mark task ready: %v", err), nil)
			continue
		}

		if err := h.taskRepo.Update(ctx, task); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to persist task: %v", err), nil)
			continue
		}

		h.taskQueue.Enqueue(task)

		logger.Log("INFO", fmt.Sprintf("Task %s (%s) is now ready", task.ID(), task.TaskType()), map[string]interface{}{
			"task_id":   task.ID(),
			"task_type": string(task.TaskType()),
			"good":      task.Good(),
		})
	}
}

// updateFactoryStateOnDelivery records a delivery in the factory state tracker
func (h *RunParallelManufacturingCoordinatorHandler) updateFactoryStateOnDelivery(
	ctx context.Context,
	taskID string,
	shipSymbol string,
	pipelineID string,
) {
	logger := common.LoggerFromContext(ctx)

	if h.taskRepo == nil || h.factoryStateRepo == nil {
		return
	}

	// Get the task
	task, err := h.taskRepo.FindByID(ctx, taskID)
	if err != nil || task == nil {
		return
	}

	// Only process DELIVER tasks
	if task.TaskType() != manufacturing.TaskTypeDeliver {
		return
	}

	// Get factory state for this delivery's target
	factorySymbol := task.TargetMarket()
	if factorySymbol == "" {
		return
	}

	// Find the factory state for this pipeline and factory
	factoryStates, err := h.factoryStateRepo.FindByPipelineID(ctx, pipelineID)
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("Failed to find factory states: %v", err), nil)
		return
	}

	for _, fs := range factoryStates {
		if fs.FactorySymbol() == factorySymbol {
			// Record this delivery
			if err := fs.RecordDelivery(task.Good(), task.ActualQuantity(), shipSymbol); err != nil {
				logger.Log("WARN", fmt.Sprintf("Failed to record delivery: %v", err), nil)
				continue
			}

			// Persist the update
			if err := h.factoryStateRepo.Update(ctx, fs); err != nil {
				logger.Log("WARN", fmt.Sprintf("Failed to persist factory state: %v", err), nil)
				continue
			}

			// Update the in-memory tracker
			if h.factoryTracker != nil {
				h.factoryTracker.LoadState(fs)
			}

			logger.Log("INFO", "Recorded delivery to factory", map[string]interface{}{
				"factory":            factorySymbol,
				"good":               task.Good(),
				"all_inputs_delivered": fs.AllInputsDelivered(),
			})
		}
	}
}

// checkPipelineCompletion checks if a pipeline is complete and removes it from active
func (h *RunParallelManufacturingCoordinatorHandler) checkPipelineCompletion(
	ctx context.Context,
	pipelineID string,
) {
	logger := common.LoggerFromContext(ctx)

	h.mu.Lock()
	defer h.mu.Unlock()

	pipeline, exists := h.activePipelines[pipelineID]
	if !exists {
		return
	}

	// Check if all tasks are completed
	if h.taskRepo == nil {
		return
	}

	tasks, err := h.taskRepo.FindByPipelineID(ctx, pipelineID)
	if err != nil {
		return
	}

	allCompleted := true
	anyFailed := false
	for _, task := range tasks {
		if task.Status() != manufacturing.TaskStatusCompleted {
			allCompleted = false
		}
		if task.Status() == manufacturing.TaskStatusFailed {
			anyFailed = true
		}
	}

	if allCompleted {
		// Mark pipeline as completed
		if err := pipeline.Complete(); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to mark pipeline completed: %v", err), nil)
		} else {
			if h.pipelineRepo != nil {
				_ = h.pipelineRepo.Update(ctx, pipeline)
			}
			delete(h.activePipelines, pipelineID)

			// Calculate net profit for metrics
			netProfit := pipeline.TotalRevenue() - pipeline.TotalCost()

			// Record pipeline completion metrics
			metrics.RecordManufacturingPipelineCompletion(
				pipeline.PlayerID(),
				pipeline.ProductGood(),
				"completed",
				pipeline.RuntimeDuration(),
				netProfit,
			)

			logger.Log("INFO", fmt.Sprintf("Pipeline %s completed successfully!", pipelineID), map[string]interface{}{
				"pipeline_id":   pipelineID,
				"good":          pipeline.ProductGood(),
				"total_cost":    pipeline.TotalCost(),
				"total_revenue": pipeline.TotalRevenue(),
				"net_profit":    netProfit,
			})
		}
	} else if anyFailed {
		// Mark pipeline as failed
		if err := pipeline.Fail("One or more tasks failed"); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to mark pipeline failed: %v", err), nil)
		} else {
			if h.pipelineRepo != nil {
				_ = h.pipelineRepo.Update(ctx, pipeline)
			}
			delete(h.activePipelines, pipelineID)

			// Record failed pipeline metrics
			metrics.RecordManufacturingPipelineCompletion(
				pipeline.PlayerID(),
				pipeline.ProductGood(),
				"failed",
				pipeline.RuntimeDuration(),
				0,
			)

			logger.Log("WARN", fmt.Sprintf("Pipeline %s failed", pipelineID), map[string]interface{}{
				"pipeline_id": pipelineID,
				"good":        pipeline.ProductGood(),
			})
		}
	}
}

// recoverState recovers coordinator state from database after restart
func (h *RunParallelManufacturingCoordinatorHandler) recoverState(ctx context.Context, playerID int) error {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Recovering parallel manufacturing state from database...", nil)

	// Skip if no repos configured
	if h.pipelineRepo == nil || h.taskRepo == nil {
		logger.Log("DEBUG", "No repositories configured, skipping state recovery", nil)
		return nil
	}

	// Step 1: Load incomplete pipelines
	pipelines, err := h.pipelineRepo.FindByStatus(ctx, playerID, []manufacturing.PipelineStatus{
		manufacturing.PipelineStatusPlanning,
		manufacturing.PipelineStatusExecuting,
	})
	if err != nil {
		return fmt.Errorf("failed to load pipelines: %w", err)
	}

	h.mu.Lock()
	for _, pipeline := range pipelines {
		h.activePipelines[pipeline.ID()] = pipeline
	}
	h.mu.Unlock()

	logger.Log("INFO", fmt.Sprintf("Recovered %d active pipelines", len(pipelines)), nil)

	// Step 2: Load incomplete tasks and rebuild queue
	tasks, err := h.taskRepo.FindIncomplete(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to load tasks: %w", err)
	}

	readyCount := 0
	for _, task := range tasks {
		// Re-evaluate task readiness
		if task.Status() == manufacturing.TaskStatusPending {
			// Check if dependencies are met
			allDepsMet := true
			for _, depID := range task.DependsOn() {
				depTask, err := h.taskRepo.FindByID(ctx, depID)
				if err != nil || depTask == nil || depTask.Status() != manufacturing.TaskStatusCompleted {
					allDepsMet = false
					break
				}
			}

			if allDepsMet {
				if err := task.MarkReady(); err == nil {
					_ = h.taskRepo.Update(ctx, task)
				}
			}
		}

		if task.Status() == manufacturing.TaskStatusReady {
			h.taskQueue.Enqueue(task)
			readyCount++
		}
	}

	logger.Log("INFO", fmt.Sprintf("Recovered %d tasks, %d ready", len(tasks), readyCount), nil)

	// Step 3: Load factory states
	if h.factoryStateRepo != nil {
		factoryStates, err := h.factoryStateRepo.FindPending(ctx, playerID)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to load factory states: %v", err), nil)
		} else {
			for _, state := range factoryStates {
				h.factoryTracker.LoadState(state)
			}
			logger.Log("INFO", fmt.Sprintf("Recovered %d factory states", len(factoryStates)), nil)
		}
	}

	logger.Log("INFO", "State recovery complete", map[string]interface{}{
		"pipelines":      len(h.activePipelines),
		"tasks_in_queue": h.taskQueue.Size(),
	})

	return nil
}

// logIdleShips logs available idle ships for debugging
func (h *RunParallelManufacturingCoordinatorHandler) logIdleShips(
	ctx context.Context,
	cmd *RunParallelManufacturingCoordinatorCommand,
) {
	logger := common.LoggerFromContext(ctx)

	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	_, idleShipSymbols, err := contract.FindIdleLightHaulers(
		ctx,
		playerID,
		h.shipRepo,
		h.shipAssignmentRepo,
	)
	if err != nil {
		return
	}

	if len(idleShipSymbols) > 0 {
		logger.Log("DEBUG", fmt.Sprintf("Available idle ships: %d", len(idleShipSymbols)), map[string]interface{}{
			"count": len(idleShipSymbols),
		})
	}
}

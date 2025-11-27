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
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
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
	Strategy             string // Acquisition strategy: prefer-buy, prefer-fabricate, smart (default: prefer-fabricate)
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

// coordinatorContext holds command context for event-driven callbacks
type coordinatorContext struct {
	cmd                *RunParallelManufacturingCoordinatorCommand
	minPurchasePrice   int
	maxPipelines       int
	maxConcurrentTasks int
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
// ContainerRemover is a minimal interface for cleaning up orphaned containers
type ContainerRemover interface {
	Remove(ctx context.Context, containerID string, playerID int) error
}

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
	containerRemover   ContainerRemover        // For cleaning up orphaned PENDING containers

	// Infrastructure
	mediator         common.Mediator
	daemonClient     daemon.DaemonClient
	clock            shared.Clock
	waypointProvider system.IWaypointProvider // For looking up waypoint coordinates

	// Runtime state
	mu                    sync.RWMutex
	activePipelines       map[string]*manufacturing.ManufacturingPipeline
	assignedTasks         map[string]string // taskID -> shipSymbol
	taskContainers        map[string]string // taskID -> containerID
	completionChan        chan TaskCompletion
	workerCompletionChan  chan string // Worker container completion signals
	taskReadyChan         chan struct{} // Notified when SupplyMonitor marks tasks ready
	cmdContext            *coordinatorContext // For event-driven callbacks
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
		demandFinder:         demandFinder,
		pipelinePlanner:      pipelinePlanner,
		taskQueue:            taskQueue,
		factoryTracker:       factoryTracker,
		shipRepo:             shipRepo,
		shipAssignmentRepo:   shipAssignmentRepo,
		pipelineRepo:         pipelineRepo,
		taskRepo:             taskRepo,
		factoryStateRepo:     factoryStateRepo,
		marketRepo:           marketRepo,
		containerRemover:     containerRemover,
		mediator:             mediator,
		daemonClient:         daemonClient,
		clock:                clock,
		waypointProvider:     waypointProvider,
		activePipelines:      make(map[string]*manufacturing.ManufacturingPipeline),
		assignedTasks:        make(map[string]string),
		taskContainers:       make(map[string]string),
		completionChan:       make(chan TaskCompletion, 100),
		workerCompletionChan: make(chan string, 100),
		taskReadyChan:        make(chan struct{}, 10), // Buffered to avoid blocking SupplyMonitor
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

	// Configure acquisition strategy for supply chain resolver
	// This determines whether intermediate goods are bought or fabricated
	strategy := cmd.Strategy
	if strategy == "" {
		strategy = "prefer-fabricate" // Default: recursive manufacturing for better economics
	}
	h.demandFinder.SetStrategy(strategy)

	logger.Log("INFO", "Starting parallel manufacturing coordinator", map[string]interface{}{
		"system":               cmd.SystemSymbol,
		"min_purchase_price":   minPurchasePrice,
		"max_concurrent_tasks": maxConcurrentTasks,
		"max_pipelines":        maxPipelines,
		"supply_poll_interval": supplyPollInterval.String(),
		"strategy":             strategy,
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
			h.factoryStateRepo,
			h.pipelineRepo, // For looking up pipeline's sell_market
			h.taskQueue,
			h.taskRepo,
			h.pipelinePlanner.MarketLocator(), // For finding export markets when creating ACQUIRE_DELIVER tasks
			supplyPollInterval,
			cmd.PlayerID,
		)
		// Enable event-driven task assignment notifications
		supplyMonitor.SetTaskReadyChannel(h.taskReadyChan)
		go supplyMonitor.Run(ctx)
		logger.Log("INFO", "Supply monitor started", map[string]interface{}{
			"poll_interval": supplyPollInterval.String(),
		})
	}

	// Set up ticker for background opportunity scanning
	// Background opportunity scan (3 minutes) - catches external market changes
	opportunityScanTicker := time.NewTicker(3 * time.Minute)

	// Set up ticker for stuck pipeline detection (5 minutes)
	// Detects and recycles pipelines that aren't making progress
	stuckPipelineTicker := time.NewTicker(5 * time.Minute)

	// Set up ticker for idle ship scanning (10 seconds)
	// Catches ships that became idle between completions or missed assignments
	// This ensures ships don't sit idle when there are ready tasks
	idleShipTicker := time.NewTicker(10 * time.Second)
	defer opportunityScanTicker.Stop()
	defer stuckPipelineTicker.Stop()
	defer idleShipTicker.Stop()


	// Store command context for event-driven callbacks
	h.cmdContext = &coordinatorContext{
		cmd:               cmd,
		minPurchasePrice:  minPurchasePrice,
		maxPipelines:      maxPipelines,
		maxConcurrentTasks: maxConcurrentTasks,
	}

	// Initial scan and task assignment
	h.scanAndCreatePipelines(ctx, cmd, minPurchasePrice, maxPipelines)
	h.assignTasks(ctx, cmd, maxConcurrentTasks)

	// Main coordination loop
	// Fully event-driven design:
	// - Worker completions immediately reassign freed ships (zero delay)
	// - SupplyMonitor notifies when COLLECT tasks become ready (zero delay)
	// - Pipeline completions trigger opportunity rescans (our actions may have changed supply)
	// - Background ticker catches external market changes (3-minute interval)
	// - Idle ship scanner catches missed assignments (10-second interval)
	for {
		select {
		case <-opportunityScanTicker.C:
			// Background rescan for external market changes
			h.scanAndCreatePipelines(ctx, cmd, minPurchasePrice, maxPipelines)

		case <-stuckPipelineTicker.C:
			// Detect and recycle stuck pipelines (no progress for 30+ minutes)
			h.detectAndRecycleStuckPipelines(ctx, cmd)

		case <-idleShipTicker.C:
			// Periodic idle ship scanning - catches missed assignments
			// This ensures ships don't sit idle when there are ready tasks
			h.assignTasks(ctx, cmd, maxConcurrentTasks)

		case <-h.taskReadyChan:
			// EVENT-DRIVEN: SupplyMonitor marked tasks as ready - assign ships immediately
			h.assignTasks(ctx, cmd, maxConcurrentTasks)

		case completion := <-h.completionChan:
			// Handle task completion
			h.handleTaskCompletion(ctx, cmd, completion)

		case shipSymbol := <-h.workerCompletionChan:
			// Handle worker container completion with immediate ship reassignment
			h.handleWorkerContainerCompletion(ctx, cmd, shipSymbol, maxConcurrentTasks)

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

		// Start the pipeline (transitions PLANNING -> EXECUTING)
		if err := pipeline.Start(); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to start pipeline for %s: %v", opp.Good(), err), nil)
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
		// STREAMING MODEL: Skip COLLECT_SELL tasks here - they're gated by SupplyMonitor
		// which checks both supply level AND delivery status
		for _, task := range tasks {
			// COLLECT_SELL tasks must wait for:
			//   1. Factory supply to be HIGH/ABUNDANT
			//   2. At least one delivery to be recorded
			// They're handled entirely by SupplyMonitor
			if task.TaskType() == manufacturing.TaskTypeCollectSell {
				continue
			}

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

	// Get idle ships FIRST (before checking ready tasks)
	// This allows us to handle ships with existing cargo before the normal assignment loop
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

	// PRIORITY: Handle ships with existing cargo from interrupted operations
	// This marks matching PENDING COLLECT_SELL tasks as READY and assigns ships to them
	// Ships are removed from idleShips as they get assigned
	idleShips = h.handleShipsWithExistingCargo(ctx, cmd, idleShips, maxConcurrentTasks)

	// Now get ready tasks (may include tasks marked READY by handleShipsWithExistingCargo)
	readyTasks := h.taskQueue.GetReadyTasks()
	if len(readyTasks) == 0 || len(idleShips) == 0 {
		return
	}

	// Refresh assigned count after handling ships with cargo
	h.mu.RLock()
	assignedCount = len(h.assignedTasks)
	h.mu.RUnlock()

	// Track factory+good assignment counts to balance input deliveries
	// This ensures all input goods for a factory get workers before any good gets excess workers
	// Key: "factorySymbol:good" for ACQUIRE_DELIVER tasks, Value: count of assigned ships
	// Pre-populate with currently ASSIGNED tasks to ensure global balance (not just per-round)
	assignedCounts := make(map[string]int)
	if h.taskRepo != nil {
		assignedTasks, _ := h.taskRepo.FindByStatus(ctx, cmd.PlayerID, manufacturing.TaskStatusAssigned)
		for _, t := range assignedTasks {
			if t.TaskType() == manufacturing.TaskTypeAcquireDeliver {
				factoryGoodKey := t.FactorySymbol() + ":" + t.Good()
				assignedCounts[factoryGoodKey]++
			}
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

		// BALANCE CHECK: For ACQUIRE_DELIVER, prefer tasks for factory+good with fewer assigned ships
		// This ensures all input goods for a factory get workers before any good gets excess workers
		// We allow up to 2 ships per factory+good to enable parallelism, but prioritize unassigned goods
		if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
			factoryGoodKey := task.FactorySymbol() + ":" + task.Good()
			currentCount := assignedCounts[factoryGoodKey]

			// Find minimum assignment count across all factory+good combinations we've seen
			// This allows doubling up AFTER all inputs have at least one ship
			minCount := currentCount
			for _, count := range assignedCounts {
				if count < minCount {
					minCount = count
				}
			}

			// Skip if this factory+good has more ships than the minimum (prefer underserved goods)
			// Exception: allow up to 2 ships per good for parallelism even if unbalanced
			if currentCount > minCount && currentCount >= 2 {
				continue
			}
		}

		// SATURATION CHECK: For COLLECT_SELL tasks, verify sell market isn't saturated
		// This catches tasks that were READY before code restart (recovered from DB)
		if task.TaskType() == manufacturing.TaskTypeCollectSell {
			if h.isSellMarketSaturated(ctx, task.TargetMarket(), task.Good(), cmd.PlayerID) {
				logger.Log("DEBUG", "Skipping COLLECT_SELL task - sell market saturated", map[string]interface{}{
					"task_id":     task.ID()[:8],
					"good":        task.Good(),
					"sell_market": task.TargetMarket(),
				})
				// Reset task to PENDING so it can be re-evaluated by SupplyMonitor
				task.ResetToPending()
				if h.taskRepo != nil {
					_ = h.taskRepo.Update(ctx, task)
				}
				continue
			}
		}

		// Find closest ship to task source location
		sourceLocation := h.getTaskSourceLocation(ctx, task, cmd.PlayerID)
		selectedShip, selectedSymbol := h.findClosestShip(idleShips, sourceLocation)

		if selectedShip == nil {
			continue
		}

		// Assign task to ship
		err := h.assignTaskToShip(ctx, cmd, task, selectedShip)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to assign task %s to ship %s: %v", task.ID(), selectedSymbol, err), nil)
			// Remove ship from pool even on failure to prevent retry loop
			delete(idleShips, selectedSymbol)
			continue
		}

		// Track this factory+good assignment for balancing
		if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
			factoryGoodKey := task.FactorySymbol() + ":" + task.Good()
			assignedCounts[factoryGoodKey]++
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

// getTaskSourceLocation returns the waypoint where the task starts
func (h *RunParallelManufacturingCoordinatorHandler) getTaskSourceLocation(ctx context.Context, task *manufacturing.ManufacturingTask, playerID int) *shared.Waypoint {
	var symbol string

	switch task.TaskType() {
	case manufacturing.TaskTypeAcquireDeliver:
		// Starts at source market (buy from there, then deliver to factory)
		symbol = task.SourceMarket()
	case manufacturing.TaskTypeCollectSell:
		// Starts at factory (collect from there, then sell at market)
		symbol = task.FactorySymbol()
	case manufacturing.TaskTypeLiquidate:
		// Starts at target market (where to sell orphaned cargo)
		symbol = task.TargetMarket()
	}

	if symbol == "" {
		return nil
	}

	// Look up actual waypoint coordinates using waypointProvider
	if h.waypointProvider != nil {
		// Extract system from waypoint symbol (e.g., X1-YZ19-K84 -> X1-YZ19)
		systemSymbol := extractSystemFromWaypoint(symbol)
		waypoint, err := h.waypointProvider.GetWaypoint(ctx, symbol, systemSymbol, playerID)
		if err == nil && waypoint != nil {
			return waypoint
		}
	}

	// Fallback to symbol-only waypoint (distance calculations will be inaccurate)
	return &shared.Waypoint{Symbol: symbol, X: 0, Y: 0}
}

// extractSystemFromWaypoint extracts the system symbol from a waypoint symbol
// e.g., X1-YZ19-K84 -> X1-YZ19
func extractSystemFromWaypoint(waypointSymbol string) string {
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

// handleShipsWithExistingCargo handles ships that have cargo from interrupted operations.
// For each idle ship with cargo, it finds a matching PENDING COLLECT_SELL task and marks it READY.
// This ensures ships don't sit idle with cargo waiting for new deliveries to be recorded.
//
// The flow is:
// 1. Find idle ships with cargo
// 2. For each ship with cargo, find a PENDING COLLECT_SELL task matching the cargo type
// 3. Mark the task READY (bypassing the normal delivery-gated check)
// 4. Assign the ship directly to the task
//
// This is necessary because:
// - COLLECT_SELL tasks are normally gated by deliveries being recorded
// - But ships may already have cargo from previous interrupted operations
// - These ships need to sell their cargo immediately, not wait for new deliveries
func (h *RunParallelManufacturingCoordinatorHandler) handleShipsWithExistingCargo(
	ctx context.Context,
	cmd *RunParallelManufacturingCoordinatorCommand,
	idleShips map[string]*navigation.Ship,
	maxConcurrentTasks int,
) map[string]*navigation.Ship {
	logger := common.LoggerFromContext(ctx)

	if h.taskRepo == nil {
		return idleShips
	}

	// Check how many tasks are currently assigned
	h.mu.RLock()
	assignedCount := len(h.assignedTasks)
	h.mu.RUnlock()

	if assignedCount >= maxConcurrentTasks {
		return idleShips
	}

	// Find ships with cargo
	shipsWithCargo := make(map[string]*navigation.Ship)
	for symbol, ship := range idleShips {
		if ship.CargoUnits() > 0 {
			shipsWithCargo[symbol] = ship
		}
	}

	if len(shipsWithCargo) == 0 {
		return idleShips
	}

	logger.Log("INFO", "Found idle ships with cargo - checking for matching COLLECT_SELL tasks", map[string]interface{}{
		"ships_with_cargo": len(shipsWithCargo),
	})

	// Get all active pipelines to find PENDING COLLECT_SELL tasks
	h.mu.RLock()
	pipelineIDs := make([]string, 0, len(h.activePipelines))
	for id := range h.activePipelines {
		pipelineIDs = append(pipelineIDs, id)
	}
	h.mu.RUnlock()

	// For each ship with cargo, try to find a matching PENDING COLLECT_SELL task
	for shipSymbol, ship := range shipsWithCargo {
		if assignedCount >= maxConcurrentTasks {
			break
		}

		// Get the cargo type(s) from the ship
		cargo := ship.Cargo()
		if cargo == nil || len(cargo.Inventory) == 0 {
			continue
		}

		// Use the primary cargo type (most units)
		var primaryCargo string
		var maxUnits int
		for _, item := range cargo.Inventory {
			if item.Units > maxUnits {
				primaryCargo = item.Symbol
				maxUnits = item.Units
			}
		}

		// Find a PENDING COLLECT_SELL task for this cargo type
		var matchingTask *manufacturing.ManufacturingTask
		for _, pipelineID := range pipelineIDs {
			tasks, err := h.taskRepo.FindByPipelineID(ctx, pipelineID)
			if err != nil {
				continue
			}

			for _, task := range tasks {
				if task.TaskType() == manufacturing.TaskTypeCollectSell &&
					task.Status() == manufacturing.TaskStatusPending &&
					task.Good() == primaryCargo {
					matchingTask = task
					break
				}
			}

			if matchingTask != nil {
				break
			}
		}

		if matchingTask == nil {
			// No matching task - create an ad-hoc sell task for orphaned cargo
			logger.Log("INFO", "No matching COLLECT_SELL task - creating ad-hoc sell task for orphaned cargo", map[string]interface{}{
				"ship":        shipSymbol,
				"cargo_type":  primaryCargo,
				"cargo_units": maxUnits,
			})

			// Find best sell market for this cargo
			sellMarket, err := h.findBestSellMarket(ctx, ship.CurrentLocation().Symbol, primaryCargo, cmd.PlayerID)
			if err != nil {
				logger.Log("WARN", "Failed to find sell market for orphaned cargo", map[string]interface{}{
					"ship":       shipSymbol,
					"cargo_type": primaryCargo,
					"error":      err.Error(),
				})
				continue
			}

			// Check if sell market is saturated - skip if HIGH/ABUNDANT to avoid 1-credit sales
			if h.isSellMarketSaturated(ctx, sellMarket, primaryCargo, cmd.PlayerID) {
				logger.Log("INFO", "Sell market saturated - holding orphaned cargo until demand returns", map[string]interface{}{
					"ship":        shipSymbol,
					"cargo_type":  primaryCargo,
					"sell_market": sellMarket,
				})
				continue
			}

			// Create an ad-hoc COLLECT_SELL task (it will skip collect since ship already has cargo)
			// Use empty pipeline ID for ad-hoc tasks - the FK constraint allows NULL
			adHocTask := manufacturing.NewCollectSellTask(
				"", // Empty pipeline ID for ad-hoc tasks (stored as NULL in database)
				cmd.PlayerID,
				primaryCargo,
				ship.CurrentLocation().Symbol, // Factory = current location (not used since we skip collect)
				sellMarket,
				nil, // No dependencies
			)

			// Mark immediately ready since ship already has cargo
			if err := adHocTask.MarkReady(); err != nil {
				logger.Log("WARN", "Failed to mark ad-hoc task ready", map[string]interface{}{
					"error": err.Error(),
				})
				continue
			}

			// Persist the ad-hoc task to database for resilient recovery
			// Note: There's no FK constraint on pipeline_id, so UUID-based ad-hoc IDs work fine
			if err := h.taskRepo.Create(ctx, adHocTask); err != nil {
				logger.Log("WARN", "Failed to persist ad-hoc sell task", map[string]interface{}{
					"ship":       shipSymbol,
					"cargo_type": primaryCargo,
					"error":      err.Error(),
				})
				continue
			}

			logger.Log("INFO", "Created and persisted ad-hoc sell task for orphaned cargo", map[string]interface{}{
				"ship":        shipSymbol,
				"task_id":     adHocTask.ID()[:8],
				"cargo_type":  primaryCargo,
				"cargo_units": maxUnits,
				"sell_market": sellMarket,
			})

			// Assign using normal task assignment flow (persists container, assigns ship, starts worker)
			err = h.assignTaskToShip(ctx, cmd, adHocTask, ship)
			if err != nil {
				logger.Log("WARN", "Failed to assign ad-hoc sell task to ship", map[string]interface{}{
					"ship":  shipSymbol,
					"error": err.Error(),
				})
				// Task is persisted and READY, will be picked up in next assignment cycle
				h.taskQueue.Enqueue(adHocTask)
				continue
			}

			logger.Log("INFO", "Assigned ad-hoc COLLECT_SELL task to ship with orphaned cargo", map[string]interface{}{
				"ship":        shipSymbol,
				"task_id":     adHocTask.ID()[:8],
				"cargo_type":  primaryCargo,
				"cargo_units": maxUnits,
				"sell_market": sellMarket,
			})

			// Remove ship from idle pool
			delete(idleShips, shipSymbol)
			assignedCount++
			continue // Skip the normal matching task flow
		}

		// Mark the task as READY (bypassing the normal delivery gate)
		if err := matchingTask.MarkReady(); err != nil {
			logger.Log("WARN", "Failed to mark COLLECT_SELL task ready for ship with cargo", map[string]interface{}{
				"task":  matchingTask.ID(),
				"ship":  shipSymbol,
				"error": err.Error(),
			})
			continue
		}

		// Persist the task state
		if err := h.taskRepo.Update(ctx, matchingTask); err != nil {
			logger.Log("WARN", "Failed to persist COLLECT_SELL task state", map[string]interface{}{
				"task":  matchingTask.ID(),
				"error": err.Error(),
			})
			continue
		}

		// Assign the task to this ship directly
		err := h.assignTaskToShip(ctx, cmd, matchingTask, ship)
		if err != nil {
			logger.Log("WARN", "Failed to assign COLLECT_SELL task to ship with cargo", map[string]interface{}{
				"task":  matchingTask.ID(),
				"ship":  shipSymbol,
				"error": err.Error(),
			})
			// Task is still READY status, will be picked up in next assignment cycle
			// Enqueue it so it can be assigned to another ship
			h.taskQueue.Enqueue(matchingTask)
			continue
		}

		logger.Log("INFO", "Assigned COLLECT_SELL task to ship with existing cargo", map[string]interface{}{
			"task_id":     matchingTask.ID()[:8],
			"ship":        shipSymbol,
			"cargo_type":  primaryCargo,
			"cargo_units": maxUnits,
		})

		// Remove ship from idle pool
		delete(idleShips, shipSymbol)
		assignedCount++
	}

	return idleShips
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

	// Look up pipeline to get sequence number and product good
	h.mu.RLock()
	pipeline := h.activePipelines[task.PipelineID()]
	h.mu.RUnlock()

	var pipelineNumber int
	var productGood string
	if pipeline != nil {
		pipelineNumber = pipeline.SequenceNumber()
		productGood = pipeline.ProductGood()
	}

	// Create worker command
	workerCmd := &RunManufacturingTaskWorkerCommand{
		ShipSymbol:     shipSymbol,
		Task:           task,
		PlayerID:       cmd.PlayerID,
		ContainerID:    workerContainerID,
		CoordinatorID:  cmd.ContainerID,
		PipelineNumber: pipelineNumber,
		ProductGood:    productGood,
	}

	// Step 1: Persist worker container to DB (synchronous, no start)
	// Container must exist first due to FK constraint on ship_assignments
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
			// Rollback: Delete the PENDING container (StopContainer doesn't work on PENDING containers)
			// This prevents orphaned PENDING containers when ship assignment fails
			if h.containerRemover != nil {
				_ = h.containerRemover.Remove(ctx, workerContainerID, cmd.PlayerID)
			}
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
// This is the event-driven entry point for immediate ship reassignment
func (h *RunParallelManufacturingCoordinatorHandler) handleWorkerContainerCompletion(
	ctx context.Context,
	cmd *RunParallelManufacturingCoordinatorCommand,
	shipSymbol string,
	maxConcurrentTasks int,
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
		taskErr = fmt.Errorf("%s", task.ErrorMessage())
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

	// EVENT-DRIVEN: Assign ALL idle ships to ready tasks (not just the freed ship)
	// This ensures ships that were idle before this completion also get assigned
	h.assignTasks(ctx, cmd, maxConcurrentTasks)
}

// assignFreedShipToTask immediately assigns a freed ship to the next available task
// This provides zero-delay ship reassignment after task completion
func (h *RunParallelManufacturingCoordinatorHandler) assignFreedShipToTask(
	ctx context.Context,
	cmd *RunParallelManufacturingCoordinatorCommand,
	shipSymbol string,
	maxConcurrentTasks int,
) {
	logger := common.LoggerFromContext(ctx)

	// Check if we have room for more task assignments
	h.mu.RLock()
	assignedCount := len(h.assignedTasks)
	h.mu.RUnlock()

	if assignedCount >= maxConcurrentTasks {
		logger.Log("DEBUG", "Max concurrent tasks reached, freed ship will wait", map[string]interface{}{
			"ship":     shipSymbol,
			"assigned": assignedCount,
			"max":      maxConcurrentTasks,
		})
		return
	}

	// Get ready tasks
	readyTasks := h.taskQueue.GetReadyTasks()
	if len(readyTasks) == 0 {
		logger.Log("DEBUG", "No ready tasks for freed ship", map[string]interface{}{
			"ship": shipSymbol,
		})
		return
	}

	// Load the freed ship
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to load freed ship %s: %v", shipSymbol, err), nil)
		return
	}

	// Find the best task for this ship
	for _, task := range readyTasks {
		// Try to assign this task to the freed ship
		err := h.assignTaskToShip(ctx, cmd, task, ship)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to assign task to freed ship: %v", err), nil)
			continue
		}

		logger.Log("INFO", fmt.Sprintf("Immediately assigned task %s (%s) to freed ship %s", task.ID()[:8], task.TaskType(), shipSymbol), map[string]interface{}{
			"task_id":   task.ID()[:8],
			"task_type": string(task.TaskType()),
			"ship":      shipSymbol,
		})
		return
	}

	logger.Log("DEBUG", "No suitable task found for freed ship", map[string]interface{}{
		"ship":        shipSymbol,
		"ready_tasks": len(readyTasks),
	})
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

		// Handle task failure: retry if possible, otherwise mark pipeline as failed
		h.handleTaskFailure(ctx, completion)
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

// handleTaskFailure handles a failed task by retrying if possible
func (h *RunParallelManufacturingCoordinatorHandler) handleTaskFailure(
	ctx context.Context,
	completion TaskCompletion,
) {
	logger := common.LoggerFromContext(ctx)

	if h.taskRepo == nil {
		return
	}

	// Fetch the task from the database
	task, err := h.taskRepo.FindByID(ctx, completion.TaskID)
	if err != nil || task == nil {
		logger.Log("WARN", fmt.Sprintf("Failed to fetch task for retry: %v", err), map[string]interface{}{
			"task_id": completion.TaskID,
		})
		return
	}

	// Check if the task can be retried
	if task.CanRetry() {
		retryCount := task.RetryCount()
		maxRetries := task.MaxRetries()

		// Reset the task for retry (FAILED -> PENDING)
		if err := task.ResetForRetry(); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to reset task for retry: %v", err), map[string]interface{}{
				"task_id": completion.TaskID,
			})
			return
		}

		// Mark as ready (PENDING -> READY)
		if err := task.MarkReady(); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to mark task ready after reset: %v", err), map[string]interface{}{
				"task_id": completion.TaskID,
			})
			return
		}

		// Persist the updated task state
		if err := h.taskRepo.Update(ctx, task); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to persist task retry state: %v", err), map[string]interface{}{
				"task_id": completion.TaskID,
			})
			return
		}

		// Enqueue the task for re-assignment
		h.taskQueue.Enqueue(task)

		logger.Log("INFO", fmt.Sprintf("Task %s scheduled for retry (%d/%d attempts used)",
			completion.TaskID[:8], retryCount, maxRetries), map[string]interface{}{
			"task_id":     completion.TaskID,
			"task_type":   string(task.TaskType()),
			"good":        task.Good(),
			"retry_count": retryCount,
			"max_retries": maxRetries,
		})
	} else {
		// Task has exhausted all retries - mark pipeline as failed
		logger.Log("ERROR", fmt.Sprintf("Task %s permanently failed after %d retries",
			completion.TaskID[:8], task.RetryCount()), map[string]interface{}{
			"task_id":     completion.TaskID,
			"task_type":   string(task.TaskType()),
			"good":        task.Good(),
			"retry_count": task.RetryCount(),
			"max_retries": task.MaxRetries(),
			"pipeline_id": completion.PipelineID,
		})

		// If this is a pipeline task, check if we should fail the pipeline
		if completion.PipelineID != "" {
			h.checkPipelineCompletion(ctx, completion.PipelineID)
		}
	}
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

		// STREAMING MODEL: COLLECT_SELL tasks have no structural dependencies
		// They are gated by SupplyMonitor which checks:
		//   1. Factory supply is HIGH/ABUNDANT
		//   2. At least one delivery has been recorded
		// Skip them here - they're handled entirely by SupplyMonitor
		if task.TaskType() == manufacturing.TaskTypeCollectSell {
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

// updateFactoryStateOnDelivery records a delivery in the factory state tracker.
// If factory supply is not HIGH after delivery, creates new ACQUIRE→DELIVER tasks
// to continue feeding the factory (continuous delivery loop).
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

	// Only process ACQUIRE_DELIVER tasks (atomic buy + deliver to factory)
	if task.TaskType() != manufacturing.TaskTypeAcquireDeliver {
		return
	}

	// Get factory state for this delivery's target
	factorySymbol := task.FactorySymbol()
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
				"factory":              factorySymbol,
				"good":                 task.Good(),
				"all_inputs_delivered": fs.AllInputsDelivered(),
				"ready_for_collection": fs.IsReadyForCollection(),
			})

			// CONTINUOUS DELIVERY LOOP: If factory not ready (supply not HIGH),
			// create new ACQUIRE→DELIVER tasks to continue feeding the factory
			if !fs.IsReadyForCollection() {
				h.createContinuedDeliveryTasks(ctx, task, pipelineID, factorySymbol)
			}
		}
	}
}

// createContinuedDeliveryTasks creates new ACQUIRE_DELIVER atomic task to continue
// feeding a factory when supply is not yet HIGH.
// Uses atomic task to prevent "orphaned cargo" bug where one ship buys and another delivers.
func (h *RunParallelManufacturingCoordinatorHandler) createContinuedDeliveryTasks(
	ctx context.Context,
	completedDeliverTask *manufacturing.ManufacturingTask,
	pipelineID string,
	factorySymbol string,
) {
	logger := common.LoggerFromContext(ctx)

	// Find the source market for the good (use source market from ACQUIRE_DELIVER task)
	sourceMarket := completedDeliverTask.SourceMarket()

	if sourceMarket == "" {
		logger.Log("WARN", "Cannot create continued delivery - no source market found", map[string]interface{}{
			"deliver_task": completedDeliverTask.ID()[:8],
			"good":         completedDeliverTask.Good(),
		})
		return
	}

	// Check if there's already a pending/ready ACQUIRE_DELIVER task for this good
	// to avoid creating duplicates
	existingTasks, err := h.taskRepo.FindByPipelineID(ctx, pipelineID)
	if err == nil {
		for _, t := range existingTasks {
			if t.Good() == completedDeliverTask.Good() &&
				t.TaskType() == manufacturing.TaskTypeAcquireDeliver &&
				(t.Status() == manufacturing.TaskStatusPending ||
					t.Status() == manufacturing.TaskStatusReady ||
					t.Status() == manufacturing.TaskStatusAssigned) {
				logger.Log("DEBUG", "Skipping continued delivery - task already exists", map[string]interface{}{
					"existing_task": t.ID()[:8],
					"good":          t.Good(),
				})
				return
			}
		}
	}

	// Get player ID from completed task
	playerID := completedDeliverTask.PlayerID()

	// Create atomic ACQUIRE_DELIVER task (same ship buys AND delivers)
	acquireDeliverTask := manufacturing.NewAcquireDeliverTask(
		pipelineID,
		playerID,
		completedDeliverTask.Good(),
		sourceMarket,
		factorySymbol,
		nil, // No dependencies for continued delivery
	)

	// Mark as READY (no dependencies)
	if err := acquireDeliverTask.MarkReady(); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to mark acquire_deliver task ready: %v", err), nil)
		return
	}

	// Persist task
	if err := h.taskRepo.Create(ctx, acquireDeliverTask); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to persist acquire_deliver task: %v", err), nil)
		return
	}

	// Enqueue the task
	h.taskQueue.Enqueue(acquireDeliverTask)

	logger.Log("INFO", "Created continued delivery task (atomic)", map[string]interface{}{
		"good":    completedDeliverTask.Good(),
		"source":  sourceMarket,
		"factory": factorySymbol,
		"task_id": acquireDeliverTask.ID()[:8],
	})
}

// checkPipelineCompletion checks if a pipeline is complete and removes it from active
// EVENT-DRIVEN: When a pipeline completes, triggers an opportunity rescan since our
// actions may have changed market supply levels.
// MaxFinalCollections is the number of successful COLLECT_SELL cycles for the final product
// before a pipeline is considered complete. This allows:
// - Enough cycles to profit from setup cost
// - Regular discovery of new opportunities
// - Natural exit when market conditions change
const MaxFinalCollections = 3

func (h *RunParallelManufacturingCoordinatorHandler) checkPipelineCompletion(
	ctx context.Context,
	pipelineID string,
) {
	logger := common.LoggerFromContext(ctx)

	var pipelineCompleted bool
	var pipelineFailed bool

	// Scope the lock to just state modification
	func() {
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

		// Count completed COLLECT_SELL tasks for the FINAL product
		// This tracks how many successful collection cycles we've done
		finalCollections := 0
		for _, task := range tasks {
			if task.TaskType() == manufacturing.TaskTypeCollectSell &&
				task.Good() == pipeline.ProductGood() &&
				task.Status() == manufacturing.TaskStatusCompleted {
				finalCollections++
			}
		}

		// Check if sell market is saturated (supply HIGH or ABUNDANT)
		// If saturated, complete early to avoid wasting resources
		sellMarketSaturated := h.isSellMarketSaturated(ctx, pipeline.SellMarket(), pipeline.ProductGood(), pipeline.PlayerID())

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

		// COMPLETION CONDITIONS:
		// 1. All tasks completed (original behavior)
		// 2. Reached max final collections (new: collection count limit)
		// 3. Sell market saturated after at least 1 collection (new: market-aware exit)
		shouldComplete := allCompleted ||
			(finalCollections >= MaxFinalCollections) ||
			(finalCollections >= 1 && sellMarketSaturated)

		if shouldComplete && finalCollections > 0 {
			completionReason := "all_tasks_done"
			if finalCollections >= MaxFinalCollections {
				completionReason = fmt.Sprintf("reached_%d_collections", MaxFinalCollections)
			} else if sellMarketSaturated {
				completionReason = "sell_market_saturated"
			}

			logger.Log("INFO", fmt.Sprintf("Pipeline ready for completion: %s", completionReason), map[string]interface{}{
				"pipeline_id":       pipelineID,
				"good":              pipeline.ProductGood(),
				"final_collections": finalCollections,
				"market_saturated":  sellMarketSaturated,
				"reason":            completionReason,
			})

			// Mark pipeline as completed
			if err := pipeline.Complete(); err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to mark pipeline completed: %v", err), nil)
			} else {
				if h.pipelineRepo != nil {
					_ = h.pipelineRepo.Update(ctx, pipeline)
				}
				delete(h.activePipelines, pipelineID)
				pipelineCompleted = true

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

				logger.Log("INFO", fmt.Sprintf("Pipeline %s completed successfully after %d collections!", pipelineID[:8], finalCollections), map[string]interface{}{
					"pipeline_id":       pipelineID,
					"good":              pipeline.ProductGood(),
					"final_collections": finalCollections,
					"total_cost":        pipeline.TotalCost(),
					"total_revenue":     pipeline.TotalRevenue(),
					"net_profit":        netProfit,
					"completion_reason": completionReason,
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
				pipelineFailed = true

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
	}()

	// EVENT-DRIVEN: When a pipeline completes or fails, rescan for new opportunities
	// Our manufacturing actions may have changed supply levels, creating new opportunities
	if (pipelineCompleted || pipelineFailed) && h.cmdContext != nil {
		logger.Log("INFO", "Pipeline finished - triggering opportunity rescan", map[string]interface{}{
			"pipeline_id": pipelineID,
			"completed":   pipelineCompleted,
			"failed":      pipelineFailed,
		})
		h.scanAndCreatePipelines(ctx, h.cmdContext.cmd, h.cmdContext.minPurchasePrice, h.cmdContext.maxPipelines)
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

	// Start any PLANNING pipelines (they were never started before crash)
	startedPipelines := make([]*manufacturing.ManufacturingPipeline, 0)
	for _, pipeline := range pipelines {
		if pipeline.Status() == manufacturing.PipelineStatusPlanning {
			if err := pipeline.Start(); err != nil {
				logger.Log("WARN", fmt.Sprintf("Failed to start recovered PLANNING pipeline %s: %v", pipeline.ID()[:8], err), nil)
			} else {
				logger.Log("INFO", fmt.Sprintf("Started recovered PLANNING pipeline %s", pipeline.ID()[:8]), nil)
				startedPipelines = append(startedPipelines, pipeline)
			}
		}
	}

	// Persist status changes for started pipelines
	for _, pipeline := range startedPipelines {
		if h.pipelineRepo != nil {
			_ = h.pipelineRepo.Update(ctx, pipeline)
		}
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
	interruptedCount := 0
	for _, task := range tasks {
		// Step 2a: Reset interrupted tasks (ASSIGNED or EXECUTING) back to READY
		// These tasks were in-flight when the daemon was interrupted
		if task.Status() == manufacturing.TaskStatusAssigned {
			shipSymbol := task.AssignedShip() // Save before rollback
			if err := task.RollbackAssignment(); err == nil {
				_ = h.taskRepo.Update(ctx, task)
				interruptedCount++
				logger.Log("INFO", fmt.Sprintf("Reset interrupted ASSIGNED task %s (%s)", task.ID()[:8], task.TaskType()), nil)
				// Release ship assignment in DB so ship becomes idle again
				if shipSymbol != "" && h.shipAssignmentRepo != nil {
					_ = h.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "task_recovery")
				}
			}
		}

		if task.Status() == manufacturing.TaskStatusExecuting {
			shipSymbol := task.AssignedShip() // Save for logging (preserved after rollback)
			if err := task.RollbackExecution(); err == nil {
				_ = h.taskRepo.Update(ctx, task)
				interruptedCount++
				logger.Log("INFO", fmt.Sprintf("Reset interrupted EXECUTING task %s (%s) - ship %s preserved for re-assignment",
					task.ID()[:8], task.TaskType(), shipSymbol), nil)
				// Release ship assignment in DB so ship becomes idle again
				// NOTE: task.AssignedShip() is preserved for SELL tasks to find the right ship
				if shipSymbol != "" && h.shipAssignmentRepo != nil {
					_ = h.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "task_recovery")
				}
			}
		}

		// Step 2b: Re-evaluate PENDING tasks for readiness
		if task.Status() == manufacturing.TaskStatusPending {
			// STREAMING MODEL: COLLECT_SELL tasks have no structural dependencies
			// They are gated by SupplyMonitor which checks:
			//   1. Factory supply is HIGH/ABUNDANT
			//   2. At least one delivery has been recorded
			// Skip them here - they're handled entirely by SupplyMonitor
			if task.TaskType() == manufacturing.TaskTypeCollectSell {
				continue
			}

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

		// Step 2c: Enqueue all READY tasks
		if task.Status() == manufacturing.TaskStatusReady {
			// COLLECT_SELL tasks need special handling - reset to PENDING
			// so SupplyMonitor can re-verify factory supply is still ready.
			// Factory supply may have changed while we were offline.
			if task.TaskType() == manufacturing.TaskTypeCollectSell {
				task.ResetToPending()
				if h.taskRepo != nil {
					_ = h.taskRepo.Update(ctx, task)
				}
				logger.Log("DEBUG", fmt.Sprintf("Reset COLLECT_SELL task %s to PENDING for supply re-check", task.ID()[:8]), nil)
				continue // Don't enqueue - SupplyMonitor will handle it
			}

			h.taskQueue.Enqueue(task)
			readyCount++
		}
	}

	// Step 2d: Recover FAILED tasks that can be retried
	// FindIncomplete excludes FAILED tasks, so we need to load and retry them separately
	retriedCount := 0
	failedTasks, err := h.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusFailed)
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("Failed to load failed tasks for retry: %v", err), nil)
	} else {
		for _, task := range failedTasks {
			if task.CanRetry() {
				retryCount := task.RetryCount()

				// Reset the task for retry (FAILED -> PENDING)
				if err := task.ResetForRetry(); err != nil {
					logger.Log("WARN", fmt.Sprintf("Failed to reset task %s for retry: %v", task.ID()[:8], err), nil)
					continue
				}

				// COLLECT_SELL tasks stay PENDING - SupplyMonitor will mark them READY
				// when factory supply is confirmed. Don't mark READY blindly.
				if task.TaskType() == manufacturing.TaskTypeCollectSell {
					if err := h.taskRepo.Update(ctx, task); err != nil {
						logger.Log("WARN", fmt.Sprintf("Failed to persist task %s: %v", task.ID()[:8], err), nil)
					}
					logger.Log("INFO", fmt.Sprintf("Reset FAILED COLLECT_SELL task %s to PENDING for supply re-check (%d/%d attempts used)",
						task.ID()[:8], retryCount, task.MaxRetries()), nil)
					continue // Don't mark READY or enqueue - SupplyMonitor handles it
				}

				// Mark as ready (PENDING -> READY) for non-COLLECT_SELL tasks
				if err := task.MarkReady(); err != nil {
					logger.Log("WARN", fmt.Sprintf("Failed to mark task %s ready: %v", task.ID()[:8], err), nil)
					continue
				}

				// Persist the updated task state
				if err := h.taskRepo.Update(ctx, task); err != nil {
					logger.Log("WARN", fmt.Sprintf("Failed to persist task %s retry state: %v", task.ID()[:8], err), nil)
					continue
				}

				// Enqueue for assignment
				h.taskQueue.Enqueue(task)
				retriedCount++
				readyCount++

				logger.Log("INFO", fmt.Sprintf("Recovered FAILED task %s (%s %s) for retry (%d/%d attempts used)",
					task.ID()[:8], task.TaskType(), task.Good(), retryCount, task.MaxRetries()), nil)
			}
		}
	}

	logger.Log("INFO", fmt.Sprintf("Recovered %d tasks: %d ready, %d reset from interrupted state, %d retried from failed",
		len(tasks)+retriedCount, readyCount, interruptedCount, retriedCount), nil)

	// Step 3: Load factory states (both pending AND ready)
	// We need to load ALL factory states so the supply monitor can re-check them.
	// Ready states may need to be reset if supply dropped while we were offline.
	if h.factoryStateRepo != nil {
		// Load pending states
		pendingStates, err := h.factoryStateRepo.FindPending(ctx, playerID)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to load pending factory states: %v", err), nil)
		} else {
			for _, state := range pendingStates {
				h.factoryTracker.LoadState(state)
			}
		}

		// Also load ready states - supply monitor will re-check their supply levels
		readyStates, err := h.factoryStateRepo.FindReadyForCollection(ctx, playerID)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to load ready factory states: %v", err), nil)
		} else {
			for _, state := range readyStates {
				h.factoryTracker.LoadState(state)
			}
		}

		totalStates := len(pendingStates) + len(readyStates)
		logger.Log("INFO", fmt.Sprintf("Recovered %d factory states (%d pending, %d ready)", totalStates, len(pendingStates), len(readyStates)), nil)
	}

	logger.Log("INFO", "State recovery complete", map[string]interface{}{
		"pipelines":      len(h.activePipelines),
		"tasks_in_queue": h.taskQueue.Size(),
	})

	return nil
}

// findBestSellMarket finds the best market to sell cargo.
// Used for creating ad-hoc sell tasks for orphaned cargo.
func (h *RunParallelManufacturingCoordinatorHandler) findBestSellMarket(ctx context.Context, currentLocation string, good string, playerID int) (string, error) {
	if h.marketRepo == nil {
		return "", fmt.Errorf("no market repository configured")
	}

	// Extract system from waypoint symbol (e.g., X1-YZ19-I60 -> X1-YZ19)
	system := extractSystemFromWaypoint(currentLocation)

	// Find best market buying this good in the system
	result, err := h.marketRepo.FindBestMarketBuying(ctx, good, system, playerID)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("no market found buying %s in system %s", good, system)
	}

	return result.WaypointSymbol, nil
}

// isSellMarketSaturated checks if the sell market has HIGH or ABUNDANT supply.
// Returns true if we should NOT sell to this market (would get minimal prices).
func (h *RunParallelManufacturingCoordinatorHandler) isSellMarketSaturated(ctx context.Context, sellMarket string, good string, playerID int) bool {
	if h.marketRepo == nil {
		return false // Can't check, assume not saturated
	}

	marketData, err := h.marketRepo.GetMarketData(ctx, sellMarket, playerID)
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

// StuckPipelineThreshold is how long a pipeline can run without progress before being recycled.
const StuckPipelineThreshold = 30 * time.Minute

// StuckPipelineFailedTaskThreshold is the max failed tasks before a pipeline is considered stuck.
const StuckPipelineFailedTaskThreshold = 5

// detectAndRecycleStuckPipelines finds pipelines that aren't making progress and recycles them.
// A pipeline is considered STUCK if ALL of the following are true:
//   - Age > 30 minutes (StuckPipelineThreshold)
//   - Final collections == 0 (no successful product sales)
//   - One of: failed tasks > 5, OR no active tasks (all pending/failed, none executing)
//
// When a pipeline is stuck:
//  1. Mark all pending tasks as CANCELLED
//  2. Mark the pipeline as CANCELLED
//  3. Remove from active pipelines (frees slot for new pipeline)
func (h *RunParallelManufacturingCoordinatorHandler) detectAndRecycleStuckPipelines(
	ctx context.Context,
	cmd *RunParallelManufacturingCoordinatorCommand,
) {
	logger := common.LoggerFromContext(ctx)

	if h.taskRepo == nil || h.pipelineRepo == nil {
		return
	}

	h.mu.RLock()
	pipelinesCopy := make(map[string]*manufacturing.ManufacturingPipeline, len(h.activePipelines))
	for id, p := range h.activePipelines {
		pipelinesCopy[id] = p
	}
	h.mu.RUnlock()

	if len(pipelinesCopy) == 0 {
		return
	}

	now := h.clock.Now()
	stuckPipelines := make([]string, 0)

	for pipelineID, pipeline := range pipelinesCopy {
		// Check age
		age := now.Sub(pipeline.CreatedAt())
		if age < StuckPipelineThreshold {
			continue
		}

		// Load tasks for this pipeline
		tasks, err := h.taskRepo.FindByPipelineID(ctx, pipelineID)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to load tasks for stuck detection: %v", err), nil)
			continue
		}

		// Count task states
		var finalCollections, failedTasks, activeTasks int
		for _, task := range tasks {
			// Count successful final product collections
			if task.TaskType() == manufacturing.TaskTypeCollectSell &&
				task.Good() == pipeline.ProductGood() &&
				task.Status() == manufacturing.TaskStatusCompleted {
				finalCollections++
			}

			// Count failed tasks
			if task.Status() == manufacturing.TaskStatusFailed {
				failedTasks++
			}

			// Count active tasks (assigned or executing)
			if task.Status() == manufacturing.TaskStatusAssigned ||
				task.Status() == manufacturing.TaskStatusExecuting {
				activeTasks++
			}
		}

		// Skip if we have successful collections - pipeline is working
		if finalCollections > 0 {
			continue
		}

		// Check stuck conditions: too many failures OR no active tasks
		isStuck := false
		stuckReason := ""

		if failedTasks >= StuckPipelineFailedTaskThreshold {
			isStuck = true
			stuckReason = fmt.Sprintf("%d failed tasks", failedTasks)
		} else if activeTasks == 0 {
			// No tasks executing but also no collections - pipeline is stalled
			isStuck = true
			stuckReason = "no active tasks"
		}

		if isStuck {
			logger.Log("WARN", "Detected stuck pipeline", map[string]interface{}{
				"pipeline_id": pipelineID[:8],
				"good":        pipeline.ProductGood(),
				"age_minutes": int(age.Minutes()),
				"reason":      stuckReason,
				"failed":      failedTasks,
				"active":      activeTasks,
			})
			stuckPipelines = append(stuckPipelines, pipelineID)
		}
	}

	// Recycle stuck pipelines
	for _, pipelineID := range stuckPipelines {
		h.recyclePipeline(ctx, pipelineID, cmd.PlayerID)
	}

	// If we recycled any pipelines, trigger opportunity scan to fill the slots
	if len(stuckPipelines) > 0 {
		logger.Log("INFO", fmt.Sprintf("Recycled %d stuck pipelines - scanning for new opportunities", len(stuckPipelines)), nil)
		h.scanAndCreatePipelines(ctx, cmd, h.cmdContext.minPurchasePrice, h.cmdContext.maxPipelines)
	}
}

// recyclePipeline cancels a stuck pipeline and frees its slot.
func (h *RunParallelManufacturingCoordinatorHandler) recyclePipeline(
	ctx context.Context,
	pipelineID string,
	playerID int,
) {
	logger := common.LoggerFromContext(ctx)

	// Load tasks and cancel pending ones
	tasks, err := h.taskRepo.FindByPipelineID(ctx, pipelineID)
	if err == nil {
		for _, task := range tasks {
			// Cancel pending/ready tasks
			if task.Status() == manufacturing.TaskStatusPending ||
				task.Status() == manufacturing.TaskStatusReady {
				if err := task.Cancel("pipeline recycled"); err == nil {
					_ = h.taskRepo.Update(ctx, task)
				}
			}

			// Remove from queue if queued
			h.taskQueue.Remove(task.ID())
		}
	}

	// Remove factory states for this pipeline from the in-memory tracker
	// The database records remain for historical tracking
	if h.factoryTracker != nil {
		h.factoryTracker.RemovePipeline(pipelineID)
	}

	// Mark pipeline as cancelled
	h.mu.Lock()
	pipeline, exists := h.activePipelines[pipelineID]
	if exists {
		if err := pipeline.Cancel(); err == nil {
			if h.pipelineRepo != nil {
				_ = h.pipelineRepo.Update(ctx, pipeline)
			}

			// Record metrics
			metrics.RecordManufacturingPipelineCompletion(
				pipeline.PlayerID(),
				pipeline.ProductGood(),
				"cancelled",
				pipeline.RuntimeDuration(),
				0,
			)
		}
		delete(h.activePipelines, pipelineID)
	}
	h.mu.Unlock()

	logger.Log("INFO", "Recycled stuck pipeline", map[string]interface{}{
		"pipeline_id": pipelineID[:8],
		"good":        pipeline.ProductGood(),
	})
}


package manufacturing

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// TaskAssigner assigns ready tasks to idle ships
type TaskAssigner interface {
	// AssignTasks assigns ready tasks to idle ships
	AssignTasks(ctx context.Context, params AssignParams) (int, error)

	// GetTaskSourceLocation returns the waypoint where task starts
	GetTaskSourceLocation(ctx context.Context, task *manufacturing.ManufacturingTask, playerID int) *shared.Waypoint

	// FindClosestShip finds the ship closest to target waypoint
	FindClosestShip(ships map[string]*navigation.Ship, target *shared.Waypoint) (*navigation.Ship, string)

	// IsSellMarketSaturated checks if market has HIGH/ABUNDANT supply
	IsSellMarketSaturated(ctx context.Context, sellMarket, good string, playerID int) bool

	// ReconcileAssignedTasksWithDB syncs in-memory state with DB
	ReconcileAssignedTasksWithDB(ctx context.Context, playerID int)
}

// AssignParams contains parameters for task assignment
type AssignParams struct {
	PlayerID           int
	MaxConcurrentTasks int
}

// TaskAssignmentManager implements TaskAssigner
type TaskAssignmentManager struct {
	taskRepo           manufacturing.TaskRepository
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo domainContainer.ShipAssignmentRepository
	marketRepo         market.MarketRepository
	waypointProvider   system.IWaypointProvider
	taskQueue          *services.TaskQueue

	// Runtime state
	mu             sync.RWMutex
	assignedTasks  map[string]string // taskID -> shipSymbol
	taskContainers map[string]string // taskID -> containerID

	// Dependencies (will be set via callback)
	workerManager    WorkerManager
	orphanedHandler  OrphanedCargoManager
	pipelineManager  PipelineManager
	activePipelines  func() map[string]*manufacturing.ManufacturingPipeline
}

// NewTaskAssignmentManager creates a new task assignment manager
func NewTaskAssignmentManager(
	taskRepo manufacturing.TaskRepository,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo domainContainer.ShipAssignmentRepository,
	marketRepo market.MarketRepository,
	waypointProvider system.IWaypointProvider,
	taskQueue *services.TaskQueue,
) *TaskAssignmentManager {
	return &TaskAssignmentManager{
		taskRepo:           taskRepo,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		marketRepo:         marketRepo,
		waypointProvider:   waypointProvider,
		taskQueue:          taskQueue,
		assignedTasks:      make(map[string]string),
		taskContainers:     make(map[string]string),
	}
}

// SetWorkerManager sets the worker manager dependency
func (m *TaskAssignmentManager) SetWorkerManager(wm WorkerManager) {
	m.workerManager = wm
}

// SetOrphanedHandler sets the orphaned cargo handler dependency
func (m *TaskAssignmentManager) SetOrphanedHandler(oh OrphanedCargoManager) {
	m.orphanedHandler = oh
}

// SetPipelineManager sets the pipeline manager dependency
func (m *TaskAssignmentManager) SetPipelineManager(pm PipelineManager) {
	m.pipelineManager = pm
}

// SetActivePipelinesGetter sets the function to get active pipelines
func (m *TaskAssignmentManager) SetActivePipelinesGetter(getter func() map[string]*manufacturing.ManufacturingPipeline) {
	m.activePipelines = getter
}

// GetAssignedTasks returns the assigned tasks map
func (m *TaskAssignmentManager) GetAssignedTasks() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]string, len(m.assignedTasks))
	for k, v := range m.assignedTasks {
		result[k] = v
	}
	return result
}

// GetTaskContainers returns the task containers map
func (m *TaskAssignmentManager) GetTaskContainers() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]string, len(m.taskContainers))
	for k, v := range m.taskContainers {
		result[k] = v
	}
	return result
}

// TrackAssignment tracks a task assignment in memory
func (m *TaskAssignmentManager) TrackAssignment(taskID, shipSymbol, containerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assignedTasks[taskID] = shipSymbol
	m.taskContainers[taskID] = containerID
}

// UntrackAssignment removes a task assignment from memory
func (m *TaskAssignmentManager) UntrackAssignment(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.assignedTasks, taskID)
	delete(m.taskContainers, taskID)
}

// GetAssignmentCount returns the number of assigned tasks
func (m *TaskAssignmentManager) GetAssignmentCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.assignedTasks)
}

// ClearAssignments clears all task assignments
func (m *TaskAssignmentManager) ClearAssignments() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assignedTasks = make(map[string]string)
	m.taskContainers = make(map[string]string)
}

// AssignTasks assigns ready tasks to idle ships using task type reservation
// to prevent starvation. Minimum workers are reserved for each task type.
func (m *TaskAssignmentManager) AssignTasks(ctx context.Context, params AssignParams) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Reconcile with DB first
	m.ReconcileAssignedTasksWithDB(ctx, params.PlayerID)

	// Check assignment count
	assignedCount := m.GetAssignmentCount()
	if assignedCount >= params.MaxConcurrentTasks {
		return 0, nil
	}

	// Get idle ships
	playerID := shared.MustNewPlayerID(params.PlayerID)
	_, idleShipSymbols, err := contract.FindIdleLightHaulers(
		ctx, playerID, m.shipRepo, m.shipAssignmentRepo,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to find idle ships: %w", err)
	}

	if len(idleShipSymbols) == 0 {
		return 0, nil
	}

	// Load ship entities
	idleShips := make(map[string]*navigation.Ship)
	for _, symbol := range idleShipSymbols {
		ship, err := m.shipRepo.FindBySymbol(ctx, symbol, playerID)
		if err == nil {
			idleShips[symbol] = ship
		}
	}

	// Handle ships with existing cargo first
	if m.orphanedHandler != nil {
		idleShips, _ = m.orphanedHandler.HandleShipsWithExistingCargo(ctx, OrphanedCargoParams{
			IdleShips:          idleShips,
			PlayerID:           params.PlayerID,
			MaxConcurrentTasks: params.MaxConcurrentTasks,
		})
	}

	// Get ready tasks
	readyTasks := m.taskQueue.GetReadyTasks()
	if len(readyTasks) == 0 || len(idleShips) == 0 {
		return 0, nil
	}

	// Refresh assigned count
	assignedCount = m.GetAssignmentCount()

	// Count currently assigned tasks by type from database for reservation logic
	assignedByType := m.countAssignedByType(ctx, params.PlayerID)
	collectSellCount := assignedByType[manufacturing.TaskTypeCollectSell]
	acquireDeliverCount := assignedByType[manufacturing.TaskTypeAcquireDeliver]

	// Check if we have ready tasks of each type
	hasReadyCollectSell := m.taskQueue.HasReadyTasksByType(manufacturing.TaskTypeCollectSell)
	hasReadyAcquireDeliver := m.taskQueue.HasReadyTasksByType(manufacturing.TaskTypeAcquireDeliver)

	// Track factory+good assignment counts for balancing within ACQUIRE_DELIVER
	factoryGoodCounts := make(map[string]int)
	if m.taskRepo != nil {
		assignedTasksList, _ := m.taskRepo.FindByStatus(ctx, params.PlayerID, manufacturing.TaskStatusAssigned)
		for _, t := range assignedTasksList {
			if t.TaskType() == manufacturing.TaskTypeAcquireDeliver {
				factoryGoodKey := t.FactorySymbol() + ":" + t.Good()
				factoryGoodCounts[factoryGoodKey]++
			}
		}
	}

	tasksAssigned := 0

	// Assign tasks with task type reservation
	for _, task := range readyTasks {
		if assignedCount >= params.MaxConcurrentTasks || len(idleShips) == 0 {
			break
		}

		// Task Type Reservation: Prevent starvation by reserving minimum workers for each type
		// Skip this task if it would starve the other task type
		if !m.shouldAssignWithReservation(task.TaskType(), collectSellCount, acquireDeliverCount, hasReadyCollectSell, hasReadyAcquireDeliver) {
			continue
		}

		// Balance check for ACQUIRE_DELIVER within factory+good combinations
		if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
			factoryGoodKey := task.FactorySymbol() + ":" + task.Good()
			currentCount := factoryGoodCounts[factoryGoodKey]

			minCount := currentCount
			for _, count := range factoryGoodCounts {
				if count < minCount {
					minCount = count
				}
			}

			if currentCount > minCount && currentCount >= 2 {
				continue
			}
		}

		// Pre-flight checks for COLLECT_SELL tasks
		if task.TaskType() == manufacturing.TaskTypeCollectSell {
			// Check 1: Factory must have HIGH/ABUNDANT supply to collect
			if !m.IsFactorySupplyFavorable(ctx, task.FactorySymbol(), task.Good(), params.PlayerID) {
				logger.Log("DEBUG", "Skipping COLLECT_SELL - factory supply not HIGH/ABUNDANT", map[string]interface{}{
					"task_id": task.ID()[:8],
					"factory": task.FactorySymbol(),
					"good":    task.Good(),
				})
				task.ResetToPending()
				if m.taskRepo != nil {
					_ = m.taskRepo.Update(ctx, task)
				}
				continue
			}

			// Check 2: Sell market must not be saturated
			if m.IsSellMarketSaturated(ctx, task.TargetMarket(), task.Good(), params.PlayerID) {
				logger.Log("DEBUG", "Skipping COLLECT_SELL - sell market saturated", map[string]interface{}{
					"task_id": task.ID()[:8],
				})
				task.ResetToPending()
				if m.taskRepo != nil {
					_ = m.taskRepo.Update(ctx, task)
				}
				continue
			}
		}

		// Find closest ship
		sourceLocation := m.GetTaskSourceLocation(ctx, task, params.PlayerID)
		selectedShip, selectedSymbol := m.FindClosestShip(idleShips, sourceLocation)

		if selectedShip == nil {
			continue
		}

		// Assign via worker manager
		if m.workerManager != nil {
			err := m.workerManager.AssignTaskToShip(ctx, AssignTaskParams{
				Task:     task,
				Ship:     selectedShip,
				PlayerID: params.PlayerID,
			})
			if err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to assign task: %v", err), nil)
				delete(idleShips, selectedSymbol)
				continue
			}
		}

		// Record task assignment metrics
		metrics.RecordManufacturingTaskAssignment(params.PlayerID, string(task.TaskType()))

		// Update tracking counts
		if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
			factoryGoodKey := task.FactorySymbol() + ":" + task.Good()
			factoryGoodCounts[factoryGoodKey]++
			acquireDeliverCount++
		} else if task.TaskType() == manufacturing.TaskTypeCollectSell {
			collectSellCount++
		}

		delete(idleShips, selectedSymbol)
		assignedCount++
		tasksAssigned++

		logger.Log("INFO", fmt.Sprintf("Assigned task %s (%s) to ship %s", task.ID()[:8], task.TaskType(), selectedSymbol), nil)
	}

	return tasksAssigned, nil
}

// countAssignedByType returns counts of currently assigned tasks by task type
func (m *TaskAssignmentManager) countAssignedByType(ctx context.Context, playerID int) map[manufacturing.TaskType]int {
	counts := make(map[manufacturing.TaskType]int)

	if m.taskRepo == nil {
		return counts
	}

	// Get ASSIGNED tasks
	assignedTasks, err := m.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusAssigned)
	if err == nil {
		for _, task := range assignedTasks {
			counts[task.TaskType()]++
		}
	}

	// Get EXECUTING tasks (also count as assigned for reservation purposes)
	executingTasks, err := m.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusExecuting)
	if err == nil {
		for _, task := range executingTasks {
			counts[task.TaskType()]++
		}
	}

	return counts
}

// shouldAssignWithReservation checks if a task should be assigned based on reservation rules.
// It ensures minimum workers are reserved for each task type to prevent starvation.
//
// Logic:
//   - If BOTH types are below minimum AND BOTH have ready tasks → allow either (break deadlock)
//   - If assigning ACQUIRE_DELIVER: Skip if COLLECT_SELL is below minimum AND has ready tasks
//   - If assigning COLLECT_SELL: Skip if ACQUIRE_DELIVER is below minimum AND has ready tasks
//
// This ensures both task types always get their minimum workers when tasks are available,
// while preventing a deadlock where both types block each other when starting from zero.
func (m *TaskAssignmentManager) shouldAssignWithReservation(
	taskType manufacturing.TaskType,
	collectSellCount int,
	acquireDeliverCount int,
	hasReadyCollectSell bool,
	hasReadyAcquireDeliver bool,
) bool {
	// Deadlock prevention: If BOTH counts are below minimum and BOTH have ready tasks,
	// allow either type to be assigned (first come, first served based on priority queue).
	// This breaks the chicken-and-egg problem where each type blocks the other.
	bothBelowMinimum := collectSellCount < manufacturing.MinCollectSellWorkers &&
		acquireDeliverCount < manufacturing.MinAcquireDeliverWorkers
	bothHaveReady := hasReadyCollectSell && hasReadyAcquireDeliver

	if bothBelowMinimum && bothHaveReady {
		// Allow any task type to break the deadlock
		return true
	}

	switch taskType {
	case manufacturing.TaskTypeAcquireDeliver:
		// Skip ACQUIRE_DELIVER if COLLECT_SELL is starved (below minimum) and has ready tasks
		if collectSellCount < manufacturing.MinCollectSellWorkers && hasReadyCollectSell {
			return false
		}
		return true

	case manufacturing.TaskTypeCollectSell:
		// Skip COLLECT_SELL if ACQUIRE_DELIVER is starved (below minimum) and has ready tasks
		if acquireDeliverCount < manufacturing.MinAcquireDeliverWorkers && hasReadyAcquireDeliver {
			return false
		}
		return true

	case manufacturing.TaskTypeLiquidate:
		// Liquidation tasks always have high priority - don't skip
		return true

	default:
		return true
	}
}

// GetTaskSourceLocation returns the waypoint where the task starts
func (m *TaskAssignmentManager) GetTaskSourceLocation(ctx context.Context, task *manufacturing.ManufacturingTask, playerID int) *shared.Waypoint {
	var symbol string

	switch task.TaskType() {
	case manufacturing.TaskTypeAcquireDeliver:
		symbol = task.SourceMarket()
	case manufacturing.TaskTypeCollectSell:
		symbol = task.FactorySymbol()
	case manufacturing.TaskTypeLiquidate:
		symbol = task.TargetMarket()
	}

	if symbol == "" {
		return nil
	}

	// Look up coordinates
	if m.waypointProvider != nil {
		systemSymbol := extractSystemFromWaypoint(symbol)
		waypoint, err := m.waypointProvider.GetWaypoint(ctx, symbol, systemSymbol, playerID)
		if err == nil && waypoint != nil {
			return waypoint
		}
	}

	return &shared.Waypoint{Symbol: symbol, X: 0, Y: 0}
}

// extractSystemFromWaypoint extracts system symbol from waypoint
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

// FindClosestShip finds the closest ship to a waypoint
func (m *TaskAssignmentManager) FindClosestShip(
	ships map[string]*navigation.Ship,
	target *shared.Waypoint,
) (*navigation.Ship, string) {
	if target == nil || len(ships) == 0 {
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

// IsSellMarketSaturated checks if the sell market has HIGH or ABUNDANT supply
func (m *TaskAssignmentManager) IsSellMarketSaturated(ctx context.Context, sellMarket, good string, playerID int) bool {
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

// IsFactorySupplyFavorable checks if the factory has ABUNDANT supply for collection.
// We require ABUNDANT (not just HIGH) to START a task, giving a buffer for supply drops during navigation.
// The executor will still collect if supply is HIGH when the ship arrives.
// This prevents assigning ships to collect when supply might drop to MODERATE during the trip.
func (m *TaskAssignmentManager) IsFactorySupplyFavorable(ctx context.Context, factorySymbol, good string, playerID int) bool {
	if m.marketRepo == nil {
		return true // Assume favorable if we can't check
	}

	marketData, err := m.marketRepo.GetMarketData(ctx, factorySymbol, playerID)
	if err != nil || marketData == nil {
		return true // Assume favorable if we can't check
	}

	tradeGood := marketData.FindGood(good)
	if tradeGood == nil || tradeGood.Supply() == nil {
		return true // Assume favorable if we can't check
	}

	supply := *tradeGood.Supply()
	return supply == "ABUNDANT" // Require ABUNDANT to start, executor allows HIGH on arrival
}

// ReconcileAssignedTasksWithDB syncs in-memory state with DB.
// This function ensures the in-memory tracking matches database state by:
// 1. Loading ASSIGNED/EXECUTING tasks from DB into memory (handles coordinator restarts)
// 2. Removing stale entries that no longer exist or have completed
func (m *TaskAssignmentManager) ReconcileAssignedTasksWithDB(ctx context.Context, playerID int) {
	if m.taskRepo == nil {
		return
	}

	logger := common.LoggerFromContext(ctx)

	// Step 1: Load ASSIGNED tasks from DB into memory (critical for restart recovery)
	assignedTasks, err := m.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusAssigned)
	if err == nil {
		m.mu.Lock()
		added := 0
		for _, task := range assignedTasks {
			if task.AssignedShip() != "" {
				if _, exists := m.assignedTasks[task.ID()]; !exists {
					m.assignedTasks[task.ID()] = task.AssignedShip()
					added++
				}
			}
		}
		m.mu.Unlock()
		if added > 0 {
			logger.Log("DEBUG", fmt.Sprintf("Reconciled: loaded %d ASSIGNED tasks from DB", added), nil)
		}
	}

	// Step 2: Load EXECUTING tasks from DB into memory
	executingTasks, err := m.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusExecuting)
	if err == nil {
		m.mu.Lock()
		added := 0
		for _, task := range executingTasks {
			if task.AssignedShip() != "" {
				if _, exists := m.assignedTasks[task.ID()]; !exists {
					m.assignedTasks[task.ID()] = task.AssignedShip()
					added++
				}
			}
		}
		m.mu.Unlock()
		if added > 0 {
			logger.Log("DEBUG", fmt.Sprintf("Reconciled: loaded %d EXECUTING tasks from DB", added), nil)
		}
	}

	// Step 3: Remove stale entries (tasks that completed or no longer exist)
	taskIDs := make([]string, 0)
	m.mu.RLock()
	for taskID := range m.assignedTasks {
		taskIDs = append(taskIDs, taskID)
	}
	m.mu.RUnlock()

	if len(taskIDs) == 0 {
		return
	}

	staleTaskIDs := make([]string, 0)
	for _, taskID := range taskIDs {
		task, err := m.taskRepo.FindByID(ctx, taskID)
		if err != nil || task == nil {
			staleTaskIDs = append(staleTaskIDs, taskID)
			continue
		}

		if task.Status() != manufacturing.TaskStatusAssigned && task.Status() != manufacturing.TaskStatusExecuting {
			staleTaskIDs = append(staleTaskIDs, taskID)
		}
	}

	if len(staleTaskIDs) > 0 {
		m.mu.Lock()
		for _, taskID := range staleTaskIDs {
			delete(m.assignedTasks, taskID)
			delete(m.taskContainers, taskID)
		}
		m.mu.Unlock()

		logger.Log("DEBUG", fmt.Sprintf("Reconciled: removed %d stale entries", len(staleTaskIDs)), nil)
	}
}

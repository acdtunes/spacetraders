package manufacturing

import (
	"context"
	"fmt"
	"sync"

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

// AssignTasks assigns ready tasks to idle ships
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

	// Track factory+good assignment counts for balancing
	assignedCounts := make(map[string]int)
	if m.taskRepo != nil {
		assignedTasksList, _ := m.taskRepo.FindByStatus(ctx, params.PlayerID, manufacturing.TaskStatusAssigned)
		for _, t := range assignedTasksList {
			if t.TaskType() == manufacturing.TaskTypeAcquireDeliver {
				factoryGoodKey := t.FactorySymbol() + ":" + t.Good()
				assignedCounts[factoryGoodKey]++
			}
		}
	}

	tasksAssigned := 0

	// Assign tasks
	for _, task := range readyTasks {
		if assignedCount >= params.MaxConcurrentTasks || len(idleShips) == 0 {
			break
		}

		// Balance check for ACQUIRE_DELIVER
		if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
			factoryGoodKey := task.FactorySymbol() + ":" + task.Good()
			currentCount := assignedCounts[factoryGoodKey]

			minCount := currentCount
			for _, count := range assignedCounts {
				if count < minCount {
					minCount = count
				}
			}

			if currentCount > minCount && currentCount >= 2 {
				continue
			}
		}

		// Saturation check for COLLECT_SELL
		if task.TaskType() == manufacturing.TaskTypeCollectSell {
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

		// Track assignment
		if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
			factoryGoodKey := task.FactorySymbol() + ":" + task.Good()
			assignedCounts[factoryGoodKey]++
		}

		delete(idleShips, selectedSymbol)
		assignedCount++
		tasksAssigned++

		logger.Log("INFO", fmt.Sprintf("Assigned task %s (%s) to ship %s", task.ID()[:8], task.TaskType(), selectedSymbol), nil)
	}

	return tasksAssigned, nil
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

// ReconcileAssignedTasksWithDB syncs in-memory state with DB
func (m *TaskAssignmentManager) ReconcileAssignedTasksWithDB(ctx context.Context, playerID int) {
	if m.taskRepo == nil {
		return
	}

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

		logger := common.LoggerFromContext(ctx)
		logger.Log("INFO", fmt.Sprintf("Reconciled: removed %d stale entries", len(staleTaskIDs)), nil)
	}
}

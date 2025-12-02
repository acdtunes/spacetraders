package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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

	// GetAssignmentCount returns the number of assigned tasks
	GetAssignmentCount() int

	// TrackAssignment tracks a task assignment in memory
	TrackAssignment(taskID, shipSymbol, containerID string)

	// UntrackAssignment removes a task assignment from memory
	UntrackAssignment(taskID string)
}

// AssignParams contains parameters for task assignment
type AssignParams struct {
	PlayerID           int
	MaxConcurrentTasks int
	CoordinatorID      string // Parent container ID for cascade stop support
}

// TaskAssignmentManager implements TaskAssigner using focused services.
// This manager coordinates task assignment by delegating to:
// - ShipSelector for ship selection
// - AssignmentTracker for in-memory state
// - MarketConditionChecker for supply checks
// - AssignmentReconciler for DB sync
// - WorkerReservationPolicy for reservation logic
type TaskAssignmentManager struct {
	// Repositories
	taskRepo           manufacturing.TaskRepository
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo domainContainer.ShipAssignmentRepository
	marketRepo         market.MarketRepository

	// Task queue (uses DualTaskQueue for collection-first priority)
	taskQueue services.ManufacturingTaskQueue

	// Focused services (injected via constructor)
	shipSelector      *ShipSelector
	tracker           *AssignmentTracker
	conditionChecker  *MarketConditionChecker
	reconciler        *AssignmentReconciler
	reservationPolicy *manufacturing.WorkerReservationPolicy

	// Pipeline registry getter (read-only, no circular dependency)
	getActivePipelines func() map[string]*manufacturing.ManufacturingPipeline

	// Dependencies (injected via constructor)
	workerManager   WorkerManager
	orphanedHandler OrphanedCargoManager
}

// NewTaskAssignmentManager creates a new task assignment manager with all dependencies.
func NewTaskAssignmentManager(
	taskRepo manufacturing.TaskRepository,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo domainContainer.ShipAssignmentRepository,
	marketRepo market.MarketRepository,
	taskQueue services.ManufacturingTaskQueue,
	shipSelector *ShipSelector,
	tracker *AssignmentTracker,
	conditionChecker *MarketConditionChecker,
	reconciler *AssignmentReconciler,
	reservationPolicy *manufacturing.WorkerReservationPolicy,
	getActivePipelines func() map[string]*manufacturing.ManufacturingPipeline,
	workerManager WorkerManager,
	orphanedHandler OrphanedCargoManager,
) *TaskAssignmentManager {
	return &TaskAssignmentManager{
		taskRepo:           taskRepo,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		marketRepo:         marketRepo,
		taskQueue:          taskQueue,
		shipSelector:       shipSelector,
		tracker:            tracker,
		conditionChecker:   conditionChecker,
		reconciler:         reconciler,
		reservationPolicy:  reservationPolicy,
		getActivePipelines: getActivePipelines,
		workerManager:      workerManager,
		orphanedHandler:    orphanedHandler,
	}
}

// GetAssignedTasks returns the assigned tasks map
func (m *TaskAssignmentManager) GetAssignedTasks() map[string]string {
	if m.tracker != nil {
		return m.tracker.GetAssignedTasks()
	}
	return make(map[string]string)
}

// GetTaskContainers returns the task containers map
func (m *TaskAssignmentManager) GetTaskContainers() map[string]string {
	if m.tracker != nil {
		return m.tracker.GetTaskContainers()
	}
	return make(map[string]string)
}

// TrackAssignment tracks a task assignment in memory
func (m *TaskAssignmentManager) TrackAssignment(taskID, shipSymbol, containerID string) {
	if m.tracker != nil {
		// Get task type for proper tracking
		taskType := manufacturing.TaskTypeAcquireDeliver // default
		if m.taskRepo != nil {
			ctx := context.Background()
			task, err := m.taskRepo.FindByID(ctx, taskID)
			if err == nil && task != nil {
				taskType = task.TaskType()
			}
		}
		m.tracker.Track(taskID, shipSymbol, containerID, taskType)
	}
}

// UntrackAssignment removes a task assignment from memory
func (m *TaskAssignmentManager) UntrackAssignment(taskID string) {
	if m.tracker != nil {
		m.tracker.Untrack(taskID)
	}
}

// GetAssignmentCount returns the number of assigned tasks
func (m *TaskAssignmentManager) GetAssignmentCount() int {
	if m.tracker != nil {
		return m.tracker.GetCount()
	}
	return 0
}

// ClearAssignments clears all task assignments
func (m *TaskAssignmentManager) ClearAssignments() {
	if m.tracker != nil {
		m.tracker.Clear()
	}
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

	// Get idle ships (FindIdleLightHaulers already calls FindAllByPlayer internally)
	playerID := shared.MustNewPlayerID(params.PlayerID)
	idleShipsList, _, err := contract.FindIdleLightHaulers(
		ctx, playerID, m.shipRepo, m.shipAssignmentRepo,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to find idle ships: %w", err)
	}

	if len(idleShipsList) == 0 {
		return 0, nil
	}

	// Convert to map for efficient lookup and removal
	idleShips := make(map[string]*navigation.Ship, len(idleShipsList))
	for _, ship := range idleShipsList {
		idleShips[ship.ShipSymbol()] = ship
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

	// Get allocations for reservation logic
	alloc := m.getAllocations(ctx, params.PlayerID)

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

		// Use reservation policy to check if assignment is allowed
		if m.reservationPolicy != nil && !m.reservationPolicy.ShouldAssign(task.TaskType(), alloc) {
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
				Task:          task,
				Ship:          selectedShip,
				PlayerID:      params.PlayerID,
				CoordinatorID: params.CoordinatorID,
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
			alloc.AcquireDeliverCount++
		} else if task.TaskType() == manufacturing.TaskTypeCollectSell {
			alloc.CollectSellCount++
		}

		delete(idleShips, selectedSymbol)
		assignedCount++
		tasksAssigned++

		logger.Log("INFO", fmt.Sprintf("Assigned task %s (%s) to ship %s", task.ID()[:8], task.TaskType(), selectedSymbol), nil)
	}

	return tasksAssigned, nil
}

// getAllocations builds the task type allocations for reservation logic.
func (m *TaskAssignmentManager) getAllocations(ctx context.Context, playerID int) manufacturing.TaskTypeAllocations {
	// First try to get from tracker
	if m.tracker != nil {
		alloc := m.tracker.GetAllocations()
		// Add ready task info from queue
		if m.taskQueue != nil {
			alloc.HasReadyCollectSell = m.taskQueue.HasReadyTasksByType(manufacturing.TaskTypeCollectSell)
			alloc.HasReadyAcquireDeliver = m.taskQueue.HasReadyTasksByType(manufacturing.TaskTypeAcquireDeliver)
		}
		return alloc
	}

	// Fallback: count from database
	alloc := manufacturing.TaskTypeAllocations{}

	if m.taskRepo != nil {
		// Get ASSIGNED tasks
		assignedTasks, err := m.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusAssigned)
		if err == nil {
			for _, task := range assignedTasks {
				if task.TaskType() == manufacturing.TaskTypeCollectSell {
					alloc.CollectSellCount++
				} else if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
					alloc.AcquireDeliverCount++
				}
			}
		}

		// Get EXECUTING tasks (also count as assigned for reservation purposes)
		executingTasks, err := m.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusExecuting)
		if err == nil {
			for _, task := range executingTasks {
				if task.TaskType() == manufacturing.TaskTypeCollectSell {
					alloc.CollectSellCount++
				} else if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
					alloc.AcquireDeliverCount++
				}
			}
		}
	}

	// Get ready task info from queue
	if m.taskQueue != nil {
		alloc.HasReadyCollectSell = m.taskQueue.HasReadyTasksByType(manufacturing.TaskTypeCollectSell)
		alloc.HasReadyAcquireDeliver = m.taskQueue.HasReadyTasksByType(manufacturing.TaskTypeAcquireDeliver)
	}

	return alloc
}

// GetTaskSourceLocation returns the waypoint where the task starts.
func (m *TaskAssignmentManager) GetTaskSourceLocation(ctx context.Context, task *manufacturing.ManufacturingTask, playerID int) *shared.Waypoint {
	return m.shipSelector.GetTaskSourceLocation(ctx, task, playerID)
}

// FindClosestShip finds the closest ship to a waypoint.
func (m *TaskAssignmentManager) FindClosestShip(
	ships map[string]*navigation.Ship,
	target *shared.Waypoint,
) (*navigation.Ship, string) {
	return m.shipSelector.FindClosestShip(ships, target)
}

// IsSellMarketSaturated checks if the sell market has HIGH or ABUNDANT supply.
func (m *TaskAssignmentManager) IsSellMarketSaturated(ctx context.Context, sellMarket, good string, playerID int) bool {
	return m.conditionChecker.IsSellMarketSaturated(ctx, sellMarket, good, playerID)
}

// IsFactorySupplyFavorable checks if the factory has HIGH/ABUNDANT supply for collection.
func (m *TaskAssignmentManager) IsFactorySupplyFavorable(ctx context.Context, factorySymbol, good string, playerID int) bool {
	return m.conditionChecker.IsFactoryOutputReady(ctx, factorySymbol, good, playerID)
}

// ReconcileAssignedTasksWithDB syncs in-memory state with DB.
func (m *TaskAssignmentManager) ReconcileAssignedTasksWithDB(ctx context.Context, playerID int) {
	m.reconciler.Reconcile(ctx, playerID)
}

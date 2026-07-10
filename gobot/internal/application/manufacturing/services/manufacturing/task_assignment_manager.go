package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TaskAssigner assigns ready tasks to idle ships
type TaskAssigner interface {
	// AssignTasks assigns ready tasks to idle ships
	AssignTasks(ctx context.Context, params AssignParams) (int, error)

	// IsSellMarketSaturated checks if market has HIGH/ABUNDANT supply
	IsSellMarketSaturated(ctx context.Context, sellMarket, good string, playerID int) bool

	// GetAssignmentCount returns the number of assigned tasks
	GetAssignmentCount() int
}

// AssignParams contains parameters for task assignment
type AssignParams struct {
	PlayerID           int
	MaxConcurrentTasks int
	CoordinatorID      string // Parent container ID for cascade stop support
	// SystemSymbol is the coordinator's operating system. Manufacturing task
	// workers never jump cross-system, so the idle-hauler pool is restricted to
	// hulls currently in this system - an out-of-system hull is unselectable
	// rather than claimed-then-failed (sp-qr3v, the second claim site). Empty
	// means no restriction (fleet-wide), preserving legacy callers' behavior.
	SystemSymbol string
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
	taskRepo manufacturing.TaskRepository
	shipRepo navigation.ShipRepository

	// Task queue (uses DualTaskQueue for collection-first priority)
	taskQueue ReadyTaskAssignmentQueue

	// Focused services (injected via constructor)
	shipSelector      *ShipSelector
	tracker           *AssignmentTracker
	conditionChecker  *MarketConditionChecker
	reconciler        *AssignmentReconciler
	reservationPolicy *manufacturing.WorkerReservationPolicy

	// Dependencies (injected via constructor)
	workerManager   WorkerManager
	orphanedHandler OrphanedCargoManager
}

// NewTaskAssignmentManager creates a new task assignment manager with all dependencies.
func NewTaskAssignmentManager(
	taskRepo manufacturing.TaskRepository,
	shipRepo navigation.ShipRepository,
	taskQueue ReadyTaskAssignmentQueue,
	shipSelector *ShipSelector,
	tracker *AssignmentTracker,
	conditionChecker *MarketConditionChecker,
	reconciler *AssignmentReconciler,
	reservationPolicy *manufacturing.WorkerReservationPolicy,
	workerManager WorkerManager,
	orphanedHandler OrphanedCargoManager,
) *TaskAssignmentManager {
	return &TaskAssignmentManager{
		taskRepo:          taskRepo,
		shipRepo:          shipRepo,
		taskQueue:         taskQueue,
		shipSelector:      shipSelector,
		tracker:           tracker,
		conditionChecker:  conditionChecker,
		reconciler:        reconciler,
		reservationPolicy: reservationPolicy,
		workerManager:     workerManager,
		orphanedHandler:   orphanedHandler,
	}
}

// GetAssignmentCount returns the number of assigned tasks
func (m *TaskAssignmentManager) GetAssignmentCount() int {
	if m.tracker != nil {
		return m.tracker.GetAssignmentCount()
	}
	return 0
}

// AssignTasks assigns ready tasks to idle ships using task type reservation
// to prevent starvation. Minimum workers are reserved for each task type.
func (m *TaskAssignmentManager) AssignTasks(ctx context.Context, params AssignParams) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Reconcile with DB first
	m.reconciler.Reconcile(ctx, params.PlayerID)

	// Check assignment count
	assignedCount := m.GetAssignmentCount()
	if assignedCount >= params.MaxConcurrentTasks {
		return 0, nil
	}

	// Get idle ships (FindIdleLightHaulers already calls FindAllByPlayer internally).
	// sp-qr3v: restricted to the coordinator's own system so an out-of-system hull
	// is never assigned to a manufacturing task worker that cannot navigate it home.
	playerID := shared.MustNewPlayerID(params.PlayerID)
	idleShipsList, _, err := contract.FindIdleLightHaulers(
		ctx, playerID, m.shipRepo, params.SystemSymbol,
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

	// Get ready tasks. Construction (gate/mission-critical) tasks are hoisted
	// ahead of manufacturing (income) tasks so an aged manufacturing backlog
	// can't starve the gate of workers - see prioritizeConstructionTasks.
	readyTasks := prioritizeConstructionTasks(m.taskQueue.GetReadyTasks())
	if len(readyTasks) == 0 || len(idleShips) == 0 {
		return 0, nil
	}

	// Refresh assigned count
	assignedCount = m.GetAssignmentCount()

	// Get allocations for reservation logic
	alloc := m.getAllocations(ctx, params.PlayerID)

	// Track factory+good assignment counts for balancing within ACQUIRE_DELIVER
	factoryGoodCounts := m.countAssignedAcquireDeliverByFactoryGood(ctx, params.PlayerID)

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
		if task.TaskType() == manufacturing.TaskTypeAcquireDeliver && exceedsFactoryGoodBalance(task, factoryGoodCounts) {
			continue
		}

		if task.TaskType() == manufacturing.TaskTypeCollectSell && !m.passesCollectSellPreflight(ctx, task, params.PlayerID) {
			continue
		}

		// Find closest ship
		sourceLocation := m.shipSelector.GetTaskSourceLocation(ctx, task, params.PlayerID)
		selectedShip, selectedSymbol := m.shipSelector.FindClosestShip(idleShips, sourceLocation)

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
			factoryGoodCounts[factoryGoodKey(task)]++
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

// prioritizeConstructionTasks partitions ready tasks so DELIVER_TO_CONSTRUCTION
// (gate/mission-critical) tasks are assigned ahead of manufacturing (income)
// tasks, regardless of the priority+aging order GetReadyTasks() returns them in.
//
// sp-q0xm: GetReadyTasks() sorts strictly by effective priority (base priority +
// an aging bonus that accrues independently per task off its own ReadyAt(),
// capped at +100 - see task.go). Because aging is per-task, an old COLLECT_SELL
// backlog (base priority 50) can out-age a freshly-ready DELIVER_TO_CONSTRUCTION
// task (base priority 75, no aging yet) once it has waited long enough. The
// assignment loop below then hands idle ships to readyTasks strictly in that
// order, so a large aged manufacturing backlog (bead: "10-14 queued COLLECT_SELL
// tasks") can starve the gate-build tasks of workers even though construction is
// the higher mission priority.
//
// This is a stable partition: relative order is preserved within each tier, so
// the existing priority+aging order among manufacturing tasks (and among
// construction tasks, if several are ready) is unaffected - only construction is
// hoisted to the front as a group.
func prioritizeConstructionTasks(tasks []*manufacturing.ManufacturingTask) []*manufacturing.ManufacturingTask {
	construction := make([]*manufacturing.ManufacturingTask, 0, len(tasks))
	rest := make([]*manufacturing.ManufacturingTask, 0, len(tasks))
	hasConstruction := false

	for _, task := range tasks {
		if task.TaskType() == manufacturing.TaskTypeDeliverToConstruction {
			construction = append(construction, task)
			hasConstruction = true
		} else {
			rest = append(rest, task)
		}
	}

	if !hasConstruction {
		return tasks
	}

	return append(construction, rest...)
}

func factoryGoodKey(task *manufacturing.ManufacturingTask) string {
	return task.FactorySymbol() + ":" + task.Good()
}

func (m *TaskAssignmentManager) countAssignedAcquireDeliverByFactoryGood(ctx context.Context, playerID int) map[string]int {
	counts := make(map[string]int)
	if m.taskRepo == nil {
		return counts
	}
	assignedTasksList, _ := m.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusAssigned)
	for _, t := range assignedTasksList {
		if t.TaskType() == manufacturing.TaskTypeAcquireDeliver {
			counts[factoryGoodKey(t)]++
		}
	}
	return counts
}

func exceedsFactoryGoodBalance(task *manufacturing.ManufacturingTask, factoryGoodCounts map[string]int) bool {
	currentCount := factoryGoodCounts[factoryGoodKey(task)]

	minCount := currentCount
	for _, count := range factoryGoodCounts {
		if count < minCount {
			minCount = count
		}
	}

	return currentCount > minCount && currentCount >= 2
}

func (m *TaskAssignmentManager) passesCollectSellPreflight(ctx context.Context, task *manufacturing.ManufacturingTask, playerID int) bool {
	logger := common.LoggerFromContext(ctx)

	if !m.conditionChecker.IsFactoryOutputReady(ctx, task.FactorySymbol(), task.Good(), playerID) {
		logger.Log("DEBUG", "Skipping COLLECT_SELL - factory supply not HIGH/ABUNDANT", map[string]interface{}{
			"task_id": task.ID()[:8],
			"factory": task.FactorySymbol(),
			"good":    task.Good(),
		})
		m.resetToPendingAndDequeue(ctx, task)
		return false
	}

	if m.IsSellMarketSaturated(ctx, task.TargetMarket(), task.Good(), playerID) {
		logger.Log("DEBUG", "Skipping COLLECT_SELL - sell market saturated", map[string]interface{}{
			"task_id": task.ID()[:8],
		})
		m.resetToPendingAndDequeue(ctx, task)
		return false
	}

	return true
}

func (m *TaskAssignmentManager) resetToPendingAndDequeue(ctx context.Context, task *manufacturing.ManufacturingTask) {
	task.ResetToPending()
	if m.taskRepo != nil {
		_ = m.taskRepo.Update(ctx, task)
	}
	m.taskQueue.Remove(task.ID())
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

// IsSellMarketSaturated checks if the sell market has HIGH or ABUNDANT supply.
func (m *TaskAssignmentManager) IsSellMarketSaturated(ctx context.Context, sellMarket, good string, playerID int) bool {
	return m.conditionChecker.IsSellMarketSaturated(ctx, sellMarket, good, playerID)
}

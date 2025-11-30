package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// StateRecoverer recovers coordinator state from database after restart
type StateRecoverer interface {
	// RecoverState loads pipelines, tasks, and factory states from database
	RecoverState(ctx context.Context, playerID int) (*RecoveryResult, error)
}

// RecoveryResult contains the result of state recovery
type RecoveryResult struct {
	ActivePipelines  map[string]*manufacturing.ManufacturingPipeline
	ReadyTaskCount   int
	InterruptedCount int
	RetriedCount     int
}

// StateRecoveryManager implements StateRecoverer
type StateRecoveryManager struct {
	pipelineRepo       manufacturing.PipelineRepository
	taskRepo           manufacturing.TaskRepository
	factoryStateRepo   manufacturing.FactoryStateRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	shipRepo           navigation.ShipQueryRepository // BUG FIX #4: Added for cargo checks
	factoryTracker     *manufacturing.FactoryStateTracker
	taskQueue          services.ManufacturingTaskQueue
}

// NewStateRecoveryManager creates a new state recovery manager
func NewStateRecoveryManager(
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
	factoryStateRepo manufacturing.FactoryStateRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	shipRepo navigation.ShipQueryRepository, // BUG FIX #4: Added for cargo checks
	factoryTracker *manufacturing.FactoryStateTracker,
	taskQueue services.ManufacturingTaskQueue,
) *StateRecoveryManager {
	return &StateRecoveryManager{
		pipelineRepo:       pipelineRepo,
		taskRepo:           taskRepo,
		factoryStateRepo:   factoryStateRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		shipRepo:           shipRepo,
		factoryTracker:     factoryTracker,
		taskQueue:          taskQueue,
	}
}

// RecoverState loads pipelines, tasks, and factory states from database.
// It also validates task states against actual market data to detect inconsistencies.
func (m *StateRecoveryManager) RecoverState(ctx context.Context, playerID int) (*RecoveryResult, error) {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Recovering parallel manufacturing state from database...", nil)

	result := &RecoveryResult{
		ActivePipelines: make(map[string]*manufacturing.ManufacturingPipeline),
	}

	if m.pipelineRepo == nil || m.taskRepo == nil {
		logger.Log("DEBUG", "No repositories configured, skipping state recovery", nil)
		return result, nil
	}

	// Step 1: Load incomplete pipelines
	pipelines, err := m.pipelineRepo.FindByStatus(ctx, playerID, []manufacturing.PipelineStatus{
		manufacturing.PipelineStatusPlanning,
		manufacturing.PipelineStatusExecuting,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load pipelines: %w", err)
	}

	// Start any PLANNING pipelines
	for _, pipeline := range pipelines {
		if pipeline.Status() == manufacturing.PipelineStatusPlanning {
			if err := pipeline.Start(); err != nil {
				logger.Log("WARN", fmt.Sprintf("Failed to start recovered PLANNING pipeline %s: %v", pipeline.ID()[:8], err), nil)
			} else {
				logger.Log("INFO", fmt.Sprintf("Started recovered PLANNING pipeline %s", pipeline.ID()[:8]), nil)
				if m.pipelineRepo != nil {
					_ = m.pipelineRepo.Update(ctx, pipeline)
				}
			}
		}
		result.ActivePipelines[pipeline.ID()] = pipeline
	}

	logger.Log("INFO", fmt.Sprintf("Recovered %d active pipelines", len(pipelines)), nil)

	// Step 1.5: Clean up orphaned COLLECTION pipelines (no tasks)
	// These can occur if task creation fails after pipeline is persisted
	for _, pipeline := range pipelines {
		if pipeline.PipelineType() != manufacturing.PipelineTypeCollection {
			continue
		}

		// Check if this COLLECTION pipeline has any tasks
		tasks, err := m.taskRepo.FindByPipelineID(ctx, pipeline.ID())
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to check tasks for COLLECTION pipeline %s: %v", pipeline.ID()[:8], err), nil)
			continue
		}

		if len(tasks) == 0 {
			// This is an orphaned COLLECTION pipeline - delete it
			if err := m.pipelineRepo.Delete(ctx, pipeline.ID()); err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to delete orphaned COLLECTION pipeline %s: %v", pipeline.ID()[:8], err), nil)
			} else {
				logger.Log("INFO", fmt.Sprintf("Deleted orphaned COLLECTION pipeline %s (%s) - no tasks",
					pipeline.ID()[:8], pipeline.ProductGood()), nil)
				delete(result.ActivePipelines, pipeline.ID())
			}
		}
	}

	// Step 2: Load incomplete tasks and rebuild queue
	tasks, err := m.taskRepo.FindIncomplete(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load tasks: %w", err)
	}

	for _, task := range tasks {
		// CRITICAL: Skip orphaned tasks with no pipeline_id
		// These are corrupted tasks that slipped through without proper pipeline association
		if task.PipelineID() == "" {
			shipSymbol := task.AssignedShip()

			// BUG FIX #4: Check if ship has cargo that needs recovery before cancelling
			if shipSymbol != "" && m.shipRepo != nil {
				ship, err := m.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
				if err == nil && ship != nil && ship.Cargo() != nil && !ship.Cargo().IsEmpty() {
					// Ship has cargo - create LIQUIDATE tasks to recover investment
					for _, item := range ship.Cargo().Inventory {
						liquidateTask := manufacturing.NewLiquidationTask(
							playerID,
							shipSymbol,
							item.Symbol,
							item.Units,
							"", // Let task find best sell market
						)
						if err := m.taskRepo.Create(ctx, liquidateTask); err == nil {
							m.taskQueue.Enqueue(liquidateTask)
							logger.Log("INFO", fmt.Sprintf(
								"BUG FIX #4: Created LIQUIDATE task for stranded %d %s on ship %s",
								item.Units, item.Symbol, shipSymbol), nil)
						}
					}
					// Cancel original task but DON'T release ship - LIQUIDATE task needs it
					if err := task.Cancel("orphaned task - cargo recovery"); err == nil {
						_ = m.taskRepo.Update(ctx, task)
						logger.Log("INFO", fmt.Sprintf("Cancelled orphaned task %s, cargo being recovered",
							task.ID()[:8]), nil)
					}
					continue
				}
			}

			// No cargo - safe to cancel and release
			if err := task.Cancel("orphaned task - no pipeline"); err == nil {
				_ = m.taskRepo.Update(ctx, task)
				logger.Log("WARN", fmt.Sprintf("Cancelled orphaned task %s (%s) - no pipeline_id",
					task.ID()[:8], task.TaskType()), nil)
			}
			// Release ship if assigned
			if shipSymbol != "" && m.shipAssignmentRepo != nil {
				_ = m.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "orphaned_task")
			}
			continue
		}

		// CRITICAL: Skip tasks from non-active pipelines (CANCELLED/COMPLETED)
		// Only tasks from PLANNING/EXECUTING pipelines should be recovered
		if _, isActive := result.ActivePipelines[task.PipelineID()]; !isActive {
			shipSymbol := task.AssignedShip()

			// BUG FIX #4: Check if ship has cargo that needs recovery before cancelling
			if shipSymbol != "" && m.shipRepo != nil {
				ship, err := m.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
				if err == nil && ship != nil && ship.Cargo() != nil && !ship.Cargo().IsEmpty() {
					// Ship has cargo - create LIQUIDATE tasks to recover investment
					for _, item := range ship.Cargo().Inventory {
						liquidateTask := manufacturing.NewLiquidationTask(
							playerID,
							shipSymbol,
							item.Symbol,
							item.Units,
							"", // Let task find best sell market
						)
						if err := m.taskRepo.Create(ctx, liquidateTask); err == nil {
							m.taskQueue.Enqueue(liquidateTask)
							logger.Log("INFO", fmt.Sprintf(
								"BUG FIX #4: Created LIQUIDATE task for stranded %d %s on ship %s (pipeline inactive)",
								item.Units, item.Symbol, shipSymbol), nil)
						}
					}
					// Cancel original task but DON'T release ship - LIQUIDATE task needs it
					if err := task.Cancel("pipeline not active - cargo recovery"); err == nil {
						_ = m.taskRepo.Update(ctx, task)
						logger.Log("INFO", fmt.Sprintf("Cancelled task %s (pipeline inactive), cargo being recovered",
							task.ID()[:8]), nil)
					}
					continue
				}
			}

			// No cargo - safe to cancel and release
			if err := task.Cancel("pipeline not active"); err == nil {
				_ = m.taskRepo.Update(ctx, task)
				logger.Log("INFO", fmt.Sprintf("Cancelled task %s (%s) - pipeline not active",
					task.ID()[:8], task.TaskType()), nil)
			}
			// Release ship if assigned
			if shipSymbol != "" && m.shipAssignmentRepo != nil {
				_ = m.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "pipeline_not_active")
			}
			continue
		}

		// Step 2a: Reset interrupted ASSIGNED tasks
		if task.Status() == manufacturing.TaskStatusAssigned {
			shipSymbol := task.AssignedShip()
			if err := task.RollbackAssignment(); err == nil {
				_ = m.taskRepo.Update(ctx, task)
				result.InterruptedCount++
				logger.Log("INFO", fmt.Sprintf("Reset interrupted ASSIGNED task %s (%s)", task.ID()[:8], task.TaskType()), nil)
				if shipSymbol != "" && m.shipAssignmentRepo != nil {
					_ = m.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "task_recovery")
				}
			}
		}

		// Step 2b: Reset interrupted EXECUTING tasks
		if task.Status() == manufacturing.TaskStatusExecuting {
			shipSymbol := task.AssignedShip()
			if err := task.RollbackExecution(); err == nil {
				_ = m.taskRepo.Update(ctx, task)
				result.InterruptedCount++
				logger.Log("INFO", fmt.Sprintf("Reset interrupted EXECUTING task %s (%s)", task.ID()[:8], task.TaskType()), nil)

				// BUG FIX #2: Don't release ship assignment for SELL tasks
				// COLLECT_SELL and LIQUIDATE tasks have cargo on the ship that needs to be sold.
				// Releasing the assignment would orphan the ship with cargo.
				// The coordinator will re-assign the same ship to complete the task.
				isSellTask := task.TaskType() == manufacturing.TaskTypeCollectSell ||
					task.TaskType() == manufacturing.TaskTypeLiquidate
				if shipSymbol != "" && m.shipAssignmentRepo != nil && !isSellTask {
					_ = m.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "task_recovery")
				} else if isSellTask && shipSymbol != "" {
					logger.Log("INFO", fmt.Sprintf("Preserving ship %s assignment (SELL task has cargo)", shipSymbol), nil)
				}
			}
		}

		// Step 2c: Re-evaluate PENDING tasks for readiness
		// FABRICATION pipeline COLLECT_SELL and ACQUIRE_DELIVER are supply-gated by SupplyMonitor
		// COLLECTION pipeline tasks should be marked READY (already validated when created)
		if task.Status() == manufacturing.TaskStatusPending {
			pipeline := result.ActivePipelines[task.PipelineID()]
			isCollectionPipeline := pipeline != nil && pipeline.PipelineType() == manufacturing.PipelineTypeCollection

			// COLLECTION pipeline tasks: mark READY immediately and enqueue
			if isCollectionPipeline {
				if err := task.MarkReady(); err == nil {
					_ = m.taskRepo.Update(ctx, task)
					m.taskQueue.Enqueue(task)
					result.ReadyTaskCount++
					logger.Log("INFO", fmt.Sprintf("Recovered COLLECTION pipeline task %s (%s) as READY and enqueued",
						task.ID()[:8], task.Good()), nil)
				}
				continue
			}

			// FABRICATION pipeline supply-gated tasks: let SupplyMonitor handle
			if task.TaskType() == manufacturing.TaskTypeCollectSell ||
				task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
				continue
			}

			// Other PENDING tasks: check dependencies
			allDepsMet := true
			for _, depID := range task.DependsOn() {
				depTask, err := m.taskRepo.FindByID(ctx, depID)
				if err != nil || depTask == nil || depTask.Status() != manufacturing.TaskStatusCompleted {
					allDepsMet = false
					break
				}
			}

			if allDepsMet {
				if err := task.MarkReady(); err == nil {
					_ = m.taskRepo.Update(ctx, task)
				}
			}
		}

		// Step 2d: Enqueue READY tasks, but reset supply-gated tasks to PENDING
		// COLLECT_SELL and ACQUIRE_DELIVER from FABRICATION pipelines are supply-gated
		// COLLECTION pipeline tasks should stay READY (already validated when created)
		if task.Status() == manufacturing.TaskStatusReady {
			pipeline := result.ActivePipelines[task.PipelineID()]
			isCollectionPipeline := pipeline != nil && pipeline.PipelineType() == manufacturing.PipelineTypeCollection

			if !isCollectionPipeline &&
				(task.TaskType() == manufacturing.TaskTypeCollectSell ||
					task.TaskType() == manufacturing.TaskTypeAcquireDeliver) {
				// Reset FABRICATION pipeline supply-gated tasks to PENDING
				// SupplyMonitor will re-evaluate supply conditions
				task.ResetToPending()
				if m.taskRepo != nil {
					_ = m.taskRepo.Update(ctx, task)
				}
				logger.Log("DEBUG", fmt.Sprintf("Reset %s task %s to PENDING for SupplyMonitor",
					task.TaskType(), task.ID()[:8]), nil)
				continue
			}

			// Enqueue READY tasks (including COLLECTION pipeline tasks)
			m.taskQueue.Enqueue(task)
			result.ReadyTaskCount++
		}
	}

	// Step 2e: Recover FAILED tasks that can be retried
	// Supply-gated tasks (COLLECT_SELL, ACQUIRE_DELIVER) stay PENDING for SupplyMonitor
	failedTasks, err := m.taskRepo.FindByStatus(ctx, playerID, manufacturing.TaskStatusFailed)
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("Failed to load failed tasks for retry: %v", err), nil)
	} else {
		for _, task := range failedTasks {
			if task.CanRetry() {
				retryCount := task.RetryCount()

				if err := task.ResetForRetry(); err != nil {
					continue
				}

				// Supply-gated tasks stay PENDING - let SupplyMonitor handle them
				if task.TaskType() == manufacturing.TaskTypeCollectSell ||
					task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
					if err := m.taskRepo.Update(ctx, task); err == nil {
						logger.Log("INFO", fmt.Sprintf("Reset FAILED %s task %s to PENDING for SupplyMonitor",
							task.TaskType(), task.ID()[:8]), nil)
					}
					continue
				}

				if err := task.MarkReady(); err != nil {
					continue
				}

				if err := m.taskRepo.Update(ctx, task); err != nil {
					continue
				}

				m.taskQueue.Enqueue(task)
				result.RetriedCount++
				result.ReadyTaskCount++

				logger.Log("INFO", fmt.Sprintf("Recovered FAILED task %s for retry (%d/%d)",
					task.ID()[:8], retryCount, task.MaxRetries()), nil)
			}
		}
	}

	logger.Log("INFO", fmt.Sprintf("Recovered %d tasks: %d ready, %d interrupted, %d retried",
		len(tasks)+result.RetriedCount, result.ReadyTaskCount, result.InterruptedCount, result.RetriedCount), nil)

	// Step 3: Load factory states
	if m.factoryStateRepo != nil {
		pendingStates, _ := m.factoryStateRepo.FindPending(ctx, playerID)
		for _, state := range pendingStates {
			m.factoryTracker.LoadState(state)
		}

		readyStates, _ := m.factoryStateRepo.FindReadyForCollection(ctx, playerID)
		for _, state := range readyStates {
			m.factoryTracker.LoadState(state)
		}

		logger.Log("INFO", fmt.Sprintf("Recovered %d factory states", len(pendingStates)+len(readyStates)), nil)

		// Step 3a: Reconcile factory states with completed ACQUIRE_DELIVER tasks
		// This fixes the bug where daemon restarts lose track of delivered inputs
		reconciledCount := m.reconcileFactoryStatesWithCompletedTasks(ctx, playerID, result.ActivePipelines)
		if reconciledCount > 0 {
			logger.Log("INFO", fmt.Sprintf("Reconciled %d factory state deliveries from completed tasks", reconciledCount), nil)
		}
	}

	logger.Log("INFO", "State recovery complete", map[string]interface{}{
		"pipelines":      len(result.ActivePipelines),
		"tasks_in_queue": m.taskQueue.Size(),
	})

	return result, nil
}

// reconcileFactoryStatesWithCompletedTasks fixes factory states by checking completed ACQUIRE_DELIVER tasks.
// This prevents the bug where daemon restarts cause factories to forget about deliveries.
//
// The bug: When a daemon restarts, factory states are loaded from DB but the delivered_inputs
// may not reflect completed tasks. This happens because:
// 1. Task completes and factory_state.delivered_inputs is updated in memory
// 2. Daemon restarts before DB persist (or DB persist fails)
// 3. Factory state is loaded with stale delivered_inputs
// 4. Pipeline is stuck because factory thinks inputs weren't delivered
//
// The fix: After loading factory states, check all COMPLETED ACQUIRE_DELIVER tasks and
// reconcile the delivered_inputs field based on task completion.
func (m *StateRecoveryManager) reconcileFactoryStatesWithCompletedTasks(
	ctx context.Context,
	playerID int,
	activePipelines map[string]*manufacturing.ManufacturingPipeline,
) int {
	logger := common.LoggerFromContext(ctx)
	reconciledCount := 0

	// Get all factory states
	allStates := m.factoryTracker.GetAllFactories()
	if len(allStates) == 0 {
		return 0
	}

	// For each factory state, find completed ACQUIRE_DELIVER tasks that delivered to it
	for _, factoryState := range allStates {
		// Skip if not in an active pipeline
		if _, ok := activePipelines[factoryState.PipelineID()]; !ok {
			continue
		}

		// Get completed ACQUIRE_DELIVER tasks for this pipeline
		completedTasks, err := m.taskRepo.FindByPipelineAndStatus(
			ctx,
			factoryState.PipelineID(),
			manufacturing.TaskStatusCompleted,
		)
		if err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to load completed tasks for pipeline %s: %v",
				factoryState.PipelineID()[:8], err), nil)
			continue
		}

		// Check each completed task to see if it delivered to this factory
		for _, task := range completedTasks {
			// Only ACQUIRE_DELIVER tasks deliver inputs to factories
			if task.TaskType() != manufacturing.TaskTypeAcquireDeliver {
				continue
			}

			// Check if this task delivered to this factory
			if task.TargetMarket() != factoryState.FactorySymbol() {
				continue
			}

			// Check if this task's good is a required input for this factory
			inputGood := task.Good()
			deliveredInputs := factoryState.DeliveredInputs()
			inputState, isRequired := deliveredInputs[inputGood]
			if !isRequired {
				continue
			}

			// If already marked as delivered, skip
			if inputState.Delivered {
				continue
			}

			// RECONCILE: Mark this input as delivered based on the completed task
			logger.Log("INFO", fmt.Sprintf("Reconciling: marking %s as delivered to factory %s (from completed task %s)",
				inputGood, factoryState.FactorySymbol(), task.ID()[:8]), nil)

			if err := factoryState.RecordDelivery(inputGood, task.ActualQuantity(), task.AssignedShip()); err != nil {
				logger.Log("WARN", fmt.Sprintf("Failed to reconcile delivery %s to factory %s: %v",
					inputGood, factoryState.FactorySymbol(), err), nil)
				continue
			}

			// Persist the updated factory state
			if m.factoryStateRepo != nil {
				if err := m.factoryStateRepo.Update(ctx, factoryState); err != nil {
					logger.Log("WARN", fmt.Sprintf("Failed to persist reconciled factory state: %v", err), nil)
				}
			}

			reconciledCount++
		}
	}

	return reconciledCount
}


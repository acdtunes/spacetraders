package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
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
	factoryTracker     *manufacturing.FactoryStateTracker
	taskQueue          *services.TaskQueue
}

// NewStateRecoveryManager creates a new state recovery manager
func NewStateRecoveryManager(
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
	factoryStateRepo manufacturing.FactoryStateRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	factoryTracker *manufacturing.FactoryStateTracker,
	taskQueue *services.TaskQueue,
) *StateRecoveryManager {
	return &StateRecoveryManager{
		pipelineRepo:       pipelineRepo,
		taskRepo:           taskRepo,
		factoryStateRepo:   factoryStateRepo,
		shipAssignmentRepo: shipAssignmentRepo,
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
			// Mark as failed to prevent re-processing
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
			// Mark as failed to prevent re-processing
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
				if shipSymbol != "" && m.shipAssignmentRepo != nil {
					_ = m.shipAssignmentRepo.Release(ctx, shipSymbol, playerID, "task_recovery")
				}
			}
		}

		// Step 2c: Re-evaluate PENDING tasks for readiness
		// FABRICATION pipeline COLLECT_SELL and ACQUIRE_DELIVER are supply-gated by SupplyMonitor
		// COLLECTION pipeline tasks should be marked READY (already validated when created)
		if task.Status() == manufacturing.TaskStatusPending {
			pipeline := result.ActivePipelines[task.PipelineID()]
			isCollectionPipeline := pipeline != nil && pipeline.PipelineType() == manufacturing.PipelineTypeCollection

			// COLLECTION pipeline tasks: mark READY immediately
			if isCollectionPipeline {
				if err := task.MarkReady(); err == nil {
					_ = m.taskRepo.Update(ctx, task)
					logger.Log("INFO", fmt.Sprintf("Recovered COLLECTION pipeline task %s (%s) as READY",
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
	}

	logger.Log("INFO", "State recovery complete", map[string]interface{}{
		"pipelines":      len(result.ActivePipelines),
		"tasks_in_queue": m.taskQueue.Size(),
	})

	return result, nil
}


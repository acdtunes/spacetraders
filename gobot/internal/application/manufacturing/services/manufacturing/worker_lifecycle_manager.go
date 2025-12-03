package manufacturing

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// WorkerManager manages worker container lifecycle
type WorkerManager interface {
	// AssignTaskToShip creates worker container and assigns ship
	AssignTaskToShip(ctx context.Context, params AssignTaskParams) error

	// HandleWorkerCompletion processes worker container completion
	HandleWorkerCompletion(ctx context.Context, shipSymbol string) (*TaskCompletion, error)

	// HandleTaskFailure processes failed task (retry or mark failed)
	HandleTaskFailure(ctx context.Context, completion TaskCompletion) error
}

// AssignTaskParams contains parameters for assigning a task to a ship
type AssignTaskParams struct {
	Task           *manufacturing.ManufacturingTask
	Ship           *navigation.Ship
	PlayerID       int
	ContainerID    string // Optional - generated if empty
	CoordinatorID  string
	PipelineNumber int
	ProductGood    string
}

// TaskCompletion represents a completed task notification
type TaskCompletion struct {
	TaskID     string
	ShipSymbol string
	PipelineID string
	Success    bool
	Error      error
}

// ContainerRemover interface for removing containers
type ContainerRemover interface {
	Remove(ctx context.Context, containerID string, playerID int) error
}

// WorkerLifecycleManager implements WorkerManager
type WorkerLifecycleManager struct {
	taskRepo         manufacturing.TaskRepository
	shipRepo         navigation.ShipRepository
	daemonClient     daemon.DaemonClient
	containerRemover ContainerRemover
	taskQueue        services.ManufacturingTaskQueue
	clock            shared.Clock

	// Dependencies (injected via constructor)
	assignmentTracker  *AssignmentTracker // Used for Track/Untrack (no circular dep)
	factoryManager     FactoryManager
	pipelineManager    PipelineManager
	workerCompletionCh chan string
}

// NewWorkerLifecycleManager creates a new worker lifecycle manager with all dependencies
func NewWorkerLifecycleManager(
	taskRepo manufacturing.TaskRepository,
	shipRepo navigation.ShipRepository,
	daemonClient daemon.DaemonClient,
	containerRemover ContainerRemover,
	taskQueue services.ManufacturingTaskQueue,
	clock shared.Clock,
	assignmentTracker *AssignmentTracker,
	factoryManager FactoryManager,
	pipelineManager PipelineManager,
	workerCompletionCh chan string,
) *WorkerLifecycleManager {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &WorkerLifecycleManager{
		taskRepo:           taskRepo,
		shipRepo:           shipRepo,
		daemonClient:       daemonClient,
		containerRemover:   containerRemover,
		taskQueue:          taskQueue,
		clock:              clock,
		assignmentTracker:  assignmentTracker,
		factoryManager:     factoryManager,
		pipelineManager:    pipelineManager,
		workerCompletionCh: workerCompletionCh,
	}
}

// AssignTaskToShip creates a worker container and assigns a ship to execute the task
func (m *WorkerLifecycleManager) AssignTaskToShip(ctx context.Context, params AssignTaskParams) error {
	logger := common.LoggerFromContext(ctx)
	task := params.Task
	shipSymbol := params.Ship.ShipSymbol()

	// Generate container ID if not provided
	containerID := params.ContainerID
	if containerID == "" {
		containerID = utils.GenerateContainerID("mfg-task", shipSymbol)
	}

	// Assign task atomically using SELECT FOR UPDATE to prevent race conditions.
	// This prevents multiple workers from assigning the same task simultaneously.
	if m.taskRepo != nil {
		if err := m.taskRepo.AssignTaskAtomically(ctx, task.ID(), shipSymbol); err != nil {
			return fmt.Errorf("failed to assign ship: %w", err)
		}
		// Update domain entity to match DB state (for container metadata)
		_ = task.AssignShip(shipSymbol)
	} else {
		// No repo - just update domain entity (for tests)
		if err := task.AssignShip(shipSymbol); err != nil {
			return fmt.Errorf("failed to assign ship: %w", err)
		}
	}

	// Get pipeline info
	var pipelineNumber int
	var productGood string
	if m.pipelineManager != nil {
		pipelines := m.pipelineManager.GetActivePipelines()
		if pipeline, ok := pipelines[task.PipelineID()]; ok {
			pipelineNumber = pipeline.SequenceNumber()
			productGood = pipeline.ProductGood()
		}
	}

	// Step 1: Persist worker container to DB
	logger.Log("INFO", fmt.Sprintf("Persisting worker container %s for %s", containerID, shipSymbol), nil)
	if err := m.daemonClient.PersistManufacturingTaskWorkerContainer(ctx, containerID, uint(params.PlayerID), &WorkerCommand{
		ShipSymbol:     shipSymbol,
		Task:           task,
		PlayerID:       params.PlayerID,
		ContainerID:    containerID,
		CoordinatorID:  params.CoordinatorID,
		PipelineNumber: pipelineNumber,
		ProductGood:    productGood,
	}); err != nil {
		if rollbackErr := task.RollbackAssignment(); rollbackErr != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to rollback task assignment: %v", rollbackErr), nil)
		}
		if m.taskRepo != nil {
			if updateErr := m.taskRepo.Update(ctx, task); updateErr != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to persist task rollback: %v (task %s may be stuck as ASSIGNED)", updateErr, task.ID()[:8]), nil)
			}
		}
		return fmt.Errorf("failed to persist worker container: %w", err)
	}

	// Step 2: Claim ship exclusively using DB-level locking to prevent race conditions
	// This ensures only one coordinator can assign the same ship
	logger.Log("INFO", fmt.Sprintf("Claiming %s for worker container %s", shipSymbol, containerID), nil)
	if m.shipRepo != nil {
		playerID, _ := shared.NewPlayerID(params.PlayerID)
		if err := m.shipRepo.ClaimShip(ctx, shipSymbol, containerID, playerID); err != nil {
			// Rollback: remove worker container
			if m.containerRemover != nil {
				if removeErr := m.containerRemover.Remove(ctx, containerID, params.PlayerID); removeErr != nil {
					logger.Log("ERROR", fmt.Sprintf("Failed to remove container %s during rollback: %v", containerID, removeErr), nil)
				}
			}
			// Rollback: reset task assignment
			if rollbackErr := task.RollbackAssignment(); rollbackErr != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to rollback task assignment: %v", rollbackErr), nil)
			}
			if m.taskRepo != nil {
				if updateErr := m.taskRepo.Update(ctx, task); updateErr != nil {
					logger.Log("ERROR", fmt.Sprintf("Failed to persist task rollback: %v (task %s may be stuck as ASSIGNED)", updateErr, task.ID()[:8]), nil)
				}
			}
			return fmt.Errorf("failed to claim ship: %w", err)
		}

		// Update Ship domain entity for in-memory consistency
		if err := params.Ship.AssignToContainer(containerID, m.clock); err != nil {
			logger.Log("WARN", fmt.Sprintf("Failed to update ship domain entity: %v (DB already updated)", err), nil)
		}
	}

	// Step 3: Start the worker container
	logger.Log("INFO", fmt.Sprintf("Starting worker container %s for task %s", containerID, task.ID()[:8]), nil)
	if err := m.daemonClient.StartManufacturingTaskWorkerContainer(ctx, containerID, m.workerCompletionCh); err != nil {
		// Release ship assignment on failure
		if m.shipRepo != nil {
			params.Ship.ForceRelease("worker_start_failed", m.clock)
			if saveErr := m.shipRepo.Save(ctx, params.Ship); saveErr != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to save ship release during rollback: %v", saveErr), nil)
			}
		}
		if rollbackErr := task.RollbackAssignment(); rollbackErr != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to rollback task assignment: %v", rollbackErr), nil)
		}
		if m.taskRepo != nil {
			if updateErr := m.taskRepo.Update(ctx, task); updateErr != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to persist task rollback: %v (task %s may be stuck as ASSIGNED)", updateErr, task.ID()[:8]), nil)
			}
		}
		return fmt.Errorf("failed to start worker container: %w", err)
	}

	// Track assignment
	if m.assignmentTracker != nil {
		m.assignmentTracker.Track(task.ID(), shipSymbol, containerID, task.TaskType())
	}

	// Remove from queue
	m.taskQueue.Remove(task.ID())

	logger.Log("INFO", "Task assigned to worker container", map[string]interface{}{
		"task_id":      task.ID()[:8],
		"task_type":    string(task.TaskType()),
		"ship":         shipSymbol,
		"container_id": containerID,
	})

	return nil
}

// HandleWorkerCompletion processes worker container completion
func (m *WorkerLifecycleManager) HandleWorkerCompletion(ctx context.Context, shipSymbol string) (*TaskCompletion, error) {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf("Worker container completed for ship %s", shipSymbol), nil)

	// Find the task by querying the database directly.
	// This is more reliable than the in-memory map which can be lost on coordinator restart.
	var task *manufacturing.ManufacturingTask
	if m.taskRepo != nil {
		var err error
		task, err = m.taskRepo.FindByAssignedShip(ctx, shipSymbol)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to find task for ship %s: %v", shipSymbol, err), nil)
			return nil, err
		}
	}

	if task == nil {
		logger.Log("WARN", fmt.Sprintf("No task found for completed ship %s", shipSymbol), nil)
		return nil, nil
	}

	taskID := task.ID()

	// Untrack from in-memory map for cleanup (optional, may not exist after restart)
	if m.assignmentTracker != nil {
		m.assignmentTracker.Untrack(taskID)
	}

	// Create completion notification
	success := task.Status() == manufacturing.TaskStatusCompleted
	var taskErr error
	if !success && task.ErrorMessage() != "" {
		taskErr = fmt.Errorf("%s", task.ErrorMessage())
	}

	// Record task completion metrics
	var duration time.Duration
	if task.StartedAt() != nil {
		duration = time.Since(*task.StartedAt())
	}
	status := "completed"
	if !success {
		status = "failed"
	}
	metrics.RecordManufacturingTaskCompletion(
		task.PlayerID(),
		string(task.TaskType()),
		status,
		duration,
	)

	completion := &TaskCompletion{
		TaskID:     taskID,
		ShipSymbol: shipSymbol,
		PipelineID: task.PipelineID(),
		Success:    success,
		Error:      taskErr,
	}

	logger.Log("INFO", fmt.Sprintf("Processed worker completion for task %s (ship: %s)", taskID[:8], shipSymbol), nil)

	return completion, nil
}

// HandleTaskFailure processes failed task (retry or mark failed)
func (m *WorkerLifecycleManager) HandleTaskFailure(ctx context.Context, completion TaskCompletion) error {
	logger := common.LoggerFromContext(ctx)

	if m.taskRepo == nil {
		return nil
	}

	task, err := m.taskRepo.FindByID(ctx, completion.TaskID)
	if err != nil || task == nil {
		return fmt.Errorf("failed to fetch task: %w", err)
	}

	if task.CanRetry() {
		retryCount := task.RetryCount()
		maxRetries := task.MaxRetries()

		if err := task.ResetForRetry(); err != nil {
			return fmt.Errorf("failed to reset task for retry: %w", err)
		}

		// COLLECT_SELL tasks should stay PENDING and let SupplyMonitor mark them READY
		// when factory supply is HIGH/ABUNDANT. Only ACQUIRE_DELIVER can be immediately ready.
		if task.TaskType() != manufacturing.TaskTypeCollectSell {
			if err := task.MarkReady(); err != nil {
				return fmt.Errorf("failed to mark task ready: %w", err)
			}
			m.taskQueue.Enqueue(task)
		}
		// COLLECT_SELL stays PENDING - SupplyMonitor will mark it READY when factory is ready

		if err := m.taskRepo.Update(ctx, task); err != nil {
			return fmt.Errorf("failed to persist task retry state: %w", err)
		}

		// Record task retry metrics
		metrics.RecordManufacturingTaskRetry(task.PlayerID(), string(task.TaskType()))

		logger.Log("INFO", fmt.Sprintf("Task %s scheduled for retry (%d/%d, type=%s)",
			completion.TaskID[:8], retryCount, maxRetries, task.TaskType()), nil)
	} else {
		logger.Log("ERROR", fmt.Sprintf("Task %s permanently failed after %d retries",
			completion.TaskID[:8], task.RetryCount()), nil)

		if completion.PipelineID != "" && m.pipelineManager != nil {
			_, _ = m.pipelineManager.CheckPipelineCompletion(ctx, completion.PipelineID)
		}
	}

	return nil
}

// WorkerCommand represents the command to execute a worker task
// This mirrors RunManufacturingTaskWorkerCommand for serialization
// (kept separate to avoid circular import with trading/commands)
type WorkerCommand struct {
	ShipSymbol     string
	Task           *manufacturing.ManufacturingTask
	PlayerID       int
	ContainerID    string
	CoordinatorID  string
	PipelineNumber int
	ProductGood    string
}


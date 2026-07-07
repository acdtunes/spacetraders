package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RunManufacturingTaskWorkerCommand executes a single manufacturing task
type RunManufacturingTaskWorkerCommand struct {
	ShipSymbol     string                           // Ship to use for this task
	Task           *manufacturing.ManufacturingTask // Task to execute
	PlayerID       int                              // Player identifier
	ContainerID    string                           // Container ID for ledger tracking
	CoordinatorID  string                           // Parent coordinator container ID
	PipelineNumber int                              // Sequential pipeline number (1, 2, 3...)
	ProductGood    string                           // Final manufactured product (e.g., LASER_RIFLES)
}

// RunManufacturingTaskWorkerResponse contains the results of task execution
type RunManufacturingTaskWorkerResponse struct {
	Success        bool   // Whether execution succeeded
	TaskID         string // Task ID
	TaskType       string // Task type
	Good           string // Trade good handled
	ActualQuantity int    // Actual quantity handled
	TotalCost      int    // Cost incurred
	TotalRevenue   int    // Revenue earned
	NetProfit      int    // Net profit (revenue - cost)
	DurationMs     int64  // Execution duration in milliseconds
	Error          string // Error message if failed
}

// RunManufacturingTaskWorkerHandler executes a single manufacturing task
// using the Strategy pattern via TaskExecutorRegistry.
//
// This is a thin orchestrator that:
// 1. Creates operation context for ledger tracking
// 2. Marks task as executing
// 3. Delegates to appropriate executor via registry (OCP compliant)
// 4. Handles success/failure and persistence
// 5. Records metrics
type RunManufacturingTaskWorkerHandler struct {
	executorRegistry *mfgServices.TaskExecutorRegistry
	taskRepo         manufacturing.TaskRepository
}

// NewRunManufacturingTaskWorkerHandler creates a new handler
func NewRunManufacturingTaskWorkerHandler(
	executorRegistry *mfgServices.TaskExecutorRegistry,
	taskRepo manufacturing.TaskRepository,
) *RunManufacturingTaskWorkerHandler {
	return &RunManufacturingTaskWorkerHandler{
		executorRegistry: executorRegistry,
		taskRepo:         taskRepo,
	}
}

// Handle executes the command
func (h *RunManufacturingTaskWorkerHandler) Handle(
	ctx context.Context,
	request common.Request,
) (common.Response, error) {
	cmd, ok := request.(*RunManufacturingTaskWorkerCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	logger := common.LoggerFromContext(ctx)
	startTime := time.Now()
	task := cmd.Task

	// Create operation context for transaction tracking
	if cmd.ContainerID != "" {
		opContext := shared.NewOperationContext(cmd.ContainerID, "manufacturing_worker")
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	logger.Log("INFO", "Starting manufacturing task", map[string]interface{}{
		"task_id": task.ID()[:8],
		"type":    task.TaskType(),
		"good":    task.Good(),
		"ship":    cmd.ShipSymbol,
	})

	// Mark task as executing
	if err := task.StartExecution(); err != nil {
		return h.failResponse(task, startTime, fmt.Sprintf("failed to start execution: %v", err)), nil
	}

	// Get executor for task type (Strategy pattern - OCP compliant)
	executor, err := h.executorRegistry.GetExecutor(task.TaskType())
	if err != nil {
		task.Fail(err.Error())
		h.persistTask(ctx, task)
		return h.failResponse(task, startTime, err.Error()), nil
	}

	// Execute task
	playerID := shared.MustNewPlayerID(cmd.PlayerID)
	err = executor.Execute(ctx, mfgServices.TaskExecutionParams{
		Task:        task,
		ShipSymbol:  cmd.ShipSymbol,
		PlayerID:    playerID,
		ContainerID: cmd.ContainerID,
	})

	if err != nil {
		// A supply deferral is NOT a failure: the good has no buy source right now.
		// Park the task back to a pending-supply state so the SupplyMonitor re-sources
		// it when supply recovers, instead of burning a retry toward permanent death
		// (which terminalized the whole construction pipeline) - sp-hs2j.
		if errors.Is(err, mfgServices.ErrDeferToSupply) {
			return h.parkForResupply(ctx, cmd, task, startTime, err), nil
		}

		logger.Log("ERROR", "Manufacturing task failed", map[string]interface{}{
			"task_id": task.ID()[:8],
			"type":    task.TaskType(),
			"error":   err.Error(),
		})
		task.Fail(err.Error())
		h.persistTask(ctx, task)

		// Record failed task metrics
		metrics.RecordManufacturingTaskCompletion(cmd.PlayerID, string(task.TaskType()), "failed", time.Since(startTime))
		metrics.RecordManufacturingTaskRetry(cmd.PlayerID, string(task.TaskType()))

		return h.failResponse(task, startTime, err.Error()), nil
	}

	// Mark task as complete
	if err := task.Complete(); err != nil {
		return h.failResponse(task, startTime, fmt.Sprintf("failed to complete task: %v", err)), nil
	}

	// Persist completed task state
	h.persistTask(ctx, task)

	duration := time.Since(startTime)
	logger.Log("INFO", "Manufacturing task completed", map[string]interface{}{
		"task_id":     task.ID()[:8],
		"type":        task.TaskType(),
		"good":        task.Good(),
		"quantity":    task.ActualQuantity(),
		"cost":        task.TotalCost(),
		"revenue":     task.TotalRevenue(),
		"duration_ms": duration.Milliseconds(),
	})

	// Record successful task metrics
	metrics.RecordManufacturingTaskCompletion(cmd.PlayerID, string(task.TaskType()), "completed", duration)

	// Record cost and revenue metrics
	if task.TotalCost() > 0 {
		metrics.RecordManufacturingCost(cmd.PlayerID, string(task.TaskType()), task.TotalCost())
	}
	if task.TotalRevenue() > 0 {
		metrics.RecordManufacturingRevenue(cmd.PlayerID, task.TotalRevenue())
	}

	return &RunManufacturingTaskWorkerResponse{
		Success:        true,
		TaskID:         task.ID(),
		TaskType:       string(task.TaskType()),
		Good:           task.Good(),
		ActualQuantity: task.ActualQuantity(),
		TotalCost:      task.TotalCost(),
		TotalRevenue:   task.TotalRevenue(),
		NetProfit:      task.NetProfit(),
		DurationMs:     duration.Milliseconds(),
	}, nil
}

// parkForResupply transitions a task that found no buy source at execution time
// back to a PENDING supply-deferral instead of failing it. The SupplyMonitor
// re-sources deferred construction tasks when market supply recovers (sp-r900),
// so the leg resumes without consuming its retry budget or terminalizing the
// pipeline (sp-hs2j). The ship is released by ParkForResupply so any ship can pick
// the task up once it is re-sourced; ship-claim cleanup happens on container exit.
func (h *RunManufacturingTaskWorkerHandler) parkForResupply(
	ctx context.Context,
	cmd *RunManufacturingTaskWorkerCommand,
	task *manufacturing.ManufacturingTask,
	startTime time.Time,
	cause error,
) *RunManufacturingTaskWorkerResponse {
	logger := common.LoggerFromContext(ctx)

	if parkErr := task.ParkForResupply(); parkErr != nil {
		// Unexpected state - fall back to a normal failure so the task is not lost.
		logger.Log("WARN", fmt.Sprintf("Could not park task %s for resupply (%v); failing instead", task.ID()[:8], parkErr), nil)
		task.Fail(cause.Error())
		h.persistTask(ctx, task)
		metrics.RecordManufacturingTaskCompletion(cmd.PlayerID, string(task.TaskType()), "failed", time.Since(startTime))
		metrics.RecordManufacturingTaskRetry(cmd.PlayerID, string(task.TaskType()))
		return h.failResponse(task, startTime, cause.Error())
	}

	h.persistTask(ctx, task)
	metrics.RecordManufacturingTaskCompletion(cmd.PlayerID, string(task.TaskType()), "deferred", time.Since(startTime))

	logger.Log("INFO", fmt.Sprintf("DELIVER_TO_CONSTRUCTION: parked task %s for resupply - no buy source for %s, awaiting supply recovery",
		task.ID()[:8], task.Good()), map[string]interface{}{
		"task_id": task.ID()[:8],
		"good":    task.Good(),
	})

	return &RunManufacturingTaskWorkerResponse{
		Success:    false,
		TaskID:     task.ID(),
		TaskType:   string(task.TaskType()),
		Good:       task.Good(),
		DurationMs: time.Since(startTime).Milliseconds(),
		Error:      cause.Error(),
	}
}

// failResponse creates a failure response
func (h *RunManufacturingTaskWorkerHandler) failResponse(
	task *manufacturing.ManufacturingTask,
	startTime time.Time,
	errMsg string,
) *RunManufacturingTaskWorkerResponse {
	return &RunManufacturingTaskWorkerResponse{
		Success:    false,
		TaskID:     task.ID(),
		TaskType:   string(task.TaskType()),
		Good:       task.Good(),
		DurationMs: time.Since(startTime).Milliseconds(),
		Error:      errMsg,
	}
}

// persistTask saves the task state to the repository
func (h *RunManufacturingTaskWorkerHandler) persistTask(ctx context.Context, task *manufacturing.ManufacturingTask) {
	if h.taskRepo != nil {
		if err := h.taskRepo.Update(ctx, task); err != nil {
			logger := common.LoggerFromContext(ctx)
			logger.Log("WARN", fmt.Sprintf("Failed to persist task: %v", err), nil)
		}
	}
}

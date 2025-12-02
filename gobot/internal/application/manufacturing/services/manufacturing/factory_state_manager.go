package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// FactoryManager manages factory state updates and task dependencies
type FactoryManager interface {
	// UpdateFactoryStateOnDelivery records delivery and updates factory state
	UpdateFactoryStateOnDelivery(ctx context.Context, taskID, shipSymbol, pipelineID string) error

	// CreateContinuedDeliveryTasks creates new ACQUIRE_DELIVER tasks when factory not ready
	CreateContinuedDeliveryTasks(ctx context.Context, completedTask *manufacturing.ManufacturingTask, pipelineID, factorySymbol string) error

	// UpdateDependentTasks marks tasks as READY when dependencies complete
	UpdateDependentTasks(ctx context.Context, completedTaskID, pipelineID string) error
}

// FactoryStateManager implements FactoryManager
type FactoryStateManager struct {
	taskRepo         manufacturing.TaskRepository
	factoryStateRepo manufacturing.FactoryStateRepository
	factoryTracker   *manufacturing.FactoryStateTracker
	taskQueue        services.ManufacturingTaskQueue
}

// NewFactoryStateManager creates a new factory state manager
func NewFactoryStateManager(
	taskRepo manufacturing.TaskRepository,
	factoryStateRepo manufacturing.FactoryStateRepository,
	factoryTracker *manufacturing.FactoryStateTracker,
	taskQueue services.ManufacturingTaskQueue,
) *FactoryStateManager {
	return &FactoryStateManager{
		taskRepo:         taskRepo,
		factoryStateRepo: factoryStateRepo,
		factoryTracker:   factoryTracker,
		taskQueue:        taskQueue,
	}
}

// UpdateFactoryStateOnDelivery records a delivery in the factory state tracker
func (m *FactoryStateManager) UpdateFactoryStateOnDelivery(
	ctx context.Context,
	taskID string,
	shipSymbol string,
	pipelineID string,
) error {
	logger := common.LoggerFromContext(ctx)

	if m.taskRepo == nil || m.factoryStateRepo == nil {
		return nil
	}

	// Get the task
	task, err := m.taskRepo.FindByID(ctx, taskID)
	if err != nil || task == nil {
		return nil
	}

	// Only process ACQUIRE_DELIVER and STORAGE_ACQUIRE_DELIVER tasks
	if task.TaskType() != manufacturing.TaskTypeAcquireDeliver &&
		task.TaskType() != manufacturing.TaskTypeStorageAcquireDeliver {
		return nil
	}

	factorySymbol := task.FactorySymbol()
	if factorySymbol == "" {
		return nil
	}

	// Find factory state for this pipeline and factory
	factoryStates, err := m.factoryStateRepo.FindByPipelineID(ctx, pipelineID)
	if err != nil {
		logger.Log("WARN", fmt.Sprintf("Failed to find factory states: %v", err), nil)
		return err
	}

	for _, fs := range factoryStates {
		if fs.FactorySymbol() == factorySymbol {
			// Record delivery
			if err := fs.RecordDelivery(task.Good(), task.ActualQuantity(), shipSymbol); err != nil {
				logger.Log("WARN", fmt.Sprintf("Failed to record delivery: %v", err), nil)
				continue
			}

			// Persist update
			if err := m.factoryStateRepo.Update(ctx, fs); err != nil {
				logger.Log("WARN", fmt.Sprintf("Failed to persist factory state: %v", err), nil)
				continue
			}

			// Update in-memory tracker
			if m.factoryTracker != nil {
				m.factoryTracker.LoadState(fs)
			}

			logger.Log("INFO", "Recorded delivery to factory", map[string]interface{}{
				"factory":              factorySymbol,
				"good":                 task.Good(),
				"all_inputs_delivered": fs.AllInputsDelivered(),
				"ready_for_collection": fs.IsReadyForCollection(),
			})

			// Create continued delivery tasks if needed
			if !fs.IsReadyForCollection() {
				m.CreateContinuedDeliveryTasks(ctx, task, pipelineID, factorySymbol)
			}
		}
	}

	return nil
}

// CreateContinuedDeliveryTasks creates new ACQUIRE_DELIVER tasks to continue feeding a factory
func (m *FactoryStateManager) CreateContinuedDeliveryTasks(
	ctx context.Context,
	completedDeliverTask *manufacturing.ManufacturingTask,
	pipelineID string,
	factorySymbol string,
) error {
	logger := common.LoggerFromContext(ctx)

	sourceMarket := completedDeliverTask.SourceMarket()
	if sourceMarket == "" {
		logger.Log("WARN", "Cannot create continued delivery - no source market", nil)
		return nil
	}

	// Check for existing pending task
	existingTasks, err := m.taskRepo.FindByPipelineID(ctx, pipelineID)
	if err == nil {
		for _, t := range existingTasks {
			if t.Good() == completedDeliverTask.Good() &&
				t.TaskType() == manufacturing.TaskTypeAcquireDeliver &&
				(t.Status() == manufacturing.TaskStatusPending ||
					t.Status() == manufacturing.TaskStatusReady ||
					t.Status() == manufacturing.TaskStatusAssigned) {
				logger.Log("DEBUG", "Skipping continued delivery - task already exists", nil)
				return nil
			}
		}
	}

	// Create atomic ACQUIRE_DELIVER task
	acquireDeliverTask := manufacturing.NewAcquireDeliverTask(
		pipelineID,
		completedDeliverTask.PlayerID(),
		completedDeliverTask.Good(),
		sourceMarket,
		factorySymbol,
		nil,
	)

	// SUPPLY GATING: Create task in PENDING state, NOT READY
	// SupplyMonitor.ActivateSupplyGatedTasks will check supply levels and:
	// - Activate immediately if supply is MODERATE/HIGH/ABUNDANT
	// - Keep PENDING if supply is LIMITED/SCARCE (wait for better prices)
	// - Re-source to a better market if available
	// This prevents buying from depleted markets at inflated prices.

	// Persist task in PENDING state
	if err := m.taskRepo.Create(ctx, acquireDeliverTask); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to persist task: %v", err), nil)
		return err
	}

	// DO NOT mark READY or enqueue - let SupplyMonitor handle activation
	// based on current market supply levels

	logger.Log("INFO", "Created continued delivery task (PENDING - supply gated)", map[string]interface{}{
		"good":    completedDeliverTask.Good(),
		"source":  sourceMarket,
		"factory": factorySymbol,
		"task_id": acquireDeliverTask.ID()[:8],
	})

	return nil
}

// UpdateDependentTasks marks tasks as READY if their dependencies are met
func (m *FactoryStateManager) UpdateDependentTasks(
	ctx context.Context,
	completedTaskID string,
	pipelineID string,
) error {
	logger := common.LoggerFromContext(ctx)

	if m.taskRepo == nil {
		return nil
	}

	tasks, err := m.taskRepo.FindByPipelineID(ctx, pipelineID)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to find tasks: %v", err), nil)
		return err
	}

	for _, task := range tasks {
		// Skip if not pending
		if task.Status() != manufacturing.TaskStatusPending {
			continue
		}

		// COLLECT_SELL tasks are handled by SupplyMonitor
		if task.TaskType() == manufacturing.TaskTypeCollectSell {
			continue
		}

		// Check if depends on completed task
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

		// Check if ALL dependencies are met
		allDepsMet := true
		for _, depID := range task.DependsOn() {
			depTask, err := m.taskRepo.FindByID(ctx, depID)
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

		if err := m.taskRepo.Update(ctx, task); err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to persist task: %v", err), nil)
			continue
		}

		m.taskQueue.Enqueue(task)

		logger.Log("INFO", fmt.Sprintf("Task %s (%s) is now ready", task.ID()[:8], task.TaskType()), nil)
	}

	return nil
}

package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
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
	marketRepo         market.MarketRepository
	factoryTracker     *manufacturing.FactoryStateTracker
	taskQueue          *services.TaskQueue
}

// NewStateRecoveryManager creates a new state recovery manager
func NewStateRecoveryManager(
	pipelineRepo manufacturing.PipelineRepository,
	taskRepo manufacturing.TaskRepository,
	factoryStateRepo manufacturing.FactoryStateRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	marketRepo market.MarketRepository,
	factoryTracker *manufacturing.FactoryStateTracker,
	taskQueue *services.TaskQueue,
) *StateRecoveryManager {
	return &StateRecoveryManager{
		pipelineRepo:       pipelineRepo,
		taskRepo:           taskRepo,
		factoryStateRepo:   factoryStateRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		marketRepo:         marketRepo,
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
		if task.Status() == manufacturing.TaskStatusPending {
			// COLLECT_SELL tasks are handled by SupplyMonitor
			if task.TaskType() == manufacturing.TaskTypeCollectSell {
				continue
			}

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

		// Step 2d: Enqueue all READY tasks with state validation
		if task.Status() == manufacturing.TaskStatusReady {
			// STATE SYNC: Validate COLLECT_SELL tasks against sell market supply
			if task.TaskType() == manufacturing.TaskTypeCollectSell {
				if m.isSellMarketSaturated(ctx, task.TargetMarket(), task.Good(), playerID) {
					task.ResetToPending()
					if m.taskRepo != nil {
						_ = m.taskRepo.Update(ctx, task)
					}
					logger.Log("DEBUG", "Reset COLLECT_SELL to PENDING: sell market saturated", map[string]interface{}{
						"task_id":     task.ID()[:8],
						"good":        task.Good(),
						"sell_market": task.TargetMarket(),
					})
					continue
				}
			}

			// STATE SYNC: Validate ACQUIRE_DELIVER tasks against factory input supply
			// If the factory already has HIGH/ABUNDANT supply of this input, reset to PENDING
			if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
				if m.isFactoryInputSaturated(ctx, task.FactorySymbol(), task.Good(), playerID) {
					task.ResetToPending()
					if m.taskRepo != nil {
						_ = m.taskRepo.Update(ctx, task)
					}
					logger.Log("DEBUG", "Reset ACQUIRE_DELIVER to PENDING: factory input already saturated", map[string]interface{}{
						"task_id": task.ID()[:8],
						"good":    task.Good(),
						"factory": task.FactorySymbol(),
					})
					continue
				}
			}

			m.taskQueue.Enqueue(task)
			result.ReadyTaskCount++
		}
	}

	// Step 2e: Recover FAILED tasks that can be retried
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

				// COLLECT_SELL tasks stay PENDING
				if task.TaskType() == manufacturing.TaskTypeCollectSell {
					if err := m.taskRepo.Update(ctx, task); err == nil {
						logger.Log("INFO", fmt.Sprintf("Reset FAILED COLLECT_SELL task %s to PENDING", task.ID()[:8]), nil)
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

// isSellMarketSaturated checks if sell market has HIGH or ABUNDANT supply
func (m *StateRecoveryManager) isSellMarketSaturated(ctx context.Context, sellMarket string, good string, playerID int) bool {
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

// isFactoryInputSaturated checks if factory already has HIGH or ABUNDANT supply of an input good.
// This is used during state recovery to avoid re-queuing ACQUIRE_DELIVER tasks for inputs
// that no longer need replenishment.
func (m *StateRecoveryManager) isFactoryInputSaturated(ctx context.Context, factorySymbol string, inputGood string, playerID int) bool {
	if m.marketRepo == nil {
		return false
	}

	marketData, err := m.marketRepo.GetMarketData(ctx, factorySymbol, playerID)
	if err != nil || marketData == nil {
		return false // Can't check, assume not saturated
	}

	tradeGood := marketData.FindGood(inputGood)
	if tradeGood == nil || tradeGood.Supply() == nil {
		return false
	}

	// For factory inputs (IMPORT goods), HIGH/ABUNDANT means we don't need more deliveries
	supply := *tradeGood.Supply()
	return supply == "HIGH" || supply == "ABUNDANT"
}

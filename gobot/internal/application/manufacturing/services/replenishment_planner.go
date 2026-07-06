package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// ReplenishmentPlanner creates ACQUIRE_DELIVER tasks when factory supply drops.
// This is the DEMAND-DRIVEN supply chain model: only acquire raw materials when needed.
// When a required input is available from a running storage operation (e.g., gas siphoning),
// it creates STORAGE_ACQUIRE_DELIVER tasks instead of regular ACQUIRE_DELIVER tasks.
type ReplenishmentPlanner struct {
	marketRepo     market.MarketRepository
	taskRepo       manufacturing.TaskRepository
	taskQueue      ManufacturingTaskQueue
	pipelineRepo   manufacturing.PipelineRepository
	marketLocator  *MarketLocator
	storageSources *StorageSourceFinder
	supply         marketSupplyReader
	playerID       int
	notifier       *taskReadyNotifier
}

// createTasksForFactory creates ACQUIRE_DELIVER tasks when factory supply drops.
//
// Algorithm:
//  1. Get market data for the factory to find IMPORT goods (factory inputs)
//  2. Check if there are already pending ACQUIRE_DELIVER tasks for these inputs
//  3. Check each input's supply level at the factory (INPUT BALANCING)
//  4. For each input that is LOW and without a pending task, find an EXPORT market and create task
func (rp *ReplenishmentPlanner) createTasksForFactory(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	if rp.marketLocator == nil {
		logger.Log("WARN", "MarketLocator not available - cannot create ACQUIRE_DELIVER tasks", map[string]interface{}{
			"factory": factory.FactorySymbol(),
		})
		return
	}

	// CRITICAL: Verify pipeline is still EXECUTING before creating new tasks
	if !pipelineExecutingForFactory(ctx, rp.pipelineRepo, factory, "Skipping ACQUIRE_DELIVER task creation") {
		return
	}

	// Get factory market data to find required inputs (IMPORT goods)
	marketData, err := rp.marketRepo.GetMarketData(ctx, factory.FactorySymbol(), factory.PlayerID())
	if err != nil {
		logger.Log("WARN", "Failed to get market data for factory", map[string]interface{}{
			"factory": factory.FactorySymbol(),
			"error":   err.Error(),
		})
		return
	}

	if len(factory.RequiredInputs()) == 0 {
		logger.Log("DEBUG", "Factory has no required inputs - may be a source factory", map[string]interface{}{
			"factory": factory.FactorySymbol(),
			"output":  factory.OutputGood(),
		})
		return
	}

	factoryInputs := requiredImportInputs(marketData, factory)
	if len(factoryInputs) == 0 {
		logger.Log("DEBUG", "Factory required inputs not available as IMPORT at market", map[string]interface{}{
			"factory":         factory.FactorySymbol(),
			"output":          factory.OutputGood(),
			"required_inputs": factory.RequiredInputs(),
		})
		return
	}

	// Get existing tasks for this pipeline to check what's already pending
	existingTasks, err := rp.taskRepo.FindByPipelineID(ctx, factory.PipelineID())
	if err != nil {
		logger.Log("WARN", "Failed to find existing tasks", map[string]interface{}{
			"pipeline": factory.PipelineID(),
			"error":    err.Error(),
		})
		return
	}

	// Build maps of goods with pending tasks, tracking task type separately.
	// This allows us to detect and replace ACQUIRE_DELIVER tasks with STORAGE_ACQUIRE_DELIVER
	// when a storage operation becomes available for a good.
	pendingStorageTasks, pendingAcquireTasks := pendingInputTasksByType(existingTasks, factory)

	systemSymbol := extractSystem(factory.FactorySymbol())
	tasksCreated := 0
	skippedHighSupply := 0

	// Create ACQUIRE_DELIVER or STORAGE_ACQUIRE_DELIVER tasks for inputs that need them.
	// INPUT BALANCING: Only deliver inputs that are actually needed (not already HIGH/ABUNDANT)
	for _, input := range factoryInputs {
		// INPUT BALANCING OPTIMIZATION: Skip inputs that already have HIGH/ABUNDANT supply at factory
		// This prevents over-delivering one input while another starves
		if isHighOrAbundant(input.supply) {
			logger.Log("DEBUG", "Input already abundant at factory, skipping delivery", map[string]interface{}{
				"factory": factory.FactorySymbol(),
				"input":   input.good,
				"supply":  input.supply,
			})
			skippedHighSupply++
			continue
		}

		// STORAGE OPERATION INTEGRATION: Check if this input is produced by a running storage operation
		// (e.g., gas siphoning produces LIQUID_HYDROGEN, LIQUID_NITROGEN, HYDROCARBON)
		storageOp := rp.storageSources.FindRunningOperationForGood(ctx, rp.playerID, input.good)

		// If there's already a correct STORAGE_ACQUIRE_DELIVER task, skip
		if storageOp != nil && pendingStorageTasks[input.good] {
			continue // Already has correct task type
		}

		// TASK TYPE MIGRATION: If there's a running storage operation but we have an ACQUIRE_DELIVER task,
		// cancel the wrong task and create the correct STORAGE_ACQUIRE_DELIVER task.
		// This fixes legacy tasks created before the storage operation was started.
		if storageOp != nil {
			if wrongTask, exists := pendingAcquireTasks[input.good]; exists {
				if wrongTask.Status() == manufacturing.TaskStatusExecuting {
					// Task is executing - let it complete, will create correct type on next cycle
					continue
				}
				rp.cancelAcquireTaskReplacedByStorage(ctx, factory, input.good, wrongTask, storageOp)
			}

			if rp.createStorageAcquireDeliverTask(ctx, factory, input.good, storageOp) {
				tasksCreated++
			}
			continue
		}

		// No storage operation - check if we already have an ACQUIRE_DELIVER task
		if pendingAcquireTasks[input.good] != nil {
			continue
		}

		if rp.createMarketAcquireDeliverTask(ctx, factory, input.good, systemSymbol) {
			tasksCreated++
		}
	}

	if tasksCreated > 0 || skippedHighSupply > 0 {
		logger.Log("INFO", "ACQUIRE_DELIVER task creation summary", map[string]interface{}{
			"factory":             factory.FactorySymbol(),
			"output":              factory.OutputGood(),
			"tasks_created":       tasksCreated,
			"skipped_high_supply": skippedHighSupply,
			"total_inputs":        len(factoryInputs),
		})
	}
	if tasksCreated > 0 {
		rp.notifier.notifyTasksReady(factory.PipelineID())
	}
}

func pipelineExecutingForFactory(ctx context.Context, pipelineRepo manufacturing.PipelineRepository, factory *manufacturing.FactoryState, skipAction string) bool {
	if pipelineRepo == nil {
		return true
	}
	logger := common.LoggerFromContext(ctx)
	pipeline, err := pipelineRepo.FindByID(ctx, factory.PipelineID())
	if err != nil || pipeline == nil {
		logger.Log("DEBUG", skipAction+" - pipeline not found", map[string]interface{}{
			"factory":     factory.FactorySymbol(),
			"pipeline_id": shortID(factory.PipelineID()),
		})
		return false
	}
	if pipeline.Status() != manufacturing.PipelineStatusExecuting {
		logger.Log("DEBUG", skipAction+" - pipeline not executing", map[string]interface{}{
			"factory":         factory.FactorySymbol(),
			"pipeline_id":     shortID(factory.PipelineID()),
			"pipeline_status": pipeline.Status(),
		})
		return false
	}
	return true
}

type factoryInput struct {
	good   string
	supply string
}

func requiredImportInputs(marketData *market.Market, factory *manufacturing.FactoryState) []factoryInput {
	requiredInputsSet := make(map[string]bool)
	for _, input := range factory.RequiredInputs() {
		requiredInputsSet[input] = true
	}

	var factoryInputs []factoryInput
	for _, tradeGood := range marketData.TradeGoods() {
		if tradeGood.TradeType() == market.TradeTypeImport && requiredInputsSet[tradeGood.Symbol()] {
			factoryInputs = append(factoryInputs, factoryInput{
				good:   tradeGood.Symbol(),
				supply: supplyOrModerate(&tradeGood),
			})
		}
	}
	return factoryInputs
}

func taskIsInFlight(task *manufacturing.ManufacturingTask) bool {
	switch task.Status() {
	case manufacturing.TaskStatusPending, manufacturing.TaskStatusReady,
		manufacturing.TaskStatusAssigned, manufacturing.TaskStatusExecuting:
		return true
	}
	return false
}

func pendingInputTasksByType(existingTasks []*manufacturing.ManufacturingTask, factory *manufacturing.FactoryState) (map[string]bool, map[string]*manufacturing.ManufacturingTask) {
	pendingStorageTasks := make(map[string]bool)
	pendingAcquireTasks := make(map[string]*manufacturing.ManufacturingTask)
	for _, task := range existingTasks {
		if task.FactorySymbol() != factory.FactorySymbol() {
			continue
		}
		if !taskIsInFlight(task) {
			continue
		}
		if task.TaskType() == manufacturing.TaskTypeStorageAcquireDeliver {
			pendingStorageTasks[task.Good()] = true
		} else if task.TaskType() == manufacturing.TaskTypeAcquireDeliver {
			pendingAcquireTasks[task.Good()] = task
		}
	}
	return pendingStorageTasks, pendingAcquireTasks
}

func acceptableSourceSupply(supply string, isRawMaterial bool) bool {
	if isHighOrAbundant(supply) {
		return true
	}
	return !isRawMaterial && supply == supplyModerate
}

func applySourceSupplyPriority(task *manufacturing.ManufacturingTask, sourceSupply string) {
	switch manufacturing.SupplyLevel(sourceSupply) {
	case manufacturing.SupplyLevelAbundant:
		task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityAbundant)
	case manufacturing.SupplyLevelHigh:
		task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityHigh)
	case manufacturing.SupplyLevelModerate:
		task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityModerate)
	}
}

func (rp *ReplenishmentPlanner) cancelAcquireTaskReplacedByStorage(ctx context.Context, factory *manufacturing.FactoryState, good string, wrongTask *manufacturing.ManufacturingTask, storageOp *storage.StorageOperation) {
	logger := common.LoggerFromContext(ctx)

	if err := wrongTask.Cancel("Replaced by STORAGE_ACQUIRE_DELIVER - storage operation now available"); err != nil {
		logger.Log("WARN", "Failed to cancel wrong task type", map[string]interface{}{
			"task_id": shortID(wrongTask.ID()),
			"error":   err.Error(),
		})
		return
	}
	if err := rp.taskRepo.Update(ctx, wrongTask); err != nil {
		logger.Log("WARN", "Failed to persist task cancellation", map[string]interface{}{
			"task_id": shortID(wrongTask.ID()),
			"error":   err.Error(),
		})
		return
	}
	logger.Log("INFO", "Cancelled ACQUIRE_DELIVER task - replacing with STORAGE_ACQUIRE_DELIVER", map[string]interface{}{
		"factory":    factory.FactorySymbol(),
		"input":      good,
		"old_task":   shortID(wrongTask.ID()),
		"storage_op": shortID(storageOp.ID()),
	})
}

func (rp *ReplenishmentPlanner) createStorageAcquireDeliverTask(ctx context.Context, factory *manufacturing.FactoryState, good string, storageOp *storage.StorageOperation) bool {
	logger := common.LoggerFromContext(ctx)

	task := manufacturing.NewStorageAcquireDeliverTask(
		factory.PipelineID(),
		factory.PlayerID(),
		good,
		storageOp.ID(),
		storageOp.WaypointSymbol(),
		factory.FactorySymbol(),
		nil,
	)

	// Storage tasks are always ready since they don't depend on market supply
	if err := task.MarkReady(); err != nil {
		logger.Log("WARN", "Failed to mark STORAGE_ACQUIRE_DELIVER task ready", map[string]interface{}{
			"error": err.Error(),
		})
		return false
	}

	if err := rp.taskRepo.Create(ctx, task); err != nil {
		logger.Log("WARN", "Failed to persist STORAGE_ACQUIRE_DELIVER task", map[string]interface{}{
			"error": err.Error(),
		})
		return false
	}

	rp.taskQueue.Enqueue(task)

	logger.Log("INFO", "Created STORAGE_ACQUIRE_DELIVER task (from gas operation)", map[string]interface{}{
		"factory":    factory.FactorySymbol(),
		"input":      good,
		"storage_op": shortID(storageOp.ID()),
		"storage_wp": storageOp.WaypointSymbol(),
		"task_id":    shortID(task.ID()),
	})
	return true
}

func (rp *ReplenishmentPlanner) createMarketAcquireDeliverTask(ctx context.Context, factory *manufacturing.FactoryState, good string, systemSymbol string) bool {
	logger := common.LoggerFromContext(ctx)

	isRawMaterial := goods.IsMineableRawMaterial(good)

	// RAW MATERIALS: Strict filter - HIGH/ABUNDANT only (SCARCE markets have 2x+ markup)
	// INTERMEDIATE GOODS: Allow MODERATE+ to bootstrap supply chains
	var exportMarket *MarketLocatorResult
	var err error
	if isRawMaterial {
		exportMarket, err = rp.marketLocator.FindExportMarketWithGoodSupply(ctx, good, systemSymbol, factory.PlayerID())
	} else {
		exportMarket, err = rp.marketLocator.FindExportMarketBySupplyPriority(ctx, good, systemSymbol, factory.PlayerID())
	}
	if err != nil || exportMarket == nil {
		logger.Log("WARN", fmt.Sprintf("No export market for %s: %v", good, err), map[string]interface{}{
			"factory":    factory.FactorySymbol(),
			"input":      good,
			"is_raw":     isRawMaterial,
			"min_supply": map[bool]string{true: "HIGH/ABUNDANT", false: "MODERATE+"}[isRawMaterial],
		})
		return false
	}

	// SUPPLY-GATED TASK CREATION: Check source market supply before marking task ready
	sourceSupply := rp.supply.sourceMarketSupply(ctx, exportMarket.WaypointSymbol, good)
	isAcceptableSupply := acceptableSourceSupply(sourceSupply, isRawMaterial)

	task := manufacturing.NewAcquireDeliverTask(
		factory.PipelineID(),
		factory.PlayerID(),
		good,
		exportMarket.WaypointSymbol,
		factory.FactorySymbol(),
		nil,
	)
	applySourceSupplyPriority(task, sourceSupply)

	shouldEnqueue := false
	if isAcceptableSupply {
		if err := task.MarkReady(); err != nil {
			logger.Log("WARN", "Failed to mark ACQUIRE_DELIVER task ready", map[string]interface{}{
				"error": err.Error(),
			})
			return false
		}
		shouldEnqueue = true

		logger.Log("INFO", "Created ACQUIRE_DELIVER task (READY)", map[string]interface{}{
			"factory":       factory.FactorySymbol(),
			"input":         good,
			"source":        exportMarket.WaypointSymbol,
			"source_supply": sourceSupply,
			"is_raw":        isRawMaterial,
			"task_id":       shortID(task.ID()),
		})
	} else {
		// Stay PENDING - will be activated when source market supply improves
		logger.Log("INFO", "Created ACQUIRE_DELIVER task (PENDING - supply gated)", map[string]interface{}{
			"factory":       factory.FactorySymbol(),
			"input":         good,
			"source":        exportMarket.WaypointSymbol,
			"source_supply": sourceSupply,
			"waiting_for":   "HIGH/ABUNDANT",
			"task_id":       shortID(task.ID()),
		})
	}

	// Persist task BEFORE enqueueing to prevent orphaned queue entries
	if err := rp.taskRepo.Create(ctx, task); err != nil {
		logger.Log("WARN", "Failed to persist ACQUIRE_DELIVER task", map[string]interface{}{
			"error": err.Error(),
		})
		return false
	}

	if shouldEnqueue {
		rp.taskQueue.Enqueue(task)
	}
	return true
}

// hasPendingAcquireDeliverTasks checks if there are any pending/ready/assigned/executing
// ACQUIRE_DELIVER tasks for this factory. Used to avoid creating duplicate delivery tasks.
func (rp *ReplenishmentPlanner) hasPendingAcquireDeliverTasks(ctx context.Context, factory *manufacturing.FactoryState) bool {
	if rp.taskRepo == nil {
		return false
	}

	tasks, err := rp.taskRepo.FindByPipelineID(ctx, factory.PipelineID())
	if err != nil {
		return false // Assume no pending if we can't check
	}

	for _, task := range tasks {
		if task.TaskType() == manufacturing.TaskTypeAcquireDeliver &&
			task.FactorySymbol() == factory.FactorySymbol() &&
			taskIsInFlight(task) {
			return true
		}
	}

	return false
}

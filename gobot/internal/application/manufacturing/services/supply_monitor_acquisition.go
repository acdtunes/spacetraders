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

// createAcquireDeliverTasksForFactory creates ACQUIRE_DELIVER tasks when factory supply drops.
// This is the DEMAND-DRIVEN supply chain model: only acquire raw materials when needed.
//
// Algorithm:
//  1. Get market data for the factory to find IMPORT goods (factory inputs)
//  2. Check if there are already pending ACQUIRE_DELIVER tasks for these inputs
//  3. Check each input's supply level at the factory (INPUT BALANCING)
//  4. For each input that is LOW and without a pending task, find an EXPORT market and create task
func (m *SupplyMonitor) createAcquireDeliverTasksForFactory(ctx context.Context, factory *manufacturing.FactoryState) {
	logger := common.LoggerFromContext(ctx)

	if m.marketLocator == nil {
		logger.Log("WARN", "MarketLocator not available - cannot create ACQUIRE_DELIVER tasks", map[string]interface{}{
			"factory": factory.FactorySymbol(),
		})
		return
	}

	// CRITICAL: Verify pipeline is still EXECUTING before creating new tasks
	if !m.factoryPipelineExecuting(ctx, factory, "Skipping ACQUIRE_DELIVER task creation") {
		return
	}

	// Get factory market data to find required inputs (IMPORT goods)
	marketData, err := m.marketRepo.GetMarketData(ctx, factory.FactorySymbol(), factory.PlayerID())
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
	existingTasks, err := m.taskRepo.FindByPipelineID(ctx, factory.PipelineID())
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
		storageOp := m.findRunningStorageOperationForGood(ctx, input.good)

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
				m.cancelAcquireTaskReplacedByStorage(ctx, factory, input.good, wrongTask, storageOp)
			}

			if m.createStorageAcquireDeliverTask(ctx, factory, input.good, storageOp) {
				tasksCreated++
			}
			continue
		}

		// No storage operation - check if we already have an ACQUIRE_DELIVER task
		if pendingAcquireTasks[input.good] != nil {
			continue
		}

		if m.createMarketAcquireDeliverTask(ctx, factory, input.good, systemSymbol) {
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
		m.notifyTaskReady(factory.PipelineID())
	}
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
	switch sourceSupply {
	case supplyAbundant:
		task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityAbundant)
	case supplyHigh:
		task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityHigh)
	case supplyModerate:
		task.SetPriority(manufacturing.PriorityAcquireDeliver + manufacturing.SupplyPriorityModerate)
	}
}

func (m *SupplyMonitor) cancelAcquireTaskReplacedByStorage(ctx context.Context, factory *manufacturing.FactoryState, good string, wrongTask *manufacturing.ManufacturingTask, storageOp *storage.StorageOperation) {
	logger := common.LoggerFromContext(ctx)

	if err := wrongTask.Cancel("Replaced by STORAGE_ACQUIRE_DELIVER - storage operation now available"); err != nil {
		logger.Log("WARN", "Failed to cancel wrong task type", map[string]interface{}{
			"task_id": shortID(wrongTask.ID()),
			"error":   err.Error(),
		})
		return
	}
	if err := m.taskRepo.Update(ctx, wrongTask); err != nil {
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

func (m *SupplyMonitor) createStorageAcquireDeliverTask(ctx context.Context, factory *manufacturing.FactoryState, good string, storageOp *storage.StorageOperation) bool {
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

	if err := m.taskRepo.Create(ctx, task); err != nil {
		logger.Log("WARN", "Failed to persist STORAGE_ACQUIRE_DELIVER task", map[string]interface{}{
			"error": err.Error(),
		})
		return false
	}

	m.taskQueue.Enqueue(task)

	logger.Log("INFO", "Created STORAGE_ACQUIRE_DELIVER task (from gas operation)", map[string]interface{}{
		"factory":    factory.FactorySymbol(),
		"input":      good,
		"storage_op": shortID(storageOp.ID()),
		"storage_wp": storageOp.WaypointSymbol(),
		"task_id":    shortID(task.ID()),
	})
	return true
}

func (m *SupplyMonitor) createMarketAcquireDeliverTask(ctx context.Context, factory *manufacturing.FactoryState, good string, systemSymbol string) bool {
	logger := common.LoggerFromContext(ctx)

	isRawMaterial := goods.IsMineableRawMaterial(good)

	// RAW MATERIALS: Strict filter - HIGH/ABUNDANT only (SCARCE markets have 2x+ markup)
	// INTERMEDIATE GOODS: Allow MODERATE+ to bootstrap supply chains
	var exportMarket *MarketLocatorResult
	var err error
	if isRawMaterial {
		exportMarket, err = m.marketLocator.FindExportMarketWithGoodSupply(ctx, good, systemSymbol, factory.PlayerID())
	} else {
		exportMarket, err = m.marketLocator.FindExportMarketBySupplyPriority(ctx, good, systemSymbol, factory.PlayerID())
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
	sourceSupply := m.getSourceMarketSupply(ctx, exportMarket.WaypointSymbol, good)
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
	if err := m.taskRepo.Create(ctx, task); err != nil {
		logger.Log("WARN", "Failed to persist ACQUIRE_DELIVER task", map[string]interface{}{
			"error": err.Error(),
		})
		return false
	}

	if shouldEnqueue {
		m.taskQueue.Enqueue(task)
	}
	return true
}

// hasPendingAcquireDeliverTasks checks if there are any pending/ready/assigned/executing
// ACQUIRE_DELIVER tasks for this factory. Used to avoid creating duplicate delivery tasks.
func (m *SupplyMonitor) hasPendingAcquireDeliverTasks(ctx context.Context, factory *manufacturing.FactoryState) bool {
	if m.taskRepo == nil {
		return false
	}

	tasks, err := m.taskRepo.FindByPipelineID(ctx, factory.PipelineID())
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

// findRunningStorageOperationForGood checks if there's a running storage operation
// that produces the specified good. This enables integration between gas siphoning
// operations and the manufacturing pipeline - instead of buying gases from market,
// haulers can pick up cargo directly from storage ships at the extraction site.
//
// Returns the storage operation if found, nil otherwise.
func (m *SupplyMonitor) findRunningStorageOperationForGood(ctx context.Context, good string) *storage.StorageOperation {
	logger := common.LoggerFromContext(ctx)

	if m.storageOpRepo == nil {
		logger.Log("DEBUG", "Storage operation lookup: storageOpRepo is nil", map[string]interface{}{
			"good": good,
		})
		return nil
	}

	// Find storage operations that support this good
	operations, err := m.storageOpRepo.FindByGood(ctx, m.playerID, good)
	if err != nil {
		logger.Log("WARN", "Storage operation lookup: FindByGood failed", map[string]interface{}{
			"good":      good,
			"player_id": m.playerID,
			"error":     err.Error(),
		})
		return nil
	}

	logger.Log("DEBUG", "Storage operation lookup", map[string]interface{}{
		"good":             good,
		"player_id":        m.playerID,
		"operations_found": len(operations),
	})

	// Return the first RUNNING operation that supports this good
	for _, op := range operations {
		isRunning := op.IsRunning()
		supportsGood := op.SupportsGood(good)
		logger.Log("DEBUG", "Storage operation check", map[string]interface{}{
			"op_id":         shortID(op.ID()),
			"op_status":     op.Status(),
			"is_running":    isRunning,
			"supports_good": supportsGood,
		})
		if isRunning && supportsGood {
			return op
		}
	}

	return nil
}

// DeactivateSaturatedAcquireDeliverTasks resets READY ACQUIRE_DELIVER tasks to PENDING
// when the factory's input supply has become HIGH/ABUNDANT since the task was marked ready.
// This prevents wasted trips when factory already has enough supply.
func (m *SupplyMonitor) DeactivateSaturatedAcquireDeliverTasks(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	if m.taskRepo == nil {
		return 0
	}

	// Find all READY ACQUIRE_DELIVER tasks for this player
	readyTasks, err := m.taskRepo.FindByStatus(ctx, m.playerID, manufacturing.TaskStatusReady)
	if err != nil {
		logger.Log("WARN", "Failed to find ready tasks for saturation check", map[string]interface{}{
			"error": err.Error(),
		})
		return 0
	}

	deactivated := 0
	for _, task := range readyTasks {
		// Only process ACQUIRE_DELIVER tasks
		if task.TaskType() != manufacturing.TaskTypeAcquireDeliver {
			continue
		}

		// Check factory input supply level
		factoryInputSupply := m.getSourceMarketSupply(ctx, task.FactorySymbol(), task.Good())
		if !isHighOrAbundant(factoryInputSupply) {
			continue // Factory still needs this input
		}

		// Factory input is saturated - reset task to PENDING
		task.ResetToPending()
		if err := m.taskRepo.Update(ctx, task); err != nil {
			logger.Log("WARN", "Failed to deactivate saturated task", map[string]interface{}{
				"task_id": shortID(task.ID()),
				"error":   err.Error(),
			})
			continue
		}

		deactivated++
		logger.Log("INFO", "Deactivated READY task - factory input saturated", map[string]interface{}{
			"task_id":        shortID(task.ID()),
			"good":           task.Good(),
			"factory":        task.FactorySymbol(),
			"factory_supply": factoryInputSupply,
		})
	}

	if deactivated > 0 {
		logger.Log("INFO", "Deactivated saturated ACQUIRE_DELIVER tasks", map[string]interface{}{
			"deactivated": deactivated,
		})
	}

	return deactivated
}

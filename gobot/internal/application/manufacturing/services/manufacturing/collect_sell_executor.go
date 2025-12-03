package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// CollectSellExecutor executes COLLECT_SELL tasks.
// This is an atomic task that collects from factory (when supply is HIGH)
// AND sells at demand market. Same ship does both operations.
//
// Also supports storage-based collection when task.IsStorageBasedCollection() is true.
// In that case, cargo is collected from storage ships instead of purchased from factory.
type CollectSellExecutor struct {
	navigator          Navigator
	purchaser          *ManufacturingPurchaser
	seller             *ManufacturingSeller
	storageCoordinator storage.StorageCoordinator  // Optional: for storage-based collection
	apiClient          domainPorts.APIClient       // Optional: for storage cargo transfer
	shipRepo           navigation.ShipRepository   // Optional: for syncing cargo state
}

// NewCollectSellExecutor creates a new executor for COLLECT_SELL tasks.
func NewCollectSellExecutor(
	navigator Navigator,
	purchaser *ManufacturingPurchaser,
	seller *ManufacturingSeller,
) *CollectSellExecutor {
	return &CollectSellExecutor{
		navigator: navigator,
		purchaser: purchaser,
		seller:    seller,
	}
}

// WithStorageSupport adds storage collection support to the executor.
// Required for tasks that collect from storage ships instead of factories.
func (e *CollectSellExecutor) WithStorageSupport(
	storageCoordinator storage.StorageCoordinator,
	apiClient domainPorts.APIClient,
	shipRepo navigation.ShipRepository,
) *CollectSellExecutor {
	e.storageCoordinator = storageCoordinator
	e.apiClient = apiClient
	e.shipRepo = shipRepo
	return e
}

// TaskType returns the task type this executor handles.
func (e *CollectSellExecutor) TaskType() manufacturing.TaskType {
	return manufacturing.TaskTypeCollectSell
}

// Execute runs the COLLECT_SELL task workflow:
// Phase 1: Idempotent check - already has cargo? Skip to sell
// Phase 2: Navigate to factory/storage, collect goods
// Phase 3: Navigate to sell market, sell goods
func (e *CollectSellExecutor) Execute(ctx context.Context, params TaskExecutionParams) error {
	task := params.Task
	logger := common.LoggerFromContext(ctx)

	// Determine if this is storage-based or factory-based collection
	isStorageCollection := task.IsStorageBasedCollection()

	if isStorageCollection {
		logger.Log("INFO", "COLLECT_SELL: Starting storage-based collect-and-sell", map[string]interface{}{
			"ship":             params.ShipSymbol,
			"good":             task.Good(),
			"storageWaypoint":  task.StorageWaypoint(),
			"storageOperation": task.StorageOperationID(),
			"market":           task.TargetMarket(),
		})
	} else {
		logger.Log("INFO", "COLLECT_SELL: Starting factory-based collect-and-sell", map[string]interface{}{
			"ship":    params.ShipSymbol,
			"good":    task.Good(),
			"factory": task.FactorySymbol(),
			"market":  task.TargetMarket(),
		})
	}

	// Load ship to check current state
	ship, err := e.navigator.ReloadShip(ctx, params.ShipSymbol, params.PlayerID)
	if err != nil {
		return err
	}

	// --- PHASE 1: COLLECT ---

	// Idempotent: Check if we already have the cargo (resume after crash)
	alreadyHasCargo := ship.Cargo().HasItem(task.Good(), 1)
	var totalUnitsAdded int
	var totalCost int

	if alreadyHasCargo {
		totalUnitsAdded = ship.Cargo().GetItemUnits(task.Good())
		logger.Log("INFO", "COLLECT_SELL: Already have cargo (resuming at sell phase)", map[string]interface{}{
			"good":     task.Good(),
			"quantity": totalUnitsAdded,
		})
	} else if isStorageCollection {
		// Storage-based collection: collect from storage ships
		unitsCollected, err := e.collectFromStorage(ctx, params, task, ship, logger)
		if err != nil {
			return err
		}
		totalUnitsAdded = unitsCollected
		totalCost = 0 // Storage goods are "free" (already extracted)
	} else {
		// Factory-based collection: buy from factory market
		logger.Log("INFO", "COLLECT_SELL: Navigating to factory", map[string]interface{}{
			"from": ship.CurrentLocation().Symbol,
			"to":   task.FactorySymbol(),
		})

		_, err = e.navigator.NavigateAndDock(ctx, params.ShipSymbol, task.FactorySymbol(), params.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to navigate to factory: %w", err)
		}

		// Execute purchase loop with HIGH supply requirement
		purchaseResult, err := e.purchaser.ExecutePurchaseLoop(ctx, PurchaseLoopParams{
			ShipSymbol:        params.ShipSymbol,
			PlayerID:          params.PlayerID,
			Good:              task.Good(),
			TaskID:            task.ID(),
			DesiredQty:        task.Quantity(),
			Market:            task.FactorySymbol(),
			Factory:           task.FactorySymbol(),
			RequireHighSupply: true, // COLLECT requires HIGH or ABUNDANT supply
		})
		if err != nil {
			return err
		}

		if purchaseResult.TotalUnitsAdded == 0 {
			return fmt.Errorf("COLLECT_SELL: no goods collected from factory - will retry")
		}

		totalUnitsAdded = purchaseResult.TotalUnitsAdded
		totalCost = purchaseResult.TotalCost
	}

	// --- PHASE 2: SELL (sell at target market) ---

	// Reload ship
	ship, err = e.navigator.ReloadShip(ctx, params.ShipSymbol, params.PlayerID)
	if err != nil {
		return err
	}

	// Navigate to sell market and dock
	logger.Log("INFO", "COLLECT_SELL: Navigating to sell market", map[string]interface{}{
		"from":     ship.CurrentLocation().Symbol,
		"to":       task.TargetMarket(),
		"carrying": totalUnitsAdded,
	})

	ship, err = e.navigator.NavigateAndDock(ctx, params.ShipSymbol, task.TargetMarket(), params.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to navigate to sell market: %w", err)
	}

	// Get actual cargo quantity
	sellQty := ship.Cargo().GetItemUnits(task.Good())

	logger.Log("INFO", "COLLECT_SELL: Selling at market", map[string]interface{}{
		"good":     task.Good(),
		"quantity": sellQty,
		"market":   task.TargetMarket(),
	})

	// Sell goods
	sellResult, err := e.seller.SellCargo(ctx, SellParams{
		ShipSymbol: params.ShipSymbol,
		PlayerID:   params.PlayerID,
		Good:       task.Good(),
		Quantity:   sellQty,
		TaskID:     task.ID(),
		Market:     task.TargetMarket(),
		TotalCost:  totalCost,
	})
	if err != nil {
		return err
	}

	// Update task results
	task.SetActualQuantity(sellResult.UnitsSold)
	task.SetTotalCost(totalCost)
	task.SetTotalRevenue(sellResult.TotalRevenue)

	logger.Log("INFO", "COLLECT_SELL: Complete", map[string]interface{}{
		"good":       task.Good(),
		"sold":       sellResult.UnitsSold,
		"revenue":    sellResult.TotalRevenue,
		"cost":       totalCost,
		"net_profit": sellResult.NetProfit,
	})

	return nil
}

// collectFromStorage collects cargo from storage ships for storage-based COLLECT_SELL tasks.
// Returns the number of units collected.
func (e *CollectSellExecutor) collectFromStorage(
	ctx context.Context,
	params TaskExecutionParams,
	task *manufacturing.ManufacturingTask,
	ship interface{ AvailableCargoSpace() int },
	logger common.ContainerLogger,
) (int, error) {
	// Validate storage support is configured
	if e.storageCoordinator == nil || e.apiClient == nil {
		return 0, fmt.Errorf("storage collection requires storageCoordinator and apiClient (use WithStorageSupport)")
	}

	// Get player token for API calls
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get player token: %w", err)
	}

	// Navigate to storage waypoint (stay in orbit for cargo transfer)
	logger.Log("INFO", "COLLECT_SELL: Navigating to storage waypoint", map[string]interface{}{
		"to": task.StorageWaypoint(),
	})

	err = e.navigator.NavigateTo(ctx, params.ShipSymbol, task.StorageWaypoint(), params.PlayerID)
	if err != nil {
		return 0, fmt.Errorf("failed to navigate to storage waypoint: %w", err)
	}

	// Calculate how much cargo we can pick up
	availableSpace := ship.AvailableCargoSpace()
	if availableSpace <= 0 {
		return 0, fmt.Errorf("no cargo space available on ship %s", params.ShipSymbol)
	}

	// Use low minimum threshold to avoid deadlocks (same as STORAGE_ACQUIRE_DELIVER)
	const minPickupThreshold = 1
	minUnits := minPickupThreshold
	if availableSpace < minUnits {
		minUnits = availableSpace
	}

	logger.Log("INFO", "COLLECT_SELL: Waiting for cargo from storage ships", map[string]interface{}{
		"operationID":    task.StorageOperationID(),
		"good":           task.Good(),
		"minUnits":       minUnits,
		"availableSpace": availableSpace,
	})

	// WaitForCargo blocks until cargo is available and reserved
	storageShip, unitsReserved, err := e.storageCoordinator.WaitForCargo(
		ctx,
		task.StorageOperationID(),
		task.Good(),
		minUnits,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to wait for cargo: %w", err)
	}

	logger.Log("INFO", "COLLECT_SELL: Cargo reserved, transferring", map[string]interface{}{
		"storageShip":   storageShip.ShipSymbol(),
		"unitsReserved": unitsReserved,
	})

	// Transfer cargo from storage ship to hauler
	_, err = e.apiClient.TransferCargo(
		ctx,
		storageShip.ShipSymbol(), // from
		params.ShipSymbol,        // to
		task.Good(),
		unitsReserved,
		token,
	)
	if err != nil {
		// Transfer failed - cancel reservation
		cancelErr := storageShip.CancelReservation(task.Good(), unitsReserved)
		if cancelErr != nil {
			logger.Log("ERROR", "COLLECT_SELL: Failed to cancel reservation after transfer error", map[string]interface{}{
				"error": cancelErr.Error(),
			})
		}
		return 0, fmt.Errorf("failed to transfer cargo: %w", err)
	}

	// Confirm transfer - releases reservation and updates inventory
	if err := storageShip.ConfirmTransfer(task.Good(), unitsReserved); err != nil {
		logger.Log("ERROR", "COLLECT_SELL: Failed to confirm transfer (cargo already moved)", map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Sync both ships' cargo state to database
	if e.shipRepo != nil {
		if _, syncErr := e.shipRepo.SyncShipFromAPI(ctx, storageShip.ShipSymbol(), params.PlayerID); syncErr != nil {
			logger.Log("WARN", "Failed to sync storage ship after transfer", map[string]interface{}{
				"ship":  storageShip.ShipSymbol(),
				"error": syncErr.Error(),
			})
		}
		if _, syncErr := e.shipRepo.SyncShipFromAPI(ctx, params.ShipSymbol, params.PlayerID); syncErr != nil {
			logger.Log("WARN", "Failed to sync hauler ship after transfer", map[string]interface{}{
				"ship":  params.ShipSymbol,
				"error": syncErr.Error(),
			})
		}
	}

	// Mark phase complete for recovery
	task.MarkCollectPhaseComplete()

	logger.Log("INFO", "COLLECT_SELL: Cargo collected from storage", map[string]interface{}{
		"units": unitsReserved,
	})

	return unitsReserved, nil
}

package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// StorageAcquireDeliverExecutor executes STORAGE_ACQUIRE_DELIVER tasks.
// This task type acquires cargo from storage ships at a storage operation
// waypoint and delivers it to a factory.
//
// Workflow:
//  1. Navigate to storage waypoint (in orbit, not docked)
//  2. Wait for cargo from storage ships using StorageCoordinator
//  3. Transfer cargo from storage ship to hauler
//  4. Confirm transfer (releases reservation)
//  5. Navigate to factory and dock
//  6. Deliver goods to factory
type StorageAcquireDeliverExecutor struct {
	navigator          Navigator
	seller             *ManufacturingSeller
	storageCoordinator storage.StorageCoordinator
	apiClient          domainPorts.APIClient
}

// NewStorageAcquireDeliverExecutor creates a new executor for STORAGE_ACQUIRE_DELIVER tasks.
func NewStorageAcquireDeliverExecutor(
	navigator Navigator,
	seller *ManufacturingSeller,
	storageCoordinator storage.StorageCoordinator,
	apiClient domainPorts.APIClient,
) *StorageAcquireDeliverExecutor {
	return &StorageAcquireDeliverExecutor{
		navigator:          navigator,
		seller:             seller,
		storageCoordinator: storageCoordinator,
		apiClient:          apiClient,
	}
}

// TaskType returns the task type this executor handles.
func (e *StorageAcquireDeliverExecutor) TaskType() manufacturing.TaskType {
	return manufacturing.TaskTypeStorageAcquireDeliver
}

// Execute runs the STORAGE_ACQUIRE_DELIVER task workflow.
func (e *StorageAcquireDeliverExecutor) Execute(ctx context.Context, params TaskExecutionParams) error {
	task := params.Task
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "STORAGE_ACQUIRE_DELIVER: Starting storage cargo acquisition", map[string]interface{}{
		"ship":             params.ShipSymbol,
		"good":             task.Good(),
		"storageOperation": task.StorageOperationID(),
		"storageWaypoint":  task.StorageWaypoint(),
		"factory":          task.FactorySymbol(),
	})

	// Load ship to check current state
	ship, err := e.navigator.ReloadShip(ctx, params.ShipSymbol, params.PlayerID)
	if err != nil {
		return err
	}

	// Get player token for API calls
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return fmt.Errorf("failed to get player token: %w", err)
	}

	// --- PHASE 1: ACQUIRE (get cargo from storage ships) ---

	// Idempotent: Check if we already have the cargo (resume after crash)
	alreadyHasCargo := ship.Cargo().HasItem(task.Good(), 1)
	var totalUnitsAcquired int

	if alreadyHasCargo {
		// We already have cargo - skip acquisition phase
		totalUnitsAcquired = ship.Cargo().GetItemUnits(task.Good())
		logger.Log("INFO", "STORAGE_ACQUIRE_DELIVER: Already have cargo (resuming at delivery phase)", map[string]interface{}{
			"good":     task.Good(),
			"quantity": totalUnitsAcquired,
		})
	} else {
		// Navigate to storage waypoint (stay in orbit for cargo transfer)
		logger.Log("INFO", "STORAGE_ACQUIRE_DELIVER: Navigating to storage waypoint", map[string]interface{}{
			"from": ship.CurrentLocation().Symbol,
			"to":   task.StorageWaypoint(),
		})

		err = e.navigator.NavigateTo(ctx, params.ShipSymbol, task.StorageWaypoint(), params.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to navigate to storage waypoint: %w", err)
		}

		// Reload ship after navigation
		ship, err = e.navigator.ReloadShip(ctx, params.ShipSymbol, params.PlayerID)
		if err != nil {
			return err
		}

		// Ensure ship is in orbit for cargo transfer
		if ship.IsDocked() {
			logger.Log("DEBUG", "STORAGE_ACQUIRE_DELIVER: Need to orbit for cargo transfer", nil)
			// Note: We may need to add an Orbit method to Navigator
			// For now, cargo transfer should work if both ships are in orbit
		}

		// Wait for cargo from storage ships
		availableSpace := ship.AvailableCargoSpace()
		if availableSpace <= 0 {
			return fmt.Errorf("no cargo space available on ship %s", params.ShipSymbol)
		}

		// Determine minimum cargo threshold to pick up
		// Note: We use a very low minimum (1 unit) to avoid deadlocks.
		// Gas extraction produces mixed cargo types (LIQUID_HYDROGEN, LIQUID_NITROGEN,
		// and HYDROCARBON), so a storage ship may be full (80/80) but only have
		// small amounts of the specific good we want (e.g., 4-9 units).
		// Using a higher threshold causes deadlock where haulers wait forever
		// because storage ships are full with byproducts (HYDROCARBON).
		const minPickupThreshold = 1
		minUnits := minPickupThreshold
		if availableSpace < minUnits {
			minUnits = availableSpace
		}
		// Allow picking up even 1 unit if that's all we can fit
		if minUnits < 1 {
			minUnits = 1
		}

		logger.Log("INFO", "STORAGE_ACQUIRE_DELIVER: Waiting for cargo from storage ships", map[string]interface{}{
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
			return fmt.Errorf("failed to wait for cargo: %w", err)
		}

		logger.Log("INFO", "STORAGE_ACQUIRE_DELIVER: Cargo reserved, transferring", map[string]interface{}{
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
				logger.Log("ERROR", "STORAGE_ACQUIRE_DELIVER: Failed to cancel reservation after transfer error", map[string]interface{}{
					"error": cancelErr.Error(),
				})
			}
			return fmt.Errorf("failed to transfer cargo: %w", err)
		}

		// Confirm transfer - releases reservation and updates inventory
		if err := storageShip.ConfirmTransfer(task.Good(), unitsReserved); err != nil {
			// This shouldn't happen but log it
			logger.Log("ERROR", "STORAGE_ACQUIRE_DELIVER: Failed to confirm transfer (cargo already moved)", map[string]interface{}{
				"error": err.Error(),
			})
		}

		totalUnitsAcquired = unitsReserved

		// Mark phase complete for recovery
		task.MarkAcquirePhaseComplete()

		logger.Log("INFO", "STORAGE_ACQUIRE_DELIVER: Cargo transferred successfully", map[string]interface{}{
			"units": totalUnitsAcquired,
		})
	}

	// --- PHASE 2: DELIVER (sell to factory) ---

	// Navigate to factory and dock
	logger.Log("INFO", "STORAGE_ACQUIRE_DELIVER: Navigating to factory", map[string]interface{}{
		"to":       task.FactorySymbol(),
		"carrying": totalUnitsAcquired,
	})

	ship, err = e.navigator.NavigateAndDock(ctx, params.ShipSymbol, task.FactorySymbol(), params.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to navigate to factory: %w", err)
	}

	// Get actual cargo quantity (should match what we acquired)
	deliveryQty := ship.Cargo().GetItemUnits(task.Good())

	logger.Log("INFO", "STORAGE_ACQUIRE_DELIVER: Delivering to factory", map[string]interface{}{
		"good":     task.Good(),
		"quantity": deliveryQty,
		"factory":  task.FactorySymbol(),
	})

	// Deliver to factory (sell to import market)
	sellResult, err := e.seller.DeliverToFactory(ctx, SellParams{
		ShipSymbol: params.ShipSymbol,
		PlayerID:   params.PlayerID,
		Good:       task.Good(),
		Quantity:   deliveryQty,
		TaskID:     task.ID(),
		Market:     task.FactorySymbol(),
		TotalCost:  0, // No cost for storage cargo (extracted, not purchased)
	})
	if err != nil {
		return err
	}

	// Update task results
	task.SetActualQuantity(sellResult.UnitsSold)
	task.SetTotalCost(0) // No purchase cost
	task.SetTotalRevenue(sellResult.TotalRevenue)

	logger.Log("INFO", "STORAGE_ACQUIRE_DELIVER: Complete", map[string]interface{}{
		"good":      task.Good(),
		"delivered": sellResult.UnitsSold,
		"revenue":   sellResult.TotalRevenue,
	})

	return nil
}

package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// AcquireDeliverExecutor executes ACQUIRE_DELIVER tasks.
// This is an atomic task that buys from source market AND delivers to factory.
// Same ship does both operations to prevent "orphaned cargo" bugs.
type AcquireDeliverExecutor struct {
	navigator Navigator
	purchaser *ManufacturingPurchaser
	seller    *ManufacturingSeller
}

// NewAcquireDeliverExecutor creates a new executor for ACQUIRE_DELIVER tasks.
func NewAcquireDeliverExecutor(
	navigator Navigator,
	purchaser *ManufacturingPurchaser,
	seller *ManufacturingSeller,
) *AcquireDeliverExecutor {
	return &AcquireDeliverExecutor{
		navigator: navigator,
		purchaser: purchaser,
		seller:    seller,
	}
}

// TaskType returns the task type this executor handles.
func (e *AcquireDeliverExecutor) TaskType() manufacturing.TaskType {
	return manufacturing.TaskTypeAcquireDeliver
}

// Execute runs the ACQUIRE_DELIVER task workflow:
// Phase 1: Idempotent check - already has cargo? Skip to delivery
// Phase 2: Navigate to source market, execute purchase loop
// Phase 3: Navigate to factory, deliver goods
func (e *AcquireDeliverExecutor) Execute(ctx context.Context, params TaskExecutionParams) error {
	task := params.Task
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "ACQUIRE_DELIVER: Starting atomic buy-and-deliver", map[string]interface{}{
		"ship":    params.ShipSymbol,
		"good":    task.Good(),
		"source":  task.SourceMarket(),
		"factory": task.FactorySymbol(),
	})

	// Load ship to check current state
	ship, err := e.navigator.ReloadShip(ctx, params.ShipSymbol, params.PlayerID)
	if err != nil {
		return err
	}

	// --- PHASE 1: ACQUIRE (buy from source market) ---

	// Idempotent: Check if we already have the cargo (resume after crash)
	alreadyHasCargo := ship.Cargo().HasItem(task.Good(), 1)
	var totalUnitsAdded int
	var totalCost int

	if alreadyHasCargo {
		// We already have cargo - skip acquisition phase
		totalUnitsAdded = ship.Cargo().GetItemUnits(task.Good())
		logger.Log("INFO", "ACQUIRE_DELIVER: Already have cargo (resuming at delivery phase)", map[string]interface{}{
			"good":     task.Good(),
			"quantity": totalUnitsAdded,
		})
	} else {
		// Navigate to source market and dock
		logger.Log("INFO", "ACQUIRE_DELIVER: Navigating to source market", map[string]interface{}{
			"from": ship.CurrentLocation().Symbol,
			"to":   task.SourceMarket(),
		})

		_, err = e.navigator.NavigateAndDock(ctx, params.ShipSymbol, task.SourceMarket(), params.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to navigate to source market: %w", err)
		}

		// Execute purchase loop
		purchaseResult, err := e.purchaser.ExecutePurchaseLoop(ctx, PurchaseLoopParams{
			ShipSymbol:        params.ShipSymbol,
			PlayerID:          params.PlayerID,
			Good:              task.Good(),
			TaskID:            task.ID(),
			DesiredQty:        task.Quantity(),
			Market:            task.SourceMarket(),
			Factory:           task.FactorySymbol(),
			RequireHighSupply: false,
		})
		if err != nil {
			return err
		}

		if purchaseResult.TotalUnitsAdded == 0 {
			return fmt.Errorf("ACQUIRE_DELIVER: no goods acquired at %s - will retry", task.SourceMarket())
		}

		totalUnitsAdded = purchaseResult.TotalUnitsAdded
		totalCost = purchaseResult.TotalCost
	}

	// --- PHASE 2: DELIVER (sell to factory) ---

	// Navigate to factory and dock
	logger.Log("INFO", "ACQUIRE_DELIVER: Navigating to factory", map[string]interface{}{
		"from":     ship.CurrentLocation().Symbol,
		"to":       task.FactorySymbol(),
		"carrying": totalUnitsAdded,
	})

	ship, err = e.navigator.NavigateAndDock(ctx, params.ShipSymbol, task.FactorySymbol(), params.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to navigate to factory: %w", err)
	}

	// Get actual cargo quantity (may differ from purchase amount)
	deliveryQty := ship.Cargo().GetItemUnits(task.Good())

	logger.Log("INFO", "ACQUIRE_DELIVER: Delivering to factory", map[string]interface{}{
		"good":     task.Good(),
		"quantity": deliveryQty,
		"factory":  task.FactorySymbol(),
	})

	// Deliver to factory
	sellResult, err := e.seller.DeliverToFactory(ctx, SellParams{
		ShipSymbol: params.ShipSymbol,
		PlayerID:   params.PlayerID,
		Good:       task.Good(),
		Quantity:   deliveryQty,
		TaskID:     task.ID(),
		Market:     task.FactorySymbol(),
		TotalCost:  totalCost,
	})
	if err != nil {
		return err
	}

	// Update task results
	task.SetActualQuantity(sellResult.UnitsSold)
	task.SetTotalCost(totalCost)
	task.SetTotalRevenue(sellResult.TotalRevenue)

	logger.Log("INFO", "ACQUIRE_DELIVER: Complete", map[string]interface{}{
		"good":      task.Good(),
		"delivered": sellResult.UnitsSold,
		"revenue":   sellResult.TotalRevenue,
		"cost":      totalCost,
		"net":       sellResult.NetProfit,
	})

	return nil
}

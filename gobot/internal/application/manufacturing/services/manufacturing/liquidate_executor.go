package manufacturing

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// LiquidateExecutor executes LIQUIDATE tasks.
// This task sells orphaned cargo at a demand market to recover investment.
type LiquidateExecutor struct {
	navigator Navigator
	seller    *ManufacturingSeller
}

// NewLiquidateExecutor creates a new executor for LIQUIDATE tasks.
func NewLiquidateExecutor(
	navigator Navigator,
	seller *ManufacturingSeller,
) *LiquidateExecutor {
	return &LiquidateExecutor{
		navigator: navigator,
		seller:    seller,
	}
}

// TaskType returns the task type this executor handles.
func (e *LiquidateExecutor) TaskType() manufacturing.TaskType {
	return manufacturing.TaskTypeLiquidate
}

// Execute runs the LIQUIDATE task workflow:
// 1. Idempotent check - no cargo? Already sold
// 2. Navigate to sell market
// 3. Sell goods (recovery)
func (e *LiquidateExecutor) Execute(ctx context.Context, params TaskExecutionParams) error {
	task := params.Task
	logger := common.LoggerFromContext(ctx)

	logger.Log("DEBUG", "LIQUIDATE: Loading ship state", map[string]interface{}{
		"ship": params.ShipSymbol,
		"good": task.Good(),
	})

	// Load ship
	ship, err := e.navigator.ReloadShip(ctx, params.ShipSymbol, params.PlayerID)
	if err != nil {
		return err
	}

	// Idempotent: Check if cargo is empty (already sold)
	if !ship.Cargo().HasItem(task.Good(), 1) {
		logger.Log("INFO", "LIQUIDATE: No cargo to sell (idempotent skip)", map[string]interface{}{
			"good": task.Good(),
		})
		return nil // Already sold
	}

	cargoQty := ship.Cargo().GetItemUnits(task.Good())
	logger.Log("DEBUG", "LIQUIDATE: Have cargo to sell", map[string]interface{}{
		"good":     task.Good(),
		"quantity": cargoQty,
	})

	// Navigate to sell market and dock
	logger.Log("INFO", "LIQUIDATE: Navigating to sell market", map[string]interface{}{
		"from": ship.CurrentLocation().Symbol,
		"to":   task.TargetMarket(),
		"good": task.Good(),
	})

	ship, err = e.navigator.NavigateAndDock(ctx, params.ShipSymbol, task.TargetMarket(), params.PlayerID)
	if err != nil {
		return err
	}

	// Reload ship to get cargo quantity
	ship, err = e.navigator.ReloadShip(ctx, params.ShipSymbol, params.PlayerID)
	if err != nil {
		return err
	}

	quantity := ship.Cargo().GetItemUnits(task.Good())

	logger.Log("INFO", "LIQUIDATE: Selling goods (recovery)", map[string]interface{}{
		"good":     task.Good(),
		"quantity": quantity,
		"market":   task.TargetMarket(),
	})

	// Sell goods (liquidation)
	sellResult, err := e.seller.Liquidate(ctx, SellParams{
		ShipSymbol: params.ShipSymbol,
		PlayerID:   params.PlayerID,
		Good:       task.Good(),
		Quantity:   quantity,
		TaskID:     task.ID(),
		Market:     task.TargetMarket(),
	})
	if err != nil {
		return err
	}

	// Update task results
	task.SetActualQuantity(sellResult.UnitsSold)
	task.SetTotalRevenue(sellResult.TotalRevenue)

	logger.Log("INFO", "LIQUIDATE: Sale complete", map[string]interface{}{
		"good":           task.Good(),
		"units_sold":     sellResult.UnitsSold,
		"total_revenue":  sellResult.TotalRevenue,
		"price_per_unit": sellResult.PricePerUnit,
	})

	return nil
}

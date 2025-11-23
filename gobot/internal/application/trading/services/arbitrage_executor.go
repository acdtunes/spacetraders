package services

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// ArbitrageResult represents the outcome of a single arbitrage execution cycle.
type ArbitrageResult struct {
	Good            string
	UnitsPurchased  int
	UnitsSold       int
	PurchaseCost    int
	SaleRevenue     int
	FuelCost        int
	NetProfit       int
	DurationSeconds int
}

// ArbitrageExecutor executes a complete arbitrage cycle: buy → navigate → sell.
// This is an application service that orchestrates the workflow using existing commands.
type ArbitrageExecutor struct {
	mediator common.Mediator
	shipRepo navigation.ShipRepository
}

// NewArbitrageExecutor creates a new arbitrage executor service
func NewArbitrageExecutor(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
) *ArbitrageExecutor {
	return &ArbitrageExecutor{
		mediator: mediator,
		shipRepo: shipRepo,
	}
}

// Execute performs a complete arbitrage cycle for the given opportunity.
//
// Workflow:
//  1. Navigate to buy market
//  2. Dock at buy market
//  3. Purchase cargo (fill to capacity)
//  4. Navigate to sell market
//  5. Dock at sell market
//  6. Sell all cargo
//  7. Calculate net profit
//
// All transactions (purchases, sales, fuel costs) are automatically recorded
// in the ledger via the OperationContext passed to commands.
//
// Parameters:
//   - ctx: Context for cancellation
//   - ship: Ship to use for arbitrage
//   - opportunity: Arbitrage opportunity to execute
//   - playerID: Player identifier
//   - containerID: Container ID for operation context (ledger tracking)
//
// Returns:
//   - ArbitrageResult with execution details
//   - Error if any step fails
func (e *ArbitrageExecutor) Execute(
	ctx context.Context,
	ship *navigation.Ship,
	opportunity *trading.ArbitrageOpportunity,
	playerID int,
	containerID string,
) (*ArbitrageResult, error) {
	startTime := time.Now()
	logger := common.LoggerFromContext(ctx)

	result := &ArbitrageResult{
		Good: opportunity.Good(),
	}

	// Create operation context for transaction tracking
	var opContext *shared.OperationContext
	if containerID != "" {
		opContext = shared.NewOperationContext(containerID, "arbitrage_worker")
	}

	playerIDValue := shared.MustNewPlayerID(playerID)

	logger.Log("INFO", "Starting arbitrage execution", map[string]interface{}{
		"ship":         ship.ShipSymbol(),
		"good":         opportunity.Good(),
		"buy_market":   opportunity.BuyMarket().Symbol,
		"sell_market":  opportunity.SellMarket().Symbol,
		"margin":       fmt.Sprintf("%.1f%%", opportunity.ProfitMargin()),
		"estimated":    opportunity.EstimatedProfit(),
	})

	// Step 1: Navigate to buy market
	logger.Log("INFO", "Navigating to buy market", map[string]interface{}{
		"ship":       ship.ShipSymbol(),
		"market":     opportunity.BuyMarket().Symbol,
	})

	_, err := e.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
		ShipSymbol:   ship.ShipSymbol(),
		Destination:  opportunity.BuyMarket().Symbol,
		PlayerID:     playerIDValue,
		PreferCruise: false, // Use BURN for speed
	})
	if err != nil {
		return nil, fmt.Errorf("navigation to buy market failed: %w", err)
	}

	// Note: Fuel costs are automatically tracked via ledger when using OperationContext

	// Step 2: Dock at buy market
	logger.Log("INFO", "Docking at buy market", map[string]interface{}{
		"ship":   ship.ShipSymbol(),
		"market": opportunity.BuyMarket().Symbol,
	})

	_, err = e.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerIDValue,
	})
	if err != nil {
		return nil, fmt.Errorf("docking at buy market failed: %w", err)
	}

	// Reload ship to get current cargo space
	ship, err = e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship: %w", err)
	}

	// Step 3: Purchase cargo (fill to capacity)
	availableSpace := ship.Cargo().AvailableCapacity()
	if availableSpace <= 0 {
		return nil, fmt.Errorf("ship has no available cargo space")
	}

	logger.Log("INFO", "Purchasing cargo", map[string]interface{}{
		"ship":  ship.ShipSymbol(),
		"good":  opportunity.Good(),
		"units": availableSpace,
	})

	purchaseResp, err := e.mediator.Send(ctx, &shipCmd.PurchaseCargoCommand{
		ShipSymbol: ship.ShipSymbol(),
		GoodSymbol: opportunity.Good(),
		Units:      availableSpace,
		PlayerID:   playerIDValue,
		Context:    opContext, // Auto-record transaction
	})
	if err != nil {
		return nil, fmt.Errorf("purchase failed: %w", err)
	}

	if purchaseResp, ok := purchaseResp.(*shipCmd.PurchaseCargoResponse); ok {
		result.UnitsPurchased = purchaseResp.UnitsAdded
		result.PurchaseCost = purchaseResp.TotalCost
	}

	logger.Log("INFO", "Purchase completed", map[string]interface{}{
		"ship":  ship.ShipSymbol(),
		"units": result.UnitsPurchased,
		"cost":  result.PurchaseCost,
	})

	// Step 4: Navigate to sell market
	logger.Log("INFO", "Navigating to sell market", map[string]interface{}{
		"ship":   ship.ShipSymbol(),
		"market": opportunity.SellMarket().Symbol,
	})

	_, err = e.mediator.Send(ctx, &shipCmd.NavigateRouteCommand{
		ShipSymbol:   ship.ShipSymbol(),
		Destination:  opportunity.SellMarket().Symbol,
		PlayerID:     playerIDValue,
		PreferCruise: false,
	})
	if err != nil {
		return nil, fmt.Errorf("navigation to sell market failed: %w", err)
	}

	// Note: Fuel costs are automatically tracked via ledger when using OperationContext

	// Step 5: Dock at sell market
	logger.Log("INFO", "Docking at sell market", map[string]interface{}{
		"ship":   ship.ShipSymbol(),
		"market": opportunity.SellMarket().Symbol,
	})

	_, err = e.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerIDValue,
	})
	if err != nil {
		return nil, fmt.Errorf("docking at sell market failed: %w", err)
	}

	// Step 6: Sell all cargo
	logger.Log("INFO", "Selling cargo", map[string]interface{}{
		"ship":  ship.ShipSymbol(),
		"good":  opportunity.Good(),
		"units": result.UnitsPurchased,
	})

	sellResp, err := e.mediator.Send(ctx, &shipCmd.SellCargoCommand{
		ShipSymbol: ship.ShipSymbol(),
		GoodSymbol: opportunity.Good(),
		Units:      result.UnitsPurchased,
		PlayerID:   playerIDValue,
		Context:    opContext, // Auto-record transaction
	})
	if err != nil {
		return nil, fmt.Errorf("sale failed: %w", err)
	}

	if sellResp, ok := sellResp.(*shipCmd.SellCargoResponse); ok {
		result.UnitsSold = sellResp.UnitsSold
		result.SaleRevenue = sellResp.TotalRevenue
	}

	// Step 7: Calculate net profit (estimated - actual fuel cost in ledger)
	// Note: FuelCost field is kept for future ledger query integration
	result.NetProfit = result.SaleRevenue - result.PurchaseCost
	result.DurationSeconds = int(time.Since(startTime).Seconds())

	logger.Log("INFO", "Arbitrage execution completed", map[string]interface{}{
		"ship":         ship.ShipSymbol(),
		"good":         opportunity.Good(),
		"units_sold":   result.UnitsSold,
		"revenue":      result.SaleRevenue,
		"cost":         result.PurchaseCost,
		"net_profit":   result.NetProfit,
		"duration_sec": result.DurationSeconds,
	})

	return result, nil
}

package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	scoutingQueries "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// ArbitrageResult represents the outcome of a single arbitrage execution cycle.
type ArbitrageResult struct {
	Good                  string
	UnitsPurchased        int
	UnitsSold             int
	PurchaseCost          int
	SaleRevenue           int
	FuelCost              int
	NetProfit             int
	DurationSeconds       int
	BuyPriceAtValidation  int // Price at validation time (SAFETY CHECK 3A)
	SellPriceAtValidation int // Price at validation time (SAFETY CHECK 3B)
	BuyPriceActual        int // Actual price paid per unit
	SellPriceActual       int // Actual price received per unit
}

// ArbitrageExecutor executes a complete arbitrage cycle: buy → navigate → sell.
// This is an application service that orchestrates the workflow using existing commands.
type ArbitrageExecutor struct {
	mediator   common.Mediator
	shipRepo   navigation.ShipRepository
	logRepo    trading.ArbitrageExecutionLogRepository
	purchaseMu sync.Mutex // Prevents concurrent purchases from draining account
}

// NewArbitrageExecutor creates a new arbitrage executor service
func NewArbitrageExecutor(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	logRepo trading.ArbitrageExecutionLogRepository,
) *ArbitrageExecutor {
	return &ArbitrageExecutor{
		mediator: mediator,
		shipRepo: shipRepo,
		logRepo:  logRepo,
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
//   - minBalance: Minimum credit balance to maintain (0 = no limit)
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
	minBalance int,
) (*ArbitrageResult, error) {
	startTime := time.Now()
	logger := common.LoggerFromContext(ctx)

	result := &ArbitrageResult{
		Good: opportunity.Good(),
	}

	// Capture execution outcome for logging
	var executionErr error
	defer func() {
		// Log failed executions only (successful ones are logged explicitly)
		if executionErr != nil {
			e.logExecution(ctx, ship, opportunity, result, containerID, playerID, false, executionErr.Error())
		}
	}()

	// Create operation context for transaction tracking
	if containerID != "" {
		opContext := shared.NewOperationContext(containerID, "arbitrage_worker")
		ctx = shared.WithOperationContext(ctx, opContext)
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
		executionErr = fmt.Errorf("navigation to buy market failed: %w", err)
		return nil, executionErr
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
		executionErr = fmt.Errorf("docking at buy market failed: %w", err)
		return nil, executionErr
	}

	// Reload ship to get current cargo space
	ship, err = e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerIDValue)
	if err != nil {
		executionErr = fmt.Errorf("failed to reload ship: %w", err)
		return nil, executionErr
	}

	// Step 3: Ensure ship has cargo space (clear existing cargo if needed)
	if !ship.Cargo().IsEmpty() {
		logger.Log("INFO", "Ship has existing cargo, clearing before arbitrage", map[string]interface{}{
			"ship":        ship.ShipSymbol(),
			"cargo_units": ship.Cargo().Units,
			"cargo_items": len(ship.Cargo().Inventory),
		})

		// Handle existing cargo: sell if valuable (>=20K), else jettison
		err := e.handleExistingCargo(ctx, ship, playerIDValue, logger)
		if err != nil {
			executionErr = fmt.Errorf("failed to clear existing cargo: %w", err)
			return nil, executionErr
		}

		// Reload ship to get updated cargo status after clearing
		ship, err = e.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerIDValue)
		if err != nil {
			executionErr = fmt.Errorf("failed to reload ship after clearing cargo: %w", err)
			return nil, executionErr
		}

		logger.Log("INFO", "Cargo cleared successfully", map[string]interface{}{
			"ship":        ship.ShipSymbol(),
			"cargo_units": ship.Cargo().Units,
		})
	}

	// Verify we now have space
	availableSpace := ship.Cargo().AvailableCapacity()
	if availableSpace <= 0 {
		executionErr = fmt.Errorf("ship still has no cargo space after clearing")
		return nil, executionErr
	}

	// Calculate purchase cost
	buyPrice := opportunity.BuyPrice()
	purchaseCost := availableSpace * buyPrice

	// CRITICAL SECTION: Lock to prevent concurrent purchases from draining account
	// This ensures balance check + purchase limits + validation are atomic across all workers
	// NOTE: Lock is manually released after purchase completes (not deferred) to avoid blocking other workers
	e.purchaseMu.Lock()

	logger.Log("INFO", "Acquired purchase lock", map[string]interface{}{
		"ship": ship.ShipSymbol(),
		"good": opportunity.Good(),
	})

	// Query current player balance (needed for SAFETY CHECKS 1 & 2)
	getPlayerQuery := &playerQueries.GetPlayerQuery{
		PlayerID: &playerID,
	}

	resp, err := e.mediator.Send(ctx, getPlayerQuery)
	if err != nil {
		e.purchaseMu.Unlock()
		executionErr = fmt.Errorf("failed to query player balance: %w", err)
		return nil, executionErr
	}

	playerResp, ok := resp.(*playerQueries.GetPlayerResponse)
	if !ok {
		e.purchaseMu.Unlock()
		executionErr = fmt.Errorf("invalid response from GetPlayerQuery")
		return nil, executionErr
	}

	currentBalance := playerResp.Player.Credits

	// SAFETY CHECK 1: Limit maximum purchase to 20% of current balance
	const maxPurchasePercent = 0.20 // 20% allows 5 concurrent trades max
	maxPurchaseAmount := int(float64(currentBalance) * maxPurchasePercent)
	unitsToPurchase := availableSpace

	if purchaseCost > maxPurchaseAmount {
		// Reduce units to stay under balance limit
		unitsToPurchase = maxPurchaseAmount / buyPrice
		if unitsToPurchase == 0 {
			e.purchaseMu.Unlock()
			executionErr = fmt.Errorf("single unit costs more than purchase limit (unit price: %d, limit: %d, balance: %d)",
				buyPrice, maxPurchaseAmount, currentBalance)
			logger.Log("WARN", "Aborting trade - unit price exceeds purchase limit", map[string]interface{}{
				"unit_price":       buyPrice,
				"purchase_limit":   maxPurchaseAmount,
				"current_balance":  currentBalance,
				"percent_allowed":  maxPurchasePercent * 100,
			})
			return nil, executionErr
		}
		purchaseCost = unitsToPurchase * buyPrice
		logger.Log("WARN", "Reducing purchase quantity to stay under balance limit", map[string]interface{}{
			"original_units":   availableSpace,
			"adjusted_units":   unitsToPurchase,
			"original_cost":    availableSpace * buyPrice,
			"adjusted_cost":    purchaseCost,
			"purchase_limit":   maxPurchaseAmount,
			"current_balance":  currentBalance,
			"percent_allowed":  maxPurchasePercent * 100,
		})
	}

	// SAFETY CHECK 2: Balance guardrail (minimum balance to preserve)
	if minBalance > 0 {
		// Check if purchase would drop below threshold
		if currentBalance-purchaseCost < minBalance {
			e.purchaseMu.Unlock()
			executionErr = fmt.Errorf("purchase would violate balance threshold: balance=%d, cost=%d, threshold=%d",
				currentBalance, purchaseCost, minBalance)
			logger.Log("WARN", "Aborting trade to preserve minimum balance", map[string]interface{}{
				"current_balance": currentBalance,
				"purchase_cost":   purchaseCost,
				"min_balance":     minBalance,
				"deficit":         (currentBalance - purchaseCost) - minBalance,
			})
			return nil, executionErr
		}
	}

	// SAFETY CHECK 3A: Validate BUY market current price
	getBuyMarketQuery := &scoutingQueries.GetMarketDataQuery{
		PlayerID:       playerIDValue,
		WaypointSymbol: opportunity.BuyMarket().Symbol,
	}

	buyMarketResp, err := e.mediator.Send(ctx, getBuyMarketQuery)
	if err != nil {
		e.purchaseMu.Unlock()
		executionErr = fmt.Errorf("failed to fetch current buy market data: %w", err)
		return nil, executionErr
	}

	buyMarketDataResp, ok := buyMarketResp.(*scoutingQueries.GetMarketDataResponse)
	if !ok {
		e.purchaseMu.Unlock()
		executionErr = fmt.Errorf("invalid response from GetMarketDataQuery")
		return nil, executionErr
	}

	// Find the current buy price, supply, and trade volume at this market
	var currentBuyPrice int
	var currentBuySupply string
	var currentBuyTradeVolume int
	for _, good := range buyMarketDataResp.Market.TradeGoods() {
		if good.Symbol() == opportunity.Good() {
			// At buy market, we pay their SellPrice
			currentBuyPrice = good.SellPrice()
			currentBuyTradeVolume = good.TradeVolume()
			if good.Supply() != nil {
				currentBuySupply = *good.Supply()
			}
			break
		}
	}

	if currentBuyPrice == 0 {
		e.purchaseMu.Unlock()
		executionErr = fmt.Errorf("good %s not available at buy market %s", opportunity.Good(), opportunity.BuyMarket().Symbol)
		logger.Log("WARN", "Aborting trade - good not available at buy market", map[string]interface{}{
			"good":       opportunity.Good(),
			"buy_market": opportunity.BuyMarket().Symbol,
		})
		return nil, executionErr
	}

	// MARKET IMPACT PREVENTION: Limit purchase quantity based on supply level
	// Buying too much from a shallow market causes price spikes during execution.
	// Supply level indicates market depth - limit our "footprint" accordingly.
	//
	// Data-driven multipliers (2025-11-25):
	//   - ABUNDANT: 80% of tradeVolume (plenty of goods, minimal impact)
	//   - HIGH: 60% of tradeVolume (good depth, low impact)
	//   - MODERATE: 40% of tradeVolume (medium depth, moderate impact risk)
	//   - LIMITED: 20% of tradeVolume (shallow market, high impact risk)
	//   - SCARCE: 10% of tradeVolume (very shallow, avoid large trades)
	supplyMultiplier := 0.40 // Default to MODERATE
	switch currentBuySupply {
	case "ABUNDANT":
		supplyMultiplier = 0.80
	case "HIGH":
		supplyMultiplier = 0.60
	case "MODERATE":
		supplyMultiplier = 0.40
	case "LIMITED":
		supplyMultiplier = 0.20
	case "SCARCE":
		supplyMultiplier = 0.10
	}

	maxUnitsFromSupply := int(float64(currentBuyTradeVolume) * supplyMultiplier)
	if maxUnitsFromSupply > 0 && unitsToPurchase > maxUnitsFromSupply {
		logger.Log("INFO", "Limiting purchase to reduce market impact", map[string]interface{}{
			"original_units":   unitsToPurchase,
			"limited_units":    maxUnitsFromSupply,
			"trade_volume":     currentBuyTradeVolume,
			"supply":           currentBuySupply,
			"supply_mult":      supplyMultiplier,
		})
		unitsToPurchase = maxUnitsFromSupply
	}

	// Recalculate purchase cost with current price and adjusted units
	currentPurchaseCost := unitsToPurchase * currentBuyPrice

	// SAFETY CHECK 3B: Validate SELL market current price
	getSellMarketQuery := &scoutingQueries.GetMarketDataQuery{
		PlayerID:       playerIDValue,
		WaypointSymbol: opportunity.SellMarket().Symbol,
	}

	sellMarketResp, err := e.mediator.Send(ctx, getSellMarketQuery)
	if err != nil {
		e.purchaseMu.Unlock()
		executionErr = fmt.Errorf("failed to fetch current sell market data: %w", err)
		return nil, executionErr
	}

	sellMarketDataResp, ok := sellMarketResp.(*scoutingQueries.GetMarketDataResponse)
	if !ok {
		e.purchaseMu.Unlock()
		executionErr = fmt.Errorf("invalid response from GetMarketDataQuery")
		return nil, executionErr
	}

	// Find the current sell price at this market
	var currentSellPrice int
	for _, good := range sellMarketDataResp.Market.TradeGoods() {
		if good.Symbol() == opportunity.Good() {
			// At sell market, we receive their PurchasePrice (CRITICAL FIX: was SellPrice)
			currentSellPrice = good.PurchasePrice()
			break
		}
	}

	if currentSellPrice == 0 {
		e.purchaseMu.Unlock()
		executionErr = fmt.Errorf("good %s not purchasable at sell market %s", opportunity.Good(), opportunity.SellMarket().Symbol)
		logger.Log("WARN", "Aborting trade - sell market not buying this good", map[string]interface{}{
			"good":        opportunity.Good(),
			"sell_market": opportunity.SellMarket().Symbol,
		})
		return nil, executionErr
	}

	// Calculate current profit margin with REAL prices
	currentRevenue := unitsToPurchase * currentSellPrice
	currentProfit := currentRevenue - currentPurchaseCost
	currentMargin := float64(currentProfit) / float64(currentPurchaseCost) * 100

	// Abort if current margin is below 15% (opportunity may have been stale)
	// Lowered from 20% to 15% based on data analysis (2025-11-25):
	// - 89% of failures were due to 20% threshold being too strict
	// - Normal price drift during execution is 15-20%
	// - 15% provides safety margin while reducing false rejects
	const minAcceptableMargin = 15.0
	if currentMargin < minAcceptableMargin {
		e.purchaseMu.Unlock()
		executionErr = fmt.Errorf("current profit margin %.1f%% below minimum %.1f%%", currentMargin, minAcceptableMargin)
		logger.Log("WARN", "Aborting trade - market prices changed, margin too low", map[string]interface{}{
			"good":              opportunity.Good(),
			"original_margin":   opportunity.ProfitMargin(),
			"current_margin":    currentMargin,
			"min_margin":        minAcceptableMargin,
			"buy_price_orig":    buyPrice,
			"buy_price_now":     currentBuyPrice,
			"sell_price_orig":   opportunity.SellPrice(),
			"sell_price_now":    currentSellPrice,
			"estimated_profit":  currentProfit,
			"price_change_buy":  fmt.Sprintf("%.1f%%", float64(currentBuyPrice-buyPrice)/float64(buyPrice)*100),
			"price_change_sell": fmt.Sprintf("%.1f%%", float64(currentSellPrice-opportunity.SellPrice())/float64(opportunity.SellPrice())*100),
		})
		return nil, executionErr
	}

	logger.Log("INFO", "Market price validation passed", map[string]interface{}{
		"original_margin":   opportunity.ProfitMargin(),
		"current_margin":    currentMargin,
		"buy_price_orig":    buyPrice,
		"buy_price_now":     currentBuyPrice,
		"sell_price_orig":   opportunity.SellPrice(),
		"sell_price_now":    currentSellPrice,
		"price_change_buy":  fmt.Sprintf("%.1f%%", float64(currentBuyPrice-buyPrice)/float64(buyPrice)*100),
		"price_change_sell": fmt.Sprintf("%.1f%%", float64(currentSellPrice-opportunity.SellPrice())/float64(opportunity.SellPrice())*100),
	})

	logger.Log("INFO", "Purchasing cargo", map[string]interface{}{
		"ship":  ship.ShipSymbol(),
		"good":  opportunity.Good(),
		"units": unitsToPurchase,
		"cost":  purchaseCost,
	})

	purchaseResp, err := e.mediator.Send(ctx, &shipCmd.PurchaseCargoCommand{
		ShipSymbol: ship.ShipSymbol(),
		GoodSymbol: opportunity.Good(),
		Units:      unitsToPurchase,
		PlayerID:   playerIDValue,
	})
	if err != nil {
		e.purchaseMu.Unlock()
		executionErr = fmt.Errorf("purchase failed: %w", err)
		return nil, executionErr
	}

	// Release lock immediately after purchase - navigation doesn't need serialization
	e.purchaseMu.Unlock()

	if purchaseResp, ok := purchaseResp.(*shipCmd.PurchaseCargoResponse); ok {
		result.UnitsPurchased = purchaseResp.UnitsAdded
		result.PurchaseCost = purchaseResp.TotalCost
		// Capture actual price paid per unit
		if result.UnitsPurchased > 0 {
			result.BuyPriceActual = result.PurchaseCost / result.UnitsPurchased
		}
	}

	// Capture validated prices (SAFETY CHECK 3A/3B results)
	result.BuyPriceAtValidation = currentBuyPrice
	result.SellPriceAtValidation = currentSellPrice

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
		executionErr = fmt.Errorf("navigation to sell market failed: %w", err)
		return nil, executionErr
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
		executionErr = fmt.Errorf("docking at sell market failed: %w", err)
		return nil, executionErr
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
	})
	if err != nil {
		executionErr = fmt.Errorf("sale failed: %w", err)
		return nil, executionErr
	}

	if sellResp, ok := sellResp.(*shipCmd.SellCargoResponse); ok {
		result.UnitsSold = sellResp.UnitsSold
		result.SaleRevenue = sellResp.TotalRevenue
		// Capture actual price received per unit
		if result.UnitsSold > 0 {
			result.SellPriceActual = result.SaleRevenue / result.UnitsSold
		}
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

	// Log execution for ML training (fire and forget - don't block on logging errors)
	e.logExecution(ctx, ship, opportunity, result, containerID, playerID, true, "")

	return result, nil
}

// handleExistingCargo clears cargo from the ship before arbitrage trade.
// If cargo value >= 20K credits, it sells the cargo. Otherwise, it jettisons it.
//
// This ensures the ship has space for the arbitrage purchase, maximizing profit
// by selling valuable cargo and quickly discarding low-value cargo.
//
// Parameters:
//   - ctx: Context for the operation
//   - ship: Ship with cargo to clear
//   - playerID: Player identifier
//   - logger: Logger for tracking decisions
//
// Returns error if cargo clearing fails.
func (e *ArbitrageExecutor) handleExistingCargo(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
	logger common.ContainerLogger,
) error {
	const valueThreshold = 20000

	// Get ship's current cargo inventory
	cargo := ship.Cargo()
	waypointSymbol := ship.CurrentLocation().Symbol

	// Query market data at ship's current location
	marketQuery := &scoutingQueries.GetMarketDataQuery{
		PlayerID:       playerID,
		WaypointSymbol: waypointSymbol,
	}

	marketResp, err := e.mediator.Send(ctx, marketQuery)
	if err != nil {
		return fmt.Errorf("failed to query market for cargo valuation: %w", err)
	}

	marketData := marketResp.(*scoutingQueries.GetMarketDataResponse).Market

	// Calculate total cargo value at current market
	totalValue := 0
	for _, item := range cargo.Inventory {
		good := marketData.FindGood(item.Symbol)
		if good == nil {
			logger.Log("WARN", "Market doesn't trade cargo item", map[string]interface{}{
				"item":   item.Symbol,
				"market": waypointSymbol,
			})
			continue
		}

		itemValue := item.Units * good.PurchasePrice()
		totalValue += itemValue

		logger.Log("DEBUG", "Cargo item valuation", map[string]interface{}{
			"good":       item.Symbol,
			"units":      item.Units,
			"unit_price": good.PurchasePrice(),
			"item_value": itemValue,
		})
	}

	logger.Log("INFO", "Cargo value calculated", map[string]interface{}{
		"total_value": totalValue,
		"threshold":   valueThreshold,
		"items":       len(cargo.Inventory),
	})

	if totalValue >= valueThreshold {
		// Sell cargo (we're already docked at buy market)
		logger.Log("INFO", "Selling valuable cargo before arbitrage", map[string]interface{}{
			"cargo_value": totalValue,
			"ship":        ship.ShipSymbol(),
		})

		for _, item := range cargo.Inventory {
			sellCmd := &shipCmd.SellCargoCommand{
				ShipSymbol: ship.ShipSymbol(),
				GoodSymbol: item.Symbol,
				Units:      item.Units,
				PlayerID:   playerID,
			}

			_, err := e.mediator.Send(ctx, sellCmd)
			if err != nil {
				return fmt.Errorf("failed to sell cargo item %s: %w", item.Symbol, err)
			}

			logger.Log("INFO", "Cargo item sold", map[string]interface{}{
				"good":  item.Symbol,
				"units": item.Units,
			})
		}
	} else {
		// Jettison cargo (not worth selling)
		logger.Log("INFO", "Jettisoning low-value cargo before arbitrage", map[string]interface{}{
			"cargo_value": totalValue,
			"ship":        ship.ShipSymbol(),
		})

		for _, item := range cargo.Inventory {
			jettisonCmd := &shipCmd.JettisonCargoCommand{
				ShipSymbol: ship.ShipSymbol(),
				GoodSymbol: item.Symbol,
				Units:      item.Units,
				PlayerID:   playerID,
			}

			_, err := e.mediator.Send(ctx, jettisonCmd)
			if err != nil {
				return fmt.Errorf("failed to jettison cargo item %s: %w", item.Symbol, err)
			}

			logger.Log("INFO", "Cargo item jettisoned", map[string]interface{}{
				"good":  item.Symbol,
				"units": item.Units,
			})
		}
	}

	return nil
}

// logExecution captures execution data for ML training.
// This is called asynchronously to avoid blocking the main workflow.
//
// Parameters:
//   - ctx: Context for the goroutine
//   - ship: Ship entity at execution time
//   - opportunity: Arbitrage opportunity that was executed
//   - result: Execution outcome (nil if execution failed)
//   - containerID: Container ID for correlation
//   - playerID: Player identifier
//   - success: Whether execution completed successfully
//   - errorMsg: Error message if execution failed
func (e *ArbitrageExecutor) logExecution(
	ctx context.Context,
	ship *navigation.Ship,
	opportunity *trading.ArbitrageOpportunity,
	result *ArbitrageResult,
	containerID string,
	playerID int,
	success bool,
	errorMsg string,
) {
	// Convert playerID to domain type
	playerIDValue := shared.MustNewPlayerID(playerID)

	// Convert ArbitrageResult to domain type
	var domainResult *trading.ArbitrageExecutionResult
	if result != nil {
		domainResult = &trading.ArbitrageExecutionResult{
			NetProfit:             result.NetProfit,
			DurationSeconds:       result.DurationSeconds,
			FuelCost:              result.FuelCost,
			UnitsPurchased:        result.UnitsPurchased,
			UnitsSold:             result.UnitsSold,
			PurchaseCost:          result.PurchaseCost,
			SaleRevenue:           result.SaleRevenue,
			BuyPriceAtValidation:  result.BuyPriceAtValidation,
			SellPriceAtValidation: result.SellPriceAtValidation,
			BuyPriceActual:        result.BuyPriceActual,
			SellPriceActual:       result.SellPriceActual,
		}
	}

	// Create execution log
	log := trading.NewArbitrageExecutionLog(
		opportunity,
		ship,
		domainResult,
		containerID,
		playerIDValue,
		success,
		errorMsg,
	)

	// Save asynchronously (fire and forget)
	go func() {
		// Use background context to avoid cancellation
		saveCtx := context.Background()
		logger := common.LoggerFromContext(ctx)

		if err := e.logRepo.Save(saveCtx, log); err != nil {
			logger.Log("ERROR", "Failed to save execution log", map[string]interface{}{
				"error":        err.Error(),
				"ship":         ship.ShipSymbol(),
				"good":         opportunity.Good(),
				"container_id": containerID,
			})
		} else {
			logger.Log("DEBUG", "Execution log saved", map[string]interface{}{
				"ship":         ship.ShipSymbol(),
				"good":         opportunity.Good(),
				"success":      success,
				"container_id": containerID,
			})
		}
	}()
}

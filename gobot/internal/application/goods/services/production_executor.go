package services

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ProductionExecutor orchestrates the production of goods by coordinating ship operations.
// It handles both purchasing goods from markets (BUY) and manufacturing them (FABRICATE).
type ProductionExecutor struct {
	mediator         common.Mediator
	shipRepo         navigation.ShipRepository
	marketRepo       market.MarketRepository
	marketLocator    *MarketLocator
	clock            shared.Clock
	pollingIntervals []time.Duration // Configurable polling intervals
}

// NewProductionExecutor creates a new production executor with default polling intervals
func NewProductionExecutor(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketLocator *MarketLocator,
	clock shared.Clock,
) *ProductionExecutor {
	return NewProductionExecutorWithConfig(
		mediator,
		shipRepo,
		marketRepo,
		marketLocator,
		clock,
		[]time.Duration{30 * time.Second, 60 * time.Second}, // Default intervals
	)
}

// NewProductionExecutorWithConfig creates a new production executor with custom polling intervals
func NewProductionExecutorWithConfig(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketLocator *MarketLocator,
	clock shared.Clock,
	pollingIntervals []time.Duration,
) *ProductionExecutor {
	return &ProductionExecutor{
		mediator:         mediator,
		shipRepo:         shipRepo,
		marketRepo:       marketRepo,
		marketLocator:    marketLocator,
		clock:            clock,
		pollingIntervals: pollingIntervals,
	}
}

// ProductionResult contains the outcome of a production operation
type ProductionResult struct {
	QuantityAcquired int
	TotalCost        int
	WaypointSymbol   string // Where the good was acquired
}

// ProduceGood orchestrates the production of a good using the given ship.
// For BUY nodes: finds market, navigates, purchases whatever is available.
// For FABRICATE nodes: recursively produces inputs, delivers them, polls for output, purchases output.
// Returns the quantity acquired and total cost.
func (e *ProductionExecutor) ProduceGood(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*ProductionResult, error) {
	// Add operation context to Go context for transaction tagging
	if opContext != nil && opContext.IsValid() {
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	switch node.AcquisitionMethod {
	case goods.AcquisitionBuy:
		return e.buyGood(ctx, ship, node, systemSymbol, playerID, opContext)
	case goods.AcquisitionFabricate:
		return e.fabricateGood(ctx, ship, node, systemSymbol, playerID, opContext)
	default:
		return nil, fmt.Errorf("unknown acquisition method: %s", node.AcquisitionMethod)
	}
}

// buyGood purchases a good from a market
func (e *ProductionExecutor) buyGood(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	// Find best market selling this good
	marketResult, err := e.marketLocator.FindExportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find market selling %s: %w", node.Good, err)
	}

	logger.Log("INFO", fmt.Sprintf("Found export market for %s purchase", node.Good), map[string]interface{}{
		"good":            node.Good,
		"market":          marketResult.WaypointSymbol,
		"price":           marketResult.Price,
		"activity":        marketResult.Activity,
		"supply":          marketResult.Supply,
		"trade_volume":    marketResult.TradeVolume,
	})

	// Navigate to market and dock
	playerIDValue := shared.MustNewPlayerID(playerID)
	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), marketResult.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to market: %w", err)
	}

	// Calculate purchase quantity (capped by cargo space and trade volume)
	availableSpace := updatedShip.Cargo().Capacity - updatedShip.Cargo().Units
	if availableSpace <= 0 {
		return nil, fmt.Errorf("no cargo space available for purchase")
	}

	// Cap at trade volume to leave room for other inputs
	purchaseQty := min(availableSpace, marketResult.TradeVolume)
	if purchaseQty <= 0 {
		return nil, fmt.Errorf("trade volume is zero for %s", node.Good)
	}

	logger.Log("INFO", fmt.Sprintf("Purchasing %d units of %s (cargo: %d, trade_volume: %d)", purchaseQty, node.Good, availableSpace, marketResult.TradeVolume), nil)

	// Purchase cargo (capped by trade volume)
	purchaseCmd := &appShipCmd.PurchaseCargoCommand{
		ShipSymbol: updatedShip.ShipSymbol(),
		GoodSymbol: node.Good,
		Units:      purchaseQty,
		PlayerID:   playerIDValue,
	}

	purchaseResp, err := e.mediator.Send(ctx, purchaseCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to purchase cargo: %w", err)
	}

	// Extract purchase results
	response, ok := purchaseResp.(*appShipCmd.PurchaseCargoResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type from purchase command")
	}

	logger.Log("INFO", fmt.Sprintf("Purchased %d units of %s for %d credits", response.UnitsAdded, node.Good, response.TotalCost), map[string]interface{}{
		"good":             node.Good,
		"quantity":         response.UnitsAdded,
		"total_cost":       response.TotalCost,
		"market":           marketResult.WaypointSymbol,
	})

	return &ProductionResult{
		QuantityAcquired: response.UnitsAdded,
		TotalCost:        response.TotalCost,
		WaypointSymbol:   marketResult.WaypointSymbol,
	}, nil
}

// fabricateGood manufactures a good by producing inputs and delivering them to a manufacturing waypoint
func (e *ProductionExecutor) fabricateGood(
	ctx context.Context,
	ship *navigation.Ship,
	node *goods.SupplyChainNode,
	systemSymbol string,
	playerID int,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)
	totalCost := 0

	// Step 0: Check if factory already has ABUNDANT supply - skip input production if so
	// This allows opportunistic collection when factory already has goods ready
	factoryMarket, err := e.marketLocator.FindExportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find factory (export market) for %s: %w", node.Good, err)
	}

	// Check current supply at factory
	playerIDValue := shared.MustNewPlayerID(playerID)
	marketData, err := e.marketRepo.GetMarketData(ctx, factoryMarket.WaypointSymbol, playerID)
	if err == nil && marketData != nil {
		tradeGood := marketData.FindGood(node.Good)
		if tradeGood != nil && tradeGood.Supply() != nil {
			supply := *tradeGood.Supply()
			if supply == "ABUNDANT" || supply == "HIGH" {
				logger.Log("INFO", fmt.Sprintf("Factory already has %s supply of %s - skipping input production", supply, node.Good), map[string]interface{}{
					"good":     node.Good,
					"factory":  factoryMarket.WaypointSymbol,
					"supply":   supply,
				})

				// Navigate directly to factory and purchase
				updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), factoryMarket.WaypointSymbol, playerIDValue)
				if err != nil {
					return nil, fmt.Errorf("failed to navigate to factory: %w", err)
				}

				// Purchase the goods directly (PollForProduction will find them immediately since supply is HIGH/ABUNDANT)
				quantity, cost, err := e.PollForProduction(ctx, node.Good, factoryMarket.WaypointSymbol, updatedShip.ShipSymbol(), playerIDValue, opContext)
				if err != nil {
					return nil, fmt.Errorf("failed to purchase from factory: %w", err)
				}

				return &ProductionResult{
					QuantityAcquired: quantity,
					TotalCost:        cost,
					WaypointSymbol:   factoryMarket.WaypointSymbol,
				}, nil
			}
		}
	}

	// Step 1: Recursively produce all required inputs
	logger.Log("INFO", fmt.Sprintf("Starting fabrication of %s (requires %d inputs)", node.Good, len(node.Children)), map[string]interface{}{
		"good":         node.Good,
		"input_count":  len(node.Children),
	})

	for _, child := range node.Children {
		result, err := e.ProduceGood(ctx, ship, child, systemSymbol, playerID, opContext)
		if err != nil {
			return nil, fmt.Errorf("failed to produce input %s: %w", child.Good, err)
		}
		totalCost += result.TotalCost
		logger.Log("INFO", fmt.Sprintf("Produced input: %d units of %s (cost: %d credits)", result.QuantityAcquired, child.Good, result.TotalCost), map[string]interface{}{
			"input_good":  child.Good,
			"quantity":    result.QuantityAcquired,
			"cost":        result.TotalCost,
		})
	}

	// Step 2: Navigate to factory (already found above in Step 0)
	// CRITICAL: We need an EXPORT market (factory that produces and sells cheap),
	// NOT an import market (consumer that buys at high price).
	// The factory EXPORTS the finished good (low sell price) and IMPORTS the inputs.

	logger.Log("INFO", fmt.Sprintf("Found factory (export market) for %s at %s", node.Good, factoryMarket.WaypointSymbol), map[string]interface{}{
		"good":       node.Good,
		"waypoint":   factoryMarket.WaypointSymbol,
		"sell_price": factoryMarket.Price, // Factory's sell price (what we pay to buy)
	})

	// Step 3: Navigate to factory and dock (playerIDValue already created in Step 0)
	updatedShip, err := e.NavigateAndDock(ctx, ship.ShipSymbol(), factoryMarket.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to factory: %w", err)
	}

	// Step 4: Deliver all inputs by selling cargo to the factory
	// The factory IMPORTS the inputs (we sell to them)
	deliveryCost, err := e.deliverInputs(ctx, updatedShip, playerIDValue, opContext)
	if err != nil {
		return nil, fmt.Errorf("failed to deliver inputs: %w", err)
	}
	totalCost -= deliveryCost // Revenue from selling inputs (negative cost)

	logger.Log("INFO", "Delivered inputs to factory", map[string]interface{}{
		"good":             node.Good,
		"waypoint":         factoryMarket.WaypointSymbol,
		"delivery_revenue": deliveryCost,
	})

	// Step 5: Poll for production until output good supply increases, then purchase
	// The factory EXPORTS the finished good (we buy from them at their sell price)
	quantity, cost, err := e.PollForProduction(ctx, node.Good, factoryMarket.WaypointSymbol, updatedShip.ShipSymbol(), playerIDValue, opContext)
	if err != nil {
		return nil, fmt.Errorf("failed during production polling: %w", err)
	}

	totalCost += cost

	return &ProductionResult{
		QuantityAcquired: quantity,
		TotalCost:        totalCost,
		WaypointSymbol:   factoryMarket.WaypointSymbol,
	}, nil
}

// PollForProduction polls the market database until the output good appears in exports.
// Uses exponential backoff with NO timeout - polls indefinitely until good appears or context cancelled.
// Returns quantity purchased and cost.
func (e *ProductionExecutor) PollForProduction(
	ctx context.Context,
	good string,
	waypointSymbol string,
	shipSymbol string,
	playerID shared.PlayerID,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (int, int, error) {
	logger := common.LoggerFromContext(ctx)

	// Use configured polling intervals (or defaults if not set)
	intervals := e.pollingIntervals
	if len(intervals) == 0 {
		intervals = []time.Duration{
			30 * time.Second, // Initial poll - catch fast production
			60 * time.Second, // Settled interval
		}
	}

	attempt := 0
	for {
		// Check for context cancellation (daemon stop, user command, etc.)
		select {
		case <-ctx.Done():
			return 0, 0, fmt.Errorf("production polling cancelled: %w", ctx.Err())
		default:
			// Continue polling
		}

		// Query market data from database (kept fresh by scout tours)
		marketData, err := e.marketRepo.GetMarketData(ctx, waypointSymbol, playerID.Value())
		if err != nil {
			return 0, 0, fmt.Errorf("failed to get market data during polling: %w", err)
		}

		// Check if good appears in exports
		tradeGood := marketData.FindGood(good)
		if tradeGood != nil {
			logger.Log("INFO", fmt.Sprintf("Production complete: %s now available at %s (polled %d times)", good, waypointSymbol, attempt+1), map[string]interface{}{
				"good":            good,
				"waypoint":        waypointSymbol,
				"poll_attempts":   attempt + 1,
				"sell_price":      tradeGood.SellPrice(),
			})

			// Good is now available! Purchase it
			ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
			if err != nil {
				return 0, 0, fmt.Errorf("failed to reload ship: %w", err)
			}

			availableSpace := ship.Cargo().Capacity - ship.Cargo().Units
			if availableSpace <= 0 {
				return 0, 0, fmt.Errorf("no cargo space available for output")
			}

			// Cap at trade volume
			purchaseQty := min(availableSpace, tradeGood.TradeVolume())
			if purchaseQty <= 0 {
				return 0, 0, fmt.Errorf("trade volume is zero for %s", good)
			}

			logger.Log("INFO", fmt.Sprintf("Purchasing %d units of fabricated %s (cargo: %d, trade_volume: %d)", purchaseQty, good, availableSpace, tradeGood.TradeVolume()), nil)

			purchaseCmd := &appShipCmd.PurchaseCargoCommand{
				ShipSymbol: shipSymbol,
				GoodSymbol: good,
				Units:      purchaseQty,
				PlayerID:   playerID,
			}

			purchaseResp, err := e.mediator.Send(ctx, purchaseCmd)
			if err != nil {
				return 0, 0, fmt.Errorf("failed to purchase fabricated output: %w", err)
			}

			response, ok := purchaseResp.(*appShipCmd.PurchaseCargoResponse)
			if !ok {
				return 0, 0, fmt.Errorf("unexpected response type from purchase command")
			}

			logger.Log("INFO", fmt.Sprintf("Purchased fabricated output: %d units of %s for %d credits", response.UnitsAdded, good, response.TotalCost), map[string]interface{}{
				"good":       good,
				"quantity":   response.UnitsAdded,
				"total_cost": response.TotalCost,
				"waypoint":   waypointSymbol,
			})

			return response.UnitsAdded, response.TotalCost, nil
		}

		// Log polling attempt
		if attempt == 0 || attempt%5 == 0 { // Log every 5th attempt to reduce noise
			logger.Log("INFO", "Polling for production completion", map[string]interface{}{
				"good":          good,
				"waypoint":      waypointSymbol,
				"attempt":       attempt + 1,
				"next_wait_sec": intervals[min(attempt, len(intervals)-1)].Seconds(),
			})
		}

		// Calculate wait interval
		intervalIndex := attempt
		if intervalIndex >= len(intervals) {
			intervalIndex = len(intervals) - 1 // Use last interval for all subsequent attempts
		}
		waitDuration := intervals[intervalIndex]

		// Wait before next poll
		// Create a timer for the wait duration
		timer := time.NewTimer(waitDuration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, 0, fmt.Errorf("production polling cancelled during wait: %w", ctx.Err())
		case <-timer.C:
			// Continue to next poll attempt
		}

		attempt++
	}
}

// NavigateAndDock navigates to a waypoint and docks the ship
func (e *ProductionExecutor) NavigateAndDock(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	// Navigate to destination using high-level command
	navigateCmd := &appShipCmd.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    playerID,
	}

	_, err := e.mediator.Send(ctx, navigateCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to %s: %w", destination, err)
	}

	// Poll for ship arrival and dock using domain layer
	// NavigateRouteCommand already waited for travel time, this is just a safety check
	// for any API/database propagation delays (should only take a few seconds at most)
	var ship *navigation.Ship
	maxAttempts := 10 // 10 seconds timeout (1 sec per poll)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Reload ship from API
		ship, err = e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to reload ship after navigation: %w", err)
		}

		// Try to dock using domain's idempotent EnsureDocked
		_, dockErr := ship.EnsureDocked()
		if dockErr == nil {
			// Successfully docked (or was already docked)
			break
		}

		// If error is "in transit", wait and retry
		// If error is something else, fail immediately
		if ship.NavStatus() != navigation.NavStatusInTransit {
			return nil, fmt.Errorf("unexpected dock error: %w", dockErr)
		}

		// Ship still in transit, wait before retry
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dock wait cancelled: %w", ctx.Err())
		default:
			e.clock.Sleep(1 * time.Second)
		}
	}

	// Final check - if still in transit after timeout, return error
	if ship.NavStatus() == navigation.NavStatusInTransit {
		return nil, fmt.Errorf("ship %s still in transit after %d seconds", shipSymbol, maxAttempts)
	}

	// Persist dock state to API
	dockCmd := &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
	}

	_, err = e.mediator.Send(ctx, dockCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to persist dock state: %w", err)
	}

	// Reload ship again to get docked state
	ship, err = e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship after docking: %w", err)
	}

	return ship, nil
}

// deliverInputs sells all cargo (inputs) at the current location
func (e *ProductionExecutor) deliverInputs(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (int, error) {
	logger := common.LoggerFromContext(ctx)
	totalRevenue := 0

	// Sell each cargo item
	for _, item := range ship.Cargo().Inventory {
		sellCmd := &appShipCmd.SellCargoCommand{
			ShipSymbol: ship.ShipSymbol(),
			GoodSymbol: item.Symbol,
			Units:      item.Units,
			PlayerID:   playerID,
		}

		sellResp, err := e.mediator.Send(ctx, sellCmd)
		if err != nil {
			return 0, fmt.Errorf("failed to sell %s: %w", item.Symbol, err)
		}

		response, ok := sellResp.(*appShipCmd.SellCargoResponse)
		if !ok {
			return 0, fmt.Errorf("unexpected response type from sell command")
		}

		totalRevenue += response.TotalRevenue

		logger.Log("INFO", fmt.Sprintf("Delivered input: %d units of %s (revenue: %d credits)", response.UnitsSold, item.Symbol, response.TotalRevenue), map[string]interface{}{
			"input_good":  item.Symbol,
			"units":       response.UnitsSold,
			"revenue":     response.TotalRevenue,
		})
	}

	return totalRevenue, nil
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

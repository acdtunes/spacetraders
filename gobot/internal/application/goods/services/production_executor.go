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
	mediator      common.Mediator
	shipRepo      navigation.ShipRepository
	marketRepo    market.MarketRepository
	marketLocator *MarketLocator
	clock         shared.Clock
}

// NewProductionExecutor creates a new production executor
func NewProductionExecutor(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketLocator *MarketLocator,
	clock shared.Clock,
) *ProductionExecutor {
	return &ProductionExecutor{
		mediator:      mediator,
		shipRepo:      shipRepo,
		marketRepo:    marketRepo,
		marketLocator: marketLocator,
		clock:         clock,
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
) (*ProductionResult, error) {
	switch node.AcquisitionMethod {
	case goods.AcquisitionBuy:
		return e.buyGood(ctx, ship, node, systemSymbol, playerID)
	case goods.AcquisitionFabricate:
		return e.fabricateGood(ctx, ship, node, systemSymbol, playerID)
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
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	// Find best market selling this good
	marketResult, err := e.marketLocator.FindExportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find market selling %s: %w", node.Good, err)
	}

	logger.Log("INFO", "Found export market for purchase", map[string]interface{}{
		"good":            node.Good,
		"market":          marketResult.WaypointSymbol,
		"price":           marketResult.Price,
		"activity":        marketResult.Activity,
		"supply":          marketResult.Supply,
	})

	// Navigate to market and dock
	playerIDValue := shared.MustNewPlayerID(playerID)
	updatedShip, err := e.navigateAndDock(ctx, ship.ShipSymbol(), marketResult.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to market: %w", err)
	}

	// Calculate purchase quantity (available cargo space)
	availableSpace := updatedShip.Cargo().Capacity - updatedShip.Cargo().Units
	if availableSpace <= 0 {
		return nil, fmt.Errorf("no cargo space available for purchase")
	}

	// Purchase cargo (whatever is available up to cargo capacity)
	purchaseCmd := &appShipCmd.PurchaseCargoCommand{
		ShipSymbol: updatedShip.ShipSymbol(),
		GoodSymbol: node.Good,
		Units:      availableSpace,
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

	logger.Log("INFO", "Purchased good from market", map[string]interface{}{
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
) (*ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)
	totalCost := 0

	// Step 1: Recursively produce all required inputs
	logger.Log("INFO", "Producing inputs for fabrication", map[string]interface{}{
		"good":         node.Good,
		"input_count":  len(node.Children),
	})

	for _, child := range node.Children {
		result, err := e.ProduceGood(ctx, ship, child, systemSymbol, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to produce input %s: %w", child.Good, err)
		}
		totalCost += result.TotalCost
		logger.Log("INFO", "Produced input good", map[string]interface{}{
			"input_good":  child.Good,
			"quantity":    result.QuantityAcquired,
			"cost":        result.TotalCost,
		})
	}

	// Step 2: Find manufacturing waypoint that imports this good
	importMarket, err := e.marketLocator.FindImportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find import market for %s: %w", node.Good, err)
	}

	logger.Log("INFO", "Found manufacturing waypoint", map[string]interface{}{
		"good":            node.Good,
		"waypoint":        importMarket.WaypointSymbol,
		"purchase_price":  importMarket.Price,
	})

	// Step 3: Navigate to manufacturing waypoint and dock
	playerIDValue := shared.MustNewPlayerID(playerID)
	updatedShip, err := e.navigateAndDock(ctx, ship.ShipSymbol(), importMarket.WaypointSymbol, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to manufacturing waypoint: %w", err)
	}

	// Step 4: Deliver all inputs by selling cargo
	deliveryCost, err := e.deliverInputs(ctx, updatedShip, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to deliver inputs: %w", err)
	}
	totalCost -= deliveryCost // Revenue from selling inputs (negative cost)

	logger.Log("INFO", "Delivered inputs to manufacturing waypoint", map[string]interface{}{
		"good":              node.Good,
		"waypoint":          importMarket.WaypointSymbol,
		"delivery_revenue":  deliveryCost,
	})

	// Step 5: Poll for production until output good appears
	quantity, cost, err := e.pollForProduction(ctx, node.Good, importMarket.WaypointSymbol, updatedShip.ShipSymbol(), playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed during production polling: %w", err)
	}

	totalCost += cost

	return &ProductionResult{
		QuantityAcquired: quantity,
		TotalCost:        totalCost,
		WaypointSymbol:   importMarket.WaypointSymbol,
	}, nil
}

// pollForProduction polls the market database until the output good appears in exports.
// Uses exponential backoff with NO timeout - polls indefinitely until good appears or context cancelled.
// Returns quantity purchased and cost.
func (e *ProductionExecutor) pollForProduction(
	ctx context.Context,
	good string,
	waypointSymbol string,
	shipSymbol string,
	playerID shared.PlayerID,
) (int, int, error) {
	logger := common.LoggerFromContext(ctx)

	// Polling intervals: starts fast, settles to 60s
	intervals := []time.Duration{
		30 * time.Second, // Initial poll - catch fast production
		60 * time.Second, // Settled interval
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
			logger.Log("INFO", "Production complete - good available in market", map[string]interface{}{
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

			purchaseCmd := &appShipCmd.PurchaseCargoCommand{
				ShipSymbol: shipSymbol,
				GoodSymbol: good,
				Units:      availableSpace,
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

// navigateAndDock navigates to a waypoint and docks the ship
func (e *ProductionExecutor) navigateAndDock(
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

	// Reload ship to get updated state
	ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship after navigation: %w", err)
	}

	// Dock at destination
	dockCmd := &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
	}

	_, err = e.mediator.Send(ctx, dockCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to dock at %s: %w", destination, err)
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

		logger.Log("INFO", "Delivered input to manufacturing waypoint", map[string]interface{}{
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

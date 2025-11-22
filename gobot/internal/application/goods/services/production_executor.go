package services

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ProductionExecutor orchestrates the production of goods by coordinating ship operations.
// It handles both purchasing goods from markets (BUY) and manufacturing them (FABRICATE).
type ProductionExecutor struct {
	shipRepo      navigation.ShipRepository
	marketRepo    market.MarketRepository
	marketLocator *MarketLocator
	clock         shared.Clock
}

// NewProductionExecutor creates a new production executor
func NewProductionExecutor(
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	marketLocator *MarketLocator,
	clock shared.Clock,
) *ProductionExecutor {
	return &ProductionExecutor{
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
	// Find best market selling this good
	marketResult, err := e.marketLocator.FindExportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find market selling %s: %w", node.Good, err)
	}

	// TODO: Navigate to market using NavigateRouteCommand
	// TODO: Dock at market using DockCommand
	// TODO: Purchase cargo using PurchaseCargoCommand
	// For now, return a placeholder result

	return &ProductionResult{
		QuantityAcquired: 0, // TODO: actual quantity from purchase
		TotalCost:        marketResult.Price,
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
	totalCost := 0

	// Step 1: Recursively produce all required inputs
	for _, child := range node.Children {
		result, err := e.ProduceGood(ctx, ship, child, systemSymbol, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to produce input %s: %w", child.Good, err)
		}
		totalCost += result.TotalCost
	}

	// Step 2: Find manufacturing waypoint that imports this good
	importMarket, err := e.marketLocator.FindImportMarket(ctx, node.Good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find import market for %s: %w", node.Good, err)
	}

	// TODO: Navigate to manufacturing waypoint
	// TODO: Dock at waypoint
	// TODO: Sell all cargo (inputs) using SellCargoCommand

	// Step 3: Poll for production until output good appears
	quantity, cost, err := e.pollForProduction(ctx, node.Good, importMarket.WaypointSymbol, playerID)
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
	playerID int,
) (int, int, error) {
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
		marketData, err := e.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to get market data during polling: %w", err)
		}

		// Check if good appears in exports
		tradeGood := marketData.FindGood(good)
		if tradeGood != nil {
			// Good is now available! Purchase it
			// TODO: Purchase cargo using PurchaseCargoCommand
			// For now, return placeholder values
			return 0, tradeGood.SellPrice(), nil
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

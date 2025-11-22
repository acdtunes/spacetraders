package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// MarketLocator finds optimal markets for buying and selling goods.
// It ranks markets by activity and supply levels to guide production decisions.
type MarketLocator struct {
	marketRepo market.MarketRepository
}

// NewMarketLocator creates a new market locator service
func NewMarketLocator(marketRepo market.MarketRepository) *MarketLocator {
	return &MarketLocator{
		marketRepo: marketRepo,
	}
}

// MarketLocatorResult contains market information for a good
type MarketLocatorResult struct {
	WaypointSymbol string
	Activity       string // WEAK, GROWING, STRONG, RESTRICTED
	Supply         string // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
	Price          int    // sell_price (for exports) or purchase_price (for imports)
}

// FindImportMarket finds a market that wants to buy a good (imports it).
// Returns the market with the highest purchase price, preferring STRONG activity.
func (l *MarketLocator) FindImportMarket(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Use the repository's FindBestMarketBuying method
	bestMarket, err := l.marketRepo.FindBestMarketBuying(ctx, good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find import market for %s: %w", good, err)
	}

	if bestMarket == nil {
		return nil, fmt.Errorf("no market found importing %s", good)
	}

	// Get full market data to extract activity
	marketData, err := l.marketRepo.GetMarketData(ctx, bestMarket.WaypointSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get market data: %w", err)
	}

	// Extract trade good details
	tradeGood := marketData.FindGood(good)
	if tradeGood == nil {
		return nil, fmt.Errorf("good %s not found in market %s", good, bestMarket.WaypointSymbol)
	}

	result := &MarketLocatorResult{
		WaypointSymbol: bestMarket.WaypointSymbol,
		Activity:       "",
		Supply:         bestMarket.Supply,
		Price:          bestMarket.PurchasePrice,
	}

	// Extract activity if available
	if tradeGood.Activity() != nil {
		result.Activity = *tradeGood.Activity()
	}

	return result, nil
}

// FindExportMarket finds a market that sells a good (exports it).
// Returns the market with the lowest sell price.
func (l *MarketLocator) FindExportMarket(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Use the repository's FindCheapestMarketSelling method
	cheapestMarket, err := l.marketRepo.FindCheapestMarketSelling(ctx, good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find export market for %s: %w", good, err)
	}

	if cheapestMarket == nil {
		return nil, fmt.Errorf("no market found exporting %s", good)
	}

	// Get full market data to extract activity
	marketData, err := l.marketRepo.GetMarketData(ctx, cheapestMarket.WaypointSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get market data: %w", err)
	}

	// Extract trade good details
	tradeGood := marketData.FindGood(good)
	if tradeGood == nil {
		return nil, fmt.Errorf("good %s not found in market %s", good, cheapestMarket.WaypointSymbol)
	}

	result := &MarketLocatorResult{
		WaypointSymbol: cheapestMarket.WaypointSymbol,
		Activity:       "",
		Supply:         cheapestMarket.Supply,
		Price:          cheapestMarket.SellPrice,
	}

	// Extract activity if available
	if tradeGood.Activity() != nil {
		result.Activity = *tradeGood.Activity()
	}

	return result, nil
}

// FindBestExportMarket finds the best market for selling a good.
// It prefers markets with high activity and abundant supply.
// Ranking: STRONG + ABUNDANT/HIGH > GROWING + MODERATE/HIGH > Any + MODERATE > WEAK/SCARCE
func (l *MarketLocator) FindBestExportMarket(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Get all markets in the system
	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets in system: %w", err)
	}

	var bestMarket *MarketLocatorResult
	var bestScore int

	for _, waypointSymbol := range marketWaypoints {
		// Get market data
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil {
			continue // Skip markets we can't access
		}

		// Check if this market sells the good
		tradeGood := marketData.FindGood(good)
		if tradeGood == nil {
			continue // Market doesn't sell this good
		}

		// Calculate market score based on activity and supply
		activity := ""
		if tradeGood.Activity() != nil {
			activity = *tradeGood.Activity()
		}
		supply := ""
		if tradeGood.Supply() != nil {
			supply = *tradeGood.Supply()
		}

		score := calculateMarketScore(activity, supply)

		// Update best market if this one has a higher score
		if bestMarket == nil || score > bestScore {
			bestScore = score
			bestMarket = &MarketLocatorResult{
				WaypointSymbol: waypointSymbol,
				Activity:       activity,
				Supply:         supply,
				Price:          tradeGood.SellPrice(),
			}
		}
	}

	if bestMarket == nil {
		return nil, fmt.Errorf("no market found exporting %s", good)
	}

	return bestMarket, nil
}

// calculateMarketScore assigns a numeric score to a market based on activity and supply.
// Higher scores indicate better markets for selling goods.
// Scoring hierarchy:
// 1. STRONG activity + ABUNDANT/HIGH supply (90-100)
// 2. GROWING activity + MODERATE/HIGH supply (70-80)
// 3. Any activity + MODERATE supply (40-60)
// 4. WEAK activity or SCARCE/LIMITED supply (10-30)
func calculateMarketScore(activity, supply string) int {
	activityScore := 0
	switch activity {
	case "STRONG":
		activityScore = 50
	case "GROWING":
		activityScore = 30
	case "WEAK":
		activityScore = 10
	case "RESTRICTED":
		activityScore = 5
	default:
		activityScore = 20 // Unknown/missing activity
	}

	supplyScore := 0
	switch supply {
	case "ABUNDANT":
		supplyScore = 50
	case "HIGH":
		supplyScore = 40
	case "MODERATE":
		supplyScore = 30
	case "LIMITED":
		supplyScore = 20
	case "SCARCE":
		supplyScore = 10
	default:
		supplyScore = 15 // Unknown/missing supply
	}

	return activityScore + supplyScore
}

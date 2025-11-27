package helpers

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// MockMarketRepository is a test double for the market repository
type MockMarketRepository struct {
	markets        map[string]*market.Market // Key: waypoint symbol
	supplyChainMap map[string][]string       // For inferring factories
}

// NewMockMarketRepository creates a new mock market repository
func NewMockMarketRepository() *MockMarketRepository {
	return &MockMarketRepository{
		markets:        make(map[string]*market.Market),
		supplyChainMap: make(map[string][]string),
	}
}

// SetSupplyChainMap sets the supply chain map used to infer factories
// This allows the mock to return factory results for goods that are in the
// supply chain map but don't have explicit market data set up
func (m *MockMarketRepository) SetSupplyChainMap(scm map[string][]string) {
	m.supplyChainMap = scm
}

// AddMarketSellingGood adds a market that sells a specific good
func (m *MockMarketRepository) AddMarketSellingGood(
	marketSymbol, goodSymbol, activity, supply string,
	price int,
) error {
	return m.AddMarketSellingGoodAtWaypoint(marketSymbol, marketSymbol, goodSymbol, activity, supply, price)
}

// AddMarketSellingGoodAtWaypoint adds a market at a specific waypoint that sells a good
func (m *MockMarketRepository) AddMarketSellingGoodAtWaypoint(
	marketSymbol, waypointSymbol, goodSymbol, activity, supply string,
	price int,
) error {
	// Create trade good
	var activityPtr, supplyPtr *string
	if activity != "" {
		activityPtr = &activity
	}
	if supply != "" {
		supplyPtr = &supply
	}

	tradeGood, err := market.NewTradeGood(
		goodSymbol,
		supplyPtr,
		activityPtr,
		price,              // purchase price
		price,              // sell price (same for simplicity)
		100,                // trade volume
		market.TradeType(""), // trade type (empty for tests)
	)
	if err != nil {
		return err
	}

	// Create or update market
	existingMarket, exists := m.markets[waypointSymbol]
	if exists {
		// Add good to existing market (need to recreate since Market is immutable)
		existingGoods := existingMarket.TradeGoods()
		allGoods := append(existingGoods, *tradeGood)
		newMarket, err := market.NewMarket(waypointSymbol, allGoods, time.Now())
		if err != nil {
			return err
		}
		m.markets[waypointSymbol] = newMarket
	} else {
		// Create new market
		newMarket, err := market.NewMarket(waypointSymbol, []market.TradeGood{*tradeGood}, time.Now())
		if err != nil {
			return err
		}
		m.markets[waypointSymbol] = newMarket
	}

	return nil
}

// GetMarketData retrieves market data for a specific waypoint
func (m *MockMarketRepository) GetMarketData(
	ctx context.Context,
	waypointSymbol string,
	playerID int,
) (*market.Market, error) {
	marketData, exists := m.markets[waypointSymbol]
	if !exists {
		return nil, fmt.Errorf("market not found: %s", waypointSymbol)
	}
	return marketData, nil
}

// FindCheapestMarketSelling finds the cheapest market selling a good
func (m *MockMarketRepository) FindCheapestMarketSelling(
	ctx context.Context,
	goodSymbol, systemSymbol string,
	playerID int,
) (*market.CheapestMarketResult, error) {
	var cheapest *market.CheapestMarketResult
	var cheapestPrice int

	for waypointSymbol, marketData := range m.markets {
		tradeGood := marketData.FindGood(goodSymbol)
		if tradeGood == nil {
			continue
		}

		price := tradeGood.SellPrice()
		if cheapest == nil || price < cheapestPrice {
			cheapestPrice = price
			cheapest = &market.CheapestMarketResult{
				WaypointSymbol: waypointSymbol,
				TradeSymbol:    goodSymbol,
				SellPrice:      price,
				Supply:         getStringValue(tradeGood.Supply()),
			}
		}
	}

	if cheapest == nil {
		return nil, fmt.Errorf("no market found selling %s", goodSymbol)
	}

	return cheapest, nil
}

// FindBestMarketBuying finds the best market buying a good
func (m *MockMarketRepository) FindBestMarketBuying(
	ctx context.Context,
	goodSymbol, systemSymbol string,
	playerID int,
) (*market.BestMarketBuyingResult, error) {
	var best *market.BestMarketBuyingResult
	var bestPrice int

	for waypointSymbol, marketData := range m.markets {
		tradeGood := marketData.FindGood(goodSymbol)
		if tradeGood == nil {
			continue
		}

		price := tradeGood.PurchasePrice()
		if best == nil || price > bestPrice {
			bestPrice = price
			best = &market.BestMarketBuyingResult{
				WaypointSymbol: waypointSymbol,
				TradeSymbol:    goodSymbol,
				PurchasePrice:  price,
				Supply:         getStringValue(tradeGood.Supply()),
			}
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no market found buying %s", goodSymbol)
	}

	return best, nil
}

// FindBestMarketForBuying finds the best market to buy from using trade_type/supply/activity scoring
func (m *MockMarketRepository) FindBestMarketForBuying(
	ctx context.Context,
	goodSymbol, systemSymbol string,
	playerID int,
) (*market.BestBuyingMarketResult, error) {
	var best *market.BestBuyingMarketResult
	bestScore := 100000

	for waypointSymbol, marketData := range m.markets {
		tradeGood := marketData.FindGood(goodSymbol)
		if tradeGood == nil {
			continue
		}

		supply := getStringValue(tradeGood.Supply())
		activity := getStringValue(tradeGood.Activity())
		// Mock doesn't have trade_type data - default to empty (will get worst score)
		tradeType := ""
		score := scoreMarketForBuying(tradeType, supply, activity)

		if best == nil || score < bestScore {
			bestScore = score
			best = &market.BestBuyingMarketResult{
				WaypointSymbol: waypointSymbol,
				TradeSymbol:    goodSymbol,
				SellPrice:      tradeGood.SellPrice(),
				Supply:         supply,
				Activity:       activity,
				TradeType:      market.TradeType(tradeType),
				Score:          score,
			}
		}
	}

	if best == nil {
		return nil, nil // Return nil, not error, when not found
	}

	return best, nil
}

// FindAllMarketsInSystem returns all market waypoint symbols in a system
func (m *MockMarketRepository) FindAllMarketsInSystem(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) ([]string, error) {
	waypoints := make([]string, 0, len(m.markets))
	for waypoint := range m.markets {
		waypoints = append(waypoints, waypoint)
	}
	return waypoints, nil
}

// scoreMarketForBuying calculates a score for buying (lower = better)
// Trade Type: EXPORT(0) > EXCHANGE(1) > IMPORT(2) > NULL(3) (weight: 1000)
// Supply: ABUNDANT(0) > HIGH(1) > MODERATE(2) > LIMITED(3) > SCARCE(4) (weight: 10)
// Activity: RESTRICTED(0) > WEAK(1) > GROWING(2) > STRONG(3) (weight: 1)
func scoreMarketForBuying(tradeType, supply, activity string) int {
	tradeTypeScore := 3 // Unknown/NULL = worst
	switch tradeType {
	case "EXPORT":
		tradeTypeScore = 0 // Best - factory produces this good
	case "EXCHANGE":
		tradeTypeScore = 1 // OK - trading post
	case "IMPORT":
		tradeTypeScore = 2 // Worst - consumer market
	}

	supplyScore := 5
	switch supply {
	case "ABUNDANT":
		supplyScore = 0
	case "HIGH":
		supplyScore = 1
	case "MODERATE":
		supplyScore = 2
	case "LIMITED":
		supplyScore = 3
	case "SCARCE":
		supplyScore = 4
	}

	activityScore := 4
	switch activity {
	case "RESTRICTED":
		activityScore = 0 // Best - stable prices
	case "WEAK":
		activityScore = 1
	case "GROWING":
		activityScore = 2
	case "STRONG":
		activityScore = 3
	}

	return tradeTypeScore*1000 + supplyScore*10 + activityScore
}

// Helper function to get string value from pointer
func getStringValue(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

// FindFactoryForGood finds a market that EXPORTS a specific good (factory)
// For tests, this returns a factory for any good that:
// 1. Has a market set up with that good, OR
// 2. Is in the supply chain map (manufacturable good without explicit market setup)
func (m *MockMarketRepository) FindFactoryForGood(
	ctx context.Context,
	goodSymbol, systemSymbol string,
	playerID int,
) (*market.FactoryResult, error) {
	// First, search for a market that sells this good
	for waypointSymbol, marketData := range m.markets {
		tradeGood := marketData.FindGood(goodSymbol)
		if tradeGood != nil {
			return &market.FactoryResult{
				WaypointSymbol: waypointSymbol,
				TradeSymbol:    goodSymbol,
				SellPrice:      tradeGood.SellPrice(),
				Supply:         getStringValue(tradeGood.Supply()),
				Activity:       getStringValue(tradeGood.Activity()),
			}, nil
		}
	}

	// No market found - check if it's a manufacturable good from supply chain map
	// If so, return a synthetic factory result to allow tests to work
	if _, exists := m.supplyChainMap[goodSymbol]; exists {
		// Generate a synthetic factory waypoint based on the good symbol
		syntheticWaypoint := systemSymbol + "-FACTORY-" + goodSymbol
		return &market.FactoryResult{
			WaypointSymbol: syntheticWaypoint,
			TradeSymbol:    goodSymbol,
			SellPrice:      100,
			Supply:         "MODERATE", // Default supply for test factories
			Activity:       "WEAK",     // Default activity for test factories
		}, nil
	}

	// No market and not manufacturable
	return nil, nil
}

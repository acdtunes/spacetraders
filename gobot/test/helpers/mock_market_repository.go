package helpers

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// MockMarketRepository is a test double for the market repository
type MockMarketRepository struct {
	markets map[string]*market.Market // Key: waypoint symbol
}

// NewMockMarketRepository creates a new mock market repository
func NewMockMarketRepository() *MockMarketRepository {
	return &MockMarketRepository{
		markets: make(map[string]*market.Market),
	}
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
		price,    // purchase price
		price,    // sell price (same for simplicity)
		100,      // trade volume
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

// Helper function to get string value from pointer
func getStringValue(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

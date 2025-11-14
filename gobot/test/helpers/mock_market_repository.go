package helpers

import (
	"context"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// MockMarketRepository is a test double for MarketRepository interface
type MockMarketRepository struct {
	mu      sync.RWMutex
	markets map[string]*market.Market // waypoint -> market
	upserts int                       // Track number of upserts
}

// NewMockMarketRepository creates a new mock market repository
func NewMockMarketRepository() *MockMarketRepository {
	return &MockMarketRepository{
		markets: make(map[string]*market.Market),
	}
}

// GetMarketData retrieves market data for a waypoint
func (m *MockMarketRepository) GetMarketData(ctx context.Context, playerID uint, waypointSymbol string) (*market.Market, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	marketData, ok := m.markets[waypointSymbol]
	if !ok {
		return nil, nil // Return nil market if not found (matches real behavior)
	}

	return marketData, nil
}

// UpsertMarketData inserts or updates market data
func (m *MockMarketRepository) UpsertMarketData(ctx context.Context, playerID uint, waypointSymbol string, goods []market.TradeGood, timestamp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.upserts++

	marketData, err := market.NewMarket(waypointSymbol, goods, timestamp)
	if err != nil {
		return err
	}

	m.markets[waypointSymbol] = marketData
	return nil
}

// ListMarketsInSystem retrieves all markets in a system
func (m *MockMarketRepository) ListMarketsInSystem(ctx context.Context, playerID uint, systemSymbol string, maxAgeMinutes int) ([]market.Market, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var markets []market.Market
	for _, mkt := range m.markets {
		markets = append(markets, *mkt)
	}

	return markets, nil
}

// GetUpsertCount returns the number of upserts performed (for testing)
func (m *MockMarketRepository) GetUpsertCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.upserts
}

// ResetUpsertCount resets the upsert counter
func (m *MockMarketRepository) ResetUpsertCount() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserts = 0
}

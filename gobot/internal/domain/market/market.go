package market

import (
	"errors"
	"time"
)

// Market represents an immutable snapshot of market data at a specific waypoint and time.
type Market struct {
	waypointSymbol string
	tradeGoods     []TradeGood
	lastUpdated    time.Time
}

// NewMarket creates a new Market with validation
func NewMarket(waypointSymbol string, tradeGoods []TradeGood, lastUpdated time.Time) (*Market, error) {
	if waypointSymbol == "" {
		return nil, errors.New("waypoint symbol cannot be empty")
	}

	if lastUpdated.IsZero() {
		return nil, errors.New("timestamp cannot be empty")
	}

	// Create defensive copy of trade goods to ensure immutability
	goodsCopy := make([]TradeGood, len(tradeGoods))
	copy(goodsCopy, tradeGoods)

	return &Market{
		waypointSymbol: waypointSymbol,
		tradeGoods:     goodsCopy,
		lastUpdated:    lastUpdated,
	}, nil
}

func (m *Market) WaypointSymbol() string {
	return m.waypointSymbol
}

func (m *Market) TradeGoods() []TradeGood {
	// Return defensive copy to maintain immutability
	goodsCopy := make([]TradeGood, len(m.tradeGoods))
	copy(goodsCopy, m.tradeGoods)
	return goodsCopy
}

func (m *Market) LastUpdated() time.Time {
	return m.lastUpdated
}

// FindGood searches for a specific trade good by symbol
func (m *Market) FindGood(symbol string) *TradeGood {
	for i := range m.tradeGoods {
		if m.tradeGoods[i].Symbol() == symbol {
			good := m.tradeGoods[i]
			return &good
		}
	}
	return nil
}

// HasGood checks if the market has a specific trade good
func (m *Market) HasGood(symbol string) bool {
	return m.FindGood(symbol) != nil
}

// GoodsCount returns the number of trade goods in the market
func (m *Market) GoodsCount() int {
	return len(m.tradeGoods)
}

// GetTransactionLimit returns the trade volume limit for a good.
// Returns 0 if good not found (signals caller to use single transaction fallback).
func (m *Market) GetTransactionLimit(symbol string) int {
	good := m.FindGood(symbol)
	if good == nil {
		return 0 // Signal: market doesn't have this good
	}
	return good.TradeVolume()
}

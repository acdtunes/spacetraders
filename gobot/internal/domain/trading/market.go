package trading

import "fmt"

// TradeGood is a value object representing a tradeable good at a market
type TradeGood struct {
	Symbol        string
	Supply        string
	SellPrice     int
	PurchasePrice int
	TradeVolume   int
}

// Market is a domain entity representing a marketplace
type Market struct {
	waypointSymbol string
	tradeGoods     []TradeGood
}

// NewMarket creates a new market
func NewMarket(waypointSymbol string, tradeGoods []TradeGood) (*Market, error) {
	if waypointSymbol == "" {
		return nil, fmt.Errorf("waypoint symbol cannot be empty")
	}

	return &Market{
		waypointSymbol: waypointSymbol,
		tradeGoods:     tradeGoods,
	}, nil
}

// WaypointSymbol returns the waypoint symbol
func (m *Market) WaypointSymbol() string {
	return m.waypointSymbol
}

// TradeGoods returns all trade goods
func (m *Market) TradeGoods() []TradeGood {
	return m.tradeGoods
}

// GetTradeGood finds a specific trade good by symbol
func (m *Market) GetTradeGood(symbol string) (*TradeGood, bool) {
	for i := range m.tradeGoods {
		if m.tradeGoods[i].Symbol == symbol {
			return &m.tradeGoods[i], true
		}
	}
	return nil, false
}

// GetTransactionLimit returns the trade volume for a good (or 999999 if not found)
func (m *Market) GetTransactionLimit(symbol string) int {
	good, found := m.GetTradeGood(symbol)
	if !found {
		return 999999
	}
	return good.TradeVolume
}

// HasGood checks if market sells a specific good
func (m *Market) HasGood(symbol string) bool {
	_, found := m.GetTradeGood(symbol)
	return found
}

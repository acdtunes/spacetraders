package trading

import "context"

// MarketRepository defines the interface for market data access
type MarketRepository interface {
	GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*Market, error)
	FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*CheapestMarketResult, error)
}

// DTOs for data transfer

// MarketData represents market information from external sources
type MarketData struct {
	WaypointSymbol string
	TradeGoods     []TradeGoodData
}

// TradeGoodData represents trade good information from external sources
type TradeGoodData struct {
	Symbol        string
	Supply        string
	SellPrice     int
	PurchasePrice int
	TradeVolume   int
}

// CheapestMarketResult represents the result of finding the cheapest market
type CheapestMarketResult struct {
	WaypointSymbol string
	TradeSymbol    string
	SellPrice      int
	Supply         string
}

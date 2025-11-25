package market

import (
	"context"
	"time"
)

// MarketRepository defines the interface for market data access
type MarketRepository interface {
	GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*Market, error)
	FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*CheapestMarketResult, error)
	FindBestMarketBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*BestMarketBuyingResult, error)
	FindBestMarketForBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*BestBuyingMarketResult, error)
	FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error)
	// FindFactoryForGood finds a market that EXPORTS a specific good (i.e., a factory that produces it)
	// Returns nil if no factory exists for this good in the system
	FindFactoryForGood(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*FactoryResult, error)
}

// FactoryResult represents a factory that produces (exports) a specific good
type FactoryResult struct {
	WaypointSymbol string
	TradeSymbol    string
	SellPrice      int    // Price to buy from factory
	Supply         string
	Activity       string
}

// MarketPriceHistoryRepository defines persistence operations for price history
type MarketPriceHistoryRepository interface {
	// RecordPriceChange persists a new price history entry
	RecordPriceChange(ctx context.Context, history *MarketPriceHistory) error

	// GetPriceHistory retrieves price history for a specific market/good pair
	// Returns entries ordered by recorded_at DESC (newest first)
	GetPriceHistory(
		ctx context.Context,
		waypointSymbol string,
		goodSymbol string,
		since time.Time,
		limit int,
	) ([]*MarketPriceHistory, error)

	// GetVolatilityMetrics calculates price volatility statistics for a good
	// Returns mean price, std deviation, max price change %, change frequency
	GetVolatilityMetrics(
		ctx context.Context,
		goodSymbol string,
		windowHours int,
	) (*VolatilityMetrics, error)

	// FindMostVolatileGoods identifies goods with highest price drift
	// Returns top N goods sorted by volatility score (descending)
	FindMostVolatileGoods(
		ctx context.Context,
		limit int,
		windowHours int,
	) ([]*GoodVolatility, error)

	// GetMarketStability calculates how stable a specific market is for a good
	// Returns stability score (0-100, higher = more stable)
	GetMarketStability(
		ctx context.Context,
		waypointSymbol string,
		goodSymbol string,
		windowHours int,
	) (*MarketStability, error)
}

// DTOs for data transfer

// Data represents market information from external sources
type Data struct {
	WaypointSymbol string
	TradeGoods     []TradeGoodData
}

// TradeType indicates whether a good is exported, imported, or exchanged at a market
type TradeType string

const (
	TradeTypeExport   TradeType = "EXPORT"   // Market produces and sells this good (factory)
	TradeTypeImport   TradeType = "IMPORT"   // Market consumes and buys this good (consumer)
	TradeTypeExchange TradeType = "EXCHANGE" // Market trades but doesn't produce/consume
)

// TradeGoodData represents trade good information from external sources
type TradeGoodData struct {
	Symbol        string
	Supply        string
	Activity      string
	SellPrice     int
	PurchasePrice int
	TradeVolume   int
	TradeType     TradeType // EXPORT, IMPORT, or EXCHANGE
}

// CheapestMarketResult represents the result of finding the cheapest market
type CheapestMarketResult struct {
	WaypointSymbol string
	TradeSymbol    string
	SellPrice      int
	Supply         string
}

// BestMarketBuyingResult represents the result of finding the best market to sell to
type BestMarketBuyingResult struct {
	WaypointSymbol string
	TradeSymbol    string
	PurchasePrice  int // What the market pays us
	Supply         string
}

// BestBuyingMarketResult represents the result of finding the best market to buy from
// Scored by trade type (EXPORT > EXCHANGE > IMPORT), then by supply and activity
type BestBuyingMarketResult struct {
	WaypointSymbol string
	TradeSymbol    string
	SellPrice      int       // What we pay
	Supply         string
	Activity       string
	TradeType      TradeType // EXPORT, IMPORT, or EXCHANGE
	Score          int       // Lower = better
}

// VolatilityMetrics represents price volatility statistics for a good
type VolatilityMetrics struct {
	GoodSymbol      string
	MeanPrice       float64
	StdDeviation    float64
	MaxPriceChange  float64 // Percentage
	ChangeFrequency float64 // Changes per hour
	SampleSize      int
}

// GoodVolatility represents volatility ranking for a good
type GoodVolatility struct {
	GoodSymbol      string
	VolatilityScore float64
	ChangeCount     int
}

// MarketStability represents stability metrics for a market/good pair
type MarketStability struct {
	WaypointSymbol string
	GoodSymbol     string
	StabilityScore float64 // 0-100, higher is more stable
	PriceRange     int     // Max - Min price
	AvgChangeSize  float64 // Average price change percentage
}

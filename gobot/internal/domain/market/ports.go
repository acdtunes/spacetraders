package market

import (
	"context"
	"time"
)

// MarketRepository defines the interface for market data access
type MarketRepository interface {
	GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*Market, error)
	FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*CheapestMarketResult, error)
	// FindCheapestMarketSellingWithSupply finds the cheapest market with a specific supply level.
	// This enables supply-priority selection for raw materials: ABUNDANT > HIGH > MODERATE.
	// Returns nil if no market exists with the specified supply level.
	FindCheapestMarketSellingWithSupply(ctx context.Context, goodSymbol, systemSymbol string, playerID int, supplyLevel string) (*CheapestMarketResult, error)
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
	Ask            int // the ASK: what WE PAY to buy from the factory (market_data.purchase_price, the larger; sp-en5h7)
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

// CheapestMarketResult represents the result of finding the cheapest market to BUY from
type CheapestMarketResult struct {
	WaypointSymbol string
	TradeSymbol    string
	// Ask is what WE PAY to buy here (market_data.purchase_price, the larger of the
	// two prices).
	Ask    int
	Supply string
}

// BestMarketBuyingResult represents the result of finding the best market to sell to
type BestMarketBuyingResult struct {
	WaypointSymbol string
	TradeSymbol    string
	Bid            int // the BID: what the market PAYS us (market_data.sell_price, the smaller; sp-en5h7)
	Supply         string
}

// GlobalSinkResult is the best sell destination for a good ACROSS ALL SYSTEMS (the
// single highest bid, EXPORT markets excluded so it mirrors the tour snapshot's sink
// eligibility — an EXPORT bid is a low sellback, zeroed there). It backs the tour
// coordinator's out-of-horizon lane diagnostic: a sink whose System falls outside
// the 1-gate-hop tour graph is a profitable lane the planner structurally cannot
// see. SystemSymbol is derived from the waypoint (X1-XT71-A1 → X1-XT71).
type GlobalSinkResult struct {
	WaypointSymbol string
	SystemSymbol   string
	Bid            int // What the market pays us (sell_price, the smaller; sp-en5h7), the sell-side quote
	// TradeVolume is the sink's per-tranche depth (trade_volume) — half of a lane's
	// VolumeCap (min of source and sink), needed by the long-haul engine's realized
	// price-impact pricing (sp-mepj). Additive: the tour diagnostic caller ignores it.
	TradeVolume int
}

// GlobalSourceResult is the CHEAPEST buy source for a good ACROSS ALL SYSTEMS (the single
// lowest ask), the source-side mirror of GlobalSinkResult. IMPORT markets are excluded
// (an importer only BUYS a good, it never sells one to us), symmetric to the sink scan's
// EXPORT exclusion. It is the source half of the long-haul engine's out-of-horizon lane
// discovery (sp-mepj §2): pairing it with GlobalSinkResult yields (good, source, sink, ask,
// bid) spanning any number of gate hops. SystemSymbol is derived from the waypoint.
type GlobalSourceResult struct {
	WaypointSymbol string
	SystemSymbol   string
	Ask            int // What we pay to buy here (purchase_price, the larger; sp-en5h7), the buy-side quote
	TradeVolume    int // the source's per-tranche depth (supply side of a lane's VolumeCap)
}

// BestBuyingMarketResult represents the result of finding the best market to buy from
// Scored by trade type (EXPORT > EXCHANGE > IMPORT), then by supply and activity
type BestBuyingMarketResult struct {
	WaypointSymbol string
	TradeSymbol    string
	Ask            int // the ASK: what WE PAY (market_data.purchase_price, the larger; sp-en5h7)
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

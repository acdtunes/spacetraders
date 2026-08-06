package metrics

import (
	"context"
	"database/sql"
	"log"
	"strconv"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

// MarketMetricsCollector handles all market dynamics metrics
type MarketMetricsCollector struct {
	// Dependencies
	db *gorm.DB

	// Scanner Performance Metrics (4 metrics)
	marketScansTotal          *prometheus.CounterVec
	marketScanDurationSeconds *prometheus.HistogramVec
	marketScanRate            *prometheus.GaugeVec
	marketScannerErrorsTotal  *prometheus.CounterVec

	// Coverage Metrics (3 metrics)
	marketCoverageTotal *prometheus.GaugeVec
	marketCoverageFresh *prometheus.GaugeVec
	marketDataAge       *prometheus.HistogramVec

	// Price Dynamics Metrics (3 metrics)
	marketPriceSpread       *prometheus.HistogramVec
	marketBestSpread        *prometheus.GaugeVec
	marketEfficiencyPercent *prometheus.HistogramVec

	// Supply & Demand Metrics (3 metrics)
	marketSupplyDistribution   *prometheus.GaugeVec
	marketActivityDistribution *prometheus.GaugeVec
	marketLiquidity            *prometheus.GaugeVec

	// Trading Opportunities Metrics (2 metrics)
	tradeOpportunitiesTotal *prometheus.GaugeVec
	marketBestPrice         *prometheus.GaugeVec

	// Lifecycle scaffolding (ctx/cancelFunc/wg + Start context + Stop) is shared
	// via the embedded pollingCollector.
	pollingCollector

	// Configuration
	pollInterval     time.Duration
	freshThresholds  []int // age thresholds in seconds
	marginThresholds []int // profit margin thresholds in credits
}

// NewMarketMetricsCollector creates a new market metrics collector
func NewMarketMetricsCollector(db *gorm.DB) *MarketMetricsCollector {
	return &MarketMetricsCollector{
		db: db,

		// Scanner Performance Metrics
		marketScansTotal: newCounterVec(
			"market_scans_total",
			"Total number of market scans attempted",
			"player_id",
			"waypoint_symbol",
			"status",
		),

		marketScanDurationSeconds: newHistogramVec(
			"market_scan_duration_seconds",
			"Duration of market scan operations",
			[]float64{0.5, 1.0, 1.5, 2.0, 3.0, 5.0, 10.0},
			"player_id",
			"waypoint_symbol",
		),

		marketScanRate: newGaugeVec(
			"market_scan_rate",
			"Current scans per minute in system",
			"player_id",
			"system_symbol",
		),

		marketScannerErrorsTotal: newCounterVec(
			"market_scanner_errors_total",
			"Total number of scanner errors by type",
			"player_id",
			"error_type",
		),

		// Coverage Metrics
		marketCoverageTotal: newGaugeVec(
			"market_coverage_total",
			"Total number of markets discovered/scanned in system",
			"player_id",
			"system_symbol",
		),

		marketCoverageFresh: newGaugeVec(
			"market_coverage_fresh",
			"Number of markets with data fresher than threshold",
			"player_id",
			"system_symbol",
			"age_threshold",
		),

		marketDataAge: newHistogramVec(
			"market_data_age_seconds",
			"Age distribution of market data (seconds since last_updated)",
			[]float64{60, 300, 600, 1800, 3600, 7200},
			"player_id",
			"system_symbol",
		),

		// Price Dynamics Metrics
		marketPriceSpread: newHistogramVec(
			"market_price_spread",
			"Distribution of price spreads, the market rake (purchasePrice - sellPrice, i.e. ask - bid)",
			[]float64{10, 50, 100, 500, 1000, 5000, 10000},
			"player_id",
			"good_symbol",
		),

		marketBestSpread: newGaugeVec(
			"market_best_spread",
			"Maximum price spread available for each good in system",
			"player_id",
			"good_symbol",
			"system_symbol",
		),

		marketEfficiencyPercent: newHistogramVec(
			"market_efficiency_percent",
			"Spread as percentage of the ask ((spread / purchasePrice) * 100)",
			[]float64{5, 10, 25, 50, 75, 100},
			"player_id",
			"good_symbol",
		),

		// Supply & Demand Metrics
		marketSupplyDistribution: newGaugeVec(
			"market_supply_distribution",
			"Count of markets at each supply level per good",
			"player_id",
			"good_symbol",
			"supply_level",
		),

		marketActivityDistribution: newGaugeVec(
			"market_activity_distribution",
			"Count of markets at each activity level per good",
			"player_id",
			"good_symbol",
			"activity_level",
		),

		marketLiquidity: newGaugeVec(
			"market_liquidity",
			"Trade volume limit (max units per transaction)",
			"player_id",
			"waypoint_symbol",
			"good_symbol",
		),

		// Trading Opportunities Metrics
		tradeOpportunitiesTotal: newGaugeVec(
			"trade_opportunities_total",
			"Count of profitable trade routes with margin >= threshold",
			"player_id",
			"system_symbol",
			"min_margin",
		),

		marketBestPrice: newGaugeVec(
			"market_best_price",
			"Best buy/sell prices for each good in system",
			"player_id",
			"good_symbol",
			"system_symbol",
			"type",
		),

		// Configuration
		pollInterval:     60 * time.Second,
		freshThresholds:  []int{300, 600, 3600}, // 5min, 10min, 1hour
		marginThresholds: []int{10, 25, 50, 100},
	}
}

// Register registers all market metrics with the Prometheus registry
func (c *MarketMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.marketScansTotal,
		c.marketScanDurationSeconds,
		c.marketScanRate,
		c.marketScannerErrorsTotal,
		c.marketCoverageTotal,
		c.marketCoverageFresh,
		c.marketDataAge,
		c.marketPriceSpread,
		c.marketBestSpread,
		c.marketEfficiencyPercent,
		c.marketSupplyDistribution,
		c.marketActivityDistribution,
		c.marketLiquidity,
		c.tradeOpportunitiesTotal,
		c.marketBestPrice,
	)
}

// Start begins the polling goroutine for aggregate metrics
func (c *MarketMetricsCollector) Start(ctx context.Context) {
	c.startContext(ctx)

	// Start polling (every 60 seconds), with an immediate initial poll.
	c.startPolling(c.pollInterval, true, c.updateAllMetrics)
}

// updateAllMetrics updates all polling-based metrics
func (c *MarketMetricsCollector) updateAllMetrics() {
	if c.db == nil {
		return
	}

	// One pass per (player, system) that actually holds rows — not the cross product of the two
	// sets, which spent four queries per poll on every combination that never existed.
	for _, scope := range c.activeScopes() {
		c.updateCoverageMetrics(scope)
		c.updatePriceMetrics(scope)
		c.updateSupplyDemandMetrics(scope)
		c.updateTradingOpportunities(scope)
	}
}

// marketScope is the (player, system) pair every poll query and gauge in this collector
// is keyed on, with the label string and the waypoint LIKE pattern derived once.
type marketScope struct {
	playerID       int
	playerIDLabel  string
	system         string
	waypointPrefix string
}

func newMarketScope(playerID int, system string) marketScope {
	return marketScope{
		playerID:       playerID,
		playerIDLabel:  strconv.Itoa(playerID),
		system:         system,
		waypointPrefix: system + "-%",
	}
}

// activeScopes returns the (player, system) pairs that actually hold market data in the OPEN
// era — one scope per pair, and no scope for a pair that has no rows.
//
// It replaces a version that returned two independent lists which the caller multiplied into a
// cross product (sp-hrko6). Two things were wrong with that, and they compound:
//
// THE PAIRING WAS DISCARDED. The query already knows which (player, system) combinations exist;
// splitting them into a set of players and a set of systems threw that away and re-derived every
// combination, most of which have no rows. Each empty scope still costs four SQL round trips per
// poll and still registers its Prometheus label series.
//
// AND market_data IS NOT PRUNED ON AN ERA ROLLOVER. `universe transition` deliberately preserves
// the prior era's rows (only the gated `universe close` truncates), so an unscoped enumeration
// keeps every dead era's player alive in the label set forever, and multiplies it by every system
// any era ever visited. That is not merely cost: the market dashboards aggregate with an
// unqualified `sum()` over these series — `sum(market_coverage_fresh) / sum(market_coverage_total)`
// — so a dead era contributes a permanent, never-fresh denominator and drags the fresh-coverage
// panel down forever. At the last measured ratio (27k dead rows against 3.3k live) that is most of
// the number.
//
// FAIL-OPEN on an unresolvable era: fall back to every player rather than reporting nothing. This
// is observability, not a money guard — a metrics collector that silently emits zero series is
// harder to diagnose than one reporting a superset, and a database predating the eras table would
// otherwise go dark.
// The system is derived in Go via shared.ExtractSystemSymbol rather than in SQL. The previous
// version used split_part, which is PostgreSQL-only — it made this path untestable against the
// SQLite the suite runs on, and market_data has no system column precisely because every other
// reader of this table derives it the same way.
func (c *MarketMetricsCollector) activeScopes() []marketScope {
	var results []struct {
		PlayerID       int
		WaypointSymbol string
	}

	const scopeQuery = `SELECT DISTINCT player_id, waypoint_symbol FROM market_data`

	openEraPlayer, err := c.openEraPlayerID()
	if err != nil {
		log.Printf("Failed to resolve the open era's player for market metrics (reporting every player): %v", err)
	}

	query := scopeQuery
	var args []interface{}
	if openEraPlayer != nil {
		query = scopeQuery + " WHERE player_id = ?"
		args = append(args, *openEraPlayer)
	}

	if err := c.db.Raw(query, args...).Scan(&results).Error; err != nil {
		log.Printf("Failed to get active market scopes: %v", err)
		return nil
	}

	seen := make(map[string]bool, len(results))
	scopes := make([]marketScope, 0, len(results))
	for _, r := range results {
		system := shared.ExtractSystemSymbol(r.WaypointSymbol)
		if system == "" {
			continue
		}
		key := strconv.Itoa(r.PlayerID) + "|" + system
		if seen[key] {
			continue
		}
		seen[key] = true
		scopes = append(scopes, newMarketScope(r.PlayerID, system))
	}
	return scopes
}

// openEraPlayerID returns the player of the currently-open era, or nil when no era is open (or
// the eras table does not exist yet). A nil result is not an error — it is the caller's signal
// to fall back to every player.
func (c *MarketMetricsCollector) openEraPlayerID() (*int, error) {
	var playerIDs []int
	if err := c.db.Raw(`SELECT player_id FROM eras WHERE closed_at IS NULL ORDER BY era_id DESC LIMIT 1`).
		Scan(&playerIDs).Error; err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return nil, nil
	}
	return &playerIDs[0], nil
}

// updateCoverageMetrics updates market coverage and freshness metrics
func (c *MarketMetricsCollector) updateCoverageMetrics(scope marketScope) {
	// Total markets in system
	var totalCount int64
	err := c.db.Table("market_data").
		Select("COUNT(DISTINCT waypoint_symbol)").
		Where("player_id = ? AND waypoint_symbol LIKE ?", scope.playerID, scope.waypointPrefix).
		Count(&totalCount).Error

	if err != nil {
		log.Printf("Failed to get total market count: %v", err)
		return
	}

	c.marketCoverageTotal.WithLabelValues(scope.playerIDLabel, scope.system).Set(float64(totalCount))

	// Fresh markets by threshold
	for _, threshold := range c.freshThresholds {
		var freshCount int64
		cutoff := time.Now().Add(-time.Duration(threshold) * time.Second)

		err := c.db.Table("market_data").
			Select("COUNT(DISTINCT waypoint_symbol)").
			Where("player_id = ? AND waypoint_symbol LIKE ? AND last_updated >= ?",
				scope.playerID, scope.waypointPrefix, cutoff).
			Count(&freshCount).Error

		if err != nil {
			log.Printf("Failed to get fresh market count: %v", err)
			continue
		}

		thresholdLabel := strconv.Itoa(threshold) + "s"
		c.marketCoverageFresh.WithLabelValues(scope.playerIDLabel, scope.system, thresholdLabel).Set(float64(freshCount))
	}

	// Market data age distribution
	var ageRecords []struct {
		Age int64
	}

	// PostgreSQL-compatible: Calculate age in seconds
	err = c.db.Raw(`
		SELECT EXTRACT(EPOCH FROM (NOW() - last_updated))::bigint as age
		FROM (
			SELECT DISTINCT waypoint_symbol, MAX(last_updated) as last_updated
			FROM market_data
			WHERE player_id = ? AND waypoint_symbol LIKE ?
			GROUP BY waypoint_symbol
		) as waypoint_ages
	`, scope.playerID, scope.waypointPrefix).Scan(&ageRecords).Error

	if err != nil {
		log.Printf("Failed to get market data ages: %v", err)
		return
	}

	// Clear previous observations (reset histogram)
	// Note: Histograms accumulate, so we record current snapshot
	for _, record := range ageRecords {
		c.marketDataAge.WithLabelValues(scope.playerIDLabel, scope.system).Observe(float64(record.Age))
	}
}

// updatePriceMetrics updates price spread and efficiency metrics
func (c *MarketMetricsCollector) updatePriceMetrics(scope marketScope) {
	// Get all market data for price calculations
	var records []struct {
		GoodSymbol    string
		SellPrice     int
		PurchasePrice int
	}

	err := c.db.Table("market_data").
		Select("good_symbol, sell_price, purchase_price").
		Where("player_id = ? AND waypoint_symbol LIKE ?", scope.playerID, scope.waypointPrefix).
		Scan(&records).Error

	if err != nil {
		log.Printf("Failed to get market price data: %v", err)
		return
	}

	// Track best spread per good
	bestSpreads := make(map[string]int)

	for _, record := range records {
		// The market's rake: ASK - BID = purchase_price - sell_price. purchase_price is
		// what WE PAY (the larger), sell_price what the market PAYS us (sp-en5h7), so a
		// real market always yields spread >= 0.
		spread := record.PurchasePrice - record.SellPrice

		// Record spread distribution
		c.marketPriceSpread.WithLabelValues(scope.playerIDLabel, record.GoodSymbol).Observe(float64(spread))

		// Track best spread
		if spread > bestSpreads[record.GoodSymbol] {
			bestSpreads[record.GoodSymbol] = spread
		}

		// Calculate efficiency percentage: the rake as a share of the ASK we would pay.
		if record.PurchasePrice > 0 {
			efficiencyPercent := float64(spread) / float64(record.PurchasePrice) * 100
			c.marketEfficiencyPercent.WithLabelValues(scope.playerIDLabel, record.GoodSymbol).Observe(efficiencyPercent)
		}
	}

	// Set best spread gauges
	for goodSymbol, bestSpread := range bestSpreads {
		c.marketBestSpread.WithLabelValues(scope.playerIDLabel, goodSymbol, scope.system).Set(float64(bestSpread))
	}
}

// updateSupplyDemandMetrics updates supply/demand distribution and liquidity metrics
func (c *MarketMetricsCollector) updateSupplyDemandMetrics(scope marketScope) {
	// Get supply distribution
	var supplyDist []struct {
		GoodSymbol  string
		SupplyLevel sql.NullString
		Count       int64
	}

	err := c.db.Table("market_data").
		Select("good_symbol, supply as supply_level, COUNT(*) as count").
		Where("player_id = ? AND waypoint_symbol LIKE ?", scope.playerID, scope.waypointPrefix).
		Group("good_symbol, supply").
		Scan(&supplyDist).Error

	if err != nil {
		log.Printf("Failed to get supply distribution: %v", err)
	} else {
		for _, record := range supplyDist {
			supplyLevel := "UNKNOWN"
			if record.SupplyLevel.Valid {
				supplyLevel = record.SupplyLevel.String
			}
			c.marketSupplyDistribution.WithLabelValues(scope.playerIDLabel, record.GoodSymbol, supplyLevel).Set(float64(record.Count))
		}
	}

	// Get activity distribution
	var activityDist []struct {
		GoodSymbol    string
		ActivityLevel sql.NullString
		Count         int64
	}

	err = c.db.Table("market_data").
		Select("good_symbol, activity as activity_level, COUNT(*) as count").
		Where("player_id = ? AND waypoint_symbol LIKE ?", scope.playerID, scope.waypointPrefix).
		Group("good_symbol, activity").
		Scan(&activityDist).Error

	if err != nil {
		log.Printf("Failed to get activity distribution: %v", err)
	} else {
		for _, record := range activityDist {
			activityLevel := "UNKNOWN"
			if record.ActivityLevel.Valid {
				activityLevel = record.ActivityLevel.String
			}
			c.marketActivityDistribution.WithLabelValues(scope.playerIDLabel, record.GoodSymbol, activityLevel).Set(float64(record.Count))
		}
	}

	// Get liquidity (trade volume) data
	var liquidityRecords []struct {
		WaypointSymbol string
		GoodSymbol     string
		TradeVolume    int
	}

	err = c.db.Table("market_data").
		Select("waypoint_symbol, good_symbol, trade_volume").
		Where("player_id = ? AND waypoint_symbol LIKE ?", scope.playerID, scope.waypointPrefix).
		Scan(&liquidityRecords).Error

	if err != nil {
		log.Printf("Failed to get liquidity data: %v", err)
	} else {
		for _, record := range liquidityRecords {
			c.marketLiquidity.WithLabelValues(scope.playerIDLabel, record.WaypointSymbol, record.GoodSymbol).Set(float64(record.TradeVolume))
		}
	}
}

// updateTradingOpportunities updates trading opportunity metrics
func (c *MarketMetricsCollector) updateTradingOpportunities(scope marketScope) {
	// Get the cheapest ASK (where we BUY) and the best BID (where we SELL) for each good.
	// sp-en5h7: purchase_price is the ask (what WE PAY, the larger), sell_price the bid
	// (what the market PAYS us, the smaller) — so the cheapest buy is MIN(purchase_price)
	// and the best sell is MAX(sell_price).
	var priceData []struct {
		GoodSymbol  string
		MinAsk      int
		MaxBid      int
		MinWaypoint string
		MaxWaypoint string
	}

	// PostgreSQL-compatible: Include player_id in GROUP BY to avoid ungrouped column error
	err := c.db.Raw(`
		SELECT
			good_symbol,
			MIN(purchase_price) as min_ask,
			MAX(sell_price) as max_bid,
			(SELECT waypoint_symbol FROM market_data md2
			 WHERE md2.good_symbol = md1.good_symbol
			 AND md2.player_id = ?
			 AND md2.waypoint_symbol LIKE ?
			 ORDER BY purchase_price ASC LIMIT 1) as min_waypoint,
			(SELECT waypoint_symbol FROM market_data md3
			 WHERE md3.good_symbol = md1.good_symbol
			 AND md3.player_id = ?
			 AND md3.waypoint_symbol LIKE ?
			 ORDER BY sell_price DESC LIMIT 1) as max_waypoint
		FROM market_data md1
		WHERE player_id = ? AND waypoint_symbol LIKE ?
		GROUP BY good_symbol
	`, scope.playerID, scope.waypointPrefix, scope.playerID, scope.waypointPrefix, scope.playerID, scope.waypointPrefix).Scan(&priceData).Error

	if err != nil {
		log.Printf("Failed to get trading opportunity data: %v", err)
		return
	}

	// Calculate opportunities by margin threshold
	opportunitiesByMargin := make(map[int]int)
	for _, threshold := range c.marginThresholds {
		opportunitiesByMargin[threshold] = 0
	}

	for _, record := range priceData {
		// Set best buy price (the lowest ASK - where we buy FROM)
		c.marketBestPrice.WithLabelValues(scope.playerIDLabel, record.GoodSymbol, scope.system, "buy").Set(float64(record.MinAsk))

		// Set best sell price (the highest BID - where we sell TO)
		c.marketBestPrice.WithLabelValues(scope.playerIDLabel, record.GoodSymbol, scope.system, "sell").Set(float64(record.MaxBid))

		// Calculate profit margin: best sell (bid) minus cheapest buy (ask)
		profit := record.MaxBid - record.MinAsk

		// Count opportunities by threshold
		for _, threshold := range c.marginThresholds {
			if profit >= threshold && record.MinWaypoint != record.MaxWaypoint {
				opportunitiesByMargin[threshold]++
			}
		}
	}

	// Set opportunity counts
	for threshold, count := range opportunitiesByMargin {
		thresholdLabel := strconv.Itoa(threshold)
		c.tradeOpportunitiesTotal.WithLabelValues(scope.playerIDLabel, scope.system, thresholdLabel).Set(float64(count))
	}
}

// RecordScan records a market scan event (called from MarketScanner)
func (c *MarketMetricsCollector) RecordScan(playerID int, waypointSymbol string, duration time.Duration, err error) {
	playerIDStr := strconv.Itoa(playerID)

	status := "success"
	if err != nil {
		status = "failure"

		// Classify error type
		errorType := "unknown"
		// TODO: Add more sophisticated error classification based on error messages
		c.marketScannerErrorsTotal.WithLabelValues(playerIDStr, errorType).Inc()
	}

	c.marketScansTotal.WithLabelValues(playerIDStr, waypointSymbol, status).Inc()
	c.marketScanDurationSeconds.WithLabelValues(playerIDStr, waypointSymbol).Observe(duration.Seconds())
}

// globalMarketCollector is the singleton market metrics collector
// Set by SetGlobalMarketCollector() when metrics are enabled
var globalMarketCollector *MarketMetricsCollector

// SetGlobalMarketCollector sets the global market metrics collector
func SetGlobalMarketCollector(collector *MarketMetricsCollector) {
	globalMarketCollector = collector
}

// GetGlobalMarketCollector returns the global market metrics collector
// Returns nil if metrics are not enabled
func GetGlobalMarketCollector() *MarketMetricsCollector {
	return globalMarketCollector
}

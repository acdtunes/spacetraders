package metrics

import (
	"context"
	"database/sql"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

// MarketMetricsCollector handles all market dynamics metrics
type MarketMetricsCollector struct {
	// Dependencies
	db *gorm.DB

	// Scanner Performance Metrics (4 metrics)
	marketScansTotal           *prometheus.CounterVec
	marketScanDurationSeconds  *prometheus.HistogramVec
	marketScanRate             *prometheus.GaugeVec
	marketScannerErrorsTotal   *prometheus.CounterVec

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

	// Lifecycle
	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup

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
		marketScansTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_scans_total",
				Help:      "Total number of market scans attempted",
			},
			[]string{"player_id", "waypoint_symbol", "status"},
		),

		marketScanDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_scan_duration_seconds",
				Help:      "Duration of market scan operations",
				Buckets:   []float64{0.5, 1.0, 1.5, 2.0, 3.0, 5.0, 10.0},
			},
			[]string{"player_id", "waypoint_symbol"},
		),

		marketScanRate: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_scan_rate",
				Help:      "Current scans per minute in system",
			},
			[]string{"player_id", "system_symbol"},
		),

		marketScannerErrorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_scanner_errors_total",
				Help:      "Total number of scanner errors by type",
			},
			[]string{"player_id", "error_type"},
		),

		// Coverage Metrics
		marketCoverageTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_coverage_total",
				Help:      "Total number of markets discovered/scanned in system",
			},
			[]string{"player_id", "system_symbol"},
		),

		marketCoverageFresh: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_coverage_fresh",
				Help:      "Number of markets with data fresher than threshold",
			},
			[]string{"player_id", "system_symbol", "age_threshold"},
		),

		marketDataAge: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_data_age_seconds",
				Help:      "Age distribution of market data (seconds since last_updated)",
				Buckets:   []float64{60, 300, 600, 1800, 3600, 7200},
			},
			[]string{"player_id", "system_symbol"},
		),

		// Price Dynamics Metrics
		marketPriceSpread: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_price_spread",
				Help:      "Distribution of price spreads (sellPrice - purchasePrice)",
				Buckets:   []float64{10, 50, 100, 500, 1000, 5000, 10000},
			},
			[]string{"player_id", "good_symbol"},
		),

		marketBestSpread: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_best_spread",
				Help:      "Maximum price spread available for each good in system",
			},
			[]string{"player_id", "good_symbol", "system_symbol"},
		),

		marketEfficiencyPercent: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_efficiency_percent",
				Help:      "Spread as percentage of sell price ((spread / sellPrice) * 100)",
				Buckets:   []float64{5, 10, 25, 50, 75, 100},
			},
			[]string{"player_id", "good_symbol"},
		),

		// Supply & Demand Metrics
		marketSupplyDistribution: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_supply_distribution",
				Help:      "Count of markets at each supply level per good",
			},
			[]string{"player_id", "good_symbol", "supply_level"},
		),

		marketActivityDistribution: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_activity_distribution",
				Help:      "Count of markets at each activity level per good",
			},
			[]string{"player_id", "good_symbol", "activity_level"},
		),

		marketLiquidity: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_liquidity",
				Help:      "Trade volume limit (max units per transaction)",
			},
			[]string{"player_id", "waypoint_symbol", "good_symbol"},
		),

		// Trading Opportunities Metrics
		tradeOpportunitiesTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "trade_opportunities_total",
				Help:      "Count of profitable trade routes with margin >= threshold",
			},
			[]string{"player_id", "system_symbol", "min_margin"},
		),

		marketBestPrice: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "market_best_price",
				Help:      "Best buy/sell prices for each good in system",
			},
			[]string{"player_id", "good_symbol", "system_symbol", "type"},
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
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
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
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
}

// Start begins the polling goroutine for aggregate metrics
func (c *MarketMetricsCollector) Start(ctx context.Context) {
	c.ctx, c.cancelFunc = context.WithCancel(ctx)

	// Start polling (every 60 seconds)
	c.wg.Add(1)
	go c.pollMetrics(c.pollInterval)
}

// Stop gracefully stops the market metrics collector
func (c *MarketMetricsCollector) Stop() {
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
	c.wg.Wait()
}

// pollMetrics polls market data periodically
func (c *MarketMetricsCollector) pollMetrics(interval time.Duration) {
	defer c.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Do initial poll immediately
	c.updateAllMetrics()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.updateAllMetrics()
		}
	}
}

// updateAllMetrics updates all polling-based metrics
func (c *MarketMetricsCollector) updateAllMetrics() {
	if c.db == nil {
		return
	}

	// Get list of active players and systems
	players, systems := c.getActivePlayersAndSystems()

	for _, playerID := range players {
		for _, systemSymbol := range systems {
			c.updateCoverageMetrics(playerID, systemSymbol)
			c.updatePriceMetrics(playerID, systemSymbol)
			c.updateSupplyDemandMetrics(playerID, systemSymbol)
			c.updateTradingOpportunities(playerID, systemSymbol)
		}
	}
}

// getActivePlayersAndSystems retrieves list of players and systems with market data
func (c *MarketMetricsCollector) getActivePlayersAndSystems() ([]int, []string) {
	var results []struct {
		PlayerID     int
		SystemSymbol string
	}

	// Query to get distinct player_id and system combinations
	// PostgreSQL-compatible: Extract system symbol (first two parts: X1-AU21)
	err := c.db.Raw(`
		SELECT DISTINCT
			player_id,
			split_part(waypoint_symbol, '-', 1) || '-' || split_part(waypoint_symbol, '-', 2) as system_symbol
		FROM market_data
	`).Scan(&results).Error

	if err != nil {
		log.Printf("Failed to get active players and systems: %v", err)
		return nil, nil
	}

	// Deduplicate
	playersMap := make(map[int]bool)
	systemsMap := make(map[string]bool)

	for _, r := range results {
		playersMap[r.PlayerID] = true
		systemsMap[r.SystemSymbol] = true
	}

	players := make([]int, 0, len(playersMap))
	for p := range playersMap {
		players = append(players, p)
	}

	systems := make([]string, 0, len(systemsMap))
	for s := range systemsMap {
		systems = append(systems, s)
	}

	return players, systems
}

// updateCoverageMetrics updates market coverage and freshness metrics
func (c *MarketMetricsCollector) updateCoverageMetrics(playerID int, systemSymbol string) {
	playerIDStr := strconv.Itoa(playerID)

	// Total markets in system
	var totalCount int64
	err := c.db.Table("market_data").
		Select("COUNT(DISTINCT waypoint_symbol)").
		Where("player_id = ? AND waypoint_symbol LIKE ?", playerID, systemSymbol+"-%").
		Count(&totalCount).Error

	if err != nil {
		log.Printf("Failed to get total market count: %v", err)
		return
	}

	c.marketCoverageTotal.WithLabelValues(playerIDStr, systemSymbol).Set(float64(totalCount))

	// Fresh markets by threshold
	for _, threshold := range c.freshThresholds {
		var freshCount int64
		cutoff := time.Now().Add(-time.Duration(threshold) * time.Second)

		err := c.db.Table("market_data").
			Select("COUNT(DISTINCT waypoint_symbol)").
			Where("player_id = ? AND waypoint_symbol LIKE ? AND last_updated >= ?",
				playerID, systemSymbol+"-%", cutoff).
			Count(&freshCount).Error

		if err != nil {
			log.Printf("Failed to get fresh market count: %v", err)
			continue
		}

		thresholdLabel := strconv.Itoa(threshold) + "s"
		c.marketCoverageFresh.WithLabelValues(playerIDStr, systemSymbol, thresholdLabel).Set(float64(freshCount))
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
	`, playerID, systemSymbol+"-%").Scan(&ageRecords).Error

	if err != nil {
		log.Printf("Failed to get market data ages: %v", err)
		return
	}

	// Clear previous observations (reset histogram)
	// Note: Histograms accumulate, so we record current snapshot
	for _, record := range ageRecords {
		c.marketDataAge.WithLabelValues(playerIDStr, systemSymbol).Observe(float64(record.Age))
	}
}

// updatePriceMetrics updates price spread and efficiency metrics
func (c *MarketMetricsCollector) updatePriceMetrics(playerID int, systemSymbol string) {
	playerIDStr := strconv.Itoa(playerID)

	// Get all market data for price calculations
	var records []struct {
		GoodSymbol    string
		SellPrice     int
		PurchasePrice int
	}

	err := c.db.Table("market_data").
		Select("good_symbol, sell_price, purchase_price").
		Where("player_id = ? AND waypoint_symbol LIKE ?", playerID, systemSymbol+"-%").
		Scan(&records).Error

	if err != nil {
		log.Printf("Failed to get market price data: %v", err)
		return
	}

	// Track best spread per good
	bestSpreads := make(map[string]int)

	for _, record := range records {
		spread := record.SellPrice - record.PurchasePrice

		// Record spread distribution
		c.marketPriceSpread.WithLabelValues(playerIDStr, record.GoodSymbol).Observe(float64(spread))

		// Track best spread
		if spread > bestSpreads[record.GoodSymbol] {
			bestSpreads[record.GoodSymbol] = spread
		}

		// Calculate efficiency percentage
		if record.SellPrice > 0 {
			efficiencyPercent := float64(spread) / float64(record.SellPrice) * 100
			c.marketEfficiencyPercent.WithLabelValues(playerIDStr, record.GoodSymbol).Observe(efficiencyPercent)
		}
	}

	// Set best spread gauges
	for goodSymbol, bestSpread := range bestSpreads {
		c.marketBestSpread.WithLabelValues(playerIDStr, goodSymbol, systemSymbol).Set(float64(bestSpread))
	}
}

// updateSupplyDemandMetrics updates supply/demand distribution and liquidity metrics
func (c *MarketMetricsCollector) updateSupplyDemandMetrics(playerID int, systemSymbol string) {
	playerIDStr := strconv.Itoa(playerID)

	// Get supply distribution
	var supplyDist []struct {
		GoodSymbol  string
		SupplyLevel sql.NullString
		Count       int64
	}

	err := c.db.Table("market_data").
		Select("good_symbol, supply as supply_level, COUNT(*) as count").
		Where("player_id = ? AND waypoint_symbol LIKE ?", playerID, systemSymbol+"-%").
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
			c.marketSupplyDistribution.WithLabelValues(playerIDStr, record.GoodSymbol, supplyLevel).Set(float64(record.Count))
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
		Where("player_id = ? AND waypoint_symbol LIKE ?", playerID, systemSymbol+"-%").
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
			c.marketActivityDistribution.WithLabelValues(playerIDStr, record.GoodSymbol, activityLevel).Set(float64(record.Count))
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
		Where("player_id = ? AND waypoint_symbol LIKE ?", playerID, systemSymbol+"-%").
		Scan(&liquidityRecords).Error

	if err != nil {
		log.Printf("Failed to get liquidity data: %v", err)
	} else {
		for _, record := range liquidityRecords {
			c.marketLiquidity.WithLabelValues(playerIDStr, record.WaypointSymbol, record.GoodSymbol).Set(float64(record.TradeVolume))
		}
	}
}

// updateTradingOpportunities updates trading opportunity metrics
func (c *MarketMetricsCollector) updateTradingOpportunities(playerID int, systemSymbol string) {
	playerIDStr := strconv.Itoa(playerID)

	// Get best buy and sell prices for each good
	var priceData []struct {
		GoodSymbol      string
		MinSellPrice    int
		MaxPurchasePrice int
		MinWaypoint     string
		MaxWaypoint     string
	}

	// PostgreSQL-compatible: Include player_id in GROUP BY to avoid ungrouped column error
	err := c.db.Raw(`
		SELECT
			good_symbol,
			MIN(sell_price) as min_sell_price,
			MAX(purchase_price) as max_purchase_price,
			(SELECT waypoint_symbol FROM market_data md2
			 WHERE md2.good_symbol = md1.good_symbol
			 AND md2.player_id = ?
			 AND md2.waypoint_symbol LIKE ?
			 ORDER BY sell_price ASC LIMIT 1) as min_waypoint,
			(SELECT waypoint_symbol FROM market_data md3
			 WHERE md3.good_symbol = md1.good_symbol
			 AND md3.player_id = ?
			 AND md3.waypoint_symbol LIKE ?
			 ORDER BY purchase_price DESC LIMIT 1) as max_waypoint
		FROM market_data md1
		WHERE player_id = ? AND waypoint_symbol LIKE ?
		GROUP BY good_symbol
	`, playerID, systemSymbol+"-%", playerID, systemSymbol+"-%", playerID, systemSymbol+"-%").Scan(&priceData).Error

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
		// Set best buy price (lowest sell price - where we buy FROM)
		c.marketBestPrice.WithLabelValues(playerIDStr, record.GoodSymbol, systemSymbol, "buy").Set(float64(record.MinSellPrice))

		// Set best sell price (highest purchase price - where we sell TO)
		c.marketBestPrice.WithLabelValues(playerIDStr, record.GoodSymbol, systemSymbol, "sell").Set(float64(record.MaxPurchasePrice))

		// Calculate profit margin
		profit := record.MaxPurchasePrice - record.MinSellPrice

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
		c.tradeOpportunitiesTotal.WithLabelValues(playerIDStr, systemSymbol, thresholdLabel).Set(float64(count))
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

package metrics

import (
	"context"
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

	// Market Data Age Metric (1 metric): the only poll-fed metric this collector still has a
	// consumer for (navigation.json's freshness panels). sp-3hdsk.4 deleted the other 10
	// poll-fed metrics — coverage totals/freshness, price dynamics, supply/demand, trading
	// opportunities — after a consumer audit found zero dashboards, Prometheus rules or code
	// reading any of them, while the per-(player,system) scope loop that fed them cost
	// ~1-2 Postgres cores every 60s across 340 systems.
	marketDataAge *prometheus.HistogramVec

	// Lifecycle scaffolding (ctx/cancelFunc/wg + Start context + Stop) is shared
	// via the embedded pollingCollector.
	pollingCollector

	// Configuration
	pollInterval time.Duration
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

		marketDataAge: newHistogramVec(
			"market_data_age_seconds",
			"Age distribution of market data (seconds since last_updated)",
			[]float64{60, 300, 600, 1800, 3600, 7200},
			"player_id",
			"system_symbol",
		),

		// Configuration
		pollInterval: 60 * time.Second,
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
		c.marketDataAge,
	)
}

// Start begins the polling goroutine for aggregate metrics
func (c *MarketMetricsCollector) Start(ctx context.Context) {
	c.startContext(ctx)

	// Start polling (every 60 seconds), with an immediate initial poll.
	c.startPolling(c.pollInterval, true, c.updateAllMetrics)
}

// updateAllMetrics updates all polling-based metrics: today that is market_data_age_seconds
// alone (see the marketDataAge field comment for what sp-3hdsk.4 removed and why).
func (c *MarketMetricsCollector) updateAllMetrics() {
	if c.db == nil {
		return
	}

	for _, playerID := range c.playersToPoll() {
		c.observeMarketAges(playerID)
	}
}

// playersToPoll returns the players market_data_age_seconds should be observed for this poll:
// normally the single OPEN ERA's player (one extra query total, not one per system), or, when
// no era is resolvable, every distinct player_id market_data holds.
//
// FAIL-OPEN on an unresolvable era: fall back to every player rather than reporting nothing.
// This is observability, not a money guard — a metrics collector that silently emits zero
// series is harder to diagnose than one reporting a superset, and a database predating the eras
// table would otherwise go dark.
//
// Staying era-scoped matters even for a single histogram: market_data is NOT pruned on a
// universe rollover (`universe transition` preserves the prior era's rows; only the gated
// `universe close` truncates), so an unscoped enumeration would keep every dead era's player —
// and its now-frozen ages — in the label set forever.
func (c *MarketMetricsCollector) playersToPoll() []int {
	openEraPlayer, err := c.openEraPlayerID()
	if err != nil {
		log.Printf("Failed to resolve the open era's player for market metrics (reporting every player): %v", err)
	}
	if openEraPlayer != nil {
		return []int{*openEraPlayer}
	}

	var playerIDs []int
	if err := c.db.Raw(`SELECT DISTINCT player_id FROM market_data`).Scan(&playerIDs).Error; err != nil {
		log.Printf("Failed to list market_data players: %v", err)
		return nil
	}
	return playerIDs
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

// observeMarketAges is the one query per player market_data_age_seconds now costs, replacing
// the old per-(player,system) scope loop (three queries per pair, up to ~1,020 queries/poll
// across 340 systems, for this metric alone). It reads every waypoint the player has scanned,
// across every system, in a single pass, then collapses and labels each observation in Go: the
// system via shared.ExtractSystemSymbol and the age via time.Since — never in SQL, since both
// split_part and Postgres's EXTRACT(EPOCH ...) are dialect-specific and would make this path
// untestable against the SQLite the suite runs on. Collapsing multiple (waypoint, good) rows to
// one age per waypoint (a market's goods share one scan; MAX defensively) mirrors
// MarketRepositoryGORM.MaxAgeSecondsBySystem's approach, and keeps the histogram's label set
// (player_id, system_symbol) and per-waypoint observation granularity identical to the old
// per-scope query.
func (c *MarketMetricsCollector) observeMarketAges(playerID int) {
	var rows []struct {
		WaypointSymbol string
		LastUpdated    time.Time
	}

	err := c.db.Table("market_data").
		Select("waypoint_symbol, last_updated").
		Where("player_id = ?", playerID).
		Scan(&rows).Error
	if err != nil {
		log.Printf("Failed to get market data ages: %v", err)
		return
	}

	latest := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		if existing, ok := latest[row.WaypointSymbol]; !ok || row.LastUpdated.After(existing) {
			latest[row.WaypointSymbol] = row.LastUpdated
		}
	}

	playerIDLabel := strconv.Itoa(playerID)
	now := time.Now()
	for waypoint, lastUpdated := range latest {
		system := shared.ExtractSystemSymbol(waypoint)
		if system == "" {
			continue
		}
		c.marketDataAge.WithLabelValues(playerIDLabel, system).Observe(now.Sub(lastUpdated).Seconds())
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

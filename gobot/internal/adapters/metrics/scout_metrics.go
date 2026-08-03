package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// ScoutMetricsCollector handles the scout market-freshness gauge: how
// stale the cached market data is for each system the scout fleet has POSTED
// coverage for, computed by the scout post coordinator's reconcile sweep as
// MAX(now - market_data.last_updated) across that system's markets. Pure
// OBSERVATION (RULINGS #4): a recording miss must never touch a decision path, so
// Record is nil-safe and best-effort — mirroring AbsorptionMetricsCollector
// (internal/adapters/metrics/absorption_metrics.go), the template this
// family follows.
type ScoutMetricsCollector struct {
	// freshnessActualSeconds is a gauge (not a counter): each reconcile sweep
	// SETS one value per (player_id, system) to the current worst-case staleness
	// for that POSTED system, overwriting the prior sweep's reading rather than
	// accumulating. ~30 series expected (one per POSTED system).
	freshnessActualSeconds *prometheus.GaugeVec
}

// NewScoutMetricsCollector creates a new scout metrics collector.
func NewScoutMetricsCollector() *ScoutMetricsCollector {
	return &ScoutMetricsCollector{
		freshnessActualSeconds: newGaugeVec(
			"scout_freshness_actual_seconds",
			"Worst-case market data staleness (now - last_updated, seconds) across a POSTED system's markets, per the scout post coordinator's reconcile sweep",
			"player_id",
			"system",
		),
	}
}

// Register registers the scout metrics with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *ScoutMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.freshnessActualSeconds,
	)
}

// RecordFreshness sets the market-freshness gauge for one (player, system) to
// ageSeconds — the current worst-case staleness across that POSTED system's
// cached markets, as computed by the scout post coordinator's reconcile sweep.
func (c *ScoutMetricsCollector) RecordFreshness(playerID int, system string, ageSeconds float64) {
	if c == nil || c.freshnessActualSeconds == nil {
		return // Recording is best-effort; never panic the reconcile sweep (RULINGS #4).
	}
	c.freshnessActualSeconds.WithLabelValues(strconv.Itoa(playerID), system).Set(ageSeconds)
}

// globalScoutCollector is the singleton scout metrics collector.
// Set by SetGlobalScoutCollector() when metrics are enabled; the scout post
// coordinator's reconcile sweep emits the market-freshness gauge through it.
var globalScoutCollector *ScoutMetricsCollector

// SetGlobalScoutCollector sets the global scout metrics collector.
func SetGlobalScoutCollector(collector *ScoutMetricsCollector) {
	globalScoutCollector = collector
}

// RecordScoutFreshness sets the scout market-freshness gauge for one (player, system)
// globally. No-op when metrics are disabled (RULINGS #4).
func RecordScoutFreshness(playerID int, system string, ageSeconds float64) {
	if globalScoutCollector != nil {
		globalScoutCollector.RecordFreshness(playerID, system, ageSeconds)
	}
}

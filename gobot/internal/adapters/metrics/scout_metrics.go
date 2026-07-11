package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// ScoutMetricsCollector handles the scout market-freshness gauge (sp-dp92 P7): how
// stale the cached market data is for each system the scout fleet has POSTED
// coverage for, computed by the scout post coordinator's reconcile sweep as
// MAX(now - market_data.last_updated) across that system's markets. Pure
// OBSERVATION (RULINGS #4): a recording miss must never touch a decision path, so
// Record is nil-safe and best-effort — mirroring AbsorptionMetricsCollector
// (internal/adapters/metrics/absorption_metrics.go), the sp-8cz9 template this
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
		freshnessActualSeconds: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "scout_freshness_actual_seconds",
				Help:      "Worst-case market data staleness (now - last_updated, seconds) across a POSTED system's markets, per the scout post coordinator's reconcile sweep",
			},
			[]string{"player_id", "system"},
		),
	}
}

// Register registers the scout metrics with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *ScoutMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
		c.freshnessActualSeconds,
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
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

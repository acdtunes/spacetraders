package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// ScanDedupMetricsCollector publishes how many Earning-class guard scans the
// scan-dedup A/B test avoided by reusing a visit's own arrival scan. This is
// the measurement instrument for the experiment's API-cost side.
//
// Pure observation: Record is nil-safe and best-effort, never touching an
// admission decision.
type ScanDedupMetricsCollector struct {
	callsSaved *prometheus.CounterVec
}

// NewScanDedupMetricsCollector creates a new scan-dedup collector.
func NewScanDedupMetricsCollector() *ScanDedupMetricsCollector {
	return &ScanDedupMetricsCollector{
		callsSaved: newCounterVec(
			"scan_dedup_calls_saved_total",
			"Earning-class guard checks that reused a visit's own arrival scan instead of spending a second live GetMarket call on the same waypoint, by player, ship and guard",
			"player_id",
			"ship_symbol",
			"guard",
		),
	}
}

// Register registers the scan-dedup metric with the Prometheus registry.
func (c *ScanDedupMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(c.callsSaved)
}

// RecordSaved counts one guard check that reused a scan instead of an API call.
func (c *ScanDedupMetricsCollector) RecordSaved(playerID int, shipSymbol, guard string) {
	if c == nil || c.callsSaved == nil {
		return
	}
	c.callsSaved.WithLabelValues(strconv.Itoa(playerID), shipSymbol, guard).Inc()
}

var globalScanDedupCollector *ScanDedupMetricsCollector

// SetGlobalScanDedupCollector installs the process-wide collector.
func SetGlobalScanDedupCollector(c *ScanDedupMetricsCollector) {
	globalScanDedupCollector = c
}

// GetGlobalScanDedupCollector returns the process-wide collector, or nil.
func GetGlobalScanDedupCollector() *ScanDedupMetricsCollector {
	return globalScanDedupCollector
}

// RecordScanDedupSaved is the nil-safe package-level convenience callers use.
func RecordScanDedupSaved(playerID int, shipSymbol, guard string) {
	if c := GetGlobalScanDedupCollector(); c != nil {
		c.RecordSaved(playerID, shipSymbol, guard)
	}
}

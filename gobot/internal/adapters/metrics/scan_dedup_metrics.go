package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// ScanDedupMetricsCollector publishes the GetMarket calls a visit did NOT spend because
// another live read of the same market already covers it: the Earning-class guard checks
// the scan-dedup A/B test served from a visit's own arrival scan, and the mirror image —
// arrival scans deferred to the guard that live-reads moments later. This is the
// measurement instrument for the API-cost side of both.
//
// Pure observation: Record is nil-safe and best-effort, never touching an
// admission decision.
type ScanDedupMetricsCollector struct {
	callsSaved      *prometheus.CounterVec
	arrivalDeferred *prometheus.CounterVec
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
		arrivalDeferred: newCounterVec(
			"arrival_scan_deferred_total",
			"Arrival market scans skipped because a money guard live-reads that market before trading there, by player and the side whose guard does the reading (buy, sell, mixed)",
			"player_id",
			"side",
		),
	}
}

// Register registers the scan-dedup metrics with the Prometheus registry.
func (c *ScanDedupMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(c.callsSaved, c.arrivalDeferred)
}

// RecordSaved counts one guard check that reused a scan instead of an API call.
func (c *ScanDedupMetricsCollector) RecordSaved(playerID int, shipSymbol, guard string) {
	if c == nil || c.callsSaved == nil {
		return
	}
	c.callsSaved.WithLabelValues(strconv.Itoa(playerID), shipSymbol, guard).Inc()
}

// RecordArrivalDeferred counts one arrival scan handed to a trade guard's own live read.
func (c *ScanDedupMetricsCollector) RecordArrivalDeferred(playerID int, side string) {
	if c == nil || c.arrivalDeferred == nil {
		return
	}
	c.arrivalDeferred.WithLabelValues(strconv.Itoa(playerID), side).Inc()
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

// RecordArrivalScanDeferred is the nil-safe package-level convenience callers use.
func RecordArrivalScanDeferred(playerID int, side string) {
	if c := GetGlobalScanDedupCollector(); c != nil {
		c.RecordArrivalDeferred(playerID, side)
	}
}

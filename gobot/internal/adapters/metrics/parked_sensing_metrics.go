package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// ParkedSensingMetricsCollector publishes the parked-probe sensing model's three
// observable quantities: the rate the scan pacer is actually running at, how
// stale the market data it produces is, and where the placement pipeline's slots
// are sitting.
//
// They answer the three questions the model can fail at independently — is
// sensing getting any API budget, is the data it buys fresh enough for the trade
// planner, and are placements converting into parked probes — so a flat rate
// with a healthy slot census reads very differently from a healthy rate with a
// pile of WANTED slots.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch a decision
// path, so every Record is nil-safe and best-effort, mirroring
// ScoutMetricsCollector (internal/adapters/metrics/scout_metrics.go), the
// template this family follows.
type ParkedSensingMetricsCollector struct {
	// rateReqPerSec is a gauge: each reconcile SETS the pacer rate it just
	// handed the scan rotation. One series per player.
	rateReqPerSec *prometheus.GaugeVec
	// stalenessSeconds is a gauge of the parked fleet's scan-age distribution,
	// one series per (player, tier) where tier is hot/median/cold — the p10, p50
	// and p90 slot. The COLD tier is the operational one: it is the tail the
	// trade planner's fail-closed sink-freshness cap actually binds on, which a
	// mean would hide entirely.
	stalenessSeconds *prometheus.GaugeVec
	// slots is a gauge of the placement pipeline's census, one series per
	// (player, state). Every state is republished each tick INCLUDING the zeros,
	// so a queue that drains reads as empty rather than holding its last
	// non-zero value until the series goes stale.
	slots *prometheus.GaugeVec
}

// NewParkedSensingMetricsCollector creates a new parked-probe sensing collector.
func NewParkedSensingMetricsCollector() *ParkedSensingMetricsCollector {
	return &ParkedSensingMetricsCollector{
		rateReqPerSec: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "parked_sensing_rate_req_per_sec",
				Help:      "Requests/sec the parked-probe scan pacer is running at, as handed to the rotation by the last reconcile (the residual under the utilization target, less charting, floored at min_scan_rate)",
			},
			[]string{"player_id"},
		),
		stalenessSeconds: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "parked_sensing_staleness_seconds",
				Help:      "Age of the parked fleet's market data in seconds, by percentile tier: hot=p10, median=p50, cold=p90. Never-scanned slots are excluded rather than counted as infinitely stale",
			},
			[]string{"player_id", "tier"},
		),
		slots: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "parked_sensing_slots",
				Help:      "Probe placements by lifecycle state (WANTED, QUEUED, BOUGHT, IN_TRANSIT, PARKED). QUEUED means CLAIMED for purchase — including claims whose yard then quoted above the buy floor — never purchases in flight",
			},
			[]string{"player_id", "state"},
		),
	}
}

// Register registers the parked-sensing metrics with the Prometheus registry. A
// nil Registry (metrics disabled) is a no-op, matching the sibling collectors.
func (c *ParkedSensingMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
		c.rateReqPerSec,
		c.stalenessSeconds,
		c.slots,
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
}

// RecordRate sets the scan pacer's current rate for one player.
func (c *ParkedSensingMetricsCollector) RecordRate(playerID int, reqPerSec float64) {
	if c == nil || c.rateReqPerSec == nil {
		return // Recording is best-effort; never panic the reconcile (RULINGS #4).
	}
	c.rateReqPerSec.WithLabelValues(strconv.Itoa(playerID)).Set(reqPerSec)
}

// RecordStaleness sets one staleness tier for one player.
func (c *ParkedSensingMetricsCollector) RecordStaleness(playerID int, tier string, seconds float64) {
	if c == nil || c.stalenessSeconds == nil {
		return
	}
	c.stalenessSeconds.WithLabelValues(strconv.Itoa(playerID), tier).Set(seconds)
}

// RecordSlots sets the placement count for one player and lifecycle state.
func (c *ParkedSensingMetricsCollector) RecordSlots(playerID int, state string, count int) {
	if c == nil || c.slots == nil {
		return
	}
	c.slots.WithLabelValues(strconv.Itoa(playerID), state).Set(float64(count))
}

// globalParkedSensingCollector is the process-wide collector the sensing
// coordinator records through, following the package's established global-setter
// idiom. Nil until the daemon enables metrics, and every method is nil-safe, so
// an unset collector simply records nothing.
var globalParkedSensingCollector *ParkedSensingMetricsCollector

// SetGlobalParkedSensingCollector installs the process-wide collector.
func SetGlobalParkedSensingCollector(c *ParkedSensingMetricsCollector) {
	globalParkedSensingCollector = c
}

// GetGlobalParkedSensingCollector returns the process-wide collector, which may
// be nil when metrics are disabled.
func GetGlobalParkedSensingCollector() *ParkedSensingMetricsCollector {
	return globalParkedSensingCollector
}

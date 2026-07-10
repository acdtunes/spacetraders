package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// TourStalenessMetricsCollector holds the planner staleness-exclusion counter
// (sp-k7q5 layer 2): every lane the tour candidate assembly drops for being older
// than the 75-minute freshness cap, counted per system. It is the Grafana-facing
// half of the load-bearing layer — an operator watching
// tour_lanes_stale_excluded_total{system} climb sees staleness eating a system's
// tradeable lanes in real time, the signal that was silently absent when XT71/UQ87
// ran 110-125-minute-stale and every lane went invisible.
//
// It is pure OBSERVATION (RULINGS #4): a recording miss must never touch the tour
// planning path, so every method is nil-safe and best-effort. The watchkeeper's
// scout.staleness_hiding_revenue detector does NOT read this counter (it runs in a
// separate process and derives the same condition from market_data freshness) — this
// counter is the human-facing flow signal, the detector is the wake-facing one.
type TourStalenessMetricsCollector struct {
	// staleExcludedTotal increments once per (waypoint,good) lane the planner drops
	// for exceeding the freshness cap, at either drop site: BuildTourSnapshot (the
	// depth-aware tour solver's snapshot) and the reposition pre-rank's freshListings
	// filter. Labeled by system so a single market-rich system's staleness is legible
	// on its own, plus player_id for multi-tenant separation (every sibling collector
	// carries it).
	staleExcludedTotal *prometheus.CounterVec
}

// NewTourStalenessMetricsCollector creates a new tour-staleness metrics collector.
func NewTourStalenessMetricsCollector() *TourStalenessMetricsCollector {
	return &TourStalenessMetricsCollector{
		staleExcludedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_lanes_stale_excluded_total",
				Help:      "Tour candidate lanes dropped for exceeding the freshness cap, by system (the planner staleness-exclusion counter, sp-k7q5)",
			},
			[]string{"player_id", "system"},
		),
	}
}

// Register registers the tour-staleness metrics with the Prometheus registry. A nil
// Registry (metrics disabled) is a no-op, matching the sibling collectors.
func (c *TourStalenessMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}
	return Registry.Register(c.staleExcludedTotal)
}

// RecordStaleExcluded records `count` lanes dropped for staleness in `system`. count
// <= 0 is a no-op (nothing was dropped). Best-effort and nil-safe: a recording miss
// never panics the tour path (RULINGS #4).
func (c *TourStalenessMetricsCollector) RecordStaleExcluded(playerID int, system string, count int) {
	if c == nil || c.staleExcludedTotal == nil || count <= 0 {
		return
	}
	c.staleExcludedTotal.WithLabelValues(strconv.Itoa(playerID), system).Add(float64(count))
}

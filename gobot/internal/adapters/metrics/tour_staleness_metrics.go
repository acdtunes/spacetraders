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

	// candidatesDroppedTotal increments once per profitable lane the tour candidate
	// assembly drops for a reason OTHER than staleness, labeled by that reason
	// (sp-mtvg). The load-bearing reason is "counterparty_system_unreachable": a good
	// with a cheap source IN the tour graph but its best sink in a system OUTSIDE it
	// (>1 gate hop away) — the lane the solver can never plan because source and sink
	// never co-occur in one snapshot. This is the counter that makes the "exotic
	// good-level blind spot" (20k+ LASER_RIFLES/HOLOGRAPHICS/QUANTUM_DRIVES bids never
	// traded) LOUD instead of silent: an operator watching this climb sees the tour's
	// 1-hop horizon leaking long-haul value in real time, the signal that was absent
	// when the leak got misdiagnosed as a price/volume filter. Pure OBSERVATION
	// (RULINGS #4) — the guarded horizon itself is unchanged.
	candidatesDroppedTotal *prometheus.CounterVec
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
		candidatesDroppedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "tour_candidates_dropped_total",
				Help:      "Profitable tour lanes dropped from candidate assembly by reason (sp-mtvg); reason=counterparty_system_unreachable flags a lane whose best sink is beyond the 1-gate-hop tour graph",
			},
			[]string{"player_id", "reason"},
		),
	}
}

// Register registers the tour-staleness metrics with the Prometheus registry. A nil
// Registry (metrics disabled) is a no-op, matching the sibling collectors.
func (c *TourStalenessMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}
	if err := Registry.Register(c.staleExcludedTotal); err != nil {
		return err
	}
	return Registry.Register(c.candidatesDroppedTotal)
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

// RecordCandidateDropped records `count` profitable lanes dropped from tour candidate
// assembly for `reason` (sp-mtvg). count <= 0 or an empty reason is a no-op. Best-effort
// and nil-safe: a recording miss never panics the tour path (RULINGS #4).
func (c *TourStalenessMetricsCollector) RecordCandidateDropped(playerID int, reason string, count int) {
	if c == nil || c.candidatesDroppedTotal == nil || count <= 0 || reason == "" {
		return
	}
	c.candidatesDroppedTotal.WithLabelValues(strconv.Itoa(playerID), reason).Add(float64(count))
}

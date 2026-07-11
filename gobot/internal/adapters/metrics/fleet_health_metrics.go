package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// FleetHealthMetricsCollector houses the fleet-health event counters that back the
// gobot/configs/prometheus/rules/fleet-health.yml alert rules. Today that is the
// stranded-hull counter (sp-686e): a hull whose reposition/tour exit path finds its origin
// has no durable gate adjacency AND a gate-inaccessible live probe — the TORWIND-2C shape,
// where both discovery paths correctly return empty so the hull can never self-reposition
// and silently relaunch-loops until a human notices. It is emitted ONCE per stranded
// episode (the tour coordinator's per-hull consecutive-empty counter crossing its
// threshold), mirroring the navigation/absorption collectors' event-emitted globals. Pure
// OBSERVATION (RULINGS #4): a recording miss must never touch a decision path, so every
// method is nil-safe and best-effort.
type FleetHealthMetricsCollector struct {
	// hullStrandedTotal increments once per stranded EPISODE for a (ship, system): the
	// reposition scan produced N consecutive origin-level empties whose reason is
	// no-durable-adjacency or gate-inaccessible, meaning the hull cannot self-reposition.
	// The decision it serves: page the watch (StrandedHull alert) instead of the hull
	// dark-looping. Keyed by ship+system exactly (the ship symbol is globally unique and
	// already agent-scoped), so the alert can name the specific stranded hull and where.
	hullStrandedTotal *prometheus.CounterVec
}

// NewFleetHealthMetricsCollector creates a new fleet-health metrics collector (sp-686e).
func NewFleetHealthMetricsCollector() *FleetHealthMetricsCollector {
	return &FleetHealthMetricsCollector{
		hullStrandedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "fleet_hull_stranded_total",
				Help:      "Stranded-hull episodes: a hull whose origin has no durable gate adjacency AND a gate-inaccessible live probe, detected once per episode of N consecutive empty reposition discoveries (sp-686e)",
			},
			[]string{"ship", "system"},
		),
	}
}

// Register registers the fleet-health metrics with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *FleetHealthMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}
	return Registry.Register(c.hullStrandedTotal)
}

// RecordHullStranded records one stranded-hull episode for a (ship, system). Emitted once
// per episode by the tour coordinator when its per-hull consecutive-empty counter crosses
// the stranded threshold.
func (c *FleetHealthMetricsCollector) RecordHullStranded(ship, systemSymbol string) {
	if c == nil || c.hullStrandedTotal == nil {
		return // Recording is best-effort; never panic a reposition/tour path (RULINGS #4).
	}
	c.hullStrandedTotal.WithLabelValues(ship, systemSymbol).Inc()
}

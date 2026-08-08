package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// FleetHealthMetricsCollector houses the fleet-health event counters that back the
// gobot/configs/prometheus/rules/fleet-health.yml alert rules. Today that is the
// stranded-hull counter: a hull whose reposition/tour exit path finds its origin
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

	// hullUnreadableTotal increments once per fleet-sync pass for a hull we own that
	// the API would not serve (its record is corrupt SERVER-side). The decision it
	// serves: page the watch, because the fleet read now SURVIVES that hull, which is
	// what lets it fail quietly. Keyed by ship so the alert names the hull.
	hullUnreadableTotal *prometheus.CounterVec
}

// NewFleetHealthMetricsCollector creates a new fleet-health metrics collector.
func NewFleetHealthMetricsCollector() *FleetHealthMetricsCollector {
	return &FleetHealthMetricsCollector{
		hullStrandedTotal: newCounterVec(
			"fleet_hull_stranded_total",
			"Stranded-hull episodes: a hull whose origin has no durable gate adjacency AND a gate-inaccessible live probe, detected once per episode of N consecutive empty reposition discoveries (sp-686e)",
			"ship",
			"system",
		),
		hullUnreadableTotal: newCounterVec(
			"fleet_hull_unreadable_total",
			"Fleet-sync passes in which a hull we own could not be read from the API — its record is unreadable server-side, so it is present-but-unknown: kept, counted, and acted on by nothing (sp-2br34)",
			"ship",
			"player",
		),
	}
}

// Register registers the fleet-health metrics with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *FleetHealthMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.hullStrandedTotal,
		c.hullUnreadableTotal,
	)
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

// RecordHullUnreadable records one pass in which a hull we own was unreadable.
func (c *FleetHealthMetricsCollector) RecordHullUnreadable(ship, player string) {
	if c == nil || c.hullUnreadableTotal == nil {
		return // Recording is best-effort; never fail a fleet sync on a metrics miss (RULINGS #4).
	}
	c.hullUnreadableTotal.WithLabelValues(ship, player).Inc()
}

// globalFleetHealthCollector is the singleton fleet-health collector.
// Set by SetGlobalFleetHealthCollector() when metrics are enabled; the tour
// coordinator's reposition exit path emits the stranded-hull counter through it.
var globalFleetHealthCollector *FleetHealthMetricsCollector

// SetGlobalFleetHealthCollector sets the global fleet-health collector.
func SetGlobalFleetHealthCollector(collector *FleetHealthMetricsCollector) {
	globalFleetHealthCollector = collector
}

// RecordHullStranded records one stranded-hull episode for a (ship, system) globally.
// No-op when metrics are disabled, so a metrics miss never touches the
// reposition/tour path (RULINGS #4).
func RecordHullStranded(ship, system string) {
	if globalFleetHealthCollector != nil {
		globalFleetHealthCollector.RecordHullStranded(ship, system)
	}
}

// RecordHullUnreadable records one unreadable-hull sync pass globally.
func RecordHullUnreadable(ship, player string) {
	if globalFleetHealthCollector != nil {
		globalFleetHealthCollector.RecordHullUnreadable(ship, player)
	}
}

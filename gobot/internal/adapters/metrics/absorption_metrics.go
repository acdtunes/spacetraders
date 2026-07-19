package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// AbsorptionMetricsCollector handles the two L5 burn-in counters: the
// fraction of profitable tour plans the fleet-wide absorption cap actually constrains
// (cap-binding), and the cross-plan "ladder" incidents where a tour re-buys into ground
// still recovering from its own dump. Both are event-emitted from the tour coordinator
// (not polled) via the package-level Record* globals, mirroring the navigation/API
// collectors. They are pure OBSERVATION (RULINGS #4): a recording miss must never touch
// a decision path, so every method is nil-safe and best-effort.
type AbsorptionMetricsCollector struct {
	// capBindingTotal increments once per accepted tour plan per (market, good, side)
	// the plan touches WHERE outstanding cross-container absorption exists — outcome
	// distinguishes plans whose units hit the netted availability ceiling (bound) from
	// those that touched an absorbed lane but stayed below it (unbound). The decision it
	// serves: what fraction of profitable plans does the fleet-wide cap actually
	// constrain (the L5/xmwn mutex-retirement evidence + the Admiral's heavy-#9 gate).
	capBindingTotal *prometheus.CounterVec

	// ladderIncidentsTotal increments when a tour BUY leg executes against a
	// (waypoint, good) that carries an outstanding EXECUTED recovery shadow — the fleet
	// re-bought into ground still recovering from its own dump (the cross-plan ladder
	// class the L5 retirement must measure).
	ladderIncidentsTotal *prometheus.CounterVec

	// consultVerdictsTotal increments once per absorption-consult verdict applied at a
	// lane/plan exclusion site: verdict is "skip_reserved"|"pass", engine
	// distinguishes which caller consulted (idle_arb's tour planner vs the trade-route
	// coordinator's lane filter) since both apply the same verdict shape against the
	// same underlying absorption read.
	consultVerdictsTotal *prometheus.CounterVec
}

// NewAbsorptionMetricsCollector creates a new absorption metrics collector.
func NewAbsorptionMetricsCollector() *AbsorptionMetricsCollector {
	return &AbsorptionMetricsCollector{
		capBindingTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "absorption_cap_binding_total",
				Help:      "Accepted tour plan (market,good,side) touches on an absorbed lane, by whether the fleet-wide cap bound the plan (outcome=bound|unbound)",
			},
			[]string{"player_id", "side", "outcome"},
		),

		ladderIncidentsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "absorption_ladder_incidents_total",
				Help:      "Tour BUY legs executed against a market carrying an outstanding EXECUTED recovery shadow (cross-plan ladder incidents)",
			},
			[]string{"player_id", "good_symbol"},
		),

		consultVerdictsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "absorption_consult_verdicts_total",
				Help:      "Absorption-consult verdicts applied at a lane/plan exclusion site, by verdict and consulting engine",
			},
			[]string{"player_id", "verdict", "engine"},
		),
	}
}

// Register registers the absorption metrics with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *AbsorptionMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}

	metrics := []prometheus.Collector{
		c.capBindingTotal,
		c.ladderIncidentsTotal,
		c.consultVerdictsTotal,
	}

	for _, metric := range metrics {
		if err := Registry.Register(metric); err != nil {
			return err
		}
	}

	return nil
}

// RecordCapBinding records one accepted-plan cap-binding classification for a touched,
// absorbed (market, good, side). side is "buy"|"sell"; outcome is "bound"|"unbound".
func (c *AbsorptionMetricsCollector) RecordCapBinding(playerID int, side, outcome string) {
	if c == nil || c.capBindingTotal == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.capBindingTotal.WithLabelValues(strconv.Itoa(playerID), side, outcome).Inc()
}

// RecordLadderIncident records one cross-plan ladder incident: a tour buy that executed
// against a market carrying an outstanding EXECUTED recovery shadow.
func (c *AbsorptionMetricsCollector) RecordLadderIncident(playerID int, goodSymbol string) {
	if c == nil || c.ladderIncidentsTotal == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.ladderIncidentsTotal.WithLabelValues(strconv.Itoa(playerID), goodSymbol).Inc()
}

// RecordConsultVerdict records one absorption-consult verdict applied at a lane/plan
// exclusion site. verdict is "skip_reserved"|"pass"; engine identifies the consulting
// call site (e.g. "idle_arb"|"trade_route").
func (c *AbsorptionMetricsCollector) RecordConsultVerdict(playerID int, verdict, engine string) {
	if c == nil || c.consultVerdictsTotal == nil {
		return // Recording is best-effort; never panic a trade path (RULINGS #4).
	}
	c.consultVerdictsTotal.WithLabelValues(strconv.Itoa(playerID), verdict, engine).Inc()
}

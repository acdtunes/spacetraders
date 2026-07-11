package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// ChainPnLMetricsCollector houses the per-chain realized-P&L series the factory kill-switch
// emits (sp-rh2z). Two families, both keyed by output good:
//
//   - chain_pnl_realized_per_hour{good}: a GAUGE of the chain's realized P&L per hour over the
//     rolling window, refreshed every time the coordinator runs a kill-check. This is the
//     number the kill verdict is made on — the dashboard's chain-level view of what each chain
//     actually nets (factory local sells + tour realized net − input cost − lift), the
//     accounting the realization side previously lacked.
//   - chain_pnl_kills_total{good}: a COUNTER incremented once per kill EPISODE (a chain
//     crossing from running to auto-paused), mirroring the stranded-hull episode counter
//     (sp-686e). Backs the ChainPnLKill alert.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch the kill decision, so every
// method is nil-safe and best-effort. The kill-switch itself fails OPEN independently of this
// collector — an unwired collector silently drops the metric, it never affects whether a chain
// is paused.
type ChainPnLMetricsCollector struct {
	// realizedPerHour is the chain's realized P&L per hour over the window, set (not
	// accumulated) on every kill-check so it always reflects the latest verdict input.
	realizedPerHour *prometheus.GaugeVec
	// killsTotal increments once per kill episode (running -> paused) for a good.
	killsTotal *prometheus.CounterVec
}

// NewChainPnLMetricsCollector creates a new chain-P&L metrics collector (sp-rh2z).
func NewChainPnLMetricsCollector() *ChainPnLMetricsCollector {
	return &ChainPnLMetricsCollector{
		realizedPerHour: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "chain_pnl_realized_per_hour",
				Help:      "Per-chain realized P&L per hour over the rolling window (factory local sells + tour realized net − input cost − lift), the number the auto-pause kill-switch judges (sp-rh2z)",
			},
			[]string{"good"},
		),
		killsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "chain_pnl_kills_total",
				Help:      "Chain auto-pause episodes: a chain crossing from running to paused because its realized P&L/hr fell below the kill threshold, counted once per episode (sp-rh2z)",
			},
			[]string{"good"},
		),
	}
}

// Register registers the chain-P&L metrics with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *ChainPnLMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}
	if err := Registry.Register(c.realizedPerHour); err != nil {
		return err
	}
	return Registry.Register(c.killsTotal)
}

// RecordRealizedPerHour sets the chain's realized P&L/hr gauge for a good. Called on every
// kill-check so the gauge tracks the latest verdict input.
func (c *ChainPnLMetricsCollector) RecordRealizedPerHour(good string, perHour float64) {
	if c == nil || c.realizedPerHour == nil {
		return // Recording is best-effort; never panic the kill-check path (RULINGS #4).
	}
	c.realizedPerHour.WithLabelValues(good).Set(perHour)
}

// RecordKill increments the kill-episode counter for a good. Emitted once per episode by the
// coordinator when a chain crosses from running to auto-paused.
func (c *ChainPnLMetricsCollector) RecordKill(good string) {
	if c == nil || c.killsTotal == nil {
		return // Recording is best-effort; never panic the kill-check path (RULINGS #4).
	}
	c.killsTotal.WithLabelValues(good).Inc()
}

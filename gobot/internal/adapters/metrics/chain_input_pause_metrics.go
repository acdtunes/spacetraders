package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// ChainInputPauseMetricsCollector houses the input-poison anti-cycle counter the factory
// coordinator emits (sp-r5a6). One family, keyed by output good:
//
//   - chain_input_pause_total{good}: a COUNTER incremented once per PAUSE EPISODE (a chain
//     crossing from running to auto-paused because its input layer went ineligible — no
//     MODERATE+ supply source in-system for a required input), mirroring the chain-P&L kill
//     episode counter (sp-rh2z) and the stranded-hull episode counter (sp-686e). Backs the
//     input-pause rate view and any anti-cycle alert.
//
// This is the INPUT side of the self-pruning portfolio (the C2 kill-switch counts the OUTPUT
// side, chain_pnl_kills_total): C2 pauses on realized P&L, this pauses on input eligibility
// BEFORE any spend. A high input-pause rate means the fleet is repeatedly lifting a system's
// inputs into SCARCE — the signal to re-site chains to systems with healthy in-system feeds.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch the pause decision, so every
// method is nil-safe and best-effort. The anti-cycle itself pauses independently of this
// collector — an unwired collector silently drops the metric, it never affects whether a chain
// is paused.
type ChainInputPauseMetricsCollector struct {
	// pausesTotal increments once per pause episode (running -> input-paused) for a good.
	pausesTotal *prometheus.CounterVec
}

// NewChainInputPauseMetricsCollector creates a new input-pause metrics collector (sp-r5a6).
func NewChainInputPauseMetricsCollector() *ChainInputPauseMetricsCollector {
	return &ChainInputPauseMetricsCollector{
		pausesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "chain_input_pause_total",
				Help:      "Chain input-poison pause episodes: a chain crossing from running to paused because its input layer went ineligible (no MODERATE+ supply source in-system for a required input), counted once per episode (sp-r5a6)",
			},
			[]string{"good"},
		),
	}
}

// Register registers the input-pause metric with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *ChainInputPauseMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}
	return Registry.Register(c.pausesTotal)
}

// RecordPause increments the input-pause-episode counter for a good. Emitted once per episode
// by the coordinator when a chain crosses from running to input-paused.
func (c *ChainInputPauseMetricsCollector) RecordPause(good string) {
	if c == nil || c.pausesTotal == nil {
		return // Recording is best-effort; never panic the pause-check path (RULINGS #4).
	}
	c.pausesTotal.WithLabelValues(good).Inc()
}

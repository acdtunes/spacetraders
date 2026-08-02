package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// ChainInputPauseMetricsCollector houses the input-poison anti-cycle counter the factory
// coordinator emits. One family, keyed by output good:
//
//   - chain_input_pause_total{good}: a COUNTER incremented once per PAUSE EPISODE (a chain
//     crossing from running to auto-paused because its input layer went ineligible — no
//     MODERATE+ supply source in-system for a required input), mirroring the chain-P&L kill
//     episode counter and the stranded-hull episode counter. Backs the
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

// NewChainInputPauseMetricsCollector creates a new input-pause metrics collector.
func NewChainInputPauseMetricsCollector() *ChainInputPauseMetricsCollector {
	return &ChainInputPauseMetricsCollector{
		pausesTotal: newCounterVec(
			"chain_input_pause_total",
			"Chain input-poison pause episodes: a chain crossing from running to paused because its input layer went ineligible (no MODERATE+ supply source in-system for a required input), counted once per episode (sp-r5a6)",
			"good",
		),
	}
}

// Register registers the input-pause metric with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *ChainInputPauseMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.pausesTotal,
	)
}

// RecordPause increments the input-pause-episode counter for a good. Emitted once per episode
// by the coordinator when a chain crosses from running to input-paused.
func (c *ChainInputPauseMetricsCollector) RecordPause(good string) {
	if c == nil || c.pausesTotal == nil {
		return // Recording is best-effort; never panic the pause-check path (RULINGS #4).
	}
	c.pausesTotal.WithLabelValues(good).Inc()
}

// globalChainInputPauseCollector is the singleton input-poison anti-cycle collector.
// Set by SetGlobalChainInputPauseCollector() when metrics are enabled; the
// goods_factory coordinator emits the input-pause episode counter through it (the INPUT
// side of the self-pruning portfolio, alongside the chain-P&L kill counter above).
var globalChainInputPauseCollector *ChainInputPauseMetricsCollector

// SetGlobalChainInputPauseCollector sets the global input-pause collector. Pass nil
// to clear it (e.g. in test cleanup).
func SetGlobalChainInputPauseCollector(collector *ChainInputPauseMetricsCollector) {
	globalChainInputPauseCollector = collector
}

// GetGlobalChainInputPauseCollector returns the global input-pause collector.
// Returns nil if metrics are not enabled.
func GetGlobalChainInputPauseCollector() *ChainInputPauseMetricsCollector {
	return globalChainInputPauseCollector
}

// RecordChainInputPause increments a chain's input-pause-episode counter globally.
// No-op when metrics are disabled, so a metrics miss never touches the pause-check path
// (RULINGS #4).
func RecordChainInputPause(good string) {
	if globalChainInputPauseCollector != nil {
		globalChainInputPauseCollector.RecordPause(good)
	}
}

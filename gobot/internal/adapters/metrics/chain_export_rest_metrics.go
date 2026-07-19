package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// ChainExportRestMetricsCollector houses the export-ask-subsidy REST counter the factory
// coordinator emits (C4). One family, keyed by output good:
//
//   - chain_export_rest_total{good}: a COUNTER incremented once per REST EPISODE (a chain
//     crossing from lifting to auto-rested because its OWN-market ask has laddered above the
//     eligible cross-source median — the 8w40 export-ask-subsidy signal, our own over-lifting
//     subsidizing tours to pay a premium at our own market). Mirrors the input-pause episode
//     counter and the chain-P&L kill episode counter.
//
// This is the OUTPUT-LADDER side of the self-pruning portfolio: the input-pause counts the INPUT
// side (chain_input_pause_total, no MODERATE+ supply source for an input), the C2 kill counts the
// realized-P&L side (chain_pnl_kills_total). A high export-rest rate means the fleet is
// over-lifting a chain's own output market faster than it recovers — the signal to slow the lift
// cadence or re-site the chain.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch the rest decision, so every
// method is nil-safe and best-effort. The rest signal itself rests independently of this
// collector — an unwired collector silently drops the metric, it never affects whether a chain
// rests.
type ChainExportRestMetricsCollector struct {
	// restsTotal increments once per rest episode (lifting -> export-rested) for a good.
	restsTotal *prometheus.CounterVec
}

// NewChainExportRestMetricsCollector creates a new export-rest metrics collector.
func NewChainExportRestMetricsCollector() *ChainExportRestMetricsCollector {
	return &ChainExportRestMetricsCollector{
		restsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "chain_export_rest_total",
				Help:      "Chain export-ask-subsidy rest episodes: a chain crossing from lifting to rested because its own-market ask laddered above the eligible cross-source median (the 8w40 signal), counted once per episode (sp-xdk6)",
			},
			[]string{"good"},
		),
	}
}

// Register registers the export-rest metric with the Prometheus registry. A nil Registry
// (metrics disabled) is a no-op, matching the sibling collectors.
func (c *ChainExportRestMetricsCollector) Register() error {
	if Registry == nil {
		return nil // Metrics not enabled
	}
	return Registry.Register(c.restsTotal)
}

// RecordRest increments the export-rest-episode counter for a good. Emitted once per episode
// by the coordinator when a chain crosses from lifting to export-rested.
func (c *ChainExportRestMetricsCollector) RecordRest(good string) {
	if c == nil || c.restsTotal == nil {
		return // Recording is best-effort; never panic the rest-check path (RULINGS #4).
	}
	c.restsTotal.WithLabelValues(good).Inc()
}

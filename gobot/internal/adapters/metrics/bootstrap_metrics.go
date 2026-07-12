package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// bootstrapKnownPhases enumerates the phases the phase gauge tracks, so RecordPhase can zero the
// others and leave exactly one series at 1 (the currently-derived phase).
var bootstrapKnownPhases = []string{"DATA", "INCOME", "GATE", "COMPLETE"}

// BootstrapMetricsCollector houses the captain bootstrap coordinator's observation series (sp-3nbe):
//
//   - bootstrap_phase{phase}: a GAUGE set to 1 for the currently-derived phase and 0 for the others,
//     so a dashboard shows which cold-start phase the reconciler is in (derived, never stored).
//   - bootstrap_probes_total: a COUNTER incremented once per probe the coordinator actually buys in
//     the DATA phase — real spend, real progress.
//   - bootstrap_haulers_total: a COUNTER incremented once per contract hauler the coordinator buys in
//     the INCOME phase (Slice 2) — the contract-fleet ramp made visible.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch a decision, so every method is
// nil-safe and best-effort. The reconciler's guard/act paths run independently of this collector.
type BootstrapMetricsCollector struct {
	phase        *prometheus.GaugeVec
	probesTotal  prometheus.Counter
	haulersTotal prometheus.Counter
}

// NewBootstrapMetricsCollector creates a new bootstrap metrics collector (sp-3nbe).
func NewBootstrapMetricsCollector() *BootstrapMetricsCollector {
	return &BootstrapMetricsCollector{
		phase: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "bootstrap_phase",
				Help:      "The captain bootstrap coordinator's currently-derived cold-start phase (1 = active), by phase (sp-3nbe)",
			},
			[]string{"phase"},
		),
		probesTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "bootstrap_probes_total",
				Help:      "Probes the bootstrap coordinator bought in the DATA phase, counted once per purchase (sp-3nbe)",
			},
		),
		haulersTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "bootstrap_haulers_total",
				Help:      "Contract haulers the bootstrap coordinator bought in the INCOME phase, counted once per purchase (sp-ysgb.1)",
			},
		),
	}
}

// Register registers the bootstrap metrics with the Prometheus registry. A nil Registry (metrics
// disabled) is a no-op, matching the sibling collectors.
func (c *BootstrapMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	if err := Registry.Register(c.phase); err != nil {
		return err
	}
	if err := Registry.Register(c.probesTotal); err != nil {
		return err
	}
	return Registry.Register(c.haulersTotal)
}

// RecordPhase sets the derived-phase gauge: the given phase to 1 and every other known phase to 0,
// so exactly one series is active (once per tick).
func (c *BootstrapMetricsCollector) RecordPhase(phase string) {
	if c == nil || c.phase == nil {
		return
	}
	for _, p := range bootstrapKnownPhases {
		v := 0.0
		if p == phase {
			v = 1.0
		}
		c.phase.WithLabelValues(p).Set(v)
	}
}

// RecordProbePurchased increments the probe-purchase counter (called once per executed DATA buy).
func (c *BootstrapMetricsCollector) RecordProbePurchased() {
	if c == nil || c.probesTotal == nil {
		return
	}
	c.probesTotal.Inc()
}

// RecordHaulerPurchased increments the hauler-purchase counter (called once per executed INCOME buy).
func (c *BootstrapMetricsCollector) RecordHaulerPurchased() {
	if c == nil || c.haulersTotal == nil {
		return
	}
	c.haulersTotal.Inc()
}

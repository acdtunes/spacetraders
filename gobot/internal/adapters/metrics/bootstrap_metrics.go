package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// bootstrapKnownPhases enumerates EVERY phase the coordinator can derive, so RecordPhase can zero the
// others and leave exactly one series at 1 (the currently-derived phase). The invariant: a derivable
// phase missing from this list is never published at all — no series is created, which a dashboard
// reads as a dead coordinator rather than as an active phase. A name here that the coordinator cannot
// derive is the mirror fault: a permanently-zero series reads as a real phase simply never entered.
// So this list and the Phase constants must stay in exact step.
var bootstrapKnownPhases = []string{"COLDSTART", "GATE", "EXPANSION"}

// BootstrapMetricsCollector houses the captain bootstrap coordinator's observation series:
//
//   - bootstrap_phase{phase}: a GAUGE set to 1 for the currently-derived phase and 0 for the others,
//     so a dashboard shows which cold-start phase the reconciler is in (derived, never stored).
//   - bootstrap_probes_total: a COUNTER incremented once per probe the coordinator actually buys in
//     the COLDSTART phase — real spend, real progress.
//   - bootstrap_haulers_total: a COUNTER incremented once per contract hauler the coordinator buys in
//     the COLDSTART phase — the contract-fleet ramp made visible.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch a decision, so every method is
// nil-safe and best-effort. The reconciler's guard/act paths run independently of this collector.
type BootstrapMetricsCollector struct {
	phase           *prometheus.GaugeVec
	probesTotal     prometheus.Counter
	haulersTotal    prometheus.Counter
	constructionPct prometheus.Gauge
}

// NewBootstrapMetricsCollector creates a new bootstrap metrics collector.
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
				Help:      "Probes the bootstrap coordinator bought in the COLDSTART phase, counted once per purchase (sp-3nbe)",
			},
		),
		haulersTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "bootstrap_haulers_total",
				Help:      "Contract haulers the bootstrap coordinator bought in the COLDSTART phase, counted once per purchase (sp-ysgb.1)",
			},
		),
		constructionPct: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: subsystem,
				Name:      "bootstrap_construction_pct",
				Help:      "The gate construction site's delivery progress [0,100] in the GATE phase, set each tick (sp-ysgb.2)",
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
	if err := Registry.Register(c.haulersTotal); err != nil {
		return err
	}
	return Registry.Register(c.constructionPct)
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

// RecordProbePurchased increments the probe-purchase counter (called once per executed COLDSTART buy).
func (c *BootstrapMetricsCollector) RecordProbePurchased() {
	if c == nil || c.probesTotal == nil {
		return
	}
	c.probesTotal.Inc()
}

// RecordHaulerPurchased increments the hauler-purchase counter (called once per executed COLDSTART buy).
func (c *BootstrapMetricsCollector) RecordHaulerPurchased() {
	if c == nil || c.haulersTotal == nil {
		return
	}
	c.haulersTotal.Inc()
}

// RecordConstructionPct sets the gate construction-progress gauge [0,100] (called each GATE tick).
func (c *BootstrapMetricsCollector) RecordConstructionPct(pct float64) {
	if c == nil || c.constructionPct == nil {
		return
	}
	c.constructionPct.Set(pct)
}

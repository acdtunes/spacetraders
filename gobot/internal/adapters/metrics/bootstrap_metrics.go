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

// BootstrapMetricsCollector houses the captain bootstrap coordinator's observation series, each keyed
// by player_id so a dashboard can scope to one player/era instead of blending every player that has run:
//
//   - bootstrap_phase{phase,player_id}: a GAUGE set to 1 for the currently-derived phase and 0 for the
//     others, so a dashboard shows which cold-start phase the reconciler is in (derived, never stored).
//   - bootstrap_probes_total{player_id}: a COUNTER incremented once per probe the coordinator actually
//     buys in the COLDSTART phase — real spend, real progress.
//   - bootstrap_haulers_total{player_id}: a COUNTER incremented once per contract hauler the coordinator
//     buys in the COLDSTART phase — the contract-fleet ramp made visible.
//
// Pure OBSERVATION (RULINGS #4): a recording miss must never touch a decision, so every method is
// nil-safe and best-effort. The reconciler's guard/act paths run independently of this collector.
type BootstrapMetricsCollector struct {
	phase           *prometheus.GaugeVec
	probesTotal     *prometheus.CounterVec
	haulersTotal    *prometheus.CounterVec
	constructionPct *prometheus.GaugeVec
}

// NewBootstrapMetricsCollector creates a new bootstrap metrics collector.
func NewBootstrapMetricsCollector() *BootstrapMetricsCollector {
	return &BootstrapMetricsCollector{
		phase: newGaugeVec(
			"bootstrap_phase",
			"The captain bootstrap coordinator's currently-derived cold-start phase (1 = active), by phase and player_id (sp-3nbe)",
			"phase",
			"player_id",
		),
		probesTotal: newCounterVec(
			"bootstrap_probes_total",
			"Probes the bootstrap coordinator bought in the COLDSTART phase, counted once per purchase, by player_id (sp-3nbe)",
			"player_id",
		),
		haulersTotal: newCounterVec(
			"bootstrap_haulers_total",
			"Contract haulers the bootstrap coordinator bought in the COLDSTART phase, counted once per purchase, by player_id (sp-ysgb.1)",
			"player_id",
		),
		constructionPct: newGaugeVec(
			"bootstrap_construction_pct",
			"The gate construction site's delivery progress [0,100] in the GATE phase, set each tick, by player_id (sp-ysgb.2)",
			"player_id",
		),
	}
}

// Register registers the bootstrap metrics with the Prometheus registry. A nil Registry (metrics
// disabled) is a no-op, matching the sibling collectors.
func (c *BootstrapMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(
		c.phase,
		c.probesTotal,
		c.haulersTotal,
		c.constructionPct,
	)
}

// RecordPhase sets the derived-phase gauge: the given phase to 1 and every other known phase to 0 for
// this player_id, so exactly one series per player is active (once per tick).
func (c *BootstrapMetricsCollector) RecordPhase(phase string, playerID string) {
	if c == nil || c.phase == nil {
		return
	}
	for _, p := range bootstrapKnownPhases {
		v := 0.0
		if p == phase {
			v = 1.0
		}
		c.phase.WithLabelValues(p, playerID).Set(v)
	}
}

// RecordProbePurchased increments the probe-purchase counter (called once per executed COLDSTART buy).
func (c *BootstrapMetricsCollector) RecordProbePurchased(playerID string) {
	if c == nil || c.probesTotal == nil {
		return
	}
	c.probesTotal.WithLabelValues(playerID).Inc()
}

// RecordHaulerPurchased increments the hauler-purchase counter (called once per executed COLDSTART buy).
func (c *BootstrapMetricsCollector) RecordHaulerPurchased(playerID string) {
	if c == nil || c.haulersTotal == nil {
		return
	}
	c.haulersTotal.WithLabelValues(playerID).Inc()
}

// RecordConstructionPct sets the gate construction-progress gauge [0,100] (called each GATE tick).
func (c *BootstrapMetricsCollector) RecordConstructionPct(pct float64, playerID string) {
	if c == nil || c.constructionPct == nil {
		return
	}
	c.constructionPct.WithLabelValues(playerID).Set(pct)
}

// globalBootstrapCollector is the singleton captain-bootstrap collector. Set by
// SetGlobalBootstrapCollector() when metrics are enabled; the bootstrap reconciler emits its
// derived-phase gauge + probe-purchase counter through it.
var globalBootstrapCollector *BootstrapMetricsCollector

// SetGlobalBootstrapCollector sets the global captain-bootstrap collector. Pass nil to
// clear it (e.g. in test cleanup).
func SetGlobalBootstrapCollector(collector *BootstrapMetricsCollector) {
	globalBootstrapCollector = collector
}

// GetGlobalBootstrapCollector returns the global captain-bootstrap collector.
func GetGlobalBootstrapCollector() *BootstrapMetricsCollector {
	return globalBootstrapCollector
}

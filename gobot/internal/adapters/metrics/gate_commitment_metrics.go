package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// GateCommitmentMetricsCollector makes gate-material DOUBLE-BUYING visible; without it the only
// evidence is the transaction ledger, summed by hand. Each half is load-bearing:
//
//   - skips_total is the guard's heartbeat. A guard that has stopped being consulted (a new buy
//     path, a refactor that drops the netting) looks exactly like a fleet with nothing in
//     flight, and a flat series is the only way to tell them apart.
//   - overshoot_units_total is the FAILURE. It should be flat at zero; anything else means units
//     were bought past a requirement and the guard did not hold.
//
// Labelled by good: the operator's question is always which material is being over-bought.
type GateCommitmentMetricsCollector struct {
	skipsTotal          *prometheus.CounterVec
	overshootUnitsTotal *prometheus.CounterVec
	// stallSeconds is the PROGRESS watchdog (sp-63r4f): how long an unmet gate material has gone
	// without receiving a single unit. A GAUGE, not a counter, because the question is always "how
	// bad is it RIGHT NOW" — a counter of stall events would tick once and tell you nothing about
	// whether the gate is still stopped.
	//
	// It exists because three separate defects this week each logged something true and reassuring
	// on every tick while the gate sat stopped for roughly 10 of 14 hours, and every one was found
	// by a human asking why rather than by anything saying so. A log line can be true and still
	// invisible; a gauge above zero can be alerted on.
	stallSeconds *prometheus.GaugeVec
}

// NewGateCommitmentMetricsCollector creates the gate in-flight-commitment collector.
func NewGateCommitmentMetricsCollector() *GateCommitmentMetricsCollector {
	return &GateCommitmentMetricsCollector{
		skipsTotal: newCounterVec(
			"gate_material_inflight_skips_total",
			"Gate-material purchases declined because the outstanding bill is already covered by units already bought and in a hull's hold",
			"good",
		),
		overshootUnitsTotal: newCounterVec(
			"gate_material_overshoot_units_total",
			"Units of a gate material recorded as purchased BEYOND its construction requirement (should stay flat at zero)",
			"good",
		),
		stallSeconds: newGaugeVec(
			"gate_material_stall_seconds",
			"Seconds an UNMET gate material has gone with zero delivered units. 0 means progress or a satisfied bill; a sustained non-zero value means the gate is stopped, whatever the cause",
			"good",
		),
	}
}

// Register registers the gate-commitment metrics with the Prometheus registry.
func (c *GateCommitmentMetricsCollector) Register() error {
	if Registry == nil {
		return nil
	}
	return registerAll(c.skipsTotal, c.overshootUnitsTotal, c.stallSeconds)
}

// RecordInFlightSkip counts one purchase declined because in-flight cargo already covers the bill.
func (c *GateCommitmentMetricsCollector) RecordInFlightSkip(good string) {
	c.skipsTotal.WithLabelValues(good).Inc()
}

// RecordOvershootUnits counts units bought past a material's requirement. Non-positive is ignored,
// so a caller may hand it a raw difference.
func (c *GateCommitmentMetricsCollector) RecordOvershootUnits(good string, units int) {
	if units <= 0 {
		return
	}
	c.overshootUnitsTotal.WithLabelValues(good).Add(float64(units))
}

// InFlightSkipCount reports the skips recorded for a good. Exported so the coordinator's package
// can prove it emits: without a reader reachable from there, "the collector counts what it is
// told" and "the drain tells it" are separate claims and only the first is testable. 0 if unread.
func (c *GateCommitmentMetricsCollector) InFlightSkipCount(good string) float64 {
	return counterValue(c.skipsTotal, good)
}

// OvershootUnitsCount reports the over-bought units recorded for a good so far.
func (c *GateCommitmentMetricsCollector) OvershootUnitsCount(good string) float64 {
	return counterValue(c.overshootUnitsTotal, good)
}

func counterValue(vec *prometheus.CounterVec, labels ...string) float64 {
	var m dto.Metric
	if err := vec.WithLabelValues(labels...).Write(&m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

// globalGateCommitmentCollector is the singleton set by SetGlobalGateCommitmentCollector when
// metrics are enabled. The construction drain is constructed independently of the metrics stack,
// so the package-level recorders below stand in for threading a collector through every path.
var globalGateCommitmentCollector *GateCommitmentMetricsCollector

// SetGlobalGateCommitmentCollector sets the global gate-commitment metrics collector.
func SetGlobalGateCommitmentCollector(collector *GateCommitmentMetricsCollector) {
	globalGateCommitmentCollector = collector
}

// RecordGateInFlightSkip counts an in-flight-covered purchase decline globally. A no-op when
// metrics are disabled, so the guard never depends on the metrics stack being up.
func RecordGateInFlightSkip(good string) {
	if globalGateCommitmentCollector != nil {
		globalGateCommitmentCollector.RecordInFlightSkip(good)
	}
}

// RecordGateOvershootUnits counts units bought past a gate material's requirement globally.
func RecordGateOvershootUnits(good string, units int) {
	if globalGateCommitmentCollector != nil {
		globalGateCommitmentCollector.RecordOvershootUnits(good, units)
	}
}

// SetStallSeconds publishes how long an unmet gate material has gone without a delivered unit.
//
// SET, NOT ADD, and it must be written on EVERY tick including the healthy zero. A gauge only
// written when something is wrong keeps its last bad value forever after a recovery, so the alarm
// never clears and the next real stall is indistinguishable from the stale one — which is the same
// "reassuring but wrong" failure this whole watchdog exists to end.
func (c *GateCommitmentMetricsCollector) SetStallSeconds(good string, seconds float64) {
	if c == nil || c.stallSeconds == nil {
		return
	}
	c.stallSeconds.WithLabelValues(good).Set(seconds)
}

// RecordGateStallSeconds publishes the stall gauge through the global collector.
func RecordGateStallSeconds(good string, seconds float64) {
	if globalGateCommitmentCollector != nil {
		globalGateCommitmentCollector.SetStallSeconds(good, seconds)
	}
}

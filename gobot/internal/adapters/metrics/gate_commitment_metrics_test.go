package metrics

import "testing"

// The collector counts what it is told, and the PACKAGE-LEVEL recorders reach it. Both halves are
// asserted: the second is what the construction drain actually calls, and a global left unset is
// exactly how a mitigation ships green and measures nothing (sp-v2a2h acceptance 6).
func TestGateCommitmentMetrics_CountsSkipsAndOvershoot(t *testing.T) {
	c := NewGateCommitmentMetricsCollector()
	previous := globalGateCommitmentCollector
	SetGlobalGateCommitmentCollector(c)
	t.Cleanup(func() { SetGlobalGateCommitmentCollector(previous) })

	RecordGateInFlightSkip("ADVANCED_CIRCUITRY")
	RecordGateInFlightSkip("ADVANCED_CIRCUITRY")
	RecordGateOvershootUnits("ADVANCED_CIRCUITRY", 36)

	if got := c.InFlightSkipCount("ADVANCED_CIRCUITRY"); got != 2 {
		t.Fatalf("InFlightSkipCount = %v, want 2 — the drain's declines are not reaching the collector, so a guard that stops being consulted is indistinguishable from a fleet with nothing in flight", got)
	}
	if got := c.OvershootUnitsCount("ADVANCED_CIRCUITRY"); got != 36 {
		t.Fatalf("OvershootUnitsCount = %v, want 36", got)
	}
	if got := c.InFlightSkipCount("FAB_MATS"); got != 0 {
		t.Fatalf("FAB_MATS recorded %v skips, want 0 — the counters are not separated by good", got)
	}
}

// A non-positive overshoot is not an event. The caller hands over a raw difference, so the guard
// belongs here rather than at every call site.
func TestGateCommitmentMetrics_IgnoresANonPositiveOvershoot(t *testing.T) {
	c := NewGateCommitmentMetricsCollector()
	c.RecordOvershootUnits("FAB_MATS", 0)
	c.RecordOvershootUnits("FAB_MATS", -12)

	if got := c.OvershootUnitsCount("FAB_MATS"); got != 0 {
		t.Fatalf("OvershootUnitsCount = %v, want 0 — a bill that is merely covered is not an overshoot, and counting it would make the series that must stay flat at zero useless", got)
	}
}

// With no collector set the recorders are inert: a money guard must never depend on the metrics
// stack being up.
func TestGateCommitmentMetrics_RecordersAreInertWithoutACollector(t *testing.T) {
	previous := globalGateCommitmentCollector
	SetGlobalGateCommitmentCollector(nil)
	t.Cleanup(func() { SetGlobalGateCommitmentCollector(previous) })

	RecordGateInFlightSkip("ADVANCED_CIRCUITRY")
	RecordGateOvershootUnits("ADVANCED_CIRCUITRY", 36)
}

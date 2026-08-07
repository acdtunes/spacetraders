package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// readSurface is one tick's reading with every field distinct, so a component wired to the wrong
// field reads as a wrong NUMBER rather than passing on a coincidence.
func readSurface() LaneSurface {
	return LaneSurface{
		Unserved:            3,
		Profitable:          12,
		TradePool:           9,
		SystemsScanned:      4,
		CrossSystem:         7,
		AbsorbableUnits:     264,
		RejectedUnreachable: 21,
		RejectedJumpCost:    5,
		Readable:            true,
	}
}

// unserved=0 is reached both by "nothing profitable" and by "the pool covers everything", and those
// call for opposite actions — so the parts must be readable separately, not just the difference.
// The census terms are here for the same reason one layer down: a thin surface and a reachability
// failure both print as a small profitable count and differ only in what was refused.
func TestRecordLaneSurfacePublishesEachComponent(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()
	c.RecordLaneSurface("1", readSurface())

	for _, tc := range []struct {
		component string
		want      float64
	}{
		{"unserved", 3},
		{"profitable", 12},
		{"trade_pool", 9},
		{"systems_scanned", 4},
		{"cross_system", 7},
		{"absorbable_units", 264},
		{"rejected_unreachable", 21},
		{"rejected_jump_cost", 5},
		{"readable", 1},
	} {
		if got := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", tc.component)); got != tc.want {
			t.Errorf("component %q = %v, want %v", tc.component, got, tc.want)
		}
	}
}

func TestRecordLaneSurfaceMarksAnUnreadableSurface(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()
	c.RecordLaneSurface("1", LaneSurface{TradePool: 9, SystemsScanned: 4})

	if got := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", "readable")); got != 0 {
		t.Errorf("readable = %v on an unreadable surface, want 0", got)
	}
	if got := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", "unserved")); got != 0 {
		t.Errorf("unserved = %v, want 0", got)
	}
}

// A term left standing from the last readable tick is WORSE than no term: it explains a count that
// is no longer published, next to readable=0 saying nobody looked. Every component is rewritten on
// every call or the blindness the parts remove comes back through the terms.
func TestLaneSurfaceTermsDoNotSurviveABlindRead(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()
	c.RecordLaneSurface("1", readSurface())
	c.RecordLaneSurface("1", LaneSurface{TradePool: 9, SystemsScanned: 4})

	for _, component := range []string{"cross_system", "absorbable_units", "rejected_unreachable", "rejected_jump_cost"} {
		if got := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", component)); got != 0 {
			t.Errorf("component %q = %v after a blind read, want 0 — a term from the previous tick is standing", component, got)
		}
	}
}

// The reader calls the package-level function, and a nil global is the metrics-disabled daemon.
func TestRecordGrowthLaneSurfaceGlobalPath(t *testing.T) {
	prior := globalFleetGrowthCollector
	t.Cleanup(func() { SetGlobalFleetGrowthCollector(prior) })

	SetGlobalFleetGrowthCollector(nil)
	RecordGrowthLaneSurface("1", readSurface())

	c := NewFleetGrowthMetricsCollector()
	SetGlobalFleetGrowthCollector(c)
	RecordGrowthLaneSurface("1", readSurface())

	for _, tc := range []struct {
		component string
		want      float64
	}{
		{"profitable", 12},
		{"trade_pool", 9},
		{"rejected_unreachable", 21},
	} {
		if got := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", tc.component)); got != tc.want {
			t.Errorf("component %q = %v through the global path, want %v", tc.component, got, tc.want)
		}
	}
}

// A genuine zero and a blind read differ ONLY in readable; if this passes with readable pinned to a
// constant, the series is not carrying the distinction it claims to.
func TestLaneSurfaceReadableSeparatesGenuineZeroFromBlindRead(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()

	c.RecordLaneSurface("1", LaneSurface{TradePool: 9, SystemsScanned: 4, Readable: true})
	genuine := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", "readable"))

	c.RecordLaneSurface("1", LaneSurface{TradePool: 9, SystemsScanned: 4})
	blind := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", "readable"))

	if genuine == blind {
		t.Fatalf("a genuine zero and a blind read both published readable=%v", genuine)
	}
	if genuine != 1 || blind != 0 {
		t.Errorf("readable: genuine=%v blind=%v, want 1 and 0", genuine, blind)
	}
}

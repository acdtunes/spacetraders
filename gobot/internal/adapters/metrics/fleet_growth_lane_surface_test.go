package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// unserved=0 is reached both by "nothing profitable" and by "the pool covers everything", and those
// call for opposite actions — so the parts must be readable separately, not just the difference.
func TestRecordLaneSurfacePublishesEachComponent(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()
	c.RecordLaneSurface("1", 3, 12, 9, 4, true)

	for _, tc := range []struct {
		component string
		want      float64
	}{
		{"unserved", 3},
		{"profitable", 12},
		{"trade_pool", 9},
		{"systems_scanned", 4},
		{"readable", 1},
	} {
		if got := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", tc.component)); got != tc.want {
			t.Errorf("component %q = %v, want %v", tc.component, got, tc.want)
		}
	}
}

func TestRecordLaneSurfaceMarksAnUnreadableSurface(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()
	c.RecordLaneSurface("1", 0, 0, 9, 4, false)

	if got := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", "readable")); got != 0 {
		t.Errorf("readable = %v on an unreadable surface, want 0", got)
	}
	if got := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", "unserved")); got != 0 {
		t.Errorf("unserved = %v, want 0", got)
	}
}

// The reader calls the package-level function, and a nil global is the metrics-disabled daemon.
func TestRecordGrowthLaneSurfaceGlobalPath(t *testing.T) {
	prior := globalFleetGrowthCollector
	t.Cleanup(func() { SetGlobalFleetGrowthCollector(prior) })

	SetGlobalFleetGrowthCollector(nil)
	RecordGrowthLaneSurface("1", 3, 12, 9, 4, true)

	c := NewFleetGrowthMetricsCollector()
	SetGlobalFleetGrowthCollector(c)
	RecordGrowthLaneSurface("1", 3, 12, 9, 4, true)

	if got := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", "profitable")); got != 12 {
		t.Errorf("profitable = %v through the global path, want 12", got)
	}
	if got := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", "trade_pool")); got != 9 {
		t.Errorf("trade_pool = %v through the global path, want 9", got)
	}
}

// A genuine zero and a blind read differ ONLY in readable; if this passes with readable pinned to a
// constant, the series is not carrying the distinction it claims to.
func TestLaneSurfaceReadableSeparatesGenuineZeroFromBlindRead(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()

	c.RecordLaneSurface("1", 0, 0, 9, 4, true)
	genuine := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", "readable"))

	c.RecordLaneSurface("1", 0, 0, 9, 4, false)
	blind := testutil.ToFloat64(c.laneSurface.WithLabelValues("1", "readable"))

	if genuine == blind {
		t.Fatalf("a genuine zero and a blind read both published readable=%v", genuine)
	}
	if genuine != 1 || blind != 0 {
		t.Errorf("readable: genuine=%v blind=%v, want 1 and 0", genuine, blind)
	}
}

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestTourStalenessMetrics_RegisterAndExport proves the counter REGISTERS on the
// daemon's registry AND actually appears by name once observed — the bopj P10 trap
// where a family is "registered" yet never shows on /metrics because no label
// combination was ever incremented.
func TestTourStalenessMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourStalenessMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordStaleExcluded(1, "X1-XT71", 3)

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	if !got["spacetraders_daemon_tour_lanes_stale_excluded_total"] {
		t.Errorf("metric registered but not exported on the registry")
	}
}

// TestTourStalenessMetrics_LabelsAndValues pins the {player_id, system} label set and
// that repeat records ACCUMULATE per system (Add semantics, not Inc): a system dropping
// 3 then 2 lanes reads 5.
func TestTourStalenessMetrics_LabelsAndValues(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourStalenessMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordStaleExcluded(7, "X1-XT71", 3)
	c.RecordStaleExcluded(7, "X1-XT71", 2)
	c.RecordStaleExcluded(7, "X1-UQ87", 1)
	// count <= 0 must be a no-op, never a zero-observation that would export a spurious
	// series or subtract.
	c.RecordStaleExcluded(7, "X1-UQ87", 0)

	cases := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"XT71 accumulates", map[string]string{"player_id": "7", "system": "X1-XT71"}, 5},
		{"UQ87 single drop", map[string]string{"player_id": "7", "system": "X1-UQ87"}, 1},
	}
	for _, tc := range cases {
		got, ok := gatherCounter(t, Registry, "spacetraders_daemon_tour_lanes_stale_excluded_total", tc.labels)
		if !ok {
			t.Errorf("%s: series not found", tc.name)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestTourStalenessMetrics_NilSafe mirrors the sibling collectors' guarantee: a
// recording miss on a typed-nil receiver or an uninitialized collector degrades to a
// no-op, never a SIGSEGV that would take down the tour path (RULINGS #4).
func TestTourStalenessMetrics_NilSafe(t *testing.T) {
	var nilC *TourStalenessMetricsCollector
	nilC.RecordStaleExcluded(1, "X1-XT71", 1)

	empty := &TourStalenessMetricsCollector{}
	empty.RecordStaleExcluded(1, "X1-XT71", 1)
}

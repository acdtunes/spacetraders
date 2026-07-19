package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestFleetHealthMetrics_RegisterAndExport proves the stranded-hull counter REGISTERS on
// the daemon's registry AND actually appears by name once observed. A registered
// CounterVec exports nothing until a label combination is incremented — the bopj P10 trap
// where a family was "registered" yet never showed on /metrics; the stranded detector emits
// at most once per episode, so the export path must be proven with a real observation.
func TestFleetHealthMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewFleetHealthMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Observe one series so the registry has something to gather (a stranded episode).
	c.RecordHullStranded("TORWIND-2C", "X1-PD21")

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	if !got["spacetraders_daemon_fleet_hull_stranded_total"] {
		t.Errorf("metric spacetraders_daemon_fleet_hull_stranded_total registered but not exported on the registry")
	}
}

// TestFleetHealthMetrics_LabelsAndValues pins the {ship,system} label set and that repeat
// episodes accumulate on the right series (the metric is emitted once per stranded episode,
// so a second episode for the same hull increments the same series).
func TestFleetHealthMetrics_LabelsAndValues(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewFleetHealthMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordHullStranded("TORWIND-2C", "X1-PD21")
	c.RecordHullStranded("TORWIND-2C", "X1-PD21")
	c.RecordHullStranded("TORWIND-9A", "X1-ZZ99")

	const name = "spacetraders_daemon_fleet_hull_stranded_total"
	cases := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"2c at pd21", map[string]string{"ship": "TORWIND-2C", "system": "X1-PD21"}, 2},
		{"9a at zz99", map[string]string{"ship": "TORWIND-9A", "system": "X1-ZZ99"}, 1},
	}
	for _, tc := range cases {
		got, ok := gatherCounter(t, Registry, name, tc.labels)
		if !ok {
			t.Errorf("%s: series %s%v not found", tc.name, name, tc.labels)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: %s%v = %v, want %v", tc.name, name, tc.labels, got, tc.want)
		}
	}
}

// TestFleetHealthMetrics_NilSafe mirrors the sibling collectors' guarantee: a recording miss
// on a typed-nil receiver or an uninitialized collector must degrade to a no-op, never a
// SIGSEGV that would take down the reposition/tour path (RULINGS #4 — observation only).
func TestFleetHealthMetrics_NilSafe(t *testing.T) {
	var nilC *FleetHealthMetricsCollector
	nilC.RecordHullStranded("TORWIND-2C", "X1-PD21")

	empty := &FleetHealthMetricsCollector{}
	empty.RecordHullStranded("TORWIND-2C", "X1-PD21")
}

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// gatherGauge is declared once for the whole metrics test package in
// tour_metrics_test.go — this file's gauge assertions in
// TestScoutMetrics_LabelsAndValues below reuse that shared definition rather
// than redeclaring it, since Go test files in the same package share scope.

// TestScoutMetrics_RegisterAndExport proves scout_freshness_actual_seconds
// REGISTERS on the daemon's registry AND actually appears by name once observed — the
// bopj P10 trap where a family was "registered" yet never showed on /metrics.
func TestScoutMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewScoutMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordFreshness(1, "X1-GZ7", 42.5)

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	const want = "spacetraders_daemon_scout_freshness_actual_seconds"
	if !got[want] {
		t.Errorf("metric %q registered but not exported on the registry", want)
	}
}

// TestScoutMetrics_LabelsAndValues pins scout_freshness_actual_seconds' label set
// (player_id, system) and that it SETS rather than accumulates — a later sweep's
// reading overwrites the prior one on the same series, since staleness is a snapshot
// of "right now", not a running total.
func TestScoutMetrics_LabelsAndValues(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewScoutMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordFreshness(7, "X1-GZ7", 120)
	c.RecordFreshness(7, "X1-KA42", 30)
	c.RecordFreshness(7, "X1-GZ7", 45) // same series, later sweep — overwrites, not adds

	const name = "spacetraders_daemon_scout_freshness_actual_seconds"

	cases := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"GZ7 latest sweep", map[string]string{"player_id": "7", "system": "X1-GZ7"}, 45},
		{"KA42 latest sweep", map[string]string{"player_id": "7", "system": "X1-KA42"}, 30},
	}
	for _, tc := range cases {
		got, ok := gatherGauge(t, Registry, name, tc.labels)
		if !ok {
			t.Errorf("%s: series %s%v not found", tc.name, name, tc.labels)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: %s%v = %v, want %v", tc.name, name, tc.labels, got, tc.want)
		}
	}
}

// TestScoutMetrics_NilSafe: unlike manufacturing's RecordTaskCompletion, RecordFreshness
// self-guards (convention (a) — see manufacturing_metrics_test.go's doc comment for the
// two-convention split) so a recording miss on a typed-nil receiver or an uninitialized
// collector must degrade to a no-op, never a SIGSEGV that would take down the scout post
// coordinator's reconcile sweep (RULINGS #4 — observation only).
func TestScoutMetrics_NilSafe(t *testing.T) {
	var nilC *ScoutMetricsCollector
	nilC.RecordFreshness(1, "X1-GZ7", 42.5)

	empty := &ScoutMetricsCollector{}
	empty.RecordFreshness(1, "X1-GZ7", 42.5)
}

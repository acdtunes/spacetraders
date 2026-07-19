package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TestManufacturingMetrics_RegisterAndExport proves manufacturing_tasks_completed_total
// REGISTERS on the daemon's registry AND actually appears by name once
// observed — the bopj P10 trap where a family was "registered" yet never showed on
// /metrics.
func TestManufacturingMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewManufacturingMetricsCollector(nil)
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordTaskCompletion(1, "ACQUIRE_DELIVER", "completed", 90*time.Second)

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	const want = "spacetraders_daemon_manufacturing_tasks_completed_total"
	if !got[want] {
		t.Errorf("metric %q registered but not exported on the registry", want)
	}
}

// TestManufacturingMetrics_LabelsAndValues pins manufacturing_tasks_completed_total's
// label set (player_id, task_type, status) and that repeat records accumulate on the
// right series, mirroring the three statuses actually emitted at the real call sites
// (completed, failed, deferred).
func TestManufacturingMetrics_LabelsAndValues(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewManufacturingMetricsCollector(nil)
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordTaskCompletion(7, "ACQUIRE_DELIVER", "completed", 30*time.Second)
	c.RecordTaskCompletion(7, "ACQUIRE_DELIVER", "completed", 45*time.Second)
	c.RecordTaskCompletion(7, "ACQUIRE_DELIVER", "failed", 10*time.Second)
	c.RecordTaskCompletion(7, "COLLECT_SELL", "deferred", 5*time.Second)

	const name = "spacetraders_daemon_manufacturing_tasks_completed_total"

	cases := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"acquire_deliver completed", map[string]string{"player_id": "7", "task_type": "ACQUIRE_DELIVER", "status": "completed"}, 2},
		{"acquire_deliver failed", map[string]string{"player_id": "7", "task_type": "ACQUIRE_DELIVER", "status": "failed"}, 1},
		{"collect_sell deferred", map[string]string{"player_id": "7", "task_type": "COLLECT_SELL", "status": "deferred"}, 1},
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

// No NilSafe subtest here (unlike the absorption/container families): RecordTaskCompletion
// is pre-existing code and carries no self-guard — calling it on a nil
// *ManufacturingMetricsCollector or a bare &ManufacturingMetricsCollector{} panics on the nil
// CounterVec/HistogramVec fields. Its RULINGS #4 nil-safety instead comes entirely from the
// package-level RecordManufacturingTaskCompletion wrapper (prometheus_collector.go), which
// every real call site uses and which checks globalManufacturingCollector != nil before ever
// calling this method — the same convention container_metrics.go's pre-existing sibling
// methods (RecordContainerCompletion etc.) already rely on.

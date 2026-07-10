package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// gatherGauge / gatherHistogramCount mirror gatherCounter (absorption_metrics_test.go) for
// the two non-counter tour metrics: they read a single series off the registry via Gather()
// — the same path promhttp.HandlerFor(Registry) serves on /metrics. ok=false means the
// series is absent (never registered, or never observed and therefore never exported).
func gatherGauge(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			if len(got) != len(labels) {
				continue
			}
			match := true
			for k, v := range labels {
				if got[k] != v {
					match = false
					break
				}
			}
			if match {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

// gatherHistogramCount returns the observation COUNT of a histogram series (the _count the
// exposition emits), which is all the value-level assertions here need.
func gatherHistogramCount(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) (uint64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			if len(got) != len(labels) {
				continue
			}
			match := true
			for k, v := range labels {
				if got[k] != v {
					match = false
					break
				}
			}
			if match {
				return m.GetHistogram().GetSampleCount(), true
			}
		}
	}
	return 0, false
}

// TestTourMetrics_RegisterAndExport proves ALL SIX tour metrics REGISTER on the daemon's
// registry AND actually appear by name once observed. A registered CounterVec/HistogramVec/
// GaugeVec exports nothing until a label combination is touched — the bopj P10 trap where a
// family was "registered" yet never showed on /metrics. Registration alone is not export.
func TestTourMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Observe one combination of each family so the registry has something to gather.
	c.RecordReposition(1, "success")
	c.RecordMarginsDeath(1)
	c.RecordReserveFloorEngagement(1, "shrink")
	c.RecordExit(1, "starvation")
	c.ObserveDuration(1, 420)
	c.SetResolvedMaxSpend(1, 250000)

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	for _, want := range []string{
		"spacetraders_daemon_tour_repositions_total",
		"spacetraders_daemon_tour_margins_death_total",
		"spacetraders_daemon_tour_reserve_floor_engagements_total",
		"spacetraders_daemon_tour_exit_total",
		"spacetraders_daemon_tour_duration_seconds",
		"spacetraders_daemon_tour_resolved_max_spend",
	} {
		if !got[want] {
			t.Errorf("metric %q registered but not exported on the registry", want)
		}
	}
}

// TestTourMetrics_LabelsAndValues pins the label sets and that repeat records accumulate on
// the right series: reposition keyed by outcome, floor by action, exit by reason; the gauge
// holds the last-set cap; the histogram counts each observation.
func TestTourMetrics_LabelsAndValues(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewTourMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordReposition(7, "success")
	c.RecordReposition(7, "no_candidate")
	c.RecordReposition(7, "no_candidate")
	c.RecordReposition(7, "failed")
	c.RecordMarginsDeath(7)
	c.RecordMarginsDeath(7)
	c.RecordReserveFloorEngagement(7, "skip")
	c.RecordReserveFloorEngagement(7, "shrink")
	c.RecordReserveFloorEngagement(7, "shrink")
	c.RecordExit(7, "starvation")
	c.RecordExit(7, "iterations_exhausted")
	c.ObserveDuration(7, 120)
	c.ObserveDuration(7, 3600)
	c.SetResolvedMaxSpend(7, 100000)
	c.SetResolvedMaxSpend(7, 250000) // last-write-wins

	const (
		repoName  = "spacetraders_daemon_tour_repositions_total"
		deathName = "spacetraders_daemon_tour_margins_death_total"
		floorName = "spacetraders_daemon_tour_reserve_floor_engagements_total"
		exitName  = "spacetraders_daemon_tour_exit_total"
		capName   = "spacetraders_daemon_tour_resolved_max_spend"
		durName   = "spacetraders_daemon_tour_duration_seconds"
	)

	counterCases := []struct {
		name   string
		metric string
		labels map[string]string
		want   float64
	}{
		{"reposition success", repoName, map[string]string{"player_id": "7", "outcome": "success"}, 1},
		{"reposition no_candidate", repoName, map[string]string{"player_id": "7", "outcome": "no_candidate"}, 2},
		{"reposition failed", repoName, map[string]string{"player_id": "7", "outcome": "failed"}, 1},
		{"margins death", deathName, map[string]string{"player_id": "7"}, 2},
		{"floor skip", floorName, map[string]string{"player_id": "7", "action": "skip"}, 1},
		{"floor shrink", floorName, map[string]string{"player_id": "7", "action": "shrink"}, 2},
		{"exit starvation", exitName, map[string]string{"player_id": "7", "reason": "starvation"}, 1},
		{"exit iterations", exitName, map[string]string{"player_id": "7", "reason": "iterations_exhausted"}, 1},
	}
	for _, tc := range counterCases {
		got, ok := gatherCounter(t, Registry, tc.metric, tc.labels)
		if !ok {
			t.Errorf("%s: series %s%v not found", tc.name, tc.metric, tc.labels)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: %s%v = %v, want %v", tc.name, tc.metric, tc.labels, got, tc.want)
		}
	}

	if got, ok := gatherGauge(t, Registry, capName, map[string]string{"player_id": "7"}); !ok {
		t.Errorf("gauge %s{player_id=7} not found", capName)
	} else if got != 250000 {
		t.Errorf("gauge %s{player_id=7} = %v, want 250000 (last-write-wins)", capName, got)
	}

	if got, ok := gatherHistogramCount(t, Registry, durName, map[string]string{"player_id": "7"}); !ok {
		t.Errorf("histogram %s{player_id=7} not found", durName)
	} else if got != 2 {
		t.Errorf("histogram %s{player_id=7} sample count = %v, want 2", durName, got)
	}
}

// TestTourMetrics_NilSafe mirrors the absorption collector's guarantee (RULINGS #4 —
// observation only): a recording miss on a typed-nil receiver or an uninitialized collector
// must degrade to a no-op, never a SIGSEGV that would take down the trade path.
func TestTourMetrics_NilSafe(t *testing.T) {
	var nilC *TourMetricsCollector
	nilC.RecordReposition(1, "success")
	nilC.RecordMarginsDeath(1)
	nilC.RecordReserveFloorEngagement(1, "skip")
	nilC.RecordExit(1, "starvation")
	nilC.ObserveDuration(1, 10)
	nilC.SetResolvedMaxSpend(1, 100)

	empty := &TourMetricsCollector{}
	empty.RecordReposition(1, "failed")
	empty.RecordMarginsDeath(1)
	empty.RecordReserveFloorEngagement(1, "shrink")
	empty.RecordExit(1, "iterations_exhausted")
	empty.ObserveDuration(1, 20)
	empty.SetResolvedMaxSpend(1, 200)
}

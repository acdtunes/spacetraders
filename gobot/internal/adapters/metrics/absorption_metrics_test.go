package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// gatherCounter reads the value of a single counter series (metric name + exact label
// set) off a registry via Gather() — the same path promhttp.HandlerFor(Registry) serves
// on /metrics. Method-call based so the test needs no dto import. ok=false means the
// series is absent (never registered, or never observed and therefore never exported).
func gatherCounter(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) (float64, bool) {
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
				return m.GetCounter().GetValue(), true
			}
		}
	}
	return 0, false
}

// TestAbsorptionMetrics_RegisterAndExport proves both counters REGISTER on the daemon's
// registry AND actually appear by name once observed. A registered CounterVec exports
// nothing until a label combination is incremented — the bopj P10 trap where a family was
// "registered" yet never showed on /metrics.
func TestAbsorptionMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewAbsorptionMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Observe one combination of each family so the registry has something to gather.
	c.RecordCapBinding(1, "sell", "bound")
	c.RecordLadderIncident(1, "IRON_ORE")

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	for _, want := range []string{
		"spacetraders_daemon_absorption_cap_binding_total",
		"spacetraders_daemon_absorption_ladder_incidents_total",
	} {
		if !got[want] {
			t.Errorf("metric %q registered but not exported on the registry", want)
		}
	}
}

// TestAbsorptionMetrics_LabelsAndValues pins the label sets and that repeat records
// accumulate on the right series (cap-binding keyed by side+outcome; ladder by good).
func TestAbsorptionMetrics_LabelsAndValues(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewAbsorptionMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordCapBinding(7, "sell", "bound")
	c.RecordCapBinding(7, "sell", "bound")
	c.RecordCapBinding(7, "buy", "unbound")
	c.RecordLadderIncident(7, "COPPER_ORE")
	c.RecordLadderIncident(7, "COPPER_ORE")
	c.RecordLadderIncident(7, "IRON_ORE")

	const capName = "spacetraders_daemon_absorption_cap_binding_total"
	const ladderName = "spacetraders_daemon_absorption_ladder_incidents_total"

	cases := []struct {
		name   string
		metric string
		labels map[string]string
		want   float64
	}{
		{"cap bound sell", capName, map[string]string{"player_id": "7", "side": "sell", "outcome": "bound"}, 2},
		{"cap unbound buy", capName, map[string]string{"player_id": "7", "side": "buy", "outcome": "unbound"}, 1},
		{"ladder copper", ladderName, map[string]string{"player_id": "7", "good_symbol": "COPPER_ORE"}, 2},
		{"ladder iron", ladderName, map[string]string{"player_id": "7", "good_symbol": "IRON_ORE"}, 1},
	}
	for _, tc := range cases {
		got, ok := gatherCounter(t, Registry, tc.metric, tc.labels)
		if !ok {
			t.Errorf("%s: series %s%v not found", tc.name, tc.metric, tc.labels)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: %s%v = %v, want %v", tc.name, tc.metric, tc.labels, got, tc.want)
		}
	}
}

// TestAbsorptionMetrics_NilSafe mirrors the API collector's guarantee: a recording miss
// on a typed-nil receiver or an uninitialized collector must degrade to a no-op, never a
// SIGSEGV that would take down the trade path (RULINGS #4 — observation only).
func TestAbsorptionMetrics_NilSafe(t *testing.T) {
	var nilC *AbsorptionMetricsCollector
	nilC.RecordCapBinding(1, "sell", "bound")
	nilC.RecordLadderIncident(1, "IRON_ORE")

	empty := &AbsorptionMetricsCollector{}
	empty.RecordCapBinding(1, "buy", "unbound")
	empty.RecordLadderIncident(1, "COPPER_ORE")
}

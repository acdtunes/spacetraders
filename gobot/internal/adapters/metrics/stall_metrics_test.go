package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestStallMetrics_RegisterAndExport proves both series REGISTER on the daemon's registry AND
// actually appear by name once observed. A registered Vec exports nothing until a label
// combination is touched, and an escalation is emitted at most once per stall episode, so the
// export path has to be proven with a real observation rather than a bare Register() call.
func TestStallMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewStallMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordStallStreak("fleet_autosizer", "heavy", "treasury_floor", 2)
	c.RecordStallEscalation("fleet_autosizer", "heavy", "treasury_floor")

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	for _, name := range []string{
		"spacetraders_daemon_coordinator_stall_streak",
		"spacetraders_daemon_coordinator_stall_escalations_total",
	} {
		if !got[name] {
			t.Errorf("metric %s registered but not exported on the registry", name)
		}
	}
}

// TestStallMetrics_StreakGaugeTracksTheLiveStreak pins the {coordinator,scope,reason} label set
// and that the gauge SETS rather than accumulates — including draining back to zero when the
// block clears, which is what stops a recovered coordinator from reading as permanently wedged.
func TestStallMetrics_StreakGaugeTracksTheLiveStreak(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewStallMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordStallStreak("parked_sensing", "", "ports_unwired", 1)
	c.RecordStallStreak("parked_sensing", "", "ports_unwired", 2)
	c.RecordStallStreak("parked_sensing", "", "expansion_error", 3)
	c.RecordStallStreak("parked_sensing", "", "ports_unwired", 0) // cleared

	const name = "spacetraders_daemon_coordinator_stall_streak"
	cases := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"cleared sensing block drains to 0", map[string]string{"coordinator": "parked_sensing", "scope": "", "reason": "ports_unwired"}, 0},
		{"live expansion block holds its streak", map[string]string{"coordinator": "parked_sensing", "scope": "", "reason": "expansion_error"}, 3},
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

// TestStallMetrics_EscalationCounterAccumulatesPerKey pins that escalations COUNT (a second
// episode on the same key increments the same series) and stay separated per coordinator/scope.
func TestStallMetrics_EscalationCounterAccumulatesPerKey(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewStallMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordStallEscalation("fleet_autosizer", "heavy", "treasury_floor")
	c.RecordStallEscalation("fleet_autosizer", "heavy", "treasury_floor")
	c.RecordStallEscalation("fleet_autosizer", "light", "api_util")

	const name = "spacetraders_daemon_coordinator_stall_escalations_total"
	cases := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"two heavy episodes", map[string]string{"coordinator": "fleet_autosizer", "scope": "heavy", "reason": "treasury_floor"}, 2},
		{"one light episode", map[string]string{"coordinator": "fleet_autosizer", "scope": "light", "reason": "api_util"}, 1},
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

// TestStallMetrics_NilSafe mirrors the sibling collectors' guarantee: a recording miss on a
// typed-nil receiver or an uninitialized collector degrades to a no-op, never a SIGSEGV that
// would take down the coordinator tick doing the reporting (RULINGS #4 — observation only).
func TestStallMetrics_NilSafe(t *testing.T) {
	var nilC *StallMetricsCollector
	nilC.RecordStallStreak("c", "s", "r", 1)
	nilC.RecordStallEscalation("c", "s", "r")

	empty := &StallMetricsCollector{}
	empty.RecordStallStreak("c", "s", "r", 1)
	empty.RecordStallEscalation("c", "s", "r")
}

// TestStallMetricsPort_ResolvesTheGlobalLazily proves the port the coordinators hold reaches the
// collector installed AFTER handler wiring. Handler wiring runs before NewDaemonServer builds
// the collectors, so a port that captured a reference at construction would be permanently nil —
// the alarm would ship wired to nothing, which is the failure this whole lane exists to end.
func TestStallMetricsPort_ResolvesTheGlobalLazily(t *testing.T) {
	prevRegistry := Registry
	prevGlobal := globalStallCollector
	t.Cleanup(func() {
		Registry = prevRegistry
		globalStallCollector = prevGlobal
	})
	Registry = prometheus.NewRegistry()
	globalStallCollector = nil

	port := NewStallMetricsPort() // built BEFORE the collector exists, exactly as the daemon does
	port.RecordStallEscalation("fleet_autosizer", "heavy", "treasury_floor")

	c := NewStallMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}
	SetGlobalStallCollector(c)

	port.RecordStallStreak("fleet_autosizer", "heavy", "treasury_floor", 3)
	port.RecordStallEscalation("fleet_autosizer", "heavy", "treasury_floor")

	labels := map[string]string{"coordinator": "fleet_autosizer", "scope": "heavy", "reason": "treasury_floor"}
	if got, ok := gatherCounter(t, Registry, "spacetraders_daemon_coordinator_stall_escalations_total", labels); !ok || got != 1 {
		t.Errorf("escalation counter = %v (found=%v), want 1: the port must resolve the global lazily and drop the pre-install call", got, ok)
	}
	if got, ok := gatherGauge(t, Registry, "spacetraders_daemon_coordinator_stall_streak", labels); !ok || got != 3 {
		t.Errorf("streak gauge = %v (found=%v), want 3", got, ok)
	}
}

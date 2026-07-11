package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestSitingMetrics_RegisterAndExport proves all three siting counter families REGISTER on the
// daemon's registry AND actually appear by name once observed (sp-vdld). A registered vec
// exports nothing until a label combination is incremented — the trap where a family is
// "registered" yet never shows on /metrics — so the export path is proven with real
// observations.
func TestSitingMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewSitingMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordLaunch("FABRICS", "X1-AA")
	c.RecordRetire("CLOTHING", "X1-BB")
	c.RecordScoutDemand("X1-CC")

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	for _, name := range []string{
		"spacetraders_daemon_siting_launches_total",
		"spacetraders_daemon_siting_retires_total",
		"spacetraders_daemon_siting_scout_demands_total",
	} {
		if !got[name] {
			t.Errorf("metric %s registered but not exported on the registry", name)
		}
	}
}

// TestSitingMetrics_LabelsAndValues pins the label sets and that each counter accumulates per
// decision on its own series ({good,system} for launches/retires, {system} for scout-demands).
func TestSitingMetrics_LabelsAndValues(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewSitingMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Two launches of the same chain accumulate on the same {good,system} series; a different
	// system is its own series.
	c.RecordLaunch("FABRICS", "X1-AA")
	c.RecordLaunch("FABRICS", "X1-AA")
	c.RecordLaunch("FABRICS", "X1-BB")
	c.RecordRetire("CLOTHING", "X1-AA")
	// Scout-demands key on system only.
	c.RecordScoutDemand("X1-CC")
	c.RecordScoutDemand("X1-CC")
	c.RecordScoutDemand("X1-CC")

	if got, ok := gatherCounter(t, Registry, "spacetraders_daemon_siting_launches_total", map[string]string{"good": "FABRICS", "system": "X1-AA"}); !ok || got != 2 {
		t.Errorf("FABRICS@X1-AA launches = %v (ok=%v), want 2", got, ok)
	}
	if got, ok := gatherCounter(t, Registry, "spacetraders_daemon_siting_launches_total", map[string]string{"good": "FABRICS", "system": "X1-BB"}); !ok || got != 1 {
		t.Errorf("FABRICS@X1-BB launches = %v (ok=%v), want 1", got, ok)
	}
	if got, ok := gatherCounter(t, Registry, "spacetraders_daemon_siting_retires_total", map[string]string{"good": "CLOTHING", "system": "X1-AA"}); !ok || got != 1 {
		t.Errorf("CLOTHING@X1-AA retires = %v (ok=%v), want 1", got, ok)
	}
	if got, ok := gatherCounter(t, Registry, "spacetraders_daemon_siting_scout_demands_total", map[string]string{"system": "X1-CC"}); !ok || got != 3 {
		t.Errorf("X1-CC scout-demands = %v (ok=%v), want 3", got, ok)
	}
}

// TestSitingMetrics_NilSafe mirrors the sibling collectors' guarantee: a recording miss on a
// typed-nil receiver or an uninitialized collector must degrade to a no-op, never a SIGSEGV
// that would take down an ACT/EMIT decision path (RULINGS #4 — observation only).
func TestSitingMetrics_NilSafe(t *testing.T) {
	var nilC *SitingMetricsCollector
	nilC.RecordLaunch("FABRICS", "X1-AA")
	nilC.RecordRetire("FABRICS", "X1-AA")
	nilC.RecordScoutDemand("X1-AA")

	empty := &SitingMetricsCollector{}
	empty.RecordLaunch("FABRICS", "X1-AA")
	empty.RecordRetire("FABRICS", "X1-AA")
	empty.RecordScoutDemand("X1-AA")
}

// TestSitingMetrics_GlobalFuncsNilSafe proves the package-level Record funcs are no-ops when the
// global collector is unset (metrics disabled) — the ACT/EMIT paths call these directly.
func TestSitingMetrics_GlobalFuncsNilSafe(t *testing.T) {
	prev := globalSitingCollector
	t.Cleanup(func() { globalSitingCollector = prev })
	globalSitingCollector = nil

	RecordSitingLaunch("FABRICS", "X1-AA")
	RecordSitingRetire("FABRICS", "X1-AA")
	RecordSitingScoutDemand("X1-AA")
}

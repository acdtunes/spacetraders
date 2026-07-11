package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestChainPnLMetrics_RegisterAndExport proves both chain-P&L families REGISTER on the
// daemon's registry AND actually appear by name once observed (sp-rh2z). A registered vec
// exports nothing until a label combination is set/incremented — the trap where a family is
// "registered" yet never shows on /metrics — so the export path is proven with real
// observations.
func TestChainPnLMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewChainPnLMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordRealizedPerHour("FABRICS", -42666.67)
	c.RecordKill("FABRICS")

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	for _, name := range []string{
		"spacetraders_daemon_chain_pnl_realized_per_hour",
		"spacetraders_daemon_chain_pnl_kills_total",
	} {
		if !got[name] {
			t.Errorf("metric %s registered but not exported on the registry", name)
		}
	}
}

// TestChainPnLMetrics_LabelsAndValues pins the {good} label set, that the gauge is SET (last
// value wins, not accumulated), and that the counter accumulates per episode.
func TestChainPnLMetrics_LabelsAndValues(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewChainPnLMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Gauge: last write wins (a chain's P&L moves; the gauge shows the latest).
	c.RecordRealizedPerHour("CLOTHING", 50000)
	c.RecordRealizedPerHour("CLOTHING", 100000)
	// Counter: two kill episodes on the same chain accumulate on the same series.
	c.RecordKill("FABRICS")
	c.RecordKill("FABRICS")
	c.RecordKill("PLASTICS")

	if got, ok := gatherGauge(t, Registry, "spacetraders_daemon_chain_pnl_realized_per_hour", map[string]string{"good": "CLOTHING"}); !ok || got != 100000 {
		t.Errorf("CLOTHING realized/hr gauge = %v (ok=%v), want 100000 (last write wins)", got, ok)
	}
	if got, ok := gatherCounter(t, Registry, "spacetraders_daemon_chain_pnl_kills_total", map[string]string{"good": "FABRICS"}); !ok || got != 2 {
		t.Errorf("FABRICS kills = %v (ok=%v), want 2", got, ok)
	}
	if got, ok := gatherCounter(t, Registry, "spacetraders_daemon_chain_pnl_kills_total", map[string]string{"good": "PLASTICS"}); !ok || got != 1 {
		t.Errorf("PLASTICS kills = %v (ok=%v), want 1", got, ok)
	}
}

// TestChainPnLMetrics_NilSafe mirrors the sibling collectors' guarantee: a recording miss on a
// typed-nil receiver or an uninitialized collector must degrade to a no-op, never a SIGSEGV
// that would take down the kill-check path (RULINGS #4 — observation only).
func TestChainPnLMetrics_NilSafe(t *testing.T) {
	var nilC *ChainPnLMetricsCollector
	nilC.RecordRealizedPerHour("FABRICS", -1000)
	nilC.RecordKill("FABRICS")

	empty := &ChainPnLMetricsCollector{}
	empty.RecordRealizedPerHour("FABRICS", -1000)
	empty.RecordKill("FABRICS")
}

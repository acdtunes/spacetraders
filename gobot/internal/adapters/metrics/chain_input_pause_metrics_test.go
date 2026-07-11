package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestChainInputPauseMetrics_RegisterAndExport proves the input-pause family REGISTERS on the
// daemon's registry AND actually appears by name once observed (sp-r5a6). A registered vec
// exports nothing until a label combination is incremented — the trap where a family is
// "registered" yet never shows on /metrics — so the export path is proven with a real
// observation.
func TestChainInputPauseMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewChainInputPauseMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordPause("ADVANCED_CIRCUITRY")

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	if !got["spacetraders_daemon_chain_input_pause_total"] {
		t.Errorf("metric spacetraders_daemon_chain_input_pause_total registered but not exported on the registry")
	}
}

// TestChainInputPauseMetrics_LabelsAndValues pins the {good} label set and that the counter
// accumulates per episode across goods.
func TestChainInputPauseMetrics_LabelsAndValues(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewChainInputPauseMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Two pause episodes on the same chain accumulate on the same series; a second good is
	// its own series.
	c.RecordPause("ADVANCED_CIRCUITRY")
	c.RecordPause("ADVANCED_CIRCUITRY")
	c.RecordPause("SHIP_PARTS")

	if got, ok := gatherCounter(t, Registry, "spacetraders_daemon_chain_input_pause_total", map[string]string{"good": "ADVANCED_CIRCUITRY"}); !ok || got != 2 {
		t.Errorf("ADVANCED_CIRCUITRY input pauses = %v (ok=%v), want 2", got, ok)
	}
	if got, ok := gatherCounter(t, Registry, "spacetraders_daemon_chain_input_pause_total", map[string]string{"good": "SHIP_PARTS"}); !ok || got != 1 {
		t.Errorf("SHIP_PARTS input pauses = %v (ok=%v), want 1", got, ok)
	}
}

// TestChainInputPauseMetrics_NilSafe mirrors the sibling collectors' guarantee: a recording
// miss on a typed-nil receiver or an uninitialized collector must degrade to a no-op, never a
// SIGSEGV that would take down the pause-check path (RULINGS #4 — observation only).
func TestChainInputPauseMetrics_NilSafe(t *testing.T) {
	var nilC *ChainInputPauseMetricsCollector
	nilC.RecordPause("ADVANCED_CIRCUITRY")

	empty := &ChainInputPauseMetricsCollector{}
	empty.RecordPause("ADVANCED_CIRCUITRY")
}

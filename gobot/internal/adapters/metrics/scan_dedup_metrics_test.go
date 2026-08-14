package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestScanDedupMetrics_RegisterAndExport proves the counter registers and
// exports by name once recorded, not just "registered" with nothing shown.
func TestScanDedupMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewScanDedupMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordSaved(1, "TREAT-1", "stale_ask")

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	if !got["spacetraders_daemon_scan_dedup_calls_saved_total"] {
		t.Error("spacetraders_daemon_scan_dedup_calls_saved_total registered but not exported on the registry")
	}
}

// TestScanDedupMetrics_RecordSaved_NilSafe: an uninstalled collector must
// never panic the caller.
func TestScanDedupMetrics_RecordSaved_NilSafe(t *testing.T) {
	var c *ScanDedupMetricsCollector
	c.RecordSaved(1, "TREAT-1", "stale_ask") // must not panic

	SetGlobalScanDedupCollector(nil)
	RecordScanDedupSaved(1, "TREAT-1", "stale_ask") // must not panic
}

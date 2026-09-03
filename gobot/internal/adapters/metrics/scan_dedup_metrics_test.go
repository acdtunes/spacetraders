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
	c.RecordArrivalDeferred(1, "sell")       // must not panic

	SetGlobalScanDedupCollector(nil)
	RecordScanDedupSaved(1, "TREAT-1", "stale_ask") // must not panic
	RecordArrivalScanDeferred(1, "sell")            // must not panic
}

// The deferral counter is the queryable half of the scan_deferred_to_guard line: it must
// export under its own name and carry the side label, so buy-leg and sell-leg deferrals
// can be split without grepping logs.
func TestScanDedupMetrics_ArrivalDeferred_ExportsBySide(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewScanDedupMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}
	c.RecordArrivalDeferred(1, "sell")
	c.RecordArrivalDeferred(1, "buy")
	c.RecordArrivalDeferred(1, "sell")

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	bySide := map[string]float64{}
	for _, f := range families {
		if f.GetName() != "spacetraders_daemon_arrival_scan_deferred_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "side" {
					bySide[l.GetValue()] = m.GetCounter().GetValue()
				}
			}
		}
	}
	if bySide["sell"] != 2 || bySide["buy"] != 1 {
		t.Fatalf("arrival_scan_deferred_total by side = %v, want sell=2 buy=1", bySide)
	}
}

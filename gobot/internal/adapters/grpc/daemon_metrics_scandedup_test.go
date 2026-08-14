package grpc

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	commonMediator "github.com/andrescamacho/spacetraders-go/internal/application/mediator"
)

// TestRegisterMetricsCollectors_WiresGlobalScanDedupCollector proves the scan-dedup
// collector is reachable from the REAL daemon startup path, not merely constructed in
// a test file: RecordScanDedupSaved silently no-ops on a nil global collector (pure
// observation must never touch a purchase decision), so an unwired global is invisible
// — no panic, no log, just a permanently-empty counter on a live daemon.
func TestRegisterMetricsCollectors_WiresGlobalScanDedupCollector(t *testing.T) {
	prevGlobal := metrics.GetGlobalScanDedupCollector()
	t.Cleanup(func() { metrics.SetGlobalScanDedupCollector(prevGlobal) })
	metrics.SetGlobalScanDedupCollector(nil)

	s := &DaemonServer{mediator: commonMediator.NewMediator()}
	getContainers := func() map[string]metrics.ContainerInfo { return nil }

	if err := s.registerMetricsCollectors(getContainers); err != nil {
		t.Fatalf("registerMetricsCollectors() error: %v", err)
	}

	if metrics.GetGlobalScanDedupCollector() == nil {
		t.Fatal("registerMetricsCollectors must install the process-wide scan-dedup collector; RecordScanDedupSaved silently no-ops while it is nil")
	}

	// Record through the package-level convenience function every call site uses, then
	// read it back off the registry the way a Prometheus scrape would.
	metrics.RecordScanDedupSaved(1, "TEST-1", "buy_ceiling")

	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	var found bool
	for _, f := range families {
		if f.GetName() != "spacetraders_daemon_scan_dedup_calls_saved_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			if m.GetCounter().GetValue() > 0 {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("scan_dedup_calls_saved_total must be registered and recording through the real startup wiring, not just in isolation")
	}
}

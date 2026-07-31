package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// withFreshRegistry gives one test its own registry and puts the package-global back
// the way it found it.
//
// The restore is load-bearing, not tidiness. Registry is process-global, and
// TestRelocatorMetricsShould_BeNilSafeEverywhereMetricsAreDisabled calls Register() on a
// NIL *RelocatorMetricsCollector — which dereferences c.ticksTotal, and so panics, on any
// ordering where Registry is non-nil. It survives today only because
// daemon_component_metrics_test.go nils Registry on its way out. Leaving a registry
// installed here would break that test purely by filename ordering.
func withFreshRegistry(t *testing.T) {
	t.Helper()
	prev := Registry
	InitRegistry()
	t.Cleanup(func() { Registry = prev })
}

// The stranded-jump-container counter must actually reach Prometheus, per outcome
// (sp-rqhzh). The handler-side tests drive this through a fake collector, so they
// prove the call path but say nothing about the real series; without this test,
// deleting the Inc() leaves every one of them green and the panel permanently flat
// — which is the exact failure mode the counter exists to rule out.
func TestNavigationCollectorRecordsStrandedJumpContainersByOutcome(t *testing.T) {
	withFreshRegistry(t)

	c := NewNavigationMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	c.RecordStrandedJumpContainer(5, "cleared")
	c.RecordStrandedJumpContainer(5, "cleared")
	c.RecordStrandedJumpContainer(5, "clear_failed")

	if got := testutil.ToFloat64(c.strandedJumpContainers.WithLabelValues("5", "cleared")); got != 2 {
		t.Fatalf("stranded_jump_containers_total{player_id=5,outcome=cleared} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(c.strandedJumpContainers.WithLabelValues("5", "clear_failed")); got != 1 {
		t.Fatalf("stranded_jump_containers_total{player_id=5,outcome=clear_failed} = %v, want 1", got)
	}

	// The two outcomes must be distinct series: collapsing them would make a reaper
	// that fires and fails indistinguishable from one that succeeds.
	if n := testutil.CollectAndCount(c.strandedJumpContainers); n != 2 {
		t.Fatalf("stranded_jump_containers_total series count = %d, want 2", n)
	}
}

// Register must include the counter. A collector that is built but never registered
// emits nothing on a scrape while every unit test still passes.
func TestNavigationCollectorRegistersStrandedJumpCounter(t *testing.T) {
	withFreshRegistry(t)

	c := NewNavigationMetricsCollector()
	if err := c.Register(); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	c.RecordStrandedJumpContainer(5, "cleared")

	const name = "spacetraders_daemon_stranded_jump_containers_total"
	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return
		}
	}
	t.Fatalf("%s is absent from the registry - it will never be scraped", name)
}

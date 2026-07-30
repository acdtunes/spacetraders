package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// opportunity_relocator_metrics_test.go — the relocator's collector (sp-j1i49).
//
// Two things are pinned: the series NAMES (a rename silently breaks every dashboard and alert built on
// them, and nothing else in the build would notice), and the fact that a skip records its COUNT rather
// than a bare increment — the tick has already aggregated the reason across every hull it considered,
// so incrementing by one would under-report every multi-hull tick.

// relocatorMetricValue reads one labelled counter out of a collector, so a test asserts the exported
// series rather than the struct.
func relocatorMetricValue(t *testing.T, c prometheus.Collector, wantName string, labels map[string]string) (float64, bool) {
	t.Helper()
	ch := make(chan prometheus.Metric, 32)
	c.Collect(ch)
	close(ch)
	for metric := range ch {
		var pb dto.Metric
		if err := metric.Write(&pb); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		if !strings.Contains(metric.Desc().String(), wantName) {
			continue
		}
		matched := true
		for _, pair := range pb.GetLabel() {
			if want, ok := labels[pair.GetName()]; ok && want != pair.GetValue() {
				matched = false
			}
		}
		if matched {
			return pb.GetCounter().GetValue(), true
		}
	}
	return 0, false
}

// The three series must carry the names dashboards and alerts are written against. A rename is
// invisible to the compiler and breaks every query silently.
func TestRelocatorMetricsShould_ExportTheSeriesNamesDashboardsAreWrittenAgainst(t *testing.T) {
	c := NewRelocatorMetricsCollector()
	c.RecordTick(5, "BLOCKED")
	c.RecordDecision(5, "relocated")
	c.RecordSkip(5, "claimed_at_actuation", 1)

	for _, want := range []string{"relocator_ticks_total", "relocator_decisions_total", "relocator_skips_total"} {
		if _, ok := relocatorMetricValue(t, collectorFor(t, c, want), want, map[string]string{"player_id": "5"}); !ok {
			t.Fatalf("no series named %q was exported; a rename here silently breaks every dashboard and alert built on it", want)
		}
	}
}

// A SKIP RECORDS ITS COUNT, not a bare increment. The tick aggregates each reason across every hull it
// considered, so a per-call increment would under-report every tick that excluded more than one hull —
// exactly the ticks worth looking at.
func TestRelocatorMetricsShould_AddTheWholeSkipCountRatherThanIncrementByOne(t *testing.T) {
	c := NewRelocatorMetricsCollector()

	c.RecordSkip(1, "mid_tour", 7)

	got, ok := relocatorMetricValue(t, c.skipsTotal, "relocator_skips_total", map[string]string{"player_id": "1", "reason": "mid_tour"})
	if !ok {
		t.Fatal("no skip series was exported")
	}
	if got != 7 {
		t.Fatalf("recorded %v for a tick that excluded 7 hulls, want 7; incrementing by one under-reports every multi-hull tick", got)
	}
}

// A zero or negative count must record NOTHING, so a reason the tick never saw does not materialize a
// series at 0 and look like a real, quiet signal.
func TestRelocatorMetricsShould_RecordNothingForAnEmptySkipCount(t *testing.T) {
	c := NewRelocatorMetricsCollector()

	c.RecordSkip(1, "never_happened", 0)

	if _, ok := relocatorMetricValue(t, c.skipsTotal, "relocator_skips_total", map[string]string{"reason": "never_happened"}); ok {
		t.Fatal("a zero count materialized a series; an absent reason must stay absent rather than read as a real, quiet signal")
	}
}

// EVERY method is nil-safe, on the collector AND through the port with no global installed. Metrics are
// disabled in plenty of environments, and a metrics miss must never touch the relocation path
// (RULINGS #4).
func TestRelocatorMetricsShould_BeNilSafeEverywhereMetricsAreDisabled(t *testing.T) {
	var nilCollector *RelocatorMetricsCollector
	nilCollector.RecordTick(1, "IDLE")
	nilCollector.RecordDecision(1, "relocated")
	nilCollector.RecordSkip(1, "mid_tour", 3)
	if err := nilCollector.Register(); err != nil && Registry != nil {
		t.Fatalf("registering a nil collector errored: %v", err)
	}

	// The port with NO global installed — the state every daemon is in before the collector is built,
	// which is exactly when coordinator wiring runs.
	SetGlobalRelocatorCollector(nil)
	port := NewRelocatorMetricsPort()
	port.RecordTick(1, "BLOCKED")
	port.RecordDecision(1, "resumed")
	port.RecordSkip(1, "claimed_at_actuation", 1)
}

// collectorFor picks the right CounterVec for a series name, so the name test reads one series at a time.
func collectorFor(t *testing.T, c *RelocatorMetricsCollector, name string) prometheus.Collector {
	t.Helper()
	switch name {
	case "relocator_ticks_total":
		return c.ticksTotal
	case "relocator_decisions_total":
		return c.decisionsTotal
	case "relocator_skips_total":
		return c.skipsTotal
	}
	t.Fatalf("unknown series %q", name)
	return nil
}

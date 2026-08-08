package metrics

// The heavy-price-premium series, on the fleet-GROWTH collector.
//
// These tests were the fleet autosizer's; sp-5pclx deleted that collector with the coordinator and
// they were MOVED here rather than dropped. Growth's ObserveHeavyPricePremium carries the identical
// contract (skip an unknown basis rather than divide, clamp a negative premium to 0) and, before
// this move, had NO test of its own — deleting them would have retired the only coverage of a
// divide that can observe +Inf into a summary and poison every quantile it feeds.

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func premiumSummary(t *testing.T, c *FleetGrowthMetricsCollector) *dto.Summary {
	t.Helper()
	ch := make(chan prometheus.Metric, 8)
	c.heavyPricePremium.Collect(ch)
	close(ch)
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		if pb.Summary != nil {
			return pb.Summary
		}
	}
	return nil
}

// An UNKNOWN cheapest basis (0, or negative) is skipped, not divided by. Dividing would observe
// +Inf into the summary and poison every quantile it feeds — and an unknown basis is genuinely not
// a 0% premium, so recording one would also be a lie about what we paid.
func TestObserveHeavyPricePremium_SkipsUnknownBasis(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()

	c.ObserveHeavyPricePremium("1", 1_565_500, 0)
	c.ObserveHeavyPricePremium("1", 1_565_500, -5)

	if s := premiumSummary(t, c); s != nil && s.GetSampleCount() != 0 {
		t.Fatalf("observed %d samples against an unknown basis, want 0 (a divide would record +Inf)", s.GetSampleCount())
	}
}

// A real basis is recorded as a percentage premium over the cheapest KNOWN ask.
func TestObserveHeavyPricePremium_RecordsThePremium(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()

	// Paid 1,650,000 against a cheapest known 1,500,000 ⇒ +10%.
	c.ObserveHeavyPricePremium("1", 1_650_000, 1_500_000)

	s := premiumSummary(t, c)
	if s == nil || s.GetSampleCount() != 1 {
		t.Fatalf("expected exactly one observation, got %+v", s)
	}
	if got := s.GetSampleSum(); got < 9.99 || got > 10.01 {
		t.Fatalf("premium recorded as %.4f%%, want 10%%", got)
	}
}

// Paying the cheapest known ask is a 0% premium — the value that says presence-lag cost us nothing.
func TestObserveHeavyPricePremium_CheapestPaidIsZeroPercent(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()
	c.ObserveHeavyPricePremium("1", 1_500_000, 1_500_000)

	s := premiumSummary(t, c)
	if s == nil || s.GetSampleCount() != 1 {
		t.Fatalf("expected one observation, got %+v", s)
	}
	if got := s.GetSampleSum(); got != 0 {
		t.Fatalf("premium %.4f%%, want exactly 0%% when we paid the cheapest known ask", got)
	}
}

// Nil-safe and best-effort, like every sibling: a recording miss must never touch a buy decision
// (RULINGS #4 — metrics are pure observation).
func TestHeavyMetrics_NilCollectorIsSafe(t *testing.T) {
	var c *FleetGrowthMetricsCollector
	c.RecordHeavyReserve("1", 1_565_500, 1_565_500, 2, 5)
	c.ObserveHeavyPricePremium("1", 1_565_500, 1_500_000)
}

package scouting

import (
	"testing"
	"time"
)

func TestRequiredHulls(t *testing.T) {
	min := time.Minute
	cases := []struct {
		name      string
		markets   int
		avgHop    time.Duration
		freshness time.Duration
		want      int
	}{
		// 10 markets × 3min = 30min circuit, well under a 75min target → one probe.
		{"single probe suffices", 10, 3 * min, 75 * min, 1},
		// XT71/UQ87 class: 22 markets × 3min = 66min circuit against a 60min target →
		// 1.1 ratio → 2 probes required. A single-probe post here is undersized.
		{"market-rich needs two", 22, 3 * min, 60 * min, 2},
		// Exact boundary: 20 × 3min = 60min == 60min target → ratio 1.0 → still one probe.
		{"exact boundary rounds to one", 20, 3 * min, 60 * min, 1},
		// 40 markets × 3min = 120min against a 30min target → exactly 4 probes.
		{"names the exact requirement", 40, 3 * min, 30 * min, 4},
		// Degenerate inputs are "cannot assess" (0), never a spurious 1.
		{"no markets", 0, 3 * min, 60 * min, 0},
		{"zero freshness", 22, 3 * min, 0, 0},
		{"zero avg hop", 22, 0, 60 * min, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequiredHulls(tc.markets, tc.avgHop, tc.freshness); got != tc.want {
				t.Errorf("RequiredHulls(%d, %s, %s) = %d, want %d", tc.markets, tc.avgHop, tc.freshness, got, tc.want)
			}
		})
	}
}

func TestIsUndersized(t *testing.T) {
	min := time.Minute
	cases := []struct {
		name      string
		markets   int
		hulls     int
		avgHop    time.Duration
		freshness time.Duration
		want      bool
	}{
		// A single probe over 22 markets against a 60min target cannot keep up (needs 2).
		{"single probe on rich system is undersized", 22, 1, 3 * min, 60 * min, true},
		// The same system correctly sized with 2 probes is NOT undersized.
		{"adequately sized is silent", 22, 2, 3 * min, 60 * min, false},
		// A small system a single probe can cover is not undersized.
		{"small system fine on one probe", 10, 1, 3 * min, 75 * min, false},
		// Zero markets is never undersized (nothing to assess).
		{"no markets never undersized", 0, 1, 3 * min, 60 * min, false},
		// Exact-boundary circuit == target is not undersized.
		{"exact boundary not undersized", 20, 1, 3 * min, 60 * min, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUndersized(tc.markets, tc.hulls, tc.avgHop, tc.freshness); got != tc.want {
				t.Errorf("IsUndersized(%d, %d, %s, %s) = %v, want %v", tc.markets, tc.hulls, tc.avgHop, tc.freshness, got, tc.want)
			}
		})
	}
}

func TestMedianScanIntervalSeconds(t *testing.T) {
	base := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	at := func(offsetSeconds ...int) []time.Time {
		out := make([]time.Time, len(offsetSeconds))
		for i, s := range offsetSeconds {
			out[i] = base.Add(time.Duration(s) * time.Second)
		}
		return out
	}
	cases := []struct {
		name        string
		times       []time.Time
		wantSeconds float64
		wantSamples int
	}{
		// Evenly spaced scan events 120s apart → the market-to-market cycle IS 120s,
		// over n-1 = 4 consecutive-interval samples.
		{"evenly spaced yields the interval", at(0, 120, 240, 360, 480), 120, 4},
		// Robust to an outlier: deltas {100,140,120,2000} → median of the sorted
		// {100,120,140,2000} is the mean of the two middle values (120,140) = 130.
		// A single stalled leg (2000s) does not drag the estimate — that is the point
		// of a MEDIAN over a mean.
		{"median resists an outlier", at(0, 100, 240, 360, 2360), 130, 4},
		// Unsorted input is sorted internally before differencing (defensive: the
		// telemetry query may not guarantee order).
		{"unsorted input is sorted first", at(240, 0, 120), 120, 2},
		// Fewer than two events cannot produce an interval — "cannot measure".
		{"single event cannot measure", at(500), 0, 0},
		{"no events cannot measure", nil, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSeconds, gotSamples := MedianScanIntervalSeconds(tc.times)
			if gotSeconds != tc.wantSeconds || gotSamples != tc.wantSamples {
				t.Errorf("MedianScanIntervalSeconds(%v) = (%v, %d), want (%v, %d)",
					tc.times, gotSeconds, gotSamples, tc.wantSeconds, tc.wantSamples)
			}
		})
	}
}

func TestFreshnessRequiredHulls(t *testing.T) {
	sec := time.Second
	cases := []struct {
		name      string
		markets   int
		cycle     time.Duration
		sla       time.Duration
		actualAge time.Duration
		want      int
	}{
		// Static model when freshness is being held: 90 markets × 120s = 10800s
		// circuit / 3600s SLA = exactly 3 probes; actual age under SLA adds nothing.
		{"static model when healthy", 90, 120 * sec, 3600 * sec, 1000 * sec, 3},
		// ceil>1 exercised: 70 × 120 = 8400 / 3600 = 2.33 → 3 probes.
		{"ceil rounds up", 70, 120 * sec, 3600 * sec, 100 * sec, 3},
		// Closed-loop ground truth: a 26-market system the static model sizes at 1
		// probe (26×120=3120 < 3600) but whose OLDEST market is 8h stale against a 1h
		// SLA is breaching 8× — empirical age overrides the model and raises demand to
		// ceil(1 × 28800/3600) = 8. This is the VB74 measured pathology.
		{"breach raises beyond the static model", 26, 120 * sec, 3600 * sec, 28800 * sec, 8},
		// Exactly at the SLA is not yet breaching — no raise.
		{"exact SLA does not raise", 26, 120 * sec, 3600 * sec, 3600 * sec, 1},
		// Degenerate (no cycle telemetry AND no seed) is "cannot assess" → 0, never a
		// spurious probe. The coordinator guarantees a seed so this is the guard, not
		// the normal path.
		{"cannot assess without a cycle", 26, 0, 3600 * sec, 28800 * sec, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FreshnessRequiredHulls(tc.markets, tc.cycle, tc.sla, tc.actualAge); got != tc.want {
				t.Errorf("FreshnessRequiredHulls(%d, %s, %s, %s) = %d, want %d",
					tc.markets, tc.cycle, tc.sla, tc.actualAge, got, tc.want)
			}
		})
	}
}

func TestCircuitDuration(t *testing.T) {
	min := time.Minute
	// 22 markets on 1 probe at 3min/hop = 66min.
	if got := CircuitDuration(22, 1, 3*min); got != 66*min {
		t.Errorf("CircuitDuration(22,1,3m) = %s, want 66m", got)
	}
	// Splitting across 2 probes halves the circuit to 33min.
	if got := CircuitDuration(22, 2, 3*min); got != 33*min {
		t.Errorf("CircuitDuration(22,2,3m) = %s, want 33m", got)
	}
	// hulls < 1 is treated as one probe (a post always has its primary slot).
	if got := CircuitDuration(10, 0, 3*min); got != 30*min {
		t.Errorf("CircuitDuration(10,0,3m) = %s, want 30m", got)
	}
	if got := CircuitDuration(0, 1, 3*min); got != 0 {
		t.Errorf("CircuitDuration(0,1,3m) = %s, want 0", got)
	}
}

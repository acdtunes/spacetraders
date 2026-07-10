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

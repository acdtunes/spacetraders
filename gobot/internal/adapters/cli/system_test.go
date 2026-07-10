package cli

import (
	"strings"
	"testing"
)

// The `system gates` renderer must emit one aligned `SYS <-> a,b,c` line per
// system, systems sorted and neighbors comma-joined — the sp-7gr2 topology-dump
// shape the captain greps for manual routing.
func TestRenderGateAdjacency_TopologyDumpShape(t *testing.T) {
	got := renderGateAdjacency(map[string][]string{
		"X1-KA42": {"X1-AF2", "X1-GQ92", "X1-PA3"},
		"X1-JP61": {"X1-UQ16"},
	})

	want := "" +
		"X1-JP61 <-> X1-UQ16\n" +
		"X1-KA42 <-> X1-AF2,X1-GQ92,X1-PA3\n"
	if got != want {
		t.Fatalf("rendered adjacency mismatch\n got: %q\nwant: %q", got, want)
	}
}

// A system with no known connections renders explicitly as `(none)` rather than a
// bare, ambiguous `SYS <-> `.
func TestRenderGateAdjacency_NoNeighbors(t *testing.T) {
	got := renderGateAdjacency(map[string][]string{"X1-LONE": {}})
	if !strings.Contains(got, "X1-LONE <-> (none)") {
		t.Fatalf("expected a (none) line for a connectionless system, got %q", got)
	}
}

// Empty input renders nothing (the command prints its own hint instead).
func TestRenderGateAdjacency_Empty(t *testing.T) {
	if got := renderGateAdjacency(map[string][]string{}); got != "" {
		t.Fatalf("expected empty render for empty adjacency, got %q", got)
	}
}

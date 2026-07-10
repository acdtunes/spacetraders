package cli

import (
	"strings"
	"testing"

	domainsystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// edgesTo builds an open (built) edge set from neighbor symbols — the common case
// where nothing is under construction.
func edgesTo(systems ...string) []domainsystem.GateEdge {
	edges := make([]domainsystem.GateEdge, 0, len(systems))
	for _, s := range systems {
		edges = append(edges, domainsystem.GateEdge{ConnectedSystem: s})
	}
	return edges
}

// The `system gates` renderer must emit one aligned `SYS <-> a,b,c` line per
// system, systems sorted and neighbors comma-joined — the sp-7gr2 topology-dump
// shape the captain greps for manual routing.
func TestRenderGateAdjacency_TopologyDumpShape(t *testing.T) {
	got := renderGateAdjacency(map[string][]domainsystem.GateEdge{
		"X1-KA42": edgesTo("X1-AF2", "X1-GQ92", "X1-PA3"),
		"X1-JP61": edgesTo("X1-UQ16"),
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
	got := renderGateAdjacency(map[string][]domainsystem.GateEdge{"X1-LONE": {}})
	if !strings.Contains(got, "X1-LONE <-> (none)") {
		t.Fatalf("expected a (none) line for a connectionless system, got %q", got)
	}
}

// Empty input renders nothing (the command prints its own hint instead).
func TestRenderGateAdjacency_Empty(t *testing.T) {
	if got := renderGateAdjacency(map[string][]domainsystem.GateEdge{}); got != "" {
		t.Fatalf("expected empty render for empty adjacency, got %q", got)
	}
}

// sp-8qhu: an under-construction gate must be marked with a trailing `*` and a
// legend line appended, so the captain reading this chart never routes through an
// unbuilt (unroutable) gate. The incident gate was AF2; PA3 alongside it is open.
func TestRenderGateAdjacency_UnderConstructionAnnotated(t *testing.T) {
	got := renderGateAdjacency(map[string][]domainsystem.GateEdge{
		"X1-KA42": {
			{ConnectedSystem: "X1-AF2", UnderConstruction: true},
			{ConnectedSystem: "X1-PA3"},
		},
	})

	if !strings.Contains(got, "X1-AF2*") {
		t.Fatalf("an under-construction gate must be marked with a trailing *, got %q", got)
	}
	if strings.Contains(got, "X1-PA3*") {
		t.Fatalf("an open gate must NOT be marked, got %q", got)
	}
	if !strings.Contains(got, "under construction") {
		t.Fatalf("a legend line must explain the * marker, got %q", got)
	}
}

// With no under-construction gates present, the legend line must NOT appear — it
// would be noise on a fully-built chart.
func TestRenderGateAdjacency_NoLegendWhenAllBuilt(t *testing.T) {
	got := renderGateAdjacency(map[string][]domainsystem.GateEdge{
		"X1-KA42": edgesTo("X1-PA3", "X1-UQ16"),
	})
	if strings.Contains(got, "*") {
		t.Fatalf("a fully-built chart must carry no * marks or legend, got %q", got)
	}
}

// sp-8qhu deploy-gap: a STALE row (invalidated cache — the migration cleared its
// synced_at) must render "?" + legend, never as an authoritative verdict. This is
// the live scenario the harbormaster caught: KA42→AF2 sat at empty synced_at with
// the pre-tracking OPEN default and the raw chart wrongly printed it routable.
func TestRenderGateAdjacency_StaleAnnotated(t *testing.T) {
	got := renderGateAdjacency(map[string][]domainsystem.GateEdge{
		"X1-KA42": {
			{ConnectedSystem: "X1-AF2", Stale: true},
			{ConnectedSystem: "X1-PA3"},
		},
	})
	if !strings.Contains(got, "X1-AF2?") {
		t.Fatalf("a stale row must be marked with a trailing ?, got %q", got)
	}
	if strings.Contains(got, "X1-AF2*") {
		t.Fatalf("a stale row must NOT read as a verified verdict (*), got %q", got)
	}
	if strings.Contains(got, "X1-PA3?") || strings.Contains(got, "X1-PA3*") {
		t.Fatalf("a fresh open row must carry no marker, got %q", got)
	}
	if !strings.Contains(got, "stale cache") {
		t.Fatalf("a legend must explain the ? marker, got %q", got)
	}
}

// Stale takes precedence over under-construction: a stale row's construction value
// is exactly what we no longer trust, so it must read "?" not "*".
func TestRenderGateAdjacency_StalePrecedesUnderConstruction(t *testing.T) {
	got := renderGateAdjacency(map[string][]domainsystem.GateEdge{
		"X1-KA42": {{ConnectedSystem: "X1-AF2", UnderConstruction: true, Stale: true}},
	})
	if !strings.Contains(got, "X1-AF2?") || strings.Contains(got, "X1-AF2*") {
		t.Fatalf("stale must win over * (its construction value is unverified), got %q", got)
	}
}

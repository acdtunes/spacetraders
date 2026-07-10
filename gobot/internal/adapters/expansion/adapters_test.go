package expansion

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// bfsHops is the scanner's core reachability algorithm; these cases pin its edge
// behavior (multi-source, hop bounding, under-construction skip, virgin edge-targets).
func TestBfsHops_MultiSourceShortestDistance(t *testing.T) {
	adj := map[string][]system.GateEdge{
		"A": {{ConnectedSystem: "B"}, {ConnectedSystem: "C"}},
		"B": {{ConnectedSystem: "D"}},
		"C": {{ConnectedSystem: "D"}}, // D reachable at 2 hops from A
		"Z": {{ConnectedSystem: "D"}}, // and at 1 hop from anchor Z
	}
	hops := bfsHops(adj, map[string]bool{"A": true, "Z": true}, 3)

	require.Equal(t, 0, hops["A"])
	require.Equal(t, 0, hops["Z"])
	require.Equal(t, 1, hops["B"])
	require.Equal(t, 1, hops["D"], "multi-source BFS takes the shortest distance (via Z), not the first found")
}

func TestBfsHops_RespectsMaxHops(t *testing.T) {
	adj := map[string][]system.GateEdge{
		"A": {{ConnectedSystem: "B"}},
		"B": {{ConnectedSystem: "C"}},
		"C": {{ConnectedSystem: "D"}}, // 3 hops from A — beyond the bound
	}
	hops := bfsHops(adj, map[string]bool{"A": true}, 2)

	require.Contains(t, hops, "B")
	require.Contains(t, hops, "C")
	require.NotContains(t, hops, "D", "systems beyond maxHops are not reached")
}

func TestBfsHops_SkipsUnderConstructionEdges(t *testing.T) {
	adj := map[string][]system.GateEdge{
		"A": {{ConnectedSystem: "B", UnderConstruction: true}, {ConnectedSystem: "C"}},
	}
	hops := bfsHops(adj, map[string]bool{"A": true}, 3)

	require.NotContains(t, hops, "B", "an under-construction gate is never traversed (sp-7gr2)")
	require.Equal(t, 1, hops["C"])
}

func TestBfsHops_IncludesVirginEdgeTargets(t *testing.T) {
	// "V" is only an edge TARGET (no adjacency key of its own) — a reachable virgin
	// system. It must still surface with its hop distance so the coordinator can rank it.
	adj := map[string][]system.GateEdge{
		"A": {{ConnectedSystem: "V"}},
	}
	hops := bfsHops(adj, map[string]bool{"A": true}, 3)

	require.Equal(t, 1, hops["V"], "a virgin edge-target is reachable and hop-counted")
}

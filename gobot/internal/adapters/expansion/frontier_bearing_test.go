package expansion

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// bfsBranchRoots annotates every reachable system with its CORRIDOR identity — the hop-1
// ancestor on the shortest BFS path from the anchor set (sp-rjgr). It is the depth slice's
// "bearing": two deep virgins with different roots lie down different branches. These pin its
// contract with in-memory adjacency, mirroring the pure bfsHops cases.

// A hop-1 system is its own corridor root; a deeper system inherits the hop-1 ancestor it was
// first reached through; an anchor has no corridor.
func TestBfsBranchRoots_PropagatesHopOneAncestor(t *testing.T) {
	adj := map[string][]system.GateEdge{
		"A": {{ConnectedSystem: "B"}},
		"B": {{ConnectedSystem: "C"}}, // C is hop-2, reached via B
	}
	roots := bfsBranchRoots(adj, map[string]bool{"A": true}, 3)

	require.Equal(t, "", roots["A"], "an anchor has no corridor")
	require.Equal(t, "B", roots["B"], "a hop-1 system is its own corridor root")
	require.Equal(t, "B", roots["C"], "a deeper system inherits its hop-1 ancestor as the corridor")
}

// Two branches out of the anchor keep DISTINCT roots all the way down — the property the depth
// fan-out relies on to spread pathfinders across corridors.
func TestBfsBranchRoots_DistinctBranchesKeepDistinctRoots(t *testing.T) {
	adj := map[string][]system.GateEdge{
		"A": {{ConnectedSystem: "B"}, {ConnectedSystem: "E"}},
		"B": {{ConnectedSystem: "C"}}, // C via branch B
		"E": {{ConnectedSystem: "F"}}, // F via branch E
	}
	roots := bfsBranchRoots(adj, map[string]bool{"A": true}, 3)

	require.Equal(t, "B", roots["C"])
	require.Equal(t, "E", roots["F"])
	require.NotEqual(t, roots["C"], roots["F"], "deep virgins down different branches carry different corridors")
}

// The corridor follows the SHORTEST path's first hop (multi-source), matching bfsHops.
func TestBfsBranchRoots_FollowsShortestPathFirstHop(t *testing.T) {
	adj := map[string][]system.GateEdge{
		"A": {{ConnectedSystem: "B"}},
		"B": {{ConnectedSystem: "D"}}, // D is hop-2 from A via B
		"Z": {{ConnectedSystem: "D"}}, // but hop-1 from anchor Z → D is its own root
	}
	roots := bfsBranchRoots(adj, map[string]bool{"A": true, "Z": true}, 3)

	require.Equal(t, "D", roots["D"], "reached at hop-1 via Z, D is its own corridor root (shortest path wins)")
}

// An under-construction gate is never traversed, so a system reachable only through one is not
// assigned a corridor (mirrors bfsHops).
func TestBfsBranchRoots_SkipsUnderConstructionEdges(t *testing.T) {
	adj := map[string][]system.GateEdge{
		"A": {{ConnectedSystem: "B", UnderConstruction: true}, {ConnectedSystem: "C"}},
	}
	roots := bfsBranchRoots(adj, map[string]bool{"A": true}, 3)

	_, hasB := roots["B"]
	require.False(t, hasB, "an under-construction gate is never traversed")
	require.Equal(t, "C", roots["C"])
}

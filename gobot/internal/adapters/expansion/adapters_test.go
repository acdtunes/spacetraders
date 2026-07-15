package expansion

import (
	"errors"
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

// growFrontierGraph grows the walkable gate graph one ring per pass (sp-dc50); these cases
// pin its ring-growth contract (chart the covered ring, frugal fetch bound) with in-memory
// adjacency and closure collaborators, mirroring the pure bfsHops cases above.

// The frozen-frontier bug and its fix, at the algorithmic seam: a scouted ring's MARKETS are
// charted by the sweep but its JUMP GATE is not, so its onward edges never enter the persisted
// adjacency and the multi-source BFS dead-ends at hop-1 — the expansion queue empties (sp-dc50).
// growFrontierGraph must fetch+persist the charted ring's gate so the next BFS reaches the
// hop-2 virgin the coordinator can then rank.
func TestGrowFrontierGraph_ChartsCoveredRingToReachNextHop(t *testing.T) {
	// Anchor A gates to hop-1 B (surfaced as an edge-target). B is CHARTED (a probe swept its
	// markets) but its OWN gate edges are NOT yet persisted — the frozen-frontier state.
	adj := map[string][]system.GateEdge{
		"A": {{ConnectedSystem: "B"}},
	}
	anchors := map[string]bool{"A": true}

	// BUG REPRODUCTION: with B's gate uncharted the BFS dead-ends at hop-1 — the hop-2
	// virgin C is unreachable, so the coordinator's queue would be empty.
	require.NotContains(t, bfsHops(adj, anchors, 3), "C",
		"precondition: an uncharted intermediate gate caps the frontier at hop-1")

	// B's live gate (fetch-through) connects onward to the virgin hop-2 system C.
	liveGates := map[string][]system.GateEdge{
		"B": {{ConnectedSystem: "C"}},
	}
	fetched := map[string]int{}
	charted := func(sys string) bool { return sys == "B" } // only B has been swept (has markets)
	fetch := func(sys string) ([]system.GateEdge, error) {
		fetched[sys]++
		if edges, ok := liveGates[sys]; ok {
			return edges, nil
		}
		return nil, errors.New("gate unreadable: no ship present") // a virgin gate 400s
	}

	growFrontierGraph(adj, anchors, 3, charted, fetch)

	// FIX: B's onward gate is now persisted, so the BFS reaches the hop-2 virgin C — exactly
	// the candidate the empty queue was missing.
	require.Contains(t, adj, "B", "B's onward gate edges are now persisted into the adjacency")
	require.Equal(t, 2, bfsHops(adj, anchors, 3)["C"],
		"the hop-2 virgin C is now reachable and hop-counted — the queue can rank it")
	require.Equal(t, 1, fetched["B"], "B's gate is fetched exactly once")
}

// API-frugality: growFrontierGraph fetches ONLY a charted, not-yet-persisted frontier
// system's gate. It never re-fetches a gate already in the graph (served from the map,
// zero API), and never probes an UNCHARTED virgin system (its live gate would 400 "no ship
// present" and trip the sp-ikx1 negative-result backoff for nothing).
func TestGrowFrontierGraph_SkipsPersistedAndUnchartedGates(t *testing.T) {
	// A(anchor) → B(charted, ALREADY has persisted edges) and V(virgin hop-1, UNcharted).
	// B's stored edge reaches C (charted hop-2, NO edges — the sole legitimate fetch target).
	adj := map[string][]system.GateEdge{
		"A": {{ConnectedSystem: "B"}, {ConnectedSystem: "V"}},
		"B": {{ConnectedSystem: "C"}}, // B already persisted → must not be re-fetched
	}
	anchors := map[string]bool{"A": true}
	charted := map[string]bool{"A": true, "B": true, "C": true} // V is uncharted (no markets)
	liveGates := map[string][]system.GateEdge{
		"C": {{ConnectedSystem: "D"}},
		"V": {{ConnectedSystem: "W"}}, // present in topology, but V must NEVER be fetched
	}
	fetched := map[string]int{}
	fetch := func(sys string) ([]system.GateEdge, error) {
		fetched[sys]++
		if edges, ok := liveGates[sys]; ok {
			return edges, nil
		}
		return nil, errors.New("gate unreadable")
	}

	growFrontierGraph(adj, anchors, 3, func(s string) bool { return charted[s] }, fetch)

	require.Equal(t, 1, fetched["C"], "only the charted, unpersisted, in-bound C is fetched")
	require.Zero(t, fetched["B"], "an already-persisted gate is never re-fetched (served from the graph)")
	require.Zero(t, fetched["V"], "an uncharted virgin gate is never probed (would 400 and trip the backoff)")
}

// The API bound is also hop-bounded: a charted, unpersisted system sitting AT the hop
// bound is not charted onward, since its neighbors fall beyond maxHops and the bounded BFS
// would discard them — fetching its gate would be a wasted API call.
func TestGrowFrontierGraph_DoesNotChartAtHopBound(t *testing.T) {
	adj := map[string][]system.GateEdge{"A": {{ConnectedSystem: "B"}}}
	anchors := map[string]bool{"A": true}
	fetched := map[string]int{}
	fetch := func(sys string) ([]system.GateEdge, error) {
		fetched[sys]++
		return []system.GateEdge{{ConnectedSystem: "C"}}, nil
	}

	// B sits at hop-1 == maxHops: charting it would surface hop-2 the BFS discards.
	growFrontierGraph(adj, anchors, 1, func(string) bool { return true }, fetch)

	require.Zero(t, fetched["B"], "a charted system at the hop bound is not charted onward")
}

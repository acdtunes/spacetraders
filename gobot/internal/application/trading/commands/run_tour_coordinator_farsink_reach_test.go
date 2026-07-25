package commands

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// The reach proof must be EXACT, not merely bounded. A far sink is by definition far from
// the tour's systems, so those systems sit at the outer depths of any walk rooted at the
// sink — precisely where a breadth budget spent on the sink's own dense neighbourhood runs
// out. A walk that stops early cannot tell "further than the executor flies" from "I stopped
// looking", and collapsing the two refuses every genuinely-reachable far sink.
//
// The fixtures below hold DEPTH constant and vary only local BRANCHING, so the verdict is
// provably about the discovery budget and nothing else.

// densePeripheryChain wires HOME—N1—MID—FAR (FAR three gate hops from home, two from N1 —
// inside the executor's five-jump flight bound) and hangs `spurs` marketless dead-end systems
// directly off FAR, listed BEFORE the MID link. A breadth-first walk rooted at FAR therefore
// meets the whole spur fan before the one edge that leads home.
func densePeripheryChain(spurs int) *fakeGateGraph {
	edges := map[string][]system.GateEdge{}
	link := func(a, b string) {
		edges[a] = append(edges[a], system.GateEdge{ConnectedSystem: b})
		edges[b] = append(edges[b], system.GateEdge{ConnectedSystem: a})
	}
	link(farHome, farN1)
	link(farN1, farMid)
	for i := 0; i < spurs; i++ {
		link(farSys, fmt.Sprintf("X1-SPUR%02d", i))
	}
	// Linked LAST so the spur fan precedes it in FAR's edge order.
	link(farMid, farSys)
	return &fakeGateGraph{edges: edges}
}

// A sink whose own neighbourhood is denser than the discovery budget is still three hops
// from home — the executor flies it, so admission must not refuse it. The two cases share
// one geometry and differ only in how many marketless spurs hang off the sink, so branching
// is provably the only variable: if the sparse case admits and the dense one does not, the
// refusal is the budget talking, not the distance.
func TestAdmitFarSinks_AdmitsThroughADenseSinkNeighbourhood(t *testing.T) {
	for _, c := range []struct {
		name  string
		spurs int
	}{
		{name: "sparse neighbourhood", spurs: 2},
		{name: "neighbourhood denser than the discovery budget", spurs: repositionBfsMaxSystems + 8},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := farSinkHandler(t, farSinkWorld(), densePeripheryChain(c.spurs), gxSinkAt(farSys+"-W", farSys, 30000))
			allowed := []string{farHome, farN1}

			got := h.admitFarSinks(context.Background(), widenedFarCmd(), allowed,
				baseSnapshotOver(t, h, allowed))

			if !candidateSetHas(got.systems, farSys) {
				t.Fatalf("%s: %s is 3 gate hops from home — inside the executor's flight bound — and must be admitted; got %v",
					c.name, farSys, got.systems)
			}
			if d, ok := hopBetween(got.hops, farHome, farSys); !ok || d != 3 {
				t.Fatalf("%s: the crossing must be priced at the 3 hops the guard proved; got %v (present=%v) in %+v",
					c.name, d, ok, got.hops)
			}
			if d, ok := hopBetween(got.hops, farN1, farSys); !ok || d != 2 {
				t.Fatalf("%s: (%s,%s) must be priced at its proven 2 gate hops; got %v (present=%v) in %+v",
					c.name, farN1, farSys, d, ok, got.hops)
			}
		})
	}
}

// Proving reach must cost NOTHING at the fetch-through seam. Connections is store-FIRST but
// falls through to a live gate fetch on a miss, and a far sink sits where the cache is
// thinnest — so a reach proof that consults it turns every admission attempt into a topology
// fetch burst, taken from a request budget the fleet needs for selling. The dense fixture is
// the worst case: a walk rooted at the sink meets 72 neighbours before the route home.
func TestAdmitFarSinks_ProvesReachWithoutReadingFetchThroughTopology(t *testing.T) {
	graph := densePeripheryChain(repositionBfsMaxSystems + 8)
	h := farSinkHandler(t, farSinkWorld(), graph, gxSinkAt(farSys+"-W", farSys, 30000))
	allowed := []string{farHome, farN1}

	got := h.admitFarSinks(context.Background(), widenedFarCmd(), allowed, baseSnapshotOver(t, h, allowed))

	if !candidateSetHas(got.systems, farSys) {
		t.Fatalf("fixture must actually admit, or a zero-fetch assertion proves only that nothing ran; got %v", got.systems)
	}
	if len(graph.connCalls) != 0 {
		t.Fatalf("far-sink admission must read no fetch-through topology (each such read is a potential live gate fetch); got %d call(s): %v",
			len(graph.connCalls), graph.connCalls)
	}
}

// The guard must still refuse — it is being made EXACT, not weakened. Each case leaves the
// sink genuinely unflyable for the executor's STRICT resolver, and each starts from the world
// that admits above, so a refusal here is the guard firing rather than an inert fixture.
//
// The uncached and stale cases are the ones the store-only proof introduces: the strict
// resolver would re-fetch that topology and may then refuse it, so treating it as proven would
// admit a route the executor cannot resolve — a hull holding cargo it cannot deliver.
func TestAdmitFarSinks_RefusesWhenTheRouteHomeIsNotProvable(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(g *fakeGateGraph)
	}{
		{name: "the only route crosses an unbuilt gate", mutate: func(g *fakeGateGraph) {
			markEdge(g, farSys, farMid, func(e *system.GateEdge) { e.UnderConstruction = true })
		}},
		{name: "an intermediate system is not cached at all", mutate: func(g *fakeGateGraph) {
			delete(g.edges, farMid) // never read — the strict resolver would fetch it live
		}},
		{name: "an intermediate system's cached topology is stale", mutate: func(g *fakeGateGraph) {
			for i := range g.edges[farMid] {
				g.edges[farMid][i].Stale = true
			}
		}},
		{name: "the stored graph is unreadable", mutate: func(g *fakeGateGraph) {
			g.storedHopErr = errors.New("gate adjacency read failed")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			graph := densePeripheryChain(2)
			c.mutate(graph)
			h := farSinkHandler(t, farSinkWorld(), graph, gxSinkAt(farSys+"-W", farSys, 30000))
			allowed := []string{farHome, farN1}

			got := h.admitFarSinks(context.Background(), widenedFarCmd(), allowed,
				baseSnapshotOver(t, h, allowed))

			if len(got.systems) != 0 {
				t.Fatalf("%s: reach is unproven, so the sink must be refused; got %v", c.name, got.systems)
			}
		})
	}
}

// markEdge applies fn to the a->b edge and its b->a mirror, so a fixture change to a link is
// symmetric the way the stored adjacency records it.
func markEdge(g *fakeGateGraph, a, b string, fn func(*system.GateEdge)) {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		for i := range g.edges[pair[0]] {
			if g.edges[pair[0]][i].ConnectedSystem == pair[1] {
				fn(&g.edges[pair[0]][i])
			}
		}
	}
}

// The reposition discovery walk is untouched. It is a DIFFERENT question — "which grounds can
// this hull reach" — whose breadth backstop bounds a downstream cached-market read per system
// surfaced, so the cap earns its keep there even though it was wrong as a reach proof. It must
// still stop at repositionBfsMaxSystems and still read its topology fetch-through.
func TestRepositionNeighborsWithinJumps_KeepsItsBreadthBackstop(t *testing.T) {
	graph := densePeripheryChain(repositionBfsMaxSystems + 8)
	h := newCandidatesHandler(t, farSinkWorld())
	h.SetGateGraph(graph)

	out, _ := h.legs.repositionNeighborsWithinJumps(context.Background(), farSys, 1, gategraph.MaxJumpPath)

	if len(out) != repositionBfsMaxSystems {
		t.Fatalf("the reposition walk must still stop at its breadth backstop (%d); got %d",
			repositionBfsMaxSystems, len(out))
	}
	if len(graph.connCalls) == 0 {
		t.Fatalf("the reposition walk must still resolve its neighbours fetch-through; got no Connections calls")
	}
}

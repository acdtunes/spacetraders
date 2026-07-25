package commands

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// The per-pair hop matrix is what stops the solver charging one flat hop for a multi-hop
// crossing. Two properties carry it. The distance must be EXACT — a walk that gives up early
// leaves the pair absent, and an absent pair is charged ONE hop in the solver, so a truncated
// search silently ranks a three-hop crossing three times too well. And a pair it genuinely
// cannot prove must be REFUSED out loud rather than falling into that same one-hop default:
// under-pricing what you could not measure is the defect, not a degradation of it.

const (
	hopPairA  = "X1-PA"
	hopPairM1 = "X1-PM1"
	hopPairM2 = "X1-PM2"
	hopPairB  = "X1-PB"
)

// spurredPairGraph wires A—M1—M2—B (three gate hops end to end) and hangs `spurs` marketless
// dead ends off BOTH endpoints, each fan listed BEFORE the link that leads toward the other
// endpoint. A breadth-budgeted walk rooted at either endpoint therefore spends its whole
// budget on that endpoint's own fan and never reaches the far one — though neither endpoint
// moved.
func spurredPairGraph(spurs int) *fakeGateGraph {
	edges := map[string][]system.GateEdge{}
	link := func(a, b string) {
		edges[a] = append(edges[a], system.GateEdge{ConnectedSystem: b})
		edges[b] = append(edges[b], system.GateEdge{ConnectedSystem: a})
	}
	for i := 0; i < spurs; i++ {
		link(hopPairA, fmt.Sprintf("X1-ASPUR%03d", i))
		link(hopPairB, fmt.Sprintf("X1-BSPUR%03d", i))
	}
	// Linked LAST so each endpoint's spur fan precedes the route to the other.
	link(hopPairA, hopPairM1)
	link(hopPairM1, hopPairM2)
	link(hopPairM2, hopPairB)
	return &fakeGateGraph{edges: edges}
}

// hopMatrixRefusalPrice is the charge a pair the matrix could not prove must carry: strictly
// more than the widest distance the walk could have resolved, so it can never be mistaken for
// a flyable crossing. Derived from the same bound the producer uses at depth 2.
func hopMatrixRefusalPrice() int { return gategraph.MaxJumpPath + 1 }

// Distance must be exact, not merely bounded. Depth is held constant across the two cases and
// only local branching varies, so a verdict that differs between them is the discovery budget
// talking rather than the graph: a pair three gate hops apart is three hops whether or not its
// endpoints are crowded.
func TestTourInterSystemHops_ExactAcrossADenseEndpointNeighbourhood(t *testing.T) {
	for _, c := range []struct {
		name  string
		spurs int
	}{
		{name: "sparse endpoints", spurs: 2},
		{name: "endpoints denser than the discovery budget", spurs: repositionBfsMaxSystems + 8},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newCandidatesHandler(t, defaultSafetyFixture())
			h.SetGateGraph(spurredPairGraph(c.spurs))

			got := h.tourInterSystemHops(context.Background(),
				[]string{hopPairA, hopPairB}, widenedFarCmd())

			d, ok := hopBetween(got, hopPairA, hopPairB)
			if !ok {
				t.Fatalf("%s: an absent pair is charged ONE hop by the solver — the crossing is 3 gate hops and must be priced; got %+v",
					c.name, got)
			}
			if d != 3 {
				t.Fatalf("%s: the crossing is 3 gate hops and must be priced at 3, not %d; got %+v", c.name, d, got)
			}
		})
	}
}

// Pricing must cost NOTHING at the fetch-through seam. Connections is store-FIRST but falls
// through to a live gate fetch on a miss or a stale set, so a matrix built through it spends
// live requests per system per plan on a budget already at the server ceiling.
func TestTourInterSystemHops_PricesWithoutReadingFetchThroughTopology(t *testing.T) {
	graph := spurredPairGraph(repositionBfsMaxSystems + 8)
	h := newCandidatesHandler(t, defaultSafetyFixture())
	h.SetGateGraph(graph)

	got := h.tourInterSystemHops(context.Background(), []string{hopPairA, hopPairB}, widenedFarCmd())

	if _, ok := hopBetween(got, hopPairA, hopPairB); !ok {
		t.Fatalf("fixture must actually price the pair, or a zero-fetch assertion proves only that nothing ran; got %+v", got)
	}
	if len(graph.connCalls) != 0 {
		t.Fatalf("the hop matrix must read no fetch-through topology (each such read is a potential live gate fetch); got %d call(s): %v",
			len(graph.connCalls), graph.connCalls)
	}
}

// countingGateGraph records how many store walks the matrix costs, so the read budget is pinned
// rather than assumed.
type countingGateGraph struct {
	*fakeGateGraph
	walks int
}

func (g *countingGateGraph) StoredRankingDistances(ctx context.Context, from string, targets []string, maxJumps int) (map[string]int, error) {
	g.walks++
	return g.fakeGateGraph.StoredRankingDistances(ctx, from, targets, maxJumps)
}

// One store walk per allowed system, whatever the graph's density — the whole matrix in a linear
// number of reads. The walk this replaced re-derived a neighbourhood per system through the
// fetch-through seam, so its cost scaled with the CLUSTER rather than with the tour graph.
func TestTourInterSystemHops_CostsOneStoreWalkPerAllowedSystem(t *testing.T) {
	graph := &countingGateGraph{fakeGateGraph: spurredPairGraph(repositionBfsMaxSystems + 8)}
	h := newCandidatesHandler(t, defaultSafetyFixture())
	h.SetGateGraph(graph)

	allowed := []string{hopPairA, hopPairM1, hopPairM2, hopPairB}
	if _, ok := hopBetween(h.tourInterSystemHops(context.Background(), allowed, widenedFarCmd()), hopPairA, hopPairB); !ok {
		t.Fatalf("fixture must actually price, or a cost assertion proves only that nothing ran")
	}
	if graph.walks != len(allowed) {
		t.Fatalf("the matrix must cost exactly one store walk per allowed system (%d); got %d", len(allowed), graph.walks)
	}
	if len(graph.connCalls) != 0 {
		t.Fatalf("and none of them may reach the fetch-through seam; got %d call(s)", len(graph.connCalls))
	}
}

// A pair whose route cannot be proven is REFUSED, and the refusal is legible. Absence is not
// an option: the solver reads an absent pair as one hop, which is the cheapest crossing it can
// charge — so staying silent about an unmeasurable route prices it as the best one available.
func TestTourInterSystemHops_UnprovableRouteIsRefusedNotDefaulted(t *testing.T) {
	for _, c := range []struct {
		name  string
		graph func() *fakeGateGraph
	}{
		{name: "the intermediate was never cached", graph: func() *fakeGateGraph {
			return &fakeGateGraph{edges: map[string][]system.GateEdge{
				hopPairA: {{ConnectedSystem: hopPairM1}},
				hopPairB: {{ConnectedSystem: hopPairM1}},
			}}
		}},
		{name: "the only route crosses an unbuilt gate", graph: func() *fakeGateGraph {
			return &fakeGateGraph{edges: map[string][]system.GateEdge{
				hopPairA:  {{ConnectedSystem: hopPairM1}},
				hopPairM1: {{ConnectedSystem: hopPairA}, {ConnectedSystem: hopPairB, UnderConstruction: true}},
				hopPairB:  {{ConnectedSystem: hopPairM1, UnderConstruction: true}},
			}}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			logger := &propFloorCapturingLogger{}
			h := newCandidatesHandler(t, defaultSafetyFixture())
			h.SetGateGraph(c.graph())

			got := h.tourInterSystemHops(propFloorCtx("HOPS", logger),
				[]string{hopPairA, hopPairB}, widenedFarCmd())

			d, ok := hopBetween(got, hopPairA, hopPairB)
			if !ok {
				t.Fatalf("%s: an unprovable crossing must be REFUSED, not omitted into the solver's 1-hop default; got %+v",
					c.name, got)
			}
			if d != hopMatrixRefusalPrice() {
				t.Fatalf("%s: an unprovable crossing must be charged past every distance the walk could prove (%d); got %d in %+v",
					c.name, hopMatrixRefusalPrice(), d, got)
			}
			if !logger.infoContains("unproven") {
				t.Fatalf("%s: the refusal must be legible, not invisible — no log line named it", c.name)
			}
		})
	}
}

// A crossing genuinely one gate hop apart still prices at the flat charge. Refusal must key on
// "could not prove", never on "not emitted": conflating the two would charge every ordinary
// neighbour crossing the refusal price and quietly veto the tours this matrix exists to rank.
func TestTourInterSystemHops_ProvenOneHopPairStillPricesFlat(t *testing.T) {
	h := newCandidatesHandler(t, defaultSafetyFixture())
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{
		hopPairA: {{ConnectedSystem: hopPairB}},
		hopPairB: {{ConnectedSystem: hopPairA}},
	}})

	got := h.tourInterSystemHops(context.Background(), []string{hopPairA, hopPairB}, widenedFarCmd())

	if len(got) != 0 {
		t.Fatalf("a proven 1-hop pair prices exactly at the solver's flat charge and must be omitted; got %+v", got)
	}
}

// One endpoint's own topology going stale must not lose the pair. The walk refuses to expand
// THROUGH a stale system but resolves fine when it merely ARRIVES at one, so the same pair is
// provable from the other end — and every allowed system is walked precisely so that one-sided
// staleness costs nothing.
func TestTourInterSystemHops_ResolvesFromTheOtherEndWhenOneEndpointIsStale(t *testing.T) {
	h := newCandidatesHandler(t, defaultSafetyFixture())
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{
		hopPairA:  {{ConnectedSystem: hopPairM1, Stale: true}},
		hopPairM1: {{ConnectedSystem: hopPairA}, {ConnectedSystem: hopPairB}},
		hopPairB:  {{ConnectedSystem: hopPairM1}},
	}})

	got := h.tourInterSystemHops(context.Background(), []string{hopPairA, hopPairB}, widenedFarCmd())

	if d, ok := hopBetween(got, hopPairA, hopPairB); !ok || d != 2 {
		t.Fatalf("the pair is provable from the fresh endpoint and must price at its real 2 hops; got %v (present=%v) in %+v", d, ok, got)
	}
}

// Topology past its freshness window is PRICED, not refused. A crossing is charged either way and
// the executor still resolves the real route strictly at flight time, so refusing a stale set does
// not make the price safer — it charges a two-hop crossing as though it were beyond the horizon.
// On a graph where a third of edge-sets age out between charting sweeps, that is most of the
// matrix.
func TestTourInterSystemHops_PricesThroughStaleTopologyRatherThanRefusingIt(t *testing.T) {
	graph := &fakeGateGraph{edges: map[string][]system.GateEdge{
		hopPairA:  {{ConnectedSystem: hopPairM1}},
		hopPairM1: {{ConnectedSystem: hopPairA, Stale: true}, {ConnectedSystem: hopPairB, Stale: true}},
		hopPairB:  {{ConnectedSystem: hopPairM1}},
	}}
	h := newCandidatesHandler(t, defaultSafetyFixture())
	h.SetGateGraph(graph)

	got := h.tourInterSystemHops(context.Background(), []string{hopPairA, hopPairB}, widenedFarCmd())

	if d, ok := hopBetween(got, hopPairA, hopPairB); !ok || d != 2 {
		t.Fatalf("the route runs over edges we have read and must price at its real 2 hops, not be refused; got %v (present=%v) in %+v", d, ok, got)
	}
	if len(graph.connCalls) != 0 {
		t.Fatalf("and reading stale topology must not become a re-fetch; got %d Connections call(s)", len(graph.connCalls))
	}
}

// rankingPermissiveGraph answers the RANKING query with a route the proof-grade query refuses.
// Far-sink admission commits a hull, so it must consult the proof-grade walk; a wiring that
// reached for the ranking one would admit a sink whose route it cannot prove and strand a laden
// hull. This stub fails that wiring outright rather than relying on fixture semantics to expose it.
type rankingPermissiveGraph struct {
	*fakeGateGraph
	rankingCalls int
}

func (g *rankingPermissiveGraph) StoredRankingDistances(_ context.Context, from string, targets []string, _ int) (map[string]int, error) {
	g.rankingCalls++
	out := map[string]int{}
	for _, to := range targets {
		if to != from {
			out[to] = 1
		}
	}
	return out, nil
}

// The far-sink admission guard is byte-identical: it still proves reach with the strict walk and
// still refuses a route through stale topology, on the same fixture the pricing matrix now
// resolves. Splitting the two walks must move pricing only.
func TestAdmitFarSinks_ProvesReachWithTheProofGradeWalkNotTheRankingOne(t *testing.T) {
	graph := densePeripheryChain(2)
	for i := range graph.edges[farMid] {
		graph.edges[farMid][i].Stale = true
	}
	permissive := &rankingPermissiveGraph{fakeGateGraph: graph}
	h := newCandidatesHandler(t, farSinkWorld())
	h.SetGateGraph(permissive)
	h.SetOutOfHorizonSinkScanner(gxSinkAt(farSys+"-W", farSys, 30000))
	allowed := []string{farHome, farN1}

	got := h.admitFarSinks(context.Background(), widenedFarCmd(), allowed, baseSnapshotOver(t, h, allowed))

	if len(got.systems) != 0 {
		t.Fatalf("reach through stale topology is unproven and the sink must still be refused; got %v", got.systems)
	}
	if permissive.rankingCalls != 0 {
		t.Fatalf("admission commits a hull and must never price its reach off the ranking walk; got %d call(s)", permissive.rankingCalls)
	}
}

// misreportingGateGraph answers the distance query with a WRONG answer alongside its failure
// flag. A caller that reads the map and ignores the error therefore prices the crossing at the
// cheapest possible charge — so this stub fails any implementation whose fail-closed branch is
// only incidentally correct.
type misreportingGateGraph struct {
	*fakeGateGraph
}

func (g *misreportingGateGraph) StoredRankingDistances(_ context.Context, from string, targets []string, _ int) (map[string]int, error) {
	wrong := map[string]int{}
	for _, t := range targets {
		if t != from {
			wrong[t] = 1
		}
	}
	return wrong, errors.New("gate adjacency read failed")
}

// An unreadable store proves nothing, so it refuses everything. The alternative is to price
// every crossing at the solver's cheapest charge on the strength of a read that failed
// (RULINGS #4).
func TestTourInterSystemHops_UnreadableStoreRefusesEveryPair(t *testing.T) {
	logger := &propFloorCapturingLogger{}
	h := newCandidatesHandler(t, defaultSafetyFixture())
	h.SetGateGraph(&misreportingGateGraph{fakeGateGraph: spurredPairGraph(2)})

	got := h.tourInterSystemHops(propFloorCtx("HOPS", logger),
		[]string{hopPairA, hopPairB}, widenedFarCmd())

	d, ok := hopBetween(got, hopPairA, hopPairB)
	if !ok || d != hopMatrixRefusalPrice() {
		t.Fatalf("an unreadable store must refuse the pair at %d, never price it on a failed read; got %v (present=%v) in %+v",
			hopMatrixRefusalPrice(), d, ok, got)
	}
	if !logger.infoContains("unproven") {
		t.Fatalf("the refusal must be legible, not invisible — no log line named it")
	}
}

// The reposition discovery walk is a DIFFERENT question — "which grounds can this hull reach" —
// whose breadth backstop bounds one cached-market read per system surfaced, and whose answer
// must come from the same fetch-through topology the hull will actually route over. Pricing
// moving off it must leave it exactly where it was, on the very fixture that exposed the
// pricing defect.
func TestRepositionNeighborsWithinJumps_UnchangedByTheHopMatrix(t *testing.T) {
	graph := spurredPairGraph(repositionBfsMaxSystems + 8)
	h := newCandidatesHandler(t, defaultSafetyFixture())
	h.SetGateGraph(graph)

	out, _ := h.legs.repositionNeighborsWithinJumps(context.Background(), hopPairA, 1, gategraph.MaxJumpPath)

	if len(out) != repositionBfsMaxSystems {
		t.Fatalf("the reposition walk must still stop at its breadth backstop (%d); got %d",
			repositionBfsMaxSystems, len(out))
	}
	for _, e := range out {
		if e.system == hopPairB {
			t.Fatalf("the breadth backstop must still truncate before the far endpoint; got %v", out)
		}
	}
	if len(graph.connCalls) == 0 {
		t.Fatalf("the reposition walk must still resolve its neighbours fetch-through; got no Connections calls")
	}
}

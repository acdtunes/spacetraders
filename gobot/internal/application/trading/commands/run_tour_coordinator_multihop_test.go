package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// sp-tp5c3 — the gate-graph FEED for the solver's real multi-hop travel model. The coordinator
// resolves the gate-hop distance between every pair of systems in allowedSystems over the SAME
// durable gate graph the reposition/candidate walk uses (repositionNeighborsWithinJumps — one
// gate-graph route model, no duplicated path-cost logic) and rides it onto cons.InterSystemHops.
// The solver then prices a cross-system crossing at gate_hops x the per-crossing charge instead
// of a flat 1 hop. COMPUTED ONLY when the horizon is widened (MaxTourSystems > 2); at the default
// cap every crossing is a single gate hop the flat charge prices exactly, so the map is empty and
// the wire is byte-identical to today. These tests drive the gate graph (the driven port) and
// assert the produced distances — directly on the producer, and end-to-end onto the planner
// request via planForState (the seam the live tour and the reposition pre-flight share).

// multiHopGateGraph: home X1-S1 gated to X1-S2 (1 hop), which gates on to X1-S9 (2 hops from
// home). Bidirectional, as real jump gates are, so the BFS resolves the distance from either end.
func multiHopGateGraph() *fakeGateGraph {
	return &fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-S1": {{ConnectedSystem: "X1-S2"}},
		"X1-S2": {{ConnectedSystem: "X1-S1"}, {ConnectedSystem: "X1-S9"}},
		"X1-S9": {{ConnectedSystem: "X1-S2"}},
	}}
}

func hopBetween(hops []routing.InterSystemHopDistance, a, b string) (int, bool) {
	for _, h := range hops {
		if (h.FromSystem == a && h.ToSystem == b) || (h.FromSystem == b && h.ToSystem == a) {
			return h.GateHops, true
		}
	}
	return 0, false
}

// RED #1 — the multi-hop computation: with the clamp lifted (MaxTourSystems > 2) and a gate graph
// where X1-S9 sits 2 hops from home, the pair (X1-S1, X1-S9) is emitted with GateHops=2, while the
// directly-gated 1-hop pairs are OMITTED (they default to 1 hop = the flat charge in the solver).
func TestTourInterSystemHops_WidenedGraph_EmitsRealDistances(t *testing.T) {
	fx := defaultSafetyFixture()
	h := newCandidatesHandler(t, fx)
	h.SetGateGraph(multiHopGateGraph())

	cmd := &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 2, MaxTourSystems: 5}
	got := h.tourInterSystemHops(context.Background(),
		[]string{"X1-S1", "X1-S2", "X1-S9"}, cmd)

	if d, ok := hopBetween(got, "X1-S1", "X1-S9"); !ok || d != 2 {
		t.Fatalf("(X1-S1,X1-S9) must be 2 gate hops; got %v (present=%v) in %+v", d, ok, got)
	}
	// 1-hop pairs are omitted — they price at the flat charge by default, so emitting them is
	// redundant and would bloat the wire.
	if _, ok := hopBetween(got, "X1-S1", "X1-S2"); ok {
		t.Fatalf("1-hop pair (X1-S1,X1-S2) must be omitted (defaults to 1 hop); got %+v", got)
	}
	if _, ok := hopBetween(got, "X1-S2", "X1-S9"); ok {
		t.Fatalf("1-hop pair (X1-S2,X1-S9) must be omitted (defaults to 1 hop); got %+v", got)
	}
	// One entry per UNORDERED pair (from < to) — no symmetric duplicate.
	if len(got) != 1 {
		t.Fatalf("expected exactly one (>1-hop, deduped) entry, got %d: %+v", len(got), got)
	}
	if got[0].FromSystem != "X1-S1" || got[0].ToSystem != "X1-S9" {
		t.Fatalf("entry must be canonicalized from<to (X1-S1->X1-S9); got %+v", got[0])
	}
}

// RED #2 — the arming gate: at the default cap (MaxTourSystems <= 2) every crossing is a single
// gate hop the flat charge prices exactly, so NO map is produced (byte-identical wire), even with
// a wider graph wired. Parametrized over the two "unlifted" signatures (0 = solver default 2; an
// explicit 2).
func TestTourInterSystemHops_DefaultCap_EmptyByteIdentical(t *testing.T) {
	for _, maxSystems := range []int{0, 2} {
		fx := defaultSafetyFixture()
		h := newCandidatesHandler(t, fx)
		h.SetGateGraph(multiHopGateGraph())

		cmd := &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 2, MaxTourSystems: maxSystems}
		got := h.tourInterSystemHops(context.Background(),
			[]string{"X1-S1", "X1-S2", "X1-S9"}, cmd)

		if len(got) != 0 {
			t.Fatalf("MaxTourSystems=%d must produce no hop map (flat pricing exact); got %+v", maxSystems, got)
		}
	}
}

// RED #3 — a nil gate graph (graph-less tests / pre-wiring) cannot resolve distances, so the map
// is empty and the solver's flat 1-hop pricing stands (never a panic, never an under-priced guess).
func TestTourInterSystemHops_NilGraph_Empty(t *testing.T) {
	fx := defaultSafetyFixture() // no SetGateGraph → legs.gateGraph == nil
	h := newCandidatesHandler(t, fx)

	cmd := &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 2, MaxTourSystems: 5}
	got := h.tourInterSystemHops(context.Background(),
		[]string{"X1-S1", "X1-S2", "X1-S9"}, cmd)

	if len(got) != 0 {
		t.Fatalf("nil gate graph must produce no hop map; got %+v", got)
	}
}

// RED #4 — the end-to-end FEED: planForState (the seam the live tour and reposition pre-flight
// share) rides the resolved distances onto the TourConstraints the planner receives, so the solver
// actually sees the real hop counts. Unwidened reaches the planner empty (byte-identical).
func TestPlanForState_FeedsInterSystemHopsToPlanner(t *testing.T) {
	fx := defaultSafetyFixture()
	fake := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: true}}}
	h := newTourHandler(t, fx, fake, nil)
	h.SetGateGraph(multiHopGateGraph())

	ship := routing.TourShipState{
		ShipSymbol: "MH-1", CurrentWaypoint: "X1-S1-A", CurrentSystem: "X1-S1", HoldCapacity: 40,
	}
	allowed := []string{"X1-S1", "X1-S2", "X1-S9"}

	// Widened: the 2-hop pair rides onto cons.InterSystemHops.
	cmd := &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 2, MaxTourSystems: 5}
	if _, _, _, err := h.planForState(context.Background(), ship, allowed, 6, 1_000_000, 0, cmd, ""); err != nil {
		t.Fatalf("planForState (widened): %v", err)
	}
	if len(fake.interSystemHops) != 1 {
		t.Fatalf("expected one planner call, got %d", len(fake.interSystemHops))
	}
	if d, ok := hopBetween(fake.interSystemHops[0], "X1-S1", "X1-S9"); !ok || d != 2 {
		t.Fatalf("planner must receive the (X1-S1,X1-S9)=2 distance; got %+v", fake.interSystemHops[0])
	}

	// Default cap: the planner receives NO hop map (byte-identical to today).
	cmdDefault := &RunTourCoordinatorCommand{PlayerID: 1}
	if _, _, _, err := h.planForState(context.Background(), ship, allowed, 6, 1_000_000, 0, cmdDefault, ""); err != nil {
		t.Fatalf("planForState (default): %v", err)
	}
	if len(fake.interSystemHops[1]) != 0 {
		t.Fatalf("default cap must feed the planner no hop map; got %+v", fake.interSystemHops[1])
	}
}

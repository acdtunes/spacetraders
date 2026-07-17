package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// sp-jsng — "Wider candidate set" tests. These drive the allowedSystems PRODUCER seam
// (tourSystems / tourSystemsFrom) directly: enter with a RunTourCoordinatorCommand and a
// legs mediator wired to (a) a live 1-hop neighbor source (GetJumpGateConnectionsQuery via
// the fixture's neighbors map) and (b) a durable gate graph (a spy recording Connections),
// then assert the exact system set that would ride into BuildTourSnapshot/OptimizeTradeTour.
// The gate graph and the jump-gate mediator are the driven ports; the assertions are on the
// produced set (return value) and on the durable-graph side effect (Connections call count).

// spyGateGraph records how many times the durable graph was consulted, so a test can prove
// the DEFAULT path never touches it (the byte-identical, zero-cost guarantee). It reuses the
// package's fakeGateGraph for every other GateGraph method.
type spyGateGraph struct {
	*fakeGateGraph
	connCalls int
}

func (s *spyGateGraph) Connections(ctx context.Context, from string, playerID int) ([]system.GateEdge, error) {
	s.connCalls++
	return s.fakeGateGraph.Connections(ctx, from, playerID)
}

func newCandidatesHandler(t *testing.T, fx *tourFixture) *RunTourCoordinatorHandler {
	t.Helper()
	return newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
}

func assertExactOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("systems = %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("systems = %v, want exactly %v", got, want)
		}
	}
}

func candidateSetHas(systems []string, want string) bool {
	for _, s := range systems {
		if s == want {
			return true
		}
	}
	return false
}

func countSystem(systems []string, want string) int {
	n := 0
	for _, s := range systems {
		if s == want {
			n++
		}
	}
	return n
}

// defaultSafetyFixture: home X1-S1 with live 1-hop neighbors [X1-S2, X1-S3]. The default
// tour graph must be exactly [X1-S1, X1-S2, X1-S3].
func defaultSafetyFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets:   map[string][]string{"X1-S1": {"X1-S1-A"}},
		neighbors: map[string][]string{"X1-S1": {"X1-S2", "X1-S3"}},
	}
}

// widerDurableGraph is a durable adjacency STRICTLY wider than the live 1-hop set — the
// default path must never consult it.
func widerDurableGraph() *spyGateGraph {
	return &spyGateGraph{fakeGateGraph: &fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-S1": {{ConnectedSystem: "X1-S2"}, {ConnectedSystem: "X1-S3"}},
		"X1-S2": {{ConnectedSystem: "X1-S4"}},
		"X1-S3": {{ConnectedSystem: "X1-S5"}},
	}}}
}

// RED #1 — THE default-safety proof. With no knobs set, the produced set is byte-identical
// to the pre-jsng 1-hop set AND the durable gate graph is never consulted.
func TestTourCandidateSystems_DefaultDepth_ByteIdentical(t *testing.T) {
	fx := defaultSafetyFixture()
	h := newCandidatesHandler(t, fx)
	spy := widerDurableGraph()
	h.SetGateGraph(spy)

	ship := fx.buildShip(t, "SHIP-1")
	cmd := &RunTourCoordinatorCommand{PlayerID: 1} // CandidateHopDepth==0, MaxTourSystems==0

	got := h.tourSystems(context.Background(), ship, cmd)

	assertExactOrder(t, got, []string{"X1-S1", "X1-S2", "X1-S3"})
	if spy.connCalls != 0 {
		t.Fatalf("default depth must not consult the durable gate graph; got %d Connections calls", spy.connCalls)
	}
}

// RED #1b — the arming gate holds the line: a configured hop depth is a NO-OP while the
// solver clamp (cmd.MaxTourSystems) is unlifted (0 → solver default 2; or an explicit 2),
// so a lone candidate_hop_depth edit can never widen or even touch the durable graph.
func TestTourCandidateSystems_ArmingGate_DepthIgnoredWhenClampUnlifted(t *testing.T) {
	for _, maxSystems := range []int{0, 2} {
		fx := defaultSafetyFixture()
		h := newCandidatesHandler(t, fx)
		spy := widerDurableGraph()
		h.SetGateGraph(spy)

		ship := fx.buildShip(t, "SHIP-1")
		cmd := &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 3, MaxTourSystems: maxSystems}

		got := h.tourSystems(context.Background(), ship, cmd)

		assertExactOrder(t, got, []string{"X1-S1", "X1-S2", "X1-S3"})
		if spy.connCalls != 0 {
			t.Fatalf("MaxTourSystems=%d: arming gate must hold depth at 1 with zero durable-graph access; got %d Connections calls", maxSystems, spy.connCalls)
		}
	}
}

// RED #2 — knob resolution, including the upper clamp (30 → 3) and the shortlist default.
func TestResolveCandidateKnobs(t *testing.T) {
	for _, c := range []struct{ in, want int }{{0, 1}, {1, 1}, {-3, 1}, {3, 3}, {30, 3}} {
		if got := resolveCandidateHopDepth(c.in); got != c.want {
			t.Errorf("resolveCandidateHopDepth(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ in, want int }{{0, 6}, {2, 2}, {-1, 6}} {
		if got := resolveCandidateShortlistTopN(c.in); got != c.want {
			t.Errorf("resolveCandidateShortlistTopN(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// RED #3 — with the clamp lifted (MaxTourSystems=5) and depth 2, the produced set reaches a
// system TWO gate hops away that carries a fresh profitable lane.
func TestTourCandidateSystems_Depth2_GathersTwoHopSystems(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-S1": {"X1-S1-M"}, // home exports GX
			"X1-S9": {"X1-S9-M"}, // 2-hop system imports GX (the profitable sink)
		},
		ask:       map[string]map[string]int{"X1-S1-M": {"GX": 100}, "X1-S9-M": {"GX": 510}},
		bid:       map[string]map[string]int{"X1-S1-M": {"GX": 90}, "X1-S9-M": {"GX": 500}},
		tv:        map[string]map[string]int{"X1-S1-M": {"GX": 1000}, "X1-S9-M": {"GX": 1000}},
		tradeType: map[string]map[string]string{"X1-S9-M": {"GX": "IMPORT"}},
		neighbors: map[string][]string{"X1-S1": {"X1-S2"}},
	}
	h := newCandidatesHandler(t, fx)
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-S1": {{ConnectedSystem: "X1-S2"}},
		"X1-S2": {{ConnectedSystem: "X1-S9"}}, // B = X1-S9 sits 2 hops out
	}})

	cmd := &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 2, MaxTourSystems: 5}
	got := h.tourSystemsFrom(context.Background(), "X1-S1", cmd)

	if !candidateSetHas(got, "X1-S9") {
		t.Fatalf("depth-2 candidate set must reach the 2-hop system X1-S9; got %v", got)
	}
}

// RED #4 — the shortlist keeps the top-N far systems by CROSS-system profitable edge and
// drops the rest. S_source is kept despite ~0 in-system spread (it is the SOURCE of the best
// cross-system lane); S_mid is cut by top-2; S_dead has no profitable edge; the 1-hop gateway
// G and home are force-kept by the floor.
func TestTourCandidateSystems_ShortlistProfitableEdgeTopN(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-HOME-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-HOME":   {"X1-HOME-M"},   // exports GB
			"X1-MID":    {"X1-MID-M"},    // imports GB (300k lane, but below top-2)
			"X1-SOURCE": {"X1-SOURCE-M"}, // exports GA (pure export cluster, ~0 in-system spread)
			"X1-RICH":   {"X1-RICH-M"},   // imports GA (900k lane, best)
			"X1-DEAD":   {"X1-DEAD-M"},   // GD trades in one market only — no profitable lane
			// X1-GW (the 1-hop gateway) intentionally has NO markets.
		},
		ask: map[string]map[string]int{
			"X1-HOME-M": {"GB": 100}, "X1-MID-M": {"GB": 410},
			"X1-SOURCE-M": {"GA": 100}, "X1-RICH-M": {"GA": 1010},
			"X1-DEAD-M": {"GD": 100},
		},
		bid: map[string]map[string]int{
			"X1-HOME-M": {"GB": 90}, "X1-MID-M": {"GB": 400},
			"X1-SOURCE-M": {"GA": 90}, "X1-RICH-M": {"GA": 1000},
			"X1-DEAD-M": {"GD": 90},
		},
		tv: map[string]map[string]int{
			"X1-HOME-M": {"GB": 1000}, "X1-MID-M": {"GB": 1000},
			"X1-SOURCE-M": {"GA": 1000}, "X1-RICH-M": {"GA": 1000},
			"X1-DEAD-M": {"GD": 1000},
		},
		tradeType: map[string]map[string]string{
			"X1-MID-M": {"GB": "IMPORT"}, "X1-RICH-M": {"GA": "IMPORT"},
		},
		neighbors: map[string][]string{"X1-HOME": {"X1-GW"}},
	}
	h := newCandidatesHandler(t, fx)
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-HOME": {{ConnectedSystem: "X1-GW"}},
		"X1-GW": {
			{ConnectedSystem: "X1-RICH"}, {ConnectedSystem: "X1-MID"},
			{ConnectedSystem: "X1-DEAD"}, {ConnectedSystem: "X1-SOURCE"},
		},
	}})

	cmd := &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 2, MaxTourSystems: 5, CandidateShortlistTopN: 2}
	got := h.tourSystemsFrom(context.Background(), "X1-HOME", cmd)

	for _, want := range []string{"X1-HOME", "X1-GW", "X1-RICH", "X1-SOURCE"} {
		if !candidateSetHas(got, want) {
			t.Fatalf("shortlist must keep %s (top-2 profitable edge or floored 1-hop); got %v", want, got)
		}
	}
	for _, drop := range []string{"X1-MID", "X1-DEAD"} {
		if candidateSetHas(got, drop) {
			t.Fatalf("shortlist must drop %s (below top-2 / no profitable edge); got %v", drop, got)
		}
	}
}

// RED #5a — nil durable graph: the widened branch cannot widen and returns EXACTLY the 1-hop
// set (never panics, never collapses narrower via a live-neighbor fallback filter).
func TestTourCandidateSystems_NilGraph_ReturnsExactlyOneHop(t *testing.T) {
	fx := defaultSafetyFixture() // no SetGateGraph → legs.gateGraph == nil
	h := newCandidatesHandler(t, fx)

	cmd := &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 3, MaxTourSystems: 5}
	got := h.tourSystemsFrom(context.Background(), "X1-S1", cmd)

	assertExactOrder(t, got, []string{"X1-S1", "X1-S2", "X1-S3"})
}

// RED #5b — present graph but genuinely no durable adjacency for home: the depth-1 baseline
// stands (non-empty).
func TestTourCandidateSystems_PresentGraphBfsEmpty_ReturnsOneHop(t *testing.T) {
	fx := defaultSafetyFixture()
	h := newCandidatesHandler(t, fx)
	// Graph present, but no entry for X1-S1 → Connections(X1-S1) yields no adjacency.
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-OTHER": {{ConnectedSystem: "X1-ELSE"}},
	}})

	cmd := &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 3, MaxTourSystems: 5}
	got := h.tourSystemsFrom(context.Background(), "X1-S1", cmd)

	assertExactOrder(t, got, []string{"X1-S1", "X1-S2", "X1-S3"})
}

// RED #5c — the floor invariant: even with a wide BFS, if NO system has a fresh profitable
// edge the shortlist collapses to empty and the result is still a superset of the 1-hop set.
func TestTourCandidateSystems_WidenedNeverNarrowerThanOneHop(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		// No markets anywhere → no profitable lanes → empty shortlist.
		neighbors: map[string][]string{"X1-S1": {"X1-S2"}},
	}
	h := newCandidatesHandler(t, fx)
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-S1": {{ConnectedSystem: "X1-S2"}},
		"X1-S2": {{ConnectedSystem: "X1-S9"}},
	}})

	cmd := &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 2, MaxTourSystems: 5}
	got := h.tourSystemsFrom(context.Background(), "X1-S1", cmd)

	for _, want := range []string{"X1-S1", "X1-S2"} {
		if !candidateSetHas(got, want) {
			t.Fatalf("widened set must never drop a 1-hop baseline system; %s missing from %v", want, got)
		}
	}
}

// RED #6 — home is retained (via the floor, with no profitable edge of its own) exactly once,
// and a far system that also appears as a 1-hop neighbor is deduped; order is stable
// (1-hop prefix first, then the newly added widened systems).
func TestTourCandidateSystems_HomeRetainedAndDeduped(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{
			// X1-S1 (home) has NO market — retained only by the floor.
			"X1-S2": {"X1-S2-M"}, // 1-hop neighbor, exports GZ (also a shortlist hit → dedup)
			"X1-S9": {"X1-S9-M"}, // 2-hop, imports GZ
		},
		ask:       map[string]map[string]int{"X1-S2-M": {"GZ": 100}, "X1-S9-M": {"GZ": 510}},
		bid:       map[string]map[string]int{"X1-S2-M": {"GZ": 90}, "X1-S9-M": {"GZ": 500}},
		tv:        map[string]map[string]int{"X1-S2-M": {"GZ": 1000}, "X1-S9-M": {"GZ": 1000}},
		tradeType: map[string]map[string]string{"X1-S9-M": {"GZ": "IMPORT"}},
		neighbors: map[string][]string{"X1-S1": {"X1-S2"}},
	}
	h := newCandidatesHandler(t, fx)
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-S1": {{ConnectedSystem: "X1-S2"}},
		"X1-S2": {{ConnectedSystem: "X1-S9"}},
	}})

	cmd := &RunTourCoordinatorCommand{PlayerID: 1, CandidateHopDepth: 2, MaxTourSystems: 5}
	got := h.tourSystemsFrom(context.Background(), "X1-S1", cmd)

	assertExactOrder(t, got, []string{"X1-S1", "X1-S2", "X1-S9"})
	if countSystem(got, "X1-S1") != 1 {
		t.Fatalf("home must appear exactly once; got %v", got)
	}
	if countSystem(got, "X1-S2") != 1 {
		t.Fatalf("a 1-hop neighbor that also scores in the shortlist must be deduped; got %v", got)
	}
}

// RED #7 — the composed arming gate (effectiveCandidateHopDepth). This is the DOUBLE
// default-safety the config plumbing (sp-jsng) rides on, proven end-to-end at the gate itself:
// the configured depth only takes EFFECT once BOTH (a) depth resolves > 1 AND (b) the solver
// clamp is lifted (cmd.MaxTourSystems > 2). Complements RED #1b (which proves the gate HOLDS at
// MaxTourSystems ∈ {0,2} via the produced set) by pinning the transition at the > 2 boundary —
// the arming point that makes the widening reachable once candidate_hop_depth is wired. A lone
// candidate_hop_depth edit (max unlifted) can never open the gate; a depth of 0/absent floors to
// 1 (byte-identical) even with the clamp lifted.
func TestEffectiveCandidateHopDepth_ComposedArmingGate(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	for _, c := range []struct {
		name       string
		depth, max int
		want       int
	}{
		{"armed: depth 2 + clamp lifted (max 4) widens", 2, 4, 2},
		{"armed: depth 3 + clamp lifted (max 4) widens to 3", 3, 4, 3},
		{"gate holds: depth 2 but clamp at explicit 2 (unlifted)", 2, 2, 1},
		{"gate holds: depth 2 but max 0 (solver default 2, unlifted)", 2, 0, 1},
		{"depth floor: absent depth (0) stays 1 even with clamp lifted", 0, 4, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd := &RunTourCoordinatorCommand{CandidateHopDepth: c.depth, MaxTourSystems: c.max}
			if got := h.effectiveCandidateHopDepth(cmd); got != c.want {
				t.Errorf("effectiveCandidateHopDepth(depth=%d, max=%d) = %d, want %d", c.depth, c.max, got, c.want)
			}
		})
	}
}

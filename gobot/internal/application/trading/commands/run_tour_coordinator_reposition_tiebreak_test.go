package commands

// run_tour_coordinator_reposition_tiebreak_test.go — when the pre-rank cannot separate the
// candidates, the bounded top-K stops being a ranking and becomes an alphabetical slice. Every
// candidate scores 0 whenever no cached in-system lane exists, the sort then falls entirely to its
// stable system-symbol tie-break, and the SAME alphabetically-first few systems are pre-flighted
// on every attempt from that origin — forever, because the tie-break is deterministic. The
// pre-rank's real verdicts are good and are left exactly as they are; what these tests pin is that
// its NON-verdicts get decided by something with signal, and that a losing slice is not retried
// unchanged.

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// tiedFixture models the live X1-RX81 shape: an origin whose reachable systems ALL carry cached
// markets with no in-system spread, so every one of them pre-ranks 0. The alphabetically-first
// systems (X1-AA*/X1-AB*) sit THREE gate hops out; the single nearest ground (X1-ZZ1, one hop) is
// alphabetically LAST, so an alphabetical slice can never reach it however many attempts are made.
func tiedFixture() *tourFixture {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-ORIG-A", cargoCap: 100,
		markets:   map[string][]string{},
		ask:       map[string]map[string]int{},
		bid:       map[string]map[string]int{},
		tv:        map[string]map[string]int{},
		neighbors: map[string][]string{}, // live scan barren — the durable graph drives discovery
	}
	for _, sys := range tiedCandidateSystems() {
		wp := sys + "-A"
		fx.markets[sys] = []string{wp}
		fx.ask[wp] = map[string]int{"G": 100} // an offer with no sink anywhere → no lane → score 0
		fx.tv[wp] = map[string]int{"G": 10}
	}
	return fx
}

// tiedCandidateSystems is the reachable set, in the alphabetical order the degenerate sort falls
// back to. X1-ZZ1 is last alphabetically and nearest by hops — the discriminator between the two.
func tiedCandidateSystems() []string {
	return []string{
		"X1-AA1", "X1-AA2", "X1-AA3", "X1-AA4", "X1-AA5",
		"X1-AA6", "X1-AA7", "X1-AA8", "X1-AA9",
		"X1-AB1", "X1-AB2", "X1-AB3", "X1-AB4",
		"X1-ZZ1",
	}
}

// tiedGateGraph puts X1-ZZ1 one hop from the origin and every alphabetically-earlier ground three
// hops out, behind two market-less interior systems (which are therefore never candidates).
func tiedGateGraph() *fakeGateGraph {
	deep := make([]system.GateEdge, 0, 13)
	for _, sys := range tiedCandidateSystems() {
		if sys == "X1-ZZ1" {
			continue
		}
		deep = append(deep, system.GateEdge{ConnectedSystem: sys, GateWaypoint: sys + "-GATE"})
	}
	return &fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-ORIG": {{ConnectedSystem: "X1-ZZ1", GateWaypoint: "X1-ZZ1-GATE"}, {ConnectedSystem: "X1-H1", GateWaypoint: "X1-H1-GATE"}},
		"X1-H1":   {{ConnectedSystem: "X1-H2", GateWaypoint: "X1-H2-GATE"}},
		"X1-H2":   deep,
	}}
}

// preflightedSystems maps the hull states the planner was asked to price back to their systems —
// the observable record of which candidates actually reached the solver.
func preflightedSystems(planner *tourFakeRoutingClient) []string {
	out := make([]string, 0, len(planner.positions))
	for _, wp := range planner.positions {
		out = append(out, shared.ExtractSystemSymbol(wp))
	}
	return out
}

// runTiedEpisode drives ONE margins-death reposition episode from X1-ORIG and returns the systems
// that reached the solver. The planner declines every ground, so the episode always evaluates its
// full bound and chooses nothing — the live "chosen none" shape.
func runTiedEpisode(t *testing.T, h *RunTourCoordinatorHandler, planner *tourFakeRoutingClient, ship string) []string {
	t.Helper()
	before := len(planner.positions)
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := &RunTourCoordinatorCommand{ShipSymbol: ship, PlayerID: 1, RepositionReachEnabled: true}
	episode := repositionEpisode{}
	repositioned, err := h.maybeReposition(ctx, cmd, &RunTourCoordinatorResponse{}, &episode, map[string]int{},
		tourPlanBudget{maxHops: 6, maxSpend: 1_000_000, reserve: 150_000})
	if err != nil {
		t.Fatalf("maybeReposition: %v", err)
	}
	if repositioned {
		t.Fatalf("the fixture's planner declines every ground - no reposition should commit")
	}
	return preflightedSystems(planner)[before:]
}

func newTiedHandler(t *testing.T) (*RunTourCoordinatorHandler, *tourFakeRoutingClient) {
	t.Helper()
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan { return infeasibleTour() }}
	h := newTourHandler(t, tiedFixture(), planner, &tourFakeTelemetry{})
	h.SetGateGraph(tiedGateGraph())
	return h, planner
}

// THE ALPHABETICAL SLICE. Every candidate pre-ranks 0, so the sort is fully degenerate and its
// stable system-symbol tie-break becomes the ONLY ordering. The nearest ground by far — one gate
// hop out against three — is alphabetically last and is therefore never priced. Hop distance is a
// cost the ranking already believes in (it charges a per-hop deadhead decay), and at score 0 that
// decay multiplies out to nothing, leaving it unconsulted precisely when it is the only signal
// left. Deciding a tie on it is not a re-fit of the score: the score expressed no preference here.
func TestReposition_TiedPrerank_BreaksTheTieOnSignalNotAlphabet(t *testing.T) {
	h, planner := newTiedHandler(t)

	evaluated := runTiedEpisode(t, h, planner, "TIE-NEAR")

	if !containsSystem(evaluated, "X1-ZZ1") {
		t.Fatalf("the nearest ground (1 hop) must be priced when nothing else separates the candidates, evaluated=%v", evaluated)
	}
	if evaluated[0] != "X1-ZZ1" {
		t.Fatalf("the tie must be broken on hop distance first, got %q first (evaluated=%v)", evaluated[0], evaluated)
	}
}

// THE BOUND STAYS A BOUND. A tie at the K-boundary means the cut between those candidates was
// decided by nothing, so the fan-out widens to give the undecided set more than a coin flip — but
// it remains a fixed, named ceiling well under the reachable set, never one solver call per
// reachable system.
func TestReposition_TiedPrerank_WidensFanOutButKeepsItBounded(t *testing.T) {
	h, planner := newTiedHandler(t)

	evaluated := runTiedEpisode(t, h, planner, "TIE-BOUND")

	// Bracketed from BOTH sides against values the tied bound is NOT, so retuning the bound leaves
	// the test meaningful while collapsing it back to the default K, or letting it run away to one
	// call per reachable system, each fail. Reading the bound's own constant here would make the
	// assertion move with any mutation of it and pin nothing.
	if len(evaluated) <= repositionMaxCandidatesDefault {
		t.Fatalf("an undecided cut must get more than the default %d blind draws, got %d (evaluated=%v)", repositionMaxCandidatesDefault, len(evaluated), evaluated)
	}
	if len(evaluated) >= len(tiedCandidateSystems()) {
		t.Fatalf("the fan-out must stay BOUNDED below the reachable set (%d candidates), got %d", len(tiedCandidateSystems()), len(evaluated))
	}
}

// THE RETRY LOOP. The tie-break is deterministic, so a hull that fails from an origin re-evaluates
// an IDENTICAL slice on every later attempt from that origin and never explores the rest of its
// reach — the shape behind a heavy that has flown zero legs in its lifetime. Consecutive episodes
// must sweep a different window.
func TestReposition_TiedPrerank_RotatesTheSliceAcrossEpisodes(t *testing.T) {
	h, planner := newTiedHandler(t)

	first := runTiedEpisode(t, h, planner, "TIE-ROT")
	second := runTiedEpisode(t, h, planner, "TIE-ROT")

	if fmt.Sprint(first) == fmt.Sprint(second) {
		t.Fatalf("a failing slice must not be retried unchanged - both episodes evaluated %v", first)
	}
	fresh := 0
	for _, s := range second {
		if !containsSystem(first, s) {
			fresh++
		}
	}
	if fresh == 0 {
		t.Fatalf("the second episode must reach candidates the first never priced, first=%v second=%v", first, second)
	}
}

// A DIFFERENT HULL EXPLORES INDEPENDENTLY. The rotation is per hull and origin, so one hull's
// sweep never robs another of the strongest candidates on its first attempt.
func TestReposition_TiedPrerank_RotationIsPerHull(t *testing.T) {
	h, planner := newTiedHandler(t)

	first := runTiedEpisode(t, h, planner, "TIE-HULL-A")
	other := runTiedEpisode(t, h, planner, "TIE-HULL-B")

	if fmt.Sprint(first) != fmt.Sprint(other) {
		t.Fatalf("a hull's FIRST attempt from an origin must start at the head of the ranking, got %v then %v", first, other)
	}
}

// discriminatedFixture gives every reachable ground a DISTINCT cached in-system spread, so the
// pre-rank separates them all and no tie-break is consulted. A system's capped spread is
// (bid−ask)·volume: with volume 10 and ask 100, a score of v needs an IMPORT sink priced 100+v/10.
func discriminatedFixture() *tourFixture {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-ORIG-A", cargoCap: 100,
		markets:   map[string][]string{},
		ask:       map[string]map[string]int{},
		bid:       map[string]map[string]int{},
		tv:        map[string]map[string]int{},
		tradeType: map[string]map[string]string{},
		neighbors: map[string][]string{},
	}
	for sys, score := range discriminatedScores() {
		src, sink := sys+"-A", sys+"-B"
		fx.markets[sys] = []string{src, sink}
		fx.ask[src] = map[string]int{"G": 100}
		fx.ask[sink] = map[string]int{"G": 100 + score/10}
		fx.bid[sink] = map[string]int{"G": 100 + score/10}
		fx.tv[src] = map[string]int{"G": 10}
		fx.tv[sink] = map[string]int{"G": 10}
		fx.tradeType[sink] = map[string]string{"G": "IMPORT"}
	}
	return fx
}

func discriminatedScores() map[string]int {
	return map[string]int{"X1-DA1": 4000, "X1-DB1": 3000, "X1-DC1": 2000, "X1-DD1": 1000, "X1-DE1": 500}
}

func discriminatedGateGraph() *fakeGateGraph {
	edges := make([]system.GateEdge, 0, len(discriminatedScores()))
	for sys := range discriminatedScores() {
		edges = append(edges, system.GateEdge{ConnectedSystem: sys, GateWaypoint: sys + "-GATE"})
	}
	return &fakeGateGraph{edges: map[string][]system.GateEdge{"X1-ORIG": edges}}
}

// THE UNTOUCHED PATH. Where the pre-rank DOES separate the candidates it is obeyed exactly: the
// default three highest-scoring grounds are priced, no wider fan-out and no rotation. This is the
// falsifier for a change that simply widened the bound everywhere — the measured ranking is a
// reasonable one within an episode, and paying extra solver calls where it works would be waste.
func TestReposition_DiscriminatedPrerank_KeepsTheDefaultTopThree(t *testing.T) {
	planner := &tourFakeRoutingClient{planFn: func(routing.TourShipState) *routing.TourPlan { return infeasibleTour() }}
	h := newTourHandler(t, discriminatedFixture(), planner, &tourFakeTelemetry{})
	h.SetGateGraph(discriminatedGateGraph())
	ctx := common.WithLogger(context.Background(), &tradeCaptureLogger{})
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TIE-NONE", PlayerID: 1, RepositionReachEnabled: true}
	episode := repositionEpisode{}

	if _, err := h.maybeReposition(ctx, cmd, &RunTourCoordinatorResponse{}, &episode, map[string]int{},
		tourPlanBudget{maxHops: 6, maxSpend: 1_000_000, reserve: 150_000}); err != nil {
		t.Fatalf("maybeReposition: %v", err)
	}

	evaluated := preflightedSystems(planner)
	want := []string{"X1-DA1", "X1-DB1", "X1-DC1"}
	if fmt.Sprint(evaluated) != fmt.Sprint(want) {
		t.Fatalf("a discriminated pre-rank must be obeyed unchanged: evaluated %v, want %v", evaluated, want)
	}
}

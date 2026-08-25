package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// The reposition pre-rank is a pure function of cached prices, so the top-K cut hands the
// solver the same grounds from a given origin on every attempt. A ground that ranks just
// outside the window is never priced, so it is never learned to be worth flying to, so it
// stays outside the window — and a fleet whose core has been grazed re-confirms the graze
// instead of sweeping its own reach.

// exploreFixture is a five-system world with the live shape: a home ground the hull drains,
// three richer-scoring neighbours the solver has nothing left to build at, and one lower-
// scoring never-priced ground that DOES carry a tour. The three rich grounds fill the whole
// bounded pre-flight, so the virgin ground is the one candidate never asked.
func exploreFixture() *tourFixture {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-S1": {"X1-S1-A", "X1-S1-B"},
			"X1-R1": {"X1-R1-A", "X1-R1-B"},
			"X1-R2": {"X1-R2-A", "X1-R2-B"},
			"X1-R3": {"X1-R3-A", "X1-R3-B"},
			"X1-V1": {"X1-V1-A", "X1-V1-B"},
		},
		bid:       map[string]map[string]int{},
		ask:       map[string]map[string]int{},
		tv:        map[string]map[string]int{},
		tradeType: map[string]map[string]string{},
		neighbors: map[string][]string{"X1-S1": {"X1-R1", "X1-R2", "X1-R3", "X1-V1"}},
	}
	// Home: one lane so the first tour is productive and the hull earns before it starves.
	fx.addLane("X1-S1", "G", 100, 200, 100)
	// The three grounds the pre-rank ranks above the virgin one, descending.
	fx.addLane("X1-R1", "G", 100, 500, 100)
	fx.addLane("X1-R2", "G", 100, 400, 100)
	fx.addLane("X1-R3", "G", 100, 300, 100)
	// The virgin ground: the lowest headline spread of the four, but broad enough for the
	// solver to work with — the evidence the exploration slot is allowed to spend a call on.
	fx.addLane("X1-V1", "V1", 100, 200, 100)
	fx.addLane("X1-V1", "V2", 900, 100, 100)
	fx.addLane("X1-V1", "V3", 900, 100, 100)
	return fx
}

// addLane writes one good's source (an EXPORT quoting ask) and sink (an IMPORT quoting bid)
// into the fixture's two markets for a system, so the pre-rank sees a real in-system lane
// worth (bid-ask)*volume rather than the EXPORT-sink-refused default.
func (fx *tourFixture) addLane(system, good string, ask, bid, volume int) {
	src, sink := system+"-A", system+"-B"
	for _, wp := range []string{src, sink} {
		if fx.ask[wp] == nil {
			fx.ask[wp] = map[string]int{}
			fx.bid[wp] = map[string]int{}
			fx.tv[wp] = map[string]int{}
			fx.tradeType[wp] = map[string]string{}
		}
	}
	fx.ask[src][good], fx.bid[src][good], fx.tv[src][good] = ask, ask, volume
	fx.tradeType[src][good] = "EXPORT"
	// The sink's own ask sits just above its bid so it can never double as a cheaper source
	// and invent a reverse lane the pre-rank would score instead.
	fx.ask[sink][good], fx.bid[sink][good], fx.tv[sink][good] = bid+10, bid, volume
	fx.tradeType[sink][good] = "IMPORT"
}

// THE UNLOCK. A hull whose margins die on a grazed ground must reach the virgin ground the
// pre-rank left just outside its bounded pre-flight: the three higher-scoring neighbours are
// all solver-infeasible, and without an exploration slot the episode ends with an honest
// "margins died" exit while a tourable ground sits one gate hop away, permanently rank #4.
func TestTour_Reposition_ExploresVirginGroundOutsideTheTopK(t *testing.T) {
	fx := exploreFixture()
	homeCalls, virginCalls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour() // margins die on the home ground (3-strike)
		case "X1-V1":
			virginCalls++
			if virginCalls <= 2 {
				return virginTour() // the pre-flight, then the live re-plan after the jump
			}
			return infeasibleTour()
		}
		return infeasibleTour() // X1-R1/R2/R3 — richer pre-rank, nothing left to build
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-EXP", PlayerID: 1, ContainerID: "ctr-exp", Iterations: -1,
		RepositionReachEnabled: true,
		ModelArtifactPath:      writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("explore run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected the hull to reposition onto the virgin ground, got %d reposition(s)", r.Repositions)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-V1" {
		t.Fatalf("expected a single jump to X1-V1, jumps were %v", fx.jumps)
	}
	if r.ToursCompleted != 2 {
		t.Fatalf("expected 2 productive tours (home + the virgin ground), got %d", r.ToursCompleted)
	}
	if virginCalls == 0 {
		t.Fatalf("the virgin ground was never priced — the exploration slot did not reach it")
	}
}

// virginTour clears the reposition floor and executes against exploreFixture's X1-V1 prices.
func virginTour() *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, ProjectedProfit: 100000, ProjectedCreditsPerHour: 200000, Legs: []routing.TourLeg{
		leg("X1-V1-A", "X1-V1", buy("V1", 40, 100)),
		leg("X1-V1-B", "X1-V1", sell("V1", 40, 200)),
	}}
}

// The slot is EVIDENCE-BOUNDED. A thin ground — too few quoted rows, or a per-visit volume
// under the absorption bar — is not worth one of the scarce pre-flight calls, so it is left
// exactly where the pre-rank put it and the window is byte-identical to today's.
func TestRepositionExplore_ThinGroundNeverEarnsTheSlot(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	rich := repositionCandidate{system: "X1-RICH", waypoint: "X1-RICH-A", score: 9000, hops: 1, freshLanes: 40, tradeVolume: 900}
	ranked := []repositionCandidate{
		{system: "X1-A", waypoint: "X1-A-A", score: 50000, hops: 1, freshLanes: 40, tradeVolume: 900},
		{system: "X1-B", waypoint: "X1-B-A", score: 40000, hops: 1, freshLanes: 40, tradeVolume: 900},
		{system: "X1-C", waypoint: "X1-C-A", score: 30000, hops: 1, freshLanes: 40, tradeVolume: 900},
		// Outside the window: a hamlet quoting a handful of rows, and one whose markets
		// cannot absorb a tranche. Neither is evidence of a ground worth a planner call.
		{system: "X1-THIN", waypoint: "X1-THIN-A", score: 20000, hops: 1, freshLanes: 3, tradeVolume: 900},
		{system: "X1-SHALLOW", waypoint: "X1-SHALLOW-A", score: 19000, hops: 1, freshLanes: 40, tradeVolume: 5},
	}
	got, k := h.admitExplorationCandidate(context.Background(), &RunTourCoordinatorCommand{}, ranked, 3)
	if k != 3 {
		t.Fatalf("a window with no eligible candidate outside it must not widen, k=%d", k)
	}
	if len(got) != len(ranked) || got[3].system != "X1-THIN" {
		t.Fatalf("the candidate order must be untouched when nothing is eligible, got %v", systemsOf(got))
	}

	// And the prior never outranks: an eligible virgin ground is ADDED after the window the
	// score decided, never promoted into it and never displacing a better-scoring ground.
	eligible := append(append([]repositionCandidate(nil), ranked[:3]...), rich)
	got, k = h.admitExplorationCandidate(context.Background(), &RunTourCoordinatorCommand{}, eligible, 3)
	if k != 4 {
		t.Fatalf("an eligible unpriced ground must earn one extra pre-flight slot, k=%d", k)
	}
	if systemsOf(got[:3]) != "X1-A,X1-B,X1-C" {
		t.Fatalf("the top-K the score decided must be preserved exactly, got %v", systemsOf(got[:3]))
	}
	if got[3].system != "X1-RICH" {
		t.Fatalf("the admitted ground must sit AFTER the score's window, got %v", systemsOf(got))
	}
}

// FAIL-OPEN. A candidate carrying no observables at all (an unstamped one from some other
// discovery path), or a window that already covers every candidate, leaves the pre-flight set
// exactly as it is — today's behaviour, byte for byte.
func TestRepositionExplore_UnreadableObservablesLeaveTodaysWindow(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	unstamped := []repositionCandidate{
		{system: "X1-A", waypoint: "X1-A-A", score: 50000, hops: 1},
		{system: "X1-B", waypoint: "X1-B-A", score: 40000, hops: 1},
		{system: "X1-C", waypoint: "X1-C-A", score: 30000, hops: 1},
		{system: "X1-D", waypoint: "X1-D-A", score: 20000, hops: 1},
	}
	got, k := h.admitExplorationCandidate(context.Background(), &RunTourCoordinatorCommand{}, unstamped, 3)
	if k != 3 || systemsOf(got) != "X1-A,X1-B,X1-C,X1-D" {
		t.Fatalf("unreadable observables must leave the window untouched, k=%d order=%v", k, systemsOf(got))
	}

	covered := unstamped[:2]
	got, k = h.admitExplorationCandidate(context.Background(), &RunTourCoordinatorCommand{}, covered, 3)
	if k != 3 || len(got) != 2 {
		t.Fatalf("a window already covering every candidate must not change, k=%d len=%d", k, len(got))
	}
}

// The slot SWEEPS. Pricing a ground records it, so the next episode from the same origin
// spends the slot on the coldest ground instead of re-asking the one it just answered — the
// property that keeps the reach from re-starving the moment the first sweep completes.
func TestRepositionExplore_SweepsColdestGroundFirst(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	cmd := &RunTourCoordinatorCommand{}
	ranked := []repositionCandidate{
		{system: "X1-A", waypoint: "X1-A-A", score: 50000, hops: 1, freshLanes: 40, tradeVolume: 900},
		{system: "X1-P", waypoint: "X1-P-A", score: 9000, hops: 1, freshLanes: 40, tradeVolume: 900},
		{system: "X1-Q", waypoint: "X1-Q-A", score: 8000, hops: 1, freshLanes: 40, tradeVolume: 900},
	}
	got, _ := h.admitExplorationCandidate(context.Background(), cmd, ranked, 1)
	if got[1].system != "X1-P" {
		t.Fatalf("the first sweep must take the best-ranked unpriced ground, got %v", systemsOf(got))
	}

	h.notePricedGround("X1-P")
	got, _ = h.admitExplorationCandidate(context.Background(), cmd, ranked, 1)
	if got[1].system != "X1-Q" {
		t.Fatalf("a ground just priced must not be re-asked while a colder one waits, got %v", systemsOf(got))
	}

	h.notePricedGround("X1-Q")
	got, _ = h.admitExplorationCandidate(context.Background(), cmd, ranked, 1)
	if got[1].system != "X1-P" {
		t.Fatalf("with every ground priced the sweep must return to the coldest, got %v", systemsOf(got))
	}
}

// The two observable bars resolve from the live tune surface with the documented in-code
// defaults, so an operator can widen or narrow the slot without a daemon bounce.
func TestRepositionExplore_BarsResolveFromTheTuneRegistry(t *testing.T) {
	defaults := TradeFleetTunableDefaults()
	if defaults[TuneKeyExploreMinFreshListings] != defaultExploreMinFreshListings {
		t.Fatalf("%s must declare its in-code default, got %d", TuneKeyExploreMinFreshListings, defaults[TuneKeyExploreMinFreshListings])
	}
	if defaults[TuneKeyExploreMinTradeVolume] != defaultExploreMinTradeVolume {
		t.Fatalf("%s must declare its in-code default, got %d", TuneKeyExploreMinTradeVolume, defaults[TuneKeyExploreMinTradeVolume])
	}
	h := &RunTourCoordinatorHandler{}
	if got := h.exploreMinFreshListings(context.Background(), 1); got != defaultExploreMinFreshListings {
		t.Fatalf("an unwired tune surface must fall back to the documented default, got %d", got)
	}
	if got := h.exploreMinTradeVolume(context.Background(), 1); got != defaultExploreMinTradeVolume {
		t.Fatalf("an unwired tune surface must fall back to the documented default, got %d", got)
	}
}

func systemsOf(candidates []repositionCandidate) string {
	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.system)
	}
	return strings.Join(names, ",")
}

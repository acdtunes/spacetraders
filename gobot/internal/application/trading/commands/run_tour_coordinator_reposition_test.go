package commands

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// repositionFixture is a two-system world for the sp-zhii margins-death reposition: the
// hull starts and dies on home X1-S1, and X1-S2 is a jump-reachable fresh ground with its
// own arb lane (buy H @100 at A, sell H @300 at B). neighbors wires X1-S1 -> X1-S2 so the
// candidate scan (GetJumpGateConnectionsQuery) finds it.
func repositionFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-S1": {"X1-S1-A", "X1-S1-B"},
			"X1-S2": {"X1-S2-A", "X1-S2-B"},
		},
		bid: map[string]map[string]int{
			"X1-S1-B": {"G": 200},
			"X1-S2-B": {"H": 300},
		},
		ask: map[string]map[string]int{
			"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200},
			"X1-S2-A": {"H": 100}, "X1-S2-B": {"H": 300},
		},
		tv: map[string]map[string]int{
			"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000},
			"X1-S2-A": {"H": 1000}, "X1-S2-B": {"H": 1000},
		},
		neighbors: map[string][]string{"X1-S1": {"X1-S2"}},
	}
}

func roundTripS1() *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, ProjectedProfit: 4000, Legs: []routing.TourLeg{
		leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
		leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
	}}
}

// roundTripS2 clears the reposition floor (ProjectedProfit 100k >> 25k default) and, when
// the loop re-plans it after the jump, its legs execute against repositionFixture's S2
// prices (buy H @100, sell H @300).
func roundTripS2() *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, ProjectedProfit: 100000, Legs: []routing.TourLeg{
		leg("X1-S2-A", "X1-S2", buy("H", 40, 100)),
		leg("X1-S2-B", "X1-S2", sell("H", 40, 300)),
	}}
}

func infeasibleTour() *routing.TourPlan {
	return &routing.TourPlan{Feasible: false, InfeasibleReason: "no_profitable_tour"}
}

// fakeRepositionPersister records every reposition-state write so a restart-resume test can
// prove the in-flight destination is persisted on commit and cleared on landing (RULINGS #2).
type fakeRepositionPersister struct {
	mu     sync.Mutex
	states []RepositionEpisode
}

func (p *fakeRepositionPersister) PersistRepositionState(_ context.Context, _ string, _ int, ep RepositionEpisode) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.states = append(p.states, ep)
	return nil
}

func (p *fakeRepositionPersister) recorded() []RepositionEpisode {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]RepositionEpisode, len(p.states))
	copy(out, p.states)
	return out
}

func plannerVisitedSystem(positions []string, system string) bool {
	for _, p := range positions {
		if shared.ExtractSystemSymbol(p) == system {
			return true
		}
	}
	return false
}

// THE sp-zhii unlock. A continuous tour whose margins die on its home ground (1 productive
// tour then 3 no-plans) must RANK jump-reachable systems, JUMP to the best one, and re-plan
// there — flying a SECOND productive tour at the fresh ground — instead of stranding the
// hull on its own sold-out home. The starvation streak resets after the reposition's
// productive tour, and the run only exits once the destination ALSO dies with no further
// candidate.
func TestTour_MarginsDeath_RanksAndRepositionsToFreshGround(t *testing.T) {
	fx := repositionFixture()
	homeCalls, s2Calls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1() // one productive tour, then...
			}
			return infeasibleTour() // ...margins die (calls 2,3,4 → 3-strike)
		case "X1-S2":
			s2Calls++
			if s2Calls <= 2 {
				return roundTripS2() // pre-flight (clears floor) + first re-plan (productive)
			}
			return infeasibleTour() // then S2 margins die too, no further candidate → exit
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RPZ", PlayerID: 1, ContainerID: "ctr-rpz", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("reposition run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition, got %d (%+v)", r.Repositions, r)
	}
	if r.ToursCompleted != 2 {
		t.Fatalf("expected 2 productive tours (home + fresh ground), got %d", r.ToursCompleted)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q (margins finally died at the destination too)", r.ExitReason, tourExitStarvation)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("expected exactly one jump to X1-S2 (the ranked fresh ground), got %v", fx.jumps)
	}
	if !plannerVisitedSystem(planner.positions, "X1-S2") {
		t.Fatalf("the ranking must have asked the planner to price a tour AT the candidate system X1-S2, positions=%v", planner.positions)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-S2" {
		t.Fatalf("the hull must end at the fresh ground X1-S2, got %q", fx.location)
	}
}

// sp-kl16 THE PD21 PIN (the C1 draw-loop blocker). A heavy whose ORIGIN gate is in the
// ikx1 unreadable-backoff set cannot be repositioned by the strict fetch-through Path — it
// fail-closes on the unreadable gate and returns ErrUnroutable "within 5 jumps" even though a
// short stored-adjacency route exists (the live incident: TORWIND-37->GQ92-D39 and
// TORWIND-2C->GQ92-K79 both died with a 2-hop PD21->KA42->GQ92 route tours fly constantly).
// A tour reposition is a MOVEMENT of the hull to a fresh trading ground, not a commitment of
// money, so — like a scout reposition (sp-8k9m) — it must resolve over the PERSISTED stored
// adjacency (RepositionPath), routing PAST the unreadable gate, under the configured bound.
//
// The fake models the real boundary the pre-fix DiscoversDurableNeighbor test HID: strict Path
// FAILS (pathErr, the unreadable-gate fail-closed), while RepositionPath returns the valid
// stored route. Under the pre-fix strict call (RepositionToWaypoint, bound 0) the reposition
// dies on pathErr; under the fix it flies via the bounded resolver. Asserting the bound reached
// the resolver (repositionBound == the configured default) proves it is RepositionPath, not Path.
func TestTour_MarginsDeath_RepositionsPastUnreadableGateOrigin_ViaStoredAdjacency(t *testing.T) {
	fx := repositionFixture()
	homeCalls, s2Calls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			s2Calls++
			if s2Calls <= 2 {
				return roundTripS2()
			}
			return infeasibleTour()
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	// The origin gate is unreadable (ikx1 backoff): the STRICT fetch-through Path fail-closes
	// on it (pathErr, the verbatim capped-router error), but the stored adjacency retains the
	// route past it (repositionPath). edges wires discovery (origin-independent Connections).
	fake := &fakeGateGraph{
		edges: map[string][]system.GateEdge{
			"X1-S1": {{ConnectedSystem: "X1-S2", GateWaypoint: "X1-S2-GATE", UnderConstruction: false}},
		},
		pathErr:        errors.New("no jump-gate route from X1-S1 to X1-S2 within 5 jumps"),
		repositionPath: []string{"X1-S1", "X1-S2"},
	}
	h.SetGateGraph(fake)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PD21", PlayerID: 1, ContainerID: "ctr-pd21", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("the reposition must fly PAST the unreadable origin gate via stored adjacency, got error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition past the unreadable gate, got %d (%+v)", r.Repositions, r)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("expected exactly one jump to X1-S2 over the stored route, got %v", fx.jumps)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-S2" {
		t.Fatalf("the hull must end at the fresh ground X1-S2, got %q", fx.location)
	}
	// The bound reaching RepositionPath is what proves the reposition used the stored-adjacency
	// resolver (routing past the unreadable gate), not the strict Path that fail-closed.
	if fake.repositionBound != repositionJumpBoundDefault {
		t.Fatalf("the tour reposition must resolve via RepositionPath at the configured default bound %d, got %d", repositionJumpBoundDefault, fake.repositionBound)
	}
}

// sp-1ki5 REPLAY (the X1-DP51 shape). Reposition discovery must find a fresh, direct-gate, BUILT
// neighbor even when the ORIGIN's own jump gate is uncharted or has no ship present — the live
// GetJumpGate API refuses that origin with 4001 "waypoint not accessible", returning ZERO
// neighbors, while the durable era-scoped gate_edges adjacency answers regardless of origin.
// Field (canary st-wisp-3i8ls): a hull 3-struck at X1-DP51, then discovery found NO candidates
// while its direct neighbor X1-GQ92 sat 1-min-fresh; DP51's uncharted origin gate had returned an
// empty live scan. Here fx.neighbors is EMPTY for the home system (the exact 4001 shape) yet the
// wired durable graph reports home->fresh-ground built, so the reposition still fires. The
// working-origin case (live scan answers) stays covered by
// TestTour_MarginsDeath_RanksAndRepositionsToFreshGround, which wires NO gate graph and remains
// green — proving the durable-first path does not disturb charted origins.
func TestTour_MarginsDeath_DiscoversDurableNeighbor_WhenLiveGateApiRefusesOrigin(t *testing.T) {
	fx := repositionFixture()
	// The DP51 discriminator: the LIVE jump-gate scan returns nothing from the origin (uncharted
	// gate / no ship present -> API 4001). Pre-sp-1ki5 this alone zeroed the candidate set.
	fx.neighbors = map[string][]string{}

	homeCalls, s2Calls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1() // one productive tour, then margins die (3-strike)
			}
			return infeasibleTour()
		case "X1-S2":
			s2Calls++
			if s2Calls <= 2 {
				return roundTripS2() // pre-flight (clears floor) + first re-plan (productive)
			}
			return infeasibleTour()
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	// The durable era-scoped gate_edges adjacency (origin-independent): home X1-S1 -> fresh X1-S2,
	// gate built. This is the DP51->GQ92 (era 2, under_construction=f) row the live API could not
	// surface from DP51's uncharted origin gate. `edges` drives discovery (Connections); `path`
	// is the same direct edge resolved for the physical reposition hop (jumpPath -> Path), which
	// in production also reads the durable cache, never DP51's refused live gate.
	h.SetGateGraph(&fakeGateGraph{
		edges: map[string][]system.GateEdge{
			"X1-S1": {{ConnectedSystem: "X1-S2", GateWaypoint: "X1-S2-GATE", UnderConstruction: false}},
		},
		path: []string{"X1-S1", "X1-S2"},
	})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DP51", PlayerID: 1, ContainerID: "ctr-dp51", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("durable-neighbor reposition run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("the durable gate_edges neighbor must be discovered and repositioned to DESPITE the empty live gate scan, got %d repositions (%+v)", r.Repositions, r)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("expected exactly one jump to the durable fresh ground X1-S2, got %v", fx.jumps)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-S2" {
		t.Fatalf("the hull must end at the durable fresh ground X1-S2, got %q", fx.location)
	}
}

// sp-1ki5 #3: an EMPTY reposition discovery must name WHY per rejected neighbor, so a "no
// candidates" verdict is self-diagnosing and never again costs a canary flight. Here the durable
// graph reports one real neighbor whose gate is still UNDER CONSTRUCTION — it cannot be a
// candidate (a jump would crash at hop time) — and the scan LOGS it as "unbuilt" instead of the
// pre-fix silent empty.
func TestReposition_EmptyDiscovery_LogsPerNeighborReason(t *testing.T) {
	fx := repositionFixture()
	fx.neighbors = map[string][]string{} // live scan empty (uncharted-origin shape)
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	h.SetGateGraph(&fakeGateGraph{edges: map[string][]system.GateEdge{
		"X1-S1": {{ConnectedSystem: "X1-S2", GateWaypoint: "X1-S2-GATE", UnderConstruction: true}},
	}})

	logger := &tradeCaptureLogger{}
	ctx := common.WithLogger(context.Background(), logger)
	cands := h.buildRepositionCandidates(ctx, &RunTourCoordinatorCommand{ShipSymbol: "TOUR-LOG", PlayerID: 1}, "X1-S1")

	if len(cands) != 0 {
		t.Fatalf("an under-construction neighbor must not become a candidate, got %d", len(cands))
	}
	if !logger.loggedContaining("X1-S2", "unbuilt") {
		t.Fatalf("empty discovery must log the per-neighbor rejection reason 'unbuilt' for X1-S2, got:\n%s", strings.Join(logger.messages, "\n"))
	}
}

// The reposition floor (RULINGS #5) gates the jump on planned FRESH profit: a candidate the
// planner CAN tour but only marginally (below the floor) is NOT worth the antimatter/fuel/
// time of the hop, so the run exits honestly WITHOUT jumping — exactly as pre-sp-zhii.
func TestTour_MarginsDeath_BelowFloorExitsHonestlyWithoutJumping(t *testing.T) {
	fx := repositionFixture()
	homeCalls := 0
	lowProfit := &routing.TourPlan{Feasible: true, ProjectedProfit: 1000, Legs: []routing.TourLeg{
		leg("X1-S2-A", "X1-S2", buy("H", 40, 100)),
		leg("X1-S2-B", "X1-S2", sell("H", 40, 300)),
	}}
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			return lowProfit // feasible but 1000 << 25000 floor
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-FLOOR", PlayerID: 1, ContainerID: "ctr-floor", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("below-floor run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 0 {
		t.Fatalf("a below-floor candidate must NOT trigger a jump, got %d repositions", r.Repositions)
	}
	if len(fx.jumps) != 0 {
		t.Fatalf("no jump may be dispatched when nothing clears the floor, got %v", fx.jumps)
	}
	if !plannerVisitedSystem(planner.positions, "X1-S2") {
		t.Fatalf("the candidate must still have been RANKED (planner priced it) before being rejected on the floor, positions=%v", planner.positions)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q (margins died, no ground worth the jump)", r.ExitReason, tourExitStarvation)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-S1" {
		t.Fatalf("the hull must stay on its home ground when no reposition clears the floor, got %q", fx.location)
	}
}

// One reposition per episode: if the destination the hull jumped to ALSO dies (a fresh
// 3-strike with no productive tour there), the run exits honestly — and the reason NAMES
// BOTH the origin and the destination system, never hop-scotching onward.
func TestTour_MarginsDeath_DestinationAlsoDies_NamesBothSystems(t *testing.T) {
	fx := repositionFixture()
	homeCalls, s2Calls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			s2Calls++
			if s2Calls == 1 {
				return roundTripS2() // pre-flight clears floor → jump commits
			}
			return infeasibleTour() // but the destination is dead on arrival → 3-strike there too
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-BOTH", PlayerID: 1, ContainerID: "ctr-both", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("destination-also-dies run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition (bounded per episode — no hop-scotching), got %d", r.Repositions)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("expected exactly one jump to X1-S2, got %v", fx.jumps)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q", r.ExitReason, tourExitStarvation)
	}
	if !strings.Contains(r.ExitDetail, "X1-S1") || !strings.Contains(r.ExitDetail, "X1-S2") {
		t.Fatalf("the honest exit detail must NAME BOTH systems (origin X1-S1 and destination X1-S2), got %q", r.ExitDetail)
	}
}

// The kill-switch: with reposition disabled, a margins-died continuous tour exits exactly
// as it did pre-sp-zhii — no ranking, no jump — even when a fresh ground is reachable.
func TestTour_MarginsDeath_RepositionDisabled_ExitsWithoutJumping(t *testing.T) {
	fx := repositionFixture()
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == "X1-S1" {
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-OFF", PlayerID: 1, ContainerID: "ctr-off", Iterations: -1,
		RepositionDisabled: true, ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("kill-switch run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("reposition disabled must never rank or jump, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if plannerVisitedSystem(planner.positions, "X1-S2") {
		t.Fatalf("reposition disabled must never even RANK a candidate (no planner call at X1-S2), positions=%v", planner.positions)
	}
	if r.ToursCompleted != 1 || r.ExitReason != tourExitStarvation {
		t.Fatalf("expected the pre-sp-zhii honest exit (1 tour, starvation), got tours=%d reason=%q", r.ToursCompleted, r.ExitReason)
	}
}

// Reposition is scoped to CONTINUOUS (-1) runs (requirement #7): a FINITE-iteration run
// whose margins die mid-budget exits as today (no reposition), never rotating grounds.
func TestTour_MarginsDeath_FiniteRunNeverRepositions(t *testing.T) {
	fx := repositionFixture()
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == "X1-S1" {
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-FIN", PlayerID: 1, ContainerID: "ctr-fin", Iterations: 5, // finite, not -1
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("finite run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 0 || len(fx.jumps) != 0 {
		t.Fatalf("a finite-iteration run must never reposition, got %d repositions / jumps %v", r.Repositions, fx.jumps)
	}
	if r.ToursCompleted != 1 || r.ExitReason != tourExitStarvation {
		t.Fatalf("expected the finite run to exit starvation after its one productive tour, got tours=%d reason=%q", r.ToursCompleted, r.ExitReason)
	}
}

// Ranking picks the BEST candidate by planned tour margin, not merely the first reachable
// one: with two jump-reachable fresh grounds, the higher-projected-profit system wins the
// jump.
func TestTour_MarginsDeath_RanksCandidatesByExpectedMargin(t *testing.T) {
	fx := repositionFixture()
	// Add a second neighbor X1-S3 with its own lane; make S3's planned tour richer than S2's.
	fx.markets["X1-S3"] = []string{"X1-S3-A", "X1-S3-B"}
	fx.ask["X1-S3-A"] = map[string]int{"J": 100}
	fx.ask["X1-S3-B"] = map[string]int{"J": 400}
	fx.bid["X1-S3-B"] = map[string]int{"J": 400}
	fx.tv["X1-S3-A"] = map[string]int{"J": 1000}
	fx.tv["X1-S3-B"] = map[string]int{"J": 1000}
	fx.neighbors["X1-S1"] = []string{"X1-S2", "X1-S3"}

	homeCalls, s2Calls, s3Calls := 0, 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			s2Calls++
			if s2Calls == 1 {
				return &routing.TourPlan{Feasible: true, ProjectedProfit: 30000} // above floor, but modest
			}
			return infeasibleTour()
		case "X1-S3":
			s3Calls++
			if s3Calls == 1 {
				return &routing.TourPlan{Feasible: true, ProjectedProfit: 90000} // the richer ground → chosen
			}
			return infeasibleTour()
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RANK", PlayerID: 1, ContainerID: "ctr-rank", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("ranking run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected one reposition, got %d", r.Repositions)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S3" {
		t.Fatalf("expected the jump to target X1-S3 (higher planned margin), got %v", fx.jumps)
	}
	// Both candidates must have been priced by the planner (the ranking evaluated them).
	if !plannerVisitedSystem(planner.positions, "X1-S2") || !plannerVisitedSystem(planner.positions, "X1-S3") {
		t.Fatalf("both candidates must be ranked (planner priced each), positions=%v", planner.positions)
	}
}

// RULINGS #2 restart-resume: a run re-adopted with a persisted in-flight reposition
// completes the jump toward the SAME destination (through the shared travel machinery),
// CLEARS the persisted flag, then re-plans at the destination — never re-planning at the
// intermediate position it was re-adopted on.
func TestTour_RepositionRestartResume_CompletesJumpThenReplans(t *testing.T) {
	fx := repositionFixture() // hull sits at X1-S1-A, as if re-adopted mid-jump
	s2Calls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == "X1-S2" {
			s2Calls++
			if s2Calls == 1 {
				return roundTripS2() // one productive tour at the resumed destination
			}
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	persister := &fakeRepositionPersister{}
	h.SetRepositionPersister(persister)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RESUME", PlayerID: 1, ContainerID: "ctr-resume", Iterations: -1,
		RepositionInProgress: true, RepositionTargetSystem: "X1-S2", RepositionTargetWaypoint: "X1-S2-A",
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("restart-resume run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("the resume must COMPLETE the jump toward the persisted destination X1-S2, got jumps %v", fx.jumps)
	}
	if r.ToursCompleted != 1 {
		t.Fatalf("after resuming to X1-S2 the loop must re-plan and fly a tour there, got ToursCompleted=%d", r.ToursCompleted)
	}
	// The persisted in-flight flag must be CLEARED once the jump landed (InProgress=false),
	// so a second restart does not re-resume a completed reposition.
	cleared := false
	for _, ep := range persister.recorded() {
		if !ep.InProgress {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("the resume must clear the persisted reposition state (InProgress=false) after landing, recorded=%+v", persister.recorded())
	}
}

// sp-kl16: the RESTART-RESUME jump (run_tour_coordinator.go, re-adopted mid-jump) must ride the
// SAME bounded stored-adjacency resolver as the fresh margins-death jump — not the strict Path.
// Otherwise a hull re-adopted mid-jump toward an unreadable-gate ground would re-hit the strict
// fail-close on every restart and crash-loop (the o34q live shape on the resume leg). The fake
// makes strict Path FAIL and RepositionPath succeed, so a resume that completes proves it used
// the bounded resolver; the bound reaching RepositionPath confirms it (== the configured default).
func TestTour_RepositionRestartResume_UsesBoundedResolverPastUnreadableGate(t *testing.T) {
	fx := repositionFixture()
	s2Calls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == "X1-S2" {
			s2Calls++
			if s2Calls == 1 {
				return roundTripS2()
			}
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetRepositionPersister(&fakeRepositionPersister{})
	fake := &fakeGateGraph{
		pathErr:        errors.New("no jump-gate route from X1-S1 to X1-S2 within 5 jumps"),
		repositionPath: []string{"X1-S1", "X1-S2"},
	}
	h.SetGateGraph(fake)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RESUME-PD21", PlayerID: 1, ContainerID: "ctr-resume-pd21", Iterations: -1,
		RepositionInProgress: true, RepositionTargetSystem: "X1-S2", RepositionTargetWaypoint: "X1-S2-A",
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("the resume must complete the jump PAST the unreadable gate via stored adjacency, got error: %v", err)
	}
	r := tourResponse(t, resp)
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("the resume must complete the jump to X1-S2 over the stored route, got %v", fx.jumps)
	}
	if r.ToursCompleted != 1 {
		t.Fatalf("after resuming to X1-S2 the loop must fly a tour there, got ToursCompleted=%d", r.ToursCompleted)
	}
	if fake.repositionBound != repositionJumpBoundDefault {
		t.Fatalf("the resume must resolve via RepositionPath at the configured default bound %d, got %d", repositionJumpBoundDefault, fake.repositionBound)
	}
}

// The reposition COMMIT persists the in-flight destination BEFORE the jump (so a restart
// mid-jump resumes toward it) and clears it AFTER landing — the write ordering RULINGS #2
// depends on.
func TestTour_Reposition_PersistsInFlightDestinationThenClears(t *testing.T) {
	fx := repositionFixture()
	homeCalls, s2Calls := 0, 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		case "X1-S2":
			s2Calls++
			if s2Calls == 1 {
				return roundTripS2()
			}
			return infeasibleTour()
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	persister := &fakeRepositionPersister{}
	h.SetRepositionPersister(persister)

	if _, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PERSIST", PlayerID: 1, ContainerID: "ctr-persist", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("persist-ordering run returned error: %v", err)
	}

	states := persister.recorded()
	// First write of the episode is the COMMIT: InProgress=true, target named. A later
	// write clears it (InProgress=false) once the jump landed.
	var committed, clearedAfter bool
	for _, ep := range states {
		if ep.InProgress && ep.TargetSystem == "X1-S2" && ep.TargetWaypoint == "X1-S2-A" {
			committed = true
		}
		if committed && !ep.InProgress {
			clearedAfter = true
		}
	}
	if !committed {
		t.Fatalf("the reposition must persist the in-flight destination (in_progress=true, target X1-S2/X1-S2-A) before jumping, got %+v", states)
	}
	if !clearedAfter {
		t.Fatalf("the reposition must clear the persisted state (in_progress=false) after the jump landed, got %+v", states)
	}
}

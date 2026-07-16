package commands

// run_tour_coordinator_reposition_offcircuit_test.go — sp-jeou: a trade hull bought at a FAR,
// off-circuit yard (X1-UF64, the only heavy yard) whose 1-gate-hop neighbours carry no fresh
// cached market found ZERO reposition candidates and stranded, even though a profitable circuit
// system sat 2-4 gate hops away — well within the 12-jump bound the reposition FLIGHT already
// routes over. maybeReposition's candidate DISCOVERY was 1-hop while its travel reach was 12.
// The fix broadens discovery (buildRepositionCandidates) to a bounded multi-hop BFS over the
// DURABLE gate graph ONLY when the 1-hop scan is empty, so discovery reach == travel reach and
// the adopted off-circuit hull reposositions to the circuit via the existing machinery.

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// jeouOffCircuitFixture models the 6C/6D strand: the hull starts and dies at the FAR yard
// X1-UF64 (off-circuit). Its ONLY 1-gate-hop neighbour X1-MID carries no cached market, so the
// pre-fix 1-hop reposition discovery yields ZERO candidates. Two gate hops out, X1-SH23 is a
// fresh circuit ground with its own arb lane (buy H@100, sell H@300). The durable gate graph
// (wired below, mirroring production's main.go SetGateGraph) exposes UF64->MID->SH23, and its
// RepositionPath resolves the physical 2-hop flight.
func jeouOffCircuitFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-UF64-AX2B", cargoCap: 100,
		markets: map[string][]string{
			"X1-UF64": {"X1-UF64-AX2B"}, // the far heavy yard: a market, but no profitable local tour
			// X1-MID: no markets entry -> the 1-hop neighbour is barren (the strand trigger)
			"X1-SH23": {"X1-SH23-A", "X1-SH23-B"}, // the circuit ground, 2 gate hops out
		},
		bid: map[string]map[string]int{
			"X1-SH23-B": {"H": 300},
		},
		ask: map[string]map[string]int{
			"X1-SH23-A": {"H": 100}, "X1-SH23-B": {"H": 300},
		},
		tv: map[string]map[string]int{
			"X1-SH23-A": {"H": 1000}, "X1-SH23-B": {"H": 1000},
		},
		// The LIVE jump-gate scan is barren for the far yard (the uncharted-origin shape); the
		// DURABLE gate graph is what exposes the multi-hop route, exactly as in production.
		neighbors: map[string][]string{},
	}
}

// roundTripSH23 clears the reposition floor (100k >> 25k) and, re-planned after the jump,
// executes against jeouOffCircuitFixture's SH23 prices (buy H@100, sell H@300).
func roundTripSH23() *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, ProjectedProfit: 100000, Legs: []routing.TourLeg{
		leg("X1-SH23-A", "X1-SH23", buy("H", 40, 100)),
		leg("X1-SH23-B", "X1-SH23", sell("H", 40, 300)),
	}}
}

// THE sp-jeou UNLOCK (RED before the fix, GREEN after). A continuous tour on a hull stranded at
// the far off-circuit yard X1-UF64 — whose only 1-hop neighbour is barren — must BROADEN its
// reposition discovery over the durable gate graph, find the profitable circuit ground X1-SH23
// two gate hops away (within the 12-jump bound), JUMP there via the existing flight, and trade.
// Before the fix the 1-hop-only discovery yields zero candidates and the hull strands forever.
func TestTour_OffCircuit_BroadensDiscovery_RepositionsToCircuit(t *testing.T) {
	fx := jeouOffCircuitFixture()
	sh23Calls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-UF64":
			return infeasibleTour() // no profitable tour at the far yard -> margins die, 3-strike
		case "X1-SH23":
			sh23Calls++
			if sh23Calls <= 2 {
				return roundTripSH23() // pre-flight (clears floor) + first re-plan (productive)
			}
			return infeasibleTour() // then the circuit ground taps out too -> honest exit
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	// The DURABLE era-scoped gate_edges adjacency (origin-independent): UF64 -> MID -> SH23, all
	// gates built. Connections drives the broadened BFS discovery; repositionPath resolves the
	// physical 2-hop flight past the barren interior hop. This is the graph production wires via
	// tourCoordinatorHandler.SetGateGraph(gateGraphService) (main.go).
	h.SetGateGraph(&fakeGateGraph{
		edges: map[string][]system.GateEdge{
			"X1-UF64": {{ConnectedSystem: "X1-MID", GateWaypoint: "X1-MID-GATE", UnderConstruction: false}},
			"X1-MID":  {{ConnectedSystem: "X1-SH23", GateWaypoint: "X1-SH23-GATE", UnderConstruction: false}},
		},
		repositionPath: []string{"X1-UF64", "X1-MID", "X1-SH23"},
	})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-6C", PlayerID: 1, ContainerID: "ctr-6c", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("the off-circuit hull must broaden discovery and reach the circuit, got error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition off the far yard to the circuit, got %d (%+v)", r.Repositions, r)
	}
	if r.ToursCompleted != 1 {
		t.Fatalf("expected one productive tour at the circuit ground after repositioning, got %d", r.ToursCompleted)
	}
	if !plannerVisitedSystem(planner.positions, "X1-SH23") {
		t.Fatalf("the broadened discovery must have priced a tour AT the 2-hop circuit system X1-SH23, positions=%v", planner.positions)
	}
	if !containsSystem(fx.jumps, "X1-SH23") {
		t.Fatalf("the hull must have jumped to the circuit system X1-SH23, jumps=%v", fx.jumps)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-SH23" {
		t.Fatalf("the hull must end at the circuit ground X1-SH23, got %q", fx.location)
	}
}

// REGRESSION (the byte-for-byte invariant). When the origin has a fresh-market 1-gate-hop
// candidate, discovery must NOT broaden — the hull reposositions to that 1-hop ground exactly as
// before, and a RICHER system reachable only at 2 hops is never even discovered or priced. This
// pins that the broadening is gated STRICTLY on a zero-candidate 1-hop scan, so on-circuit
// behaviour is unchanged. Home X1-S1 dies; X1-S2 (1-hop) clears the floor; X1-S3 (2 hops, via the
// barren interior X1-MID) is RICHER and would be chosen IF broadening wrongly fired.
func TestTour_OnCircuit_OneHopCandidate_DoesNotBroaden(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-S1": {"X1-S1-A", "X1-S1-B"},
			"X1-S2": {"X1-S2-A", "X1-S2-B"},
			// X1-MID barren; X1-S3 is the 2-hop RICHER ground the fallback would surface if it fired
			"X1-S3": {"X1-S3-A", "X1-S3-B"},
		},
		bid: map[string]map[string]int{"X1-S1-B": {"G": 200}, "X1-S2-B": {"H": 300}, "X1-S3-B": {"K": 900}},
		ask: map[string]map[string]int{
			"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200},
			"X1-S2-A": {"H": 100}, "X1-S2-B": {"H": 300},
			"X1-S3-A": {"K": 100}, "X1-S3-B": {"K": 900},
		},
		tv: map[string]map[string]int{
			"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000},
			"X1-S2-A": {"H": 1000}, "X1-S2-B": {"H": 1000},
			"X1-S3-A": {"K": 1000}, "X1-S3-B": {"K": 1000},
		},
		neighbors: map[string][]string{},
	}
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
				return roundTripS2() // the 1-hop reposition target: pre-flight + one productive re-plan
			}
			return infeasibleTour()
		case "X1-S3":
			// The 2-hop RICHER ground: only reachable IF discovery wrongly broadened. If the
			// planner is ever asked to price it, broadening fired — the regression has failed.
			return &routing.TourPlan{Feasible: true, ProjectedProfit: 500000, Legs: []routing.TourLeg{
				leg("X1-S3-A", "X1-S3", buy("K", 40, 100)),
				leg("X1-S3-B", "X1-S3", sell("K", 40, 900)),
			}}
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	// 1-hop candidate S2 present; S3 only at 2 hops via the barren X1-MID. S2 has NO onward edge,
	// so after S2 taps out the run exits (S2's own 1-hop is empty and broadening from S2 finds
	// nothing) — bounding the test to the single first reposition under assertion.
	h.SetGateGraph(&fakeGateGraph{
		edges: map[string][]system.GateEdge{
			"X1-S1":  {{ConnectedSystem: "X1-S2", GateWaypoint: "X1-S2-GATE"}, {ConnectedSystem: "X1-MID", GateWaypoint: "X1-MID-GATE"}},
			"X1-MID": {{ConnectedSystem: "X1-S3", GateWaypoint: "X1-S3-GATE"}},
		},
		repositionPath: []string{"X1-S1", "X1-S2"},
	})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-ONHOP", PlayerID: 1, ContainerID: "ctr-onhop", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("on-circuit reposition returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition to the 1-hop ground, got %d (%+v)", r.Repositions, r)
	}
	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-S2" {
		t.Fatalf("the hull must reposition to the 1-hop ground X1-S2 in a single hop (no broadening), jumps=%v", fx.jumps)
	}
	if plannerVisitedSystem(planner.positions, "X1-S3") {
		t.Fatalf("the 2-hop richer ground X1-S3 must NEVER be discovered/priced when a 1-hop candidate exists — broadening wrongly fired, positions=%v", planner.positions)
	}
	if shared.ExtractSystemSymbol(fx.location) != "X1-S2" {
		t.Fatalf("the hull must end at the 1-hop ground X1-S2, got %q", fx.location)
	}
}

// containsSystem reports whether any recorded jump destination is in the given system.
func containsSystem(jumps []string, sys string) bool {
	for _, j := range jumps {
		if shared.ExtractSystemSymbol(j) == sys || j == sys {
			return true
		}
	}
	return false
}

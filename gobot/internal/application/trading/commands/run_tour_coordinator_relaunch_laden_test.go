package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// relaunchLadenFixture is the restart shape: the hull comes up already holding 80/100 PARTS on
// home X1-L1, which has no sink for them. X1-L2 is a rich fresh-arb ground that pays nothing for
// PARTS; X1-L3 has no fresh arb at all and IMPORTS PARTS at 500. Only one of the two can actually
// move this hull's load.
func relaunchLadenFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{"PARTS": 80}, location: "X1-L1-A", cargoCap: 100,
		markets: map[string][]string{
			"X1-L1": {"X1-L1-A"},
			"X1-L2": {"X1-L2-A", "X1-L2-B"},
			"X1-L3": {"X1-L3-A"},
		},
		ask: map[string]map[string]int{
			"X1-L1-A": {"PARTS": 900},
			"X1-L2-A": {"H": 100}, "X1-L2-B": {"H": 300},
			"X1-L3-A": {"PARTS": 999},
		},
		bid: map[string]map[string]int{
			"X1-L2-B": {"H": 300},
			"X1-L3-A": {"PARTS": 500},
		},
		tv: map[string]map[string]int{
			"X1-L1-A": {"PARTS": 1000},
			"X1-L2-A": {"H": 1000}, "X1-L2-B": {"H": 1000},
			"X1-L3-A": {"PARTS": 1000},
		},
		tradeType: map[string]map[string]string{
			"X1-L3-A": {"PARTS": "IMPORT"},
		},
		neighbors: map[string][]string{"X1-L1": {"X1-L2", "X1-L3"}},
	}
}

// freshArbL2 is the tempting ground: it clears the reposition floor (100k >> 25k) on a hold the
// arriving hull will not have, because its own cargo is still aboard.
func freshArbL2() *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, ProjectedProfit: 100000, Legs: []routing.TourLeg{
		leg("X1-L2-A", "X1-L2", buy("H", 40, 100)),
		leg("X1-L2-B", "X1-L2", sell("H", 40, 300)),
	}}
}

// liquidatePartsAtL3 is the sell-only plan the live re-plan finds once the hull arrives at the
// sink carrying its load.
func liquidatePartsAtL3() *routing.TourPlan {
	return &routing.TourPlan{Feasible: true, ProjectedProfit: 40000, HeldLiquidation: 40000, Legs: []routing.TourLeg{
		leg("X1-L3-A", "X1-L3", sell("PARTS", 80, 500)),
	}}
}

// relaunchLadenPlanner answers per system and per hold. X1-L3 prices infeasible for the fresh-arb
// pre-flight, which clears the hull's cargo before ranking, and prices the liquidation for the live
// re-plan that still carries it — the exact asymmetry the ladder order has to read.
func relaunchLadenPlanner() *tourFakeRoutingClient {
	l2Calls := 0
	return &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-L2":
			l2Calls++
			if l2Calls <= 3 {
				return freshArbL2()
			}
		case "X1-L3":
			if ship.Cargo["PARTS"] > 0 {
				return liquidatePartsAtL3()
			}
		}
		return infeasibleTour()
	}}
}

// A hull whose container died mid-tour comes back holding the load and no record of where it was
// taking it. The rescue must send it to a buyer for what it holds (X1-L3), not to the ground with
// the best fresh spread (X1-L2) — that spread is priced against a cleared hold the hull will not
// arrive with, so a jump there strands the load one system further out and burns another
// veto-relaunch cycle to discover it.
func TestTour_RelaunchedLaden_JumpsToTheSinkForItsLoadNotTheFreshArbGround(t *testing.T) {
	fx := relaunchLadenFixture()
	planner := relaunchLadenPlanner()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RELAUNCH", PlayerID: 1, ContainerID: "ctr-relaunch", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("relaunched-laden run returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-L3" {
		t.Fatalf("expected exactly one jump, to the PARTS sink X1-L3, got %v (X1-L2 is the fresh-arb ground the hull has no hold for)", fx.jumps)
	}
	if fx.cargo["PARTS"] != 0 {
		t.Fatalf("the inherited load must be sold at the sink, %d PARTS still aboard", fx.cargo["PARTS"])
	}
	if r.ToursCompleted != 1 {
		t.Fatalf("expected the post-jump liquidation to count as the one productive tour, got %d (%+v)", r.ToursCompleted, r)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("exit reason = %q, want %q (the sink system has no fresh arb, so the run still exits honestly)", r.ExitReason, tourExitStarvation)
	}
}

// The offload-first order must not cost a laden hull its fresh-arb rescue. With no reachable buyer
// for the held good anywhere, the ladder has to fall through and rotate the hull to X1-L2 exactly
// as it does today.
func TestTour_RelaunchedLaden_NoReachableSink_StillTakesTheFreshArbRescue(t *testing.T) {
	fx := relaunchLadenFixture()
	fx.bid["X1-L3-A"] = map[string]int{} // nothing anywhere bids on the held PARTS
	planner := relaunchLadenPlanner()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RELAUNCH-NOSINK", PlayerID: 1, ContainerID: "ctr-relaunch-nosink", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("relaunched-laden run with no reachable sink returned error: %v", err)
	}
	r := tourResponse(t, resp)

	if len(fx.jumps) != 1 || fx.jumps[0] != "X1-L2" {
		t.Fatalf("with no buyer for the held load the hull must still take the fresh-arb rescue to X1-L2, got %v", fx.jumps)
	}
	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition, got %d (%+v)", r.Repositions, r)
	}
}

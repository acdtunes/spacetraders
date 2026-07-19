package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// Closed-tour mode must ride cmd.ClosedTours/cmd.AnchorSystem onto the
// TourConstraints the planner receives — the cmd→cons hop in planForState (the same
// wiring seam pinned for MaxTourSystems). An armed closure reaches the
// planner verbatim; the companion below pins the dormant default.
func TestTour_PlannerReceivesClosure(t *testing.T) {
	fx := dynamicCapFixture()
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{roundTripPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-CLS", PlayerID: 1, ContainerID: "ctr-cls",
		MaxSpend:          200_000, // explicit cap keeps this off the dynamic-treasury path
		ClosedTours:       true,
		AnchorSystem:      "X1-HOME",
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	if len(planner.closed) == 0 || !planner.closed[0] {
		t.Fatalf("planner closed = %v, want first = true (cmd.ClosedTours must ride cons.Closed to the solver)", planner.closed)
	}
	if len(planner.anchorSystems) == 0 || planner.anchorSystems[0] != "X1-HOME" {
		t.Fatalf("planner anchor-systems = %v, want first = %q (cmd.AnchorSystem must ride cons.AnchorSystem)", planner.anchorSystems, "X1-HOME")
	}
}

// Default-safety companion: unset closure reaches the planner as false/"" —
// the proto3 zero-values that stay OFF the wire — so a run that never opts in plans
// open tours byte-identical to today. Guards against a threading bug that hardwires
// closure on for everyone.
func TestTour_PlannerReceivesOpenDefaultsWhenClosureUnset(t *testing.T) {
	fx := dynamicCapFixture()
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{roundTripPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-CLS0", PlayerID: 1, ContainerID: "ctr-cls0",
		MaxSpend:          200_000, // ClosedTours/AnchorSystem deliberately left unset
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	if len(planner.closed) == 0 || planner.closed[0] {
		t.Fatalf("planner closed = %v, want first = false (unset → open, the dormant default)", planner.closed)
	}
	if len(planner.anchorSystems) == 0 || planner.anchorSystems[0] != "" {
		t.Fatalf("planner anchor-systems = %v, want first = \"\" (unset → floating anchor untouched)", planner.anchorSystems)
	}
}

// Execution-side pin (Contract B): the closure epilogue APPENDS a trade-less
// return leg, so a closed plan's final leg carries zero trades. The executor must fly,
// dock and COUNT that leg like any other — no validation may skip or reject it, or a
// closed tour silently never returns home. Two trades on three legs proves the return
// hop added a leg but no trade.
func TestTour_ExecutorFliesTradelessReturnLeg(t *testing.T) {
	fx := dynamicCapFixture()
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true,
		Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
			leg("X1-S1-A", "X1-S1"), // the appended no-trade return hop
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-RET", PlayerID: 1, ContainerID: "ctr-ret",
		MaxSpend:          200_000,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour with a trade-less return leg returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if r.LegsExecuted != 3 {
		t.Fatalf("LegsExecuted = %d, want 3 (the no-trade return hop must be flown and counted)", r.LegsExecuted)
	}
	if r.TradesExecuted != 2 {
		t.Fatalf("TradesExecuted = %d, want 2 (buy+sell only — the return leg trades nothing)", r.TradesExecuted)
	}
}

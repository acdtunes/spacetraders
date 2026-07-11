package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// sp-trnp regression: a tour whose PLAN opens a cross-system leg from a NON-gate origin
// waypoint must fly the gate-transit hop (navigate waypoint->source jump gate) BEFORE the
// jump, exactly as the CLI jump verb and the trade-route circuit already do.
//
// The live incident: TORWIND-37 sat at X1-DP51-X11A (a market, NOT the gate) when its
// restored multi-system plan (sp-4hl5 re-enabled the heavy fleet's tours) opened leg 1 with a
// jump to X1-NK36. The executor trusted the hull's position and fired the jump from the
// market; the driveless-hull jump API hard-rejected it ("cannot jump: ... not at a jump gate")
// and the tour crashed. No test exercised the tour's EXECUTE path with a planned cross-system
// leg — the existing cross-system tour tests all drive the reposition (margins-death) jump.
//
// The tour flies its legs through the SHARED travel() primitive
// (RunTradeRouteCoordinatorHandler.travel), which already carries the sp-5nqx departure hop, so
// the fix is structural: the tour inherits gate transit by delegating to that helper rather
// than owning a second, gate-blind jump path. This pins that the tour's execute path actually
// exercises it. The fake jump here ENFORCES the engine precondition (jumpRequiresGate), so a
// regression that skipped the departure hop reproduces the exact crash — a failing test, not a
// silent pass.
func TestTour_CrossSystemLegFromNonGateOrigin_TransitsGateThenJumps(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-DP51-X11A", cargoCap: 100,
		jumpRequiresGate: true, // reproduce the live jump precondition the crash tripped
		markets: map[string][]string{
			"X1-DP51": {"X1-DP51-X11A"},
			"X1-NK36": {"X1-NK36-A", "X1-NK36-B"},
		},
		bid: map[string]map[string]int{
			"X1-DP51-X11A": {"G0": 90},
			"X1-NK36-B":    {"G1": 200},
		},
		ask: map[string]map[string]int{
			"X1-DP51-X11A": {"G0": 100},
			"X1-NK36-A":    {"G1": 100}, "X1-NK36-B": {"G1": 200},
		},
		tv: map[string]map[string]int{
			"X1-DP51-X11A": {"G0": 1000},
			"X1-NK36-A":    {"G1": 1000}, "X1-NK36-B": {"G1": 1000},
		},
	}
	// The plan the restored multi-system solver produces: the hull at X1-DP51 must cross to
	// X1-NK36 for leg 1 (a jump), buy there, then sell within NK36 — ending empty (no strand).
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, ProjectedProfit: 4000, Legs: []routing.TourLeg{
			leg("X1-NK36-A", "X1-NK36", buy("G1", 40, 100)),
			leg("X1-NK36-B", "X1-NK36", sell("G1", 40, 200)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-37", PlayerID: 1, ContainerID: "ctr-trnp", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a cross-system tour leg from a non-gate origin must transit the gate then jump, not crash: %v", err)
	}
	r := tourResponse(t, resp)
	if !r.Completed {
		t.Fatalf("expected a completed tour, got %+v", r)
	}
	if ok, reason := r.CompletionOutcome(); !ok {
		t.Fatalf("expected honest completion, got veto: %s", reason)
	}

	fx.mu.Lock()
	jumps := append([]string(nil), fx.jumps...)
	navDests := append([]string(nil), fx.navDests...)
	timeline := strings.Join(fx.timeline, ",")
	buys, sells := fx.buys, fx.sells
	fx.mu.Unlock()

	// The hull jumped exactly once, to the leg's system — the cross-system transit executed.
	if len(jumps) != 1 || jumps[0] != "X1-NK36" {
		t.Fatalf("expected exactly one jump to X1-NK36, got %v", jumps)
	}
	// The FIRST movement was the departure hop to the SOURCE system's jump gate: the hull flew
	// waypoint->gate BEFORE the jump. Without it the jump fires from the market and crashes
	// (the sp-trnp incident), which jumpRequiresGate turns into a hard error above.
	if len(navDests) == 0 || navDests[0] != "X1-DP51-GATE" {
		t.Fatalf("expected the first navigate to be the departure hop to the source gate X1-DP51-GATE, got %v", navDests)
	}
	// Both trades executed against NK36's markets (buy then sell), so the hull ends empty and
	// the cross-system tour completes honestly rather than stranding the bought tranche.
	if buys != 1 || sells != 1 {
		t.Fatalf("expected one buy and one sell across the cross-system tour, got %d buys / %d sells", buys, sells)
	}
	if want := "BUY:G1,SELL:G1"; timeline != want {
		t.Fatalf("trade timeline = %q, want %q", timeline, want)
	}
}

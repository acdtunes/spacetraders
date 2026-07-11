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

// sp-trnp — the ACTUAL live crash shape (the leg-0 pin above missed it): a cross-system leg
// opens as leg 1, AFTER an intra-system leg 0 leaves the hull physically at a market. The
// departure hop then flies market->gate, but its navigate reports completion on a stale
// "left transit" resync (arrival_wait.go's pre-ETA safety poll, the nav-cache race) BEFORE
// the hull reaches the gate. On current main the leg-1 jump fired from the market and
// hard-crashed "cannot jump: not at a jump gate" (TORWIND-37: sold ADVANCED_CIRCUITRY at
// X1-DP51-X11A on leg 0, then crashed opening the leg-1 jump toward X1-NK36 while its
// departure hop toward gate X1-DP51-B26F had "completed" ~2m early). The fix re-confirms the
// hull on the gate via an authoritative resync and recovers, so the tour completes.
//
// departureHopNavStall models the false-positive (the departure-hop navigate to "-GATE"
// leaves the persisted position at the market); jumpRequiresGate enforces the live jump
// precondition so, WITHOUT the fix, this reproduces the exact crash rather than passing on a
// too-lenient fake.
func TestTour_CrossSystemLeg1AfterIntraLeg0_DepartureNavStalledOffGate_RecoversViaResync(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{"G0": 40}, location: "X1-DP51-X11A", cargoCap: 100,
		jumpRequiresGate:     true, // the live jump precondition the crash tripped
		departureHopNavStall: true, // the arrival-wait false-positive that left the hull off-gate
		markets: map[string][]string{
			"X1-DP51": {"X1-DP51-X11A"},
			"X1-NK36": {"X1-NK36-A", "X1-NK36-B"},
		},
		bid: map[string]map[string]int{
			"X1-DP51-X11A": {"G0": 200}, // leg 0 sells the held G0 here (intra-DP51 market)
			"X1-NK36-B":    {"G1": 300},
		},
		ask: map[string]map[string]int{
			"X1-DP51-X11A": {"G0": 100},
			"X1-NK36-A":    {"G1": 100}, "X1-NK36-B": {"G1": 300},
		},
		tv: map[string]map[string]int{
			"X1-DP51-X11A": {"G0": 1000},
			"X1-NK36-A":    {"G1": 1000}, "X1-NK36-B": {"G1": 1000},
		},
	}
	// Leg 0 INTRA-DP51 (sell the held G0 at the market) → the hull ends leg 0 physically at
	// X1-DP51-X11A. Leg 1 CROSS-SYSTEM to NK36 (the crash leg). Leg 2 sells within NK36 so
	// the hull ends empty. Mirrors the live TORWIND-37 tour shape.
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, ProjectedProfit: 8000, Legs: []routing.TourLeg{
			leg("X1-DP51-X11A", "X1-DP51", sell("G0", 40, 200)),
			leg("X1-NK36-A", "X1-NK36", buy("G1", 40, 100)),
			leg("X1-NK36-B", "X1-NK36", sell("G1", 40, 300)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-37", PlayerID: 1, ContainerID: "ctr-trnp-leg1", ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a leg-1 cross-system leg after an intra-system leg 0 must recover the stalled departure hop via resync, not crash: %v", err)
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
	timeline := strings.Join(fx.timeline, ",")
	fx.mu.Unlock()

	// The cross-system leg-1 transit executed exactly once, to NK36 — the departure hop
	// recovered onto the gate and jumped rather than crashing off it.
	if len(jumps) != 1 || jumps[0] != "X1-NK36" {
		t.Fatalf("expected exactly one jump to X1-NK36, got %v", jumps)
	}
	// Leg 0 sold the held G0 (intra-DP51); leg 1 bought G1; leg 2 sold G1 — hull ends empty.
	if want := "SELL:G0,BUY:G1,SELL:G1"; timeline != want {
		t.Fatalf("trade timeline = %q, want %q", timeline, want)
	}
}

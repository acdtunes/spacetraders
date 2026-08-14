package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// Proves tour's two purchaseWithCeiling call sites (buyLookbackItem, executeBuy)
// correctly CAPTURE and THREAD a real bracket from tour's own travel/dock timing —
// the observable effect is which ScanDedupBeforeTravel/AfterArrival land on the
// dispatched PurchaseCargoCommand, captured by tourFixture at the mediator seam. The
// shared eligibility/tranche-safety machinery is a separate concern, covered in
// run_tour_coordinator_scandedup_ceiling_test.go against the same primitive.

// --- trades.go: run_tour_coordinator_trades.go's executeBuy (a solver-plan leg) ---

func TestTourTrades_ArmedShip_ThreadsRealBracketIntoPlanBuy(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetScanDedupAllowlist(&inMemoryScanDedupAllowlist{armed: map[string]bool{"TOUR-DEDUP-1": true}})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DEDUP-1", PlayerID: 1, ContainerID: "ctr-dedup-1",
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	_ = tourResponse(t, resp)

	if len(fx.dedupBrackets) != 1 {
		t.Fatalf("expected exactly one purchase, got %d: %+v", len(fx.dedupBrackets), fx.dedupBrackets)
	}
	b := fx.dedupBrackets[0]
	if b.BeforeTravel.IsZero() || b.AfterArrival.IsZero() {
		t.Fatalf("an armed ship's plan buy must carry a real (non-zero) scan-dedup bracket, got %+v", b)
	}
	if b.AfterArrival.Before(b.BeforeTravel) {
		t.Fatalf("AfterArrival must not precede BeforeTravel, got %+v", b)
	}
}

// A ship absent from the allowlist must dispatch the zero-value bracket — the
// "empty allowlist = current behavior" contract.
func TestTourTrades_NotArmed_PlanBuyCarriesZeroBracket_ByteIdenticalToToday(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetScanDedupAllowlist(&inMemoryScanDedupAllowlist{armed: map[string]bool{}}) // explicitly empty

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-PLAIN-1", PlayerID: 1, ContainerID: "ctr-plain-1",
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	_ = tourResponse(t, resp)

	if len(fx.dedupBrackets) != 1 {
		t.Fatalf("expected exactly one purchase, got %d: %+v", len(fx.dedupBrackets), fx.dedupBrackets)
	}
	b := fx.dedupBrackets[0]
	if !b.BeforeTravel.IsZero() || !b.AfterArrival.IsZero() {
		t.Fatalf("an unarmed ship must dispatch the zero-value bracket, matching the pre-existing zero-value default, got %+v", b)
	}
}

// A leg with TWO buy trades for different goods at the same waypoint is a real
// solver output shape (tour_solver.py packs several profitable exports into one
// stop), not a contrived edge case. cargo_transaction.go's tranche-1-only fix
// protects multiple TRANCHES of one trade's own purchase; it cannot see a SECOND,
// separate trade in the same leg reusing the caller's still-armed bracket. Only the
// leg's first buy may carry it; the second must be forced back onto a live scan.
func TestTourTrades_TwoBuyTradesInOneLeg_OnlyFirstBuyCarriesTheBracket(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A"}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G1": 100, "G2": 50}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G1": 1000, "G2": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G1", 20, 100), buy("G2", 20, 50)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetScanDedupAllowlist(&inMemoryScanDedupAllowlist{armed: map[string]bool{"TOUR-DEDUP-MULTI": true}})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DEDUP-MULTI", PlayerID: 1, ContainerID: "ctr-dedup-multi",
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	_ = tourResponse(t, resp)

	if len(fx.dedupBrackets) != 2 {
		t.Fatalf("expected exactly 2 purchases (G1 then G2, sells-before-buys preserves order), got %d: %+v", len(fx.dedupBrackets), fx.dedupBrackets)
	}
	if fx.dedupBrackets[0].BeforeTravel.IsZero() {
		t.Fatalf("the leg's FIRST buy trade must still carry the real arrival bracket, got %+v", fx.dedupBrackets[0])
	}
	if !fx.dedupBrackets[1].BeforeTravel.IsZero() || !fx.dedupBrackets[1].AfterArrival.IsZero() {
		t.Fatalf("a SECOND buy trade in the same leg must NOT reuse the first buy's bracket, got %+v", fx.dedupBrackets[1])
	}
}

// --- lookback.go: run_tour_coordinator_lookback.go's buyLookbackItem (a pre-jump manifest buy) ---

// dedupLookbackPlanner mirrors TestTour_Reposition_LoadsLookbackManifestBeforeJump's
// planner: the destination must stay profitable on the pre-flight (empty-hull) probe,
// which is why it always offers its own H buy/sell — a destination that only sells
// the not-yet-carried PARTS load looks unprofitable and the reposition never commits.
// The run therefore dispatches TWO purchases: PARTS (look-back, before the jump) then
// H (the destination's own, after it).
func dedupLookbackPlanner() *tourFakeRoutingClient {
	homeCalls, destCalls := 0, 0
	return &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-HU21":
			homeCalls++
			if homeCalls == 1 {
				return &routing.TourPlan{Feasible: true, ProjectedProfit: 4000, Legs: []routing.TourLeg{
					leg("X1-HU21-A", "X1-HU21", buy("G", 40, 100)),
					leg("X1-HU21-B", "X1-HU21", sell("G", 40, 200)),
				}}
			}
			return infeasibleTour() // margins die at home (3-strike)
		case "X1-UQ16":
			destCalls++
			plan := &routing.TourPlan{Feasible: true, ProjectedProfit: 100000, Legs: []routing.TourLeg{
				leg("X1-UQ16-A", "X1-UQ16", buy("H", 40, 100)),
				leg("X1-UQ16-B", "X1-UQ16", sell("H", 40, 300)),
			}}
			if parts := ship.Cargo["PARTS"]; parts > 0 {
				plan.Legs = append([]routing.TourLeg{
					leg("X1-UQ16-B", "X1-UQ16", sell("PARTS", parts, 300)),
				}, plan.Legs...)
				plan.HeldLiquidation = int64(parts * 300)
			}
			if destCalls >= 3 {
				return infeasibleTour() // eventually the fresh ground taps too → honest exit
			}
			return plan
		}
		return infeasibleTour()
	}}
}

// buyBracketFor locates the bracket for the Nth-in-timeline BUY of good, using
// fx.timeline rather than assuming a fixed dedupBrackets index: the home leg also
// buys G before the reposition triggers, so the look-back PARTS buy is not index 0.
func buyBracketFor(t *testing.T, fx *tourFixture, good string) scanDedupBracket {
	t.Helper()
	idx, buyCount := -1, 0
	for _, e := range fx.timeline {
		if !strings.HasPrefix(e, "BUY:") {
			continue
		}
		if idx == -1 && e == "BUY:"+good {
			idx = buyCount
		}
		buyCount++
	}
	if idx == -1 {
		t.Fatalf("no BUY:%s found in timeline %v", good, fx.timeline)
	}
	if idx >= len(fx.dedupBrackets) {
		t.Fatalf("timeline says BUY:%s is purchase #%d but only %d purchases were captured: %+v", good, idx, len(fx.dedupBrackets), fx.dedupBrackets)
	}
	return fx.dedupBrackets[idx]
}

func TestTourLookback_ArmedShip_ThreadsRealBracketIntoManifestBuy(t *testing.T) {
	fx := lookbackFixture()
	planner := dedupLookbackPlanner()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetScanDedupAllowlist(&inMemoryScanDedupAllowlist{armed: map[string]bool{"TOUR-LB-DEDUP": true}})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-LB-DEDUP", PlayerID: 1, ContainerID: "ctr-lb-dedup", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("look-back run returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition (the deadhead trigger), got %d", r.Repositions)
	}

	b := buyBracketFor(t, fx, "PARTS")
	if b.BeforeTravel.IsZero() || b.AfterArrival.IsZero() {
		t.Fatalf("an armed ship's look-back manifest buy must carry a real (non-zero) scan-dedup bracket, got %+v", b)
	}
	if b.AfterArrival.Before(b.BeforeTravel) {
		t.Fatalf("AfterArrival must not precede BeforeTravel, got %+v", b)
	}
}

func TestTourLookback_NotArmed_ManifestBuyCarriesZeroBracket_ByteIdenticalToToday(t *testing.T) {
	fx := lookbackFixture()
	planner := dedupLookbackPlanner()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetScanDedupAllowlist(&inMemoryScanDedupAllowlist{armed: map[string]bool{}}) // explicitly empty

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-LB-PLAIN", PlayerID: 1, ContainerID: "ctr-lb-plain", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("look-back run returned error: %v", err)
	}
	r := tourResponse(t, resp)
	if r.Repositions != 1 {
		t.Fatalf("expected exactly one reposition (the deadhead trigger), got %d", r.Repositions)
	}

	b := buyBracketFor(t, fx, "PARTS")
	if !b.BeforeTravel.IsZero() || !b.AfterArrival.IsZero() {
		t.Fatalf("an unarmed ship must dispatch the zero-value bracket, matching the pre-existing zero-value default, got %+v", b)
	}
}

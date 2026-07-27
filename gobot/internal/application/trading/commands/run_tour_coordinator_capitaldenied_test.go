package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// THE INCIDENT (TORWIND-6, era 5): the working-capital floor refuses leg 0's buy, the tour
// abandons the plan — including the sell leg that would have cleared the hold — and after three
// such tours the breaker parks the hull holding unsold cargo. Every relaunch re-plans the same
// recovery and is killed the same way. A blocked buy is DENIED CAPITAL, not absent margin; the
// two need opposite responses.

// capitalDeniedFixture: the hull sits at X1-C1-A where G is on offer, and X1-C1-B IMPORTS G at a
// real bid. Ample hold and trade volume so ONLY the working-capital floor can bind a buy.
func capitalDeniedFixture() *tourFixture {
	return &tourFixture{
		cargo: map[string]int{}, location: "X1-C1-A", cargoCap: 200,
		markets:   map[string][]string{"X1-C1": {"X1-C1-A", "X1-C1-B"}},
		ask:       map[string]map[string]int{"X1-C1-A": {"G": 1000}, "X1-C1-B": {"G": 1200}},
		bid:       map[string]map[string]int{"X1-C1-B": {"G": 1200}},
		tradeType: map[string]map[string]string{"X1-C1-B": {"G": "IMPORT"}},
		tv:        map[string]map[string]int{"X1-C1-A": {"G": 1000}, "X1-C1-B": {"G": 1000}},
	}
}

// THE REGRESSION. A buy the floor refuses must not abandon the plan's remaining SELL legs.
// Selling cargo already aboard ADDS credits — it moves treasury AWAY from the floor — so
// "cannot buy more" can never be a reason to fly home laden. Live balance 1,000,500 against a
// 1,000,000 reserve leaves 500 of headroom and the ask is 1,000: even one unit pierces, so leg
// 0's buy is refused while leg 1 liquidates the 10 HELD the hull arrived with.
func TestTour_BuyRefusedByFloor_StillFliesRemainingSellLegs(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{"HELD": 10}, location: "X1-C1-A", cargoCap: 200,
		markets: map[string][]string{"X1-C1": {"X1-C1-A", "X1-C1-B"}},
		ask:     map[string]map[string]int{"X1-C1-A": {"G": 1000}, "X1-C1-B": {"HELD": 3000}},
		bid:     map[string]map[string]int{"X1-C1-B": {"HELD": 3000}},
		tv:      map[string]map[string]int{"X1-C1-A": {"G": 1000}, "X1-C1-B": {"HELD": 1000}},
	}
	api := &tourSeqAPIClient{balances: []int{1_000_500}}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-C1-A", "X1-C1", buy("G", 100, 1000)),
			leg("X1-C1-B", "X1-C1", sell("HELD", 10, 3000)),
		}},
		{Feasible: false, InfeasibleReason: "no_profitable_tour"},
	}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-DISCHARGE")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DISCHARGE", PlayerID: 1, ContainerID: "ctr-discharge",
		MaxSpend: 10_000_000, WorkingCapitalReserve: 1_000_000,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a floor-refused buy must not error the run: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 0 {
		t.Fatalf("the floor must still refuse the buy (RULINGS #4 untouched), got %d buys", fx.buys)
	}
	if fx.cargo["HELD"] != 0 {
		t.Fatalf("the refused buy must not abandon the sell leg - the hull must land EMPTY, still holding %d HELD", fx.cargo["HELD"])
	}
	if r.TotalRevenue != 10*3000 {
		t.Fatalf("the liquidation leg must have flown (revenue 30000), got %d", r.TotalRevenue)
	}
	if !navDestsContain(fx.navDests, "X1-C1-B") {
		t.Fatalf("the hull must still fly to the sink leg after the refused buy, navDests=%v", fx.navDests)
	}
}

// Three capital-denied tours in a row must not park a hull that is holding sellable cargo. Tour
// 1 buys 10 G against a healthy treasury; the balance then sits just above the reserve so every
// later buy is refused. The margins never died — the market still bids 1,200 for the load at
// X1-C1-B — so the hull must leave with its hold cleared, not stranded.
func TestTour_ThreeCapitalDeniedTours_NeverParkHullHoldingSellableCargo(t *testing.T) {
	fx := capitalDeniedFixture()
	api := &tourSeqAPIClient{balances: []int{2_000_000, 1_000_500}}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{leg("X1-C1-A", "X1-C1", buy("G", 10, 1000))}},
	}}
	h := newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, api)

	ctx := auth.WithPlayerToken(context.Background(), "TOUR-DENIED")
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DENIED", PlayerID: 1, ContainerID: "ctr-denied", Iterations: -1,
		MaxSpend: 10_000_000, WorkingCapitalReserve: 1_000_000,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a capital-denied run must exit cleanly: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 1 {
		t.Fatalf("only the first tour's buy clears the floor, got %d buys", fx.buys)
	}
	if fx.cargo["G"] != 0 {
		t.Fatalf("a hull denied capital must never be parked holding sellable cargo, still holding %d G", fx.cargo["G"])
	}
	if r.CargoStranded {
		t.Fatalf("the load had a reachable bid at X1-C1-B - it must not report a strand: %s", r.CargoStrandedReason)
	}
	if r.ExitReason == tourExitStarvation {
		t.Fatalf("denied capital is not a dead margin - the exit must not be classified as starvation (%+v)", r)
	}
}

// THE BREAKER MUST STILL WORK. Three tours whose only trade is skipped because the live price
// moved past tolerance — no money guard anywhere in the path — still trip the margins-death
// breaker. Capital denial is the ONLY exemption; a genuinely dead margin still parks the hull.
func TestTour_ThreeNoMarginTours_StillTripTheStarvationBreaker(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-N1-A", cargoCap: 200,
		markets: map[string][]string{"X1-N1": {"X1-N1-A"}},
		ask:     map[string]map[string]int{"X1-N1-A": {"G": 2000}}, // live 2000 vs planned 1000 = 100% moved
		tv:      map[string]map[string]int{"X1-N1-A": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{leg("X1-N1-A", "X1-N1", buy("G", 10, 1000))}},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{}) // no api client: the floor guard is not wired at all

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-DEAD", PlayerID: 1, ContainerID: "ctr-dead", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("a margins-death run must exit cleanly: %v", err)
	}
	r := tourResponse(t, resp)

	if fx.buys != 0 {
		t.Fatalf("a price-degraded trade must never dispatch, got %d buys", fx.buys)
	}
	if r.ExitReason != tourExitStarvation {
		t.Fatalf("three zero-trade tours with no money guard involved must trip the breaker, got exit %q (%+v)", r.ExitReason, r)
	}
}

// The honest stop. A hull holding tour-bought cargo that NOTHING will bid on cannot be rescued
// by selling — it must stop and report the strand rather than fly on forever looking for a buyer.
func TestTour_HeldCargoWithNoReachableBid_StopsAndReportsTheStrand(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-B1-A", cargoCap: 200,
		markets: map[string][]string{"X1-B1": {"X1-B1-A"}},
		ask:     map[string]map[string]int{"X1-B1-A": {"G": 1000}}, // on offer here, bid nowhere
		tv:      map[string]map[string]int{"X1-B1-A": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{leg("X1-B1-A", "X1-B1", buy("G", 10, 1000))}},
		{Feasible: false, InfeasibleReason: "no_profitable_tour"},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-NOBID", PlayerID: 1, ContainerID: "ctr-nobid", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("an unsellable strand must exit cleanly (the veto rides CompletionOutcome): %v", err)
	}
	r := tourResponse(t, resp)

	if fx.sells != 0 {
		t.Fatalf("nothing bids on G - no sale can be dispatched, got %d sells", fx.sells)
	}
	if !r.CargoStranded {
		t.Fatalf("cargo with no reachable bid must report the strand, got %+v", r)
	}
	if !strings.Contains(r.CargoStrandedReason, "10 G") {
		t.Fatalf("the strand must name the units and good, got %q", r.CargoStrandedReason)
	}
}

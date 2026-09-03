package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// A deferred arrival scan leaves the leg's cached ask unrefreshed, and executeBuy's
// per-unit cuts (the spend cap's affordability cut, the working-capital floor's divisor)
// divide by it — too LOW an ask sizes the tranche too LARGE. Such a leg divides by the buy
// ceiling instead; a leg that KEEPS its arrival scan is unchanged.

// Live balance 1,090,000 against a 1,000,000 reserve: 90,000 of headroom, ample hold and
// trade volume, so only the working-capital floor can bind the tranche.
func deferredSizingHandler(t *testing.T, fx *tourFixture, plans ...*routing.TourPlan) *RunTourCoordinatorHandler {
	t.Helper()
	planner := &tourFakeRoutingClient{plans: plans}
	return newTourHandlerWithAPI(t, fx, planner, &tourFakeTelemetry{}, &tourSeqAPIClient{balances: []int{1_090_000}})
}

func runDeferredSizingTour(t *testing.T, h *RunTourCoordinatorHandler, ship string) {
	t.Helper()
	ctx := auth.WithPlayerToken(context.Background(), ship)
	if _, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: ship, PlayerID: 1, ContainerID: "ctr-" + ship,
		MaxSpend: 10_000_000, WorkingCapitalReserve: 1_000_000,
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
}

// The deferred leg: 90,000 headroom / the 1,150 ceiling = 78 units, NOT the 90 the cached
// 1,000 ask would have allowed — what the floor permits if the true ask has risen to the
// top of the band while the row sat unrefreshed.
func TestTour_DeferredArrivalScan_SizesBuyOffTheGuardCeiling(t *testing.T) {
	fx := floorRoundTripFixture()
	h := deferredSizingHandler(t, fx, floorRoundTripPlan(100))

	runDeferredSizingTour(t, h, "TOUR-DEFER-SIZE")

	requireDeferred(t, fx, "X1-S1-A")
	if len(fx.buyCmds) != 1 {
		t.Fatalf("expected exactly one purchase, got %d", len(fx.buyCmds))
	}
	if got := fx.buyCmds[0].Units; got != 78 {
		t.Fatalf("a deferred-scan leg must size its buy off the 1,150 guard ceiling (90,000 headroom → 78 units), got %d — 90 means the cut divided by the unrefreshed cached ask", got)
	}
	if got := fx.buyCmds[0].MaxAskPerUnit; got != 1150 {
		t.Fatalf("sizing and the guard must share one ceiling, got MaxAskPerUnit %d", got)
	}
}

// The control: same prices, same headroom, but one UNGUARDED trade on the leg (no basis to
// band, so no guard will read this market) forfeits the deferral for the whole leg — and
// the floor then divides by its scanned cached ask, 90,000 / 1,000 = 90 units.
func TestTour_LegKeepingItsArrivalScan_SizesBuyOffTheCachedAsk(t *testing.T) {
	fx := floorRoundTripFixture()
	fx.cargo = map[string]int{"H": 20}
	fx.bid["X1-S1-A"] = map[string]int{"H": 500}
	fx.ask["X1-S1-A"]["H"] = 600
	fx.tv["X1-S1-A"]["H"] = 1000
	h := deferredSizingHandler(t, fx, &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
		leg("X1-S1-A", "X1-S1", sell("H", 20, 0), buy("G", 100, 1000)),
		leg("X1-S1-B", "X1-S1", sell("G", 100, 1200)),
	}}, &routing.TourPlan{Feasible: false, InfeasibleReason: "no profitable tour"})

	runDeferredSizingTour(t, h, "TOUR-KEEP-SIZE")

	requireNotDeferred(t, fx, "X1-S1-A")
	if len(fx.buyCmds) != 1 {
		t.Fatalf("expected exactly one purchase, got %d", len(fx.buyCmds))
	}
	if got := fx.buyCmds[0].Units; got != 90 {
		t.Fatalf("a leg that keeps its arrival scan must size off the cached ask (90,000 headroom / 1,000 = 90 units), got %d", got)
	}
}

func TestTourBuySizingAsk_Table(t *testing.T) {
	cases := []struct {
		name     string
		liveAsk  int
		trade    routing.TourTrade
		deferred bool
		want     int
	}{
		{"not deferred keeps the cached ask", 1000, buy("G", 10, 1000), false, 1000},
		{"deferred sizes off the ceiling", 1000, buy("G", 10, 1000), true, 1150},
		{"deferred with a cached ask above the ceiling keeps the higher ask", 2000, buy("G", 10, 1000), true, 2000},
		{"deferred with no guard basis keeps the cached ask", 1000, buy("G", 10, 0), true, 1000},
		{"the ceiling bands the UNDISCOUNTED basis", 1000, routing.TourTrade{Good: "G", Units: 10, ExpectedUnitPrice: 900, RawUnitPrice: 1000, IsBuy: true}, true, 1150},
	}
	for _, tc := range cases {
		if got := tourBuySizingAsk(tc.liveAsk, tc.trade, tc.deferred); got != tc.want {
			t.Errorf("%s: tourBuySizingAsk = %d, want %d", tc.name, got, tc.want)
		}
	}
}

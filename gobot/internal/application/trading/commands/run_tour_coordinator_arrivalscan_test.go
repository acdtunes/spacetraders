package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// sp-unw6h: when every trade a leg will fly is money-guarded, the guard live-re-reads
// that market seconds after arrival, so the arrival scan is a duplicate GetMarket
// (measured: 167 such pairs/hour). The tour stamps the deferral marker on the TRAVEL
// ctx of exactly those legs; every other leg — any sell, a deposit, a stop with no
// trade — navigates unstamped and keeps the arrival scan it has today. The observable
// is the marker on the ctx the navigate primitive receives, captured by tourFixture.

// deferralFor returns the waypoint the marker named on the navigate to dest, and
// whether that navigate happened at all.
func deferralFor(t *testing.T, fx *tourFixture, dest string) (string, bool) {
	t.Helper()
	for i, d := range fx.navDests {
		if d != dest {
			continue
		}
		if i >= len(fx.navDeferredScan) {
			t.Fatalf("navigate to %s at index %d has no captured deferral stamp: %v", dest, i, fx.navDeferredScan)
		}
		return fx.navDeferredScan[i], true
	}
	return "", false
}

func requireDeferred(t *testing.T, fx *tourFixture, dest string) {
	t.Helper()
	stamp, navigated := deferralFor(t, fx, dest)
	if !navigated {
		t.Fatalf("expected a navigate to %s, got %v", dest, fx.navDests)
	}
	if stamp != dest {
		t.Fatalf("leg to %s must defer its arrival scan to the trade guard (stamp %q), navDests=%v stamps=%v", dest, stamp, fx.navDests, fx.navDeferredScan)
	}
}

func requireNotDeferred(t *testing.T, fx *tourFixture, dest string) {
	t.Helper()
	stamp, navigated := deferralFor(t, fx, dest)
	if !navigated {
		t.Fatalf("expected a navigate to %s, got %v", dest, fx.navDests)
	}
	if stamp != "" {
		t.Fatalf("leg to %s must keep its arrival scan (unstamped), got %q; navDests=%v stamps=%v", dest, stamp, fx.navDests, fx.navDeferredScan)
	}
}

func runArrivalScanTour(t *testing.T, fx *tourFixture, plan *routing.TourPlan, ship string) {
	t.Helper()
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{plan}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: ship, PlayerID: 1, ContainerID: "ctr-" + ship,
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	_ = tourResponse(t, resp)
}

// A buy-only leg defers; the sell leg that follows does not. The sell is the interesting
// half: sellFloorPerUnit leaves a leg's first tranche unarmed (MinBidPerUnit == 0), so
// nothing will live-re-read that market and the arrival scan must survive.
func TestTourArrivalScan_GuardedBuyLegDefers_SellLegKeepsItsScan(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	runArrivalScanTour(t, fx, &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
		leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
		leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
	}}, "TOUR-DEFER-1")

	requireDeferred(t, fx, "X1-S1-A")
	requireNotDeferred(t, fx, "X1-S1-B")

	// The deferral is only legitimate because the guard's own live read replaces it: the
	// dispatched purchase must still carry an armed ceiling.
	if len(fx.buyCmds) != 1 {
		t.Fatalf("expected exactly one purchase, got %d", len(fx.buyCmds))
	}
	if fx.buyCmds[0].MaxAskPerUnit <= 0 {
		t.Fatalf("the deferred leg's buy must still arm the per-tranche ceiling (its live re-read is what protects the trade), got %d", fx.buyCmds[0].MaxAskPerUnit)
	}
}

// A MIXED leg — a sell and a buy at the same waypoint — keeps its arrival scan: the sell
// half dispatches unguarded, so one live guard read does not cover everything traded here.
func TestTourArrivalScan_MixedLegKeepsItsScan(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{"H": 20}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200, "H": 300}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200, "H": 400}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000, "H": 1000}},
	}
	runArrivalScanTour(t, fx, &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
		leg("X1-S1-B", "X1-S1", sell("H", 20, 300), buy("G", 40, 200)),
	}}, "TOUR-DEFER-MIX")

	requireNotDeferred(t, fx, "X1-S1-B")
}

// A leg with nothing to trade at the destination (a refuel or gate stop) has no guard to
// defer to, so it keeps the arrival scan.
func TestTourArrivalScan_LegWithNoTradeKeepsItsScan(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B", "X1-S1-C"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 200}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 100}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	runArrivalScanTour(t, fx, &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
		leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
		leg("X1-S1-C", "X1-S1"),
		leg("X1-S1-B", "X1-S1", sell("G", 40, 200)),
	}}, "TOUR-DEFER-STOP")

	requireDeferred(t, fx, "X1-S1-A")
	requireNotDeferred(t, fx, "X1-S1-C")
}

// tourLegDefersArrivalScan is the whole decision, so pin its table directly too: the
// shapes above prove the wiring, these prove the rule (including a deposit tranche and a
// discharge run, whose dropped buys leave nothing guarded to fly).
func TestTourLegDefersArrivalScan_Table(t *testing.T) {
	cases := []struct {
		name        string
		leg         routing.TourLeg
		discharging bool
		want        bool
	}{
		{"buy only, guarded", leg("W", "S", buy("G", 10, 100)), false, true},
		{"two buys, both guarded", leg("W", "S", buy("G", 10, 100), buy("H", 5, 50)), false, true},
		{"sell only", leg("W", "S", sell("G", 10, 100)), false, false},
		{"buy and sell", leg("W", "S", buy("G", 10, 100), sell("H", 10, 100)), false, false},
		{"deposit", leg("W", "S", deposit("G", 10, 100)), false, false},
		{"no trades", leg("W", "S"), false, false},
		{"buy with no guard basis", leg("W", "S", buy("G", 10, 0)), false, false},
		{"discharging drops the buys, leaving nothing", leg("W", "S", buy("G", 10, 100)), true, false},
		{"discharging leaves the sell, which is unguarded", leg("W", "S", buy("G", 10, 100), sell("H", 10, 100)), true, false},
	}
	for _, tc := range cases {
		if got := tourLegDefersArrivalScan(tc.leg, tc.discharging); got != tc.want {
			t.Errorf("%s: tourLegDefersArrivalScan = %t, want %t", tc.name, got, tc.want)
		}
	}
}

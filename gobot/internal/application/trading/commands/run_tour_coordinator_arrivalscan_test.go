package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// sp-unw6h: when every trade a leg will fly is money-guarded, the guard live-re-reads
// that market seconds after arrival, so the arrival scan is a duplicate GetMarket
// (measured: 167 such pairs/hour). sp-htzl1.11 extends that to sell legs, whose first
// tranche arms a floor in the scan's place. The tour stamps the deferral marker on the
// TRAVEL ctx of exactly those legs; every other leg — an unguarded trade, a deposit, a
// stop with no trade — navigates unstamped and keeps the arrival scan it has today. The
// observable is the marker on the ctx the navigate primitive receives, via tourFixture.

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

// A guarded buy leg and the guarded sell leg that follows BOTH defer (sp-htzl1.11): each
// hands its arrival read to the guard that runs seconds later — the buy's ceiling, and on
// the sell the floor sellFloorPerUnit now arms for that leg's first tranche.
func TestTourArrivalScan_GuardedBuyAndSellLegsBothDefer(t *testing.T) {
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
	requireDeferred(t, fx, "X1-S1-B")

	// The deferral is only legitimate because the guard's own live read replaces it: both
	// dispatches must carry the guard that does the reading.
	if len(fx.buyCmds) != 1 {
		t.Fatalf("expected exactly one purchase, got %d", len(fx.buyCmds))
	}
	if fx.buyCmds[0].MaxAskPerUnit <= 0 {
		t.Fatalf("the deferred leg's buy must still arm the per-tranche ceiling (its live re-read is what protects the trade), got %d", fx.buyCmds[0].MaxAskPerUnit)
	}
	if len(fx.sellCmds) != 1 {
		t.Fatalf("expected exactly one sell, got %d", len(fx.sellCmds))
	}
	if fx.sellCmds[0].MinBidPerUnit <= 0 {
		t.Fatalf("a deferred leg's FIRST sell tranche must carry the floor that replaces the skipped scan, got %d", fx.sellCmds[0].MinBidPerUnit)
	}
}

// A MIXED leg — a guarded sell and a guarded buy at the same waypoint — defers too: the
// ceiling covers the buy and the first-tranche floor covers the sell.
func TestTourArrivalScan_MixedGuardedLegDefers(t *testing.T) {
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

	requireDeferred(t, fx, "X1-S1-B")
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
	fresh, spentG := map[string]bool{}, map[string]bool{"G": true}
	cases := []struct {
		name        string
		leg         routing.TourLeg
		discharging bool
		spent       map[string]bool
		want        bool
	}{
		{"buy only, guarded", leg("W", "S", buy("G", 10, 100)), false, fresh, true},
		{"two buys, both guarded", leg("W", "S", buy("G", 10, 100), buy("H", 5, 50)), false, fresh, true},
		{"sell only, guarded", leg("W", "S", sell("G", 10, 100)), false, fresh, true},
		{"two sells, both guarded", leg("W", "S", sell("G", 10, 100), sell("H", 5, 50)), false, fresh, true},
		{"guarded buy and guarded sell", leg("W", "S", buy("G", 10, 100), sell("H", 10, 100)), false, fresh, true},
		{"deposit", leg("W", "S", deposit("G", 10, 100)), false, fresh, false},
		{"a deposit beside a guarded sell", leg("W", "S", sell("G", 10, 100), deposit("H", 10, 100)), false, fresh, false},
		{"no trades", leg("W", "S"), false, fresh, false},
		{"buy with no guard basis", leg("W", "S", buy("G", 10, 0)), false, fresh, false},
		{"sell with no guard basis", leg("W", "S", sell("G", 10, 0)), false, fresh, false},
		{"one unguarded sell forfeits the whole leg", leg("W", "S", sell("G", 10, 100), sell("H", 10, 0)), false, fresh, false},
		{"discharging drops the buys, leaving nothing", leg("W", "S", buy("G", 10, 100)), true, fresh, false},
		{"discharging leaves the guarded sell", leg("W", "S", buy("G", 10, 100), sell("H", 10, 100)), true, fresh, true},
		// A spent refusal budget arms no floor, so such a sell has no live read to defer to;
		// nor does one with no budget map at all, which sellFloorPerUnit refuses outright.
		{"a sell whose refusal budget is spent", leg("W", "S", sell("G", 10, 100)), false, spentG, false},
		{"one spent good beside a fresh one forfeits the leg", leg("W", "S", sell("G", 10, 100), sell("H", 10, 100)), false, spentG, false},
		{"a spent good the leg only BUYS still defers", leg("W", "S", buy("G", 10, 100)), false, spentG, true},
		{"discharging into a spent sell", leg("W", "S", buy("H", 10, 100), sell("G", 10, 100)), true, spentG, false},
		{"a sell with no budget map at all", leg("W", "S", sell("G", 10, 100)), false, nil, false},
		{"a buy with no budget map still defers", leg("W", "S", buy("G", 10, 100)), false, nil, true},
	}
	for _, tc := range cases {
		if got := tourLegDefersArrivalScan(tc.leg, tc.discharging, tc.spent); got != tc.want {
			t.Errorf("%s: tourLegDefersArrivalScan = %t, want %t", tc.name, got, tc.want)
		}
	}
}

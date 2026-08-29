package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// The main plan leg's buy ceiling is clamped to the sink the tranche is routed to, the
// guard sp-9mkf armed on the look-back buy and the circuit's first buy. These cover the
// fold (planSinkBids), the bound itself (tourBuyCeiling), and the live effect on a flown
// plan.

func TestPlanSinkBids_NearestLaterSellIsTheSink(t *testing.T) {
	plan := &routing.TourPlan{Legs: []routing.TourLeg{
		leg("A", "S", buy("G", 10, 100)),
		leg("B", "S", sell("G", 10, 300)),
		leg("C", "S", sell("G", 10, 900)),
	}}
	got := planSinkBids(plan)
	if bid := got[sinkBidKey{leg: 0, good: "G"}]; bid != 300 {
		t.Fatalf("the NEAREST later sell is the sink, want 300, got %d", bid)
	}
}

func TestPlanSinkBids_SameLegSellIsNotASink(t *testing.T) {
	// Sells run before buys on a leg, so leg 0's own sell has already discharged and is
	// no sink for leg 0's buy. With nothing selling G later, the buy has no sink at all.
	plan := &routing.TourPlan{Legs: []routing.TourLeg{
		leg("A", "S", sell("G", 10, 300), buy("G", 10, 100)),
	}}
	if bid, ok := planSinkBids(plan)[sinkBidKey{leg: 0, good: "G"}]; ok {
		t.Fatalf("a same-leg sell must not be recorded as a sink, got %d", bid)
	}
}

func TestPlanSinkBids_PerLegAndPerGood(t *testing.T) {
	plan := &routing.TourPlan{Legs: []routing.TourLeg{
		leg("A", "S", buy("G", 10, 100), buy("H", 10, 50)),
		leg("B", "S", sell("G", 10, 300), buy("G", 10, 260)),
		leg("C", "S", sell("G", 10, 700), sell("H", 10, 400)),
	}}
	got := planSinkBids(plan)
	for _, tc := range []struct {
		leg  int
		good string
		want int
	}{
		{0, "G", 300}, // leg 0's G goes to leg 1's sink
		{1, "G", 700}, // leg 1's own sell is not its sink; leg 2's is
		{0, "H", 400}, // H skips leg 1 entirely
	} {
		if bid := got[sinkBidKey{leg: tc.leg, good: tc.good}]; bid != tc.want {
			t.Fatalf("leg %d %s: want sink bid %d, got %d", tc.leg, tc.good, tc.want, bid)
		}
	}
}

func TestPlanSinkBids_DepositCountsAsASink(t *testing.T) {
	plan := &routing.TourPlan{Legs: []routing.TourLeg{
		leg("A", "S", buy("G", 10, 100)),
		leg("B", "S", deposit("G", 10, 250)),
	}}
	if bid := planSinkBids(plan)[sinkBidKey{leg: 0, good: "G"}]; bid != 250 {
		t.Fatalf("a deposit's synthetic bid is a sink bound, want 250, got %d", bid)
	}
}

func TestPlanSinkBids_NoLaterSellRecordsNothing(t *testing.T) {
	plan := &routing.TourPlan{Legs: []routing.TourLeg{leg("A", "S", buy("G", 10, 100))}}
	if bid, ok := planSinkBids(plan)[sinkBidKey{leg: 0, good: "G"}]; ok {
		t.Fatalf("a buy with no planned sink records nothing, got %d", bid)
	}
}

func TestPlanSinkBids_NonPositiveSellPriceIsNotASink(t *testing.T) {
	// An EXPORT sink is zeroed at the snapshot boundary; a zero bid must not become a
	// ceiling of zero, which disarms the guard downstream.
	plan := &routing.TourPlan{Legs: []routing.TourLeg{
		leg("A", "S", buy("G", 10, 100)),
		leg("B", "S", sell("G", 10, 0)),
	}}
	if bid, ok := planSinkBids(plan)[sinkBidKey{leg: 0, good: "G"}]; ok {
		t.Fatalf("a non-positive sell price is not a sink, got %d", bid)
	}
}

func TestTourBuyCeiling_ClampsToTheSinkAndOnlyDownward(t *testing.T) {
	for _, tc := range []struct {
		name       string
		plannedAsk int
		sinkBid    int
		want       int
	}{
		{"sink below the tolerance band binds", 1000, 1050, 1050},
		{"sink above the tolerance band leaves it alone", 1000, 5000, 1150},
		{"no planned sink leaves it alone", 1000, 0, 1150},
		{"a sink at the band boundary does not bind", 1000, 1150, 1150},
		{"sink below the planned ask still binds (margin already gone)", 1000, 900, 900},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tourBuyCeiling(tc.plannedAsk, tc.sinkBid); got != tc.want {
				t.Fatalf("want %d, got %d", tc.want, got)
			}
		})
	}
}

// The live effect: an ask that has drifted up inside the 15% tolerance band but above the
// sink's planned bid buys NOTHING. Without the clamp the tolerance ceiling is 115 and the
// 108 ask buys the full tranche at a booked loss against a sink bidding 105.
func TestTourTrades_BuyRefusedWhenTheAskPricesAboveItsPlannedSink(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 105}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 108}, "X1-S1-B": {"G": 200}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 105)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	if _, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-SINK-1", PlayerID: 1, ContainerID: "ctr-sink-1",
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	if fx.buys != 0 {
		t.Fatalf("an ask of 108 above a sink bidding 105 must buy nothing, got %d buys", fx.buys)
	}
}

// The same drift inside the band, with a sink that can still repay it, buys as before —
// the clamp must not refuse a lane whose margin survived.
func TestTourTrades_BuyProceedsWhenTheSinkStillRepaysTheDriftedAsk(t *testing.T) {
	fx := &tourFixture{
		cargo: map[string]int{}, location: "X1-S1-A", cargoCap: 100,
		markets: map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B"}},
		bid:     map[string]map[string]int{"X1-S1-B": {"G": 300}},
		ask:     map[string]map[string]int{"X1-S1-A": {"G": 108}, "X1-S1-B": {"G": 400}},
		tv:      map[string]map[string]int{"X1-S1-A": {"G": 1000}, "X1-S1-B": {"G": 1000}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G", 40, 300)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	if _, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-SINK-2", PlayerID: 1, ContainerID: "ctr-sink-2",
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	if fx.buys != 1 {
		t.Fatalf("a drifted ask a sink at 300 still repays must buy, got %d buys", fx.buys)
	}
}

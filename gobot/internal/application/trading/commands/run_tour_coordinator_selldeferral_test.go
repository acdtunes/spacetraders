package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-htzl1.11: a sell leg's arrival scan is deferred like a buy leg's, and what stands in
// for it is the floor the leg's FIRST sell tranche now arms — one live bid read where the
// arrival scan used to be, instead of an unrefreshed row and an unguarded dispatch. These
// pin the two halves: the rule that decides the deferral, and the floor that pays for it.

// A deferred leg arms the first tranche of EACH good; every later tranche is the depth
// rule's business again, and a spent refusal still zeroes the floor outright.
func TestSellFloorPerUnit_DeferredLegArmsTheFirstTrancheOfEachGood(t *testing.T) {
	const good, planned, tv = "IRON_ORE", 100, 20
	cases := []struct {
		name     string
		deferred bool
		legSold  map[string]int
		spent    bool
		want     int
	}{
		{"deferred, first tranche of the good", true, map[string]int{}, false, 85},
		{"deferred, first tranche of THIS good beside another's depth", true, map[string]int{"OTHER": 999}, false, 85},
		{"deferred, second tranche falls back to the depth rule", true, map[string]int{good: tv}, false, 0},
		{"deferred, past the depth the rule arms it again", true, map[string]int{good: defaultTourACapTranches * tv}, false, 85},
		{"deferred, but the good's refusal budget is spent", true, map[string]int{}, true, 0},
		{"not deferred, first tranche stays unarmed", false, map[string]int{}, false, 0},
		{"not deferred, past the depth", false, map[string]int{good: defaultTourACapTranches * tv}, false, 85},
	}
	for _, tc := range cases {
		run := tourPlanRun{
			sellFloorSpent:  map[string]bool{good: tc.spent},
			legSold:         tc.legSold,
			legScanDeferred: tc.deferred,
		}
		if got := run.sellFloorPerUnit(good, planned, tv); got != tc.want {
			t.Errorf("%s: sellFloorPerUnit = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// An unreadable trade volume cannot arm the DEPTH rule, but it has no bearing on the
// deferral: the missing arrival scan is the reason the first tranche is armed.
func TestSellFloorPerUnit_DeferredFirstTrancheArmsWithoutATradeVolume(t *testing.T) {
	run := tourPlanRun{sellFloorSpent: map[string]bool{}, legSold: map[string]int{}, legScanDeferred: true}
	if got := run.sellFloorPerUnit("IRON_ORE", 100, 0); got != 85 {
		t.Fatalf("sellFloorPerUnit = %d, want 85", got)
	}
}

// THE NO-BLIND-DUMP COMPOSITION: the deferral rule and the one-refusal-per-good budget must
// agree, or they compose into a hull selling blind. Sink 1 crushes the good, its first
// tranche is refused and the budget is spent; the re-plan routes the same hold to sink 2,
// where sellFloorPerUnit will arm NOTHING. That leg must therefore keep its arrival scan —
// otherwise nothing reads sink 2 at all and the leg gate compares against the cached row the
// plan was built from, which always passes. The second good is fresh and would arm on its
// own; one spent good on the leg is enough to forfeit the deferral for the whole leg.
func TestTourArrivalScan_LegSellingAGoodWithASpentRefusalBudgetKeepsItsScan(t *testing.T) {
	const good, other = "IRON_ORE", "COPPER_ORE"
	fx := &tourFixture{
		cargo: map[string]int{good: 40, other: 40}, location: "X1-SD-A", cargoCap: 200,
		markets: map[string][]string{"X1-SD": {"X1-SD-A", "X1-SD-S1", "X1-SD-S2"}},
		bid: map[string]map[string]int{
			"X1-SD-S1": {good: 100},
			"X1-SD-S2": {good: 100, other: 100},
		},
		ask: map[string]map[string]int{
			"X1-SD-A":  {good: 130, other: 130},
			"X1-SD-S1": {good: 130},
			"X1-SD-S2": {good: 130, other: 130},
		},
		tv: map[string]map[string]int{
			"X1-SD-A":  {good: 20, other: 20},
			"X1-SD-S1": {good: 20},
			"X1-SD-S2": {good: 20, other: 20},
		},
		liveBid: map[string]map[string]int{"X1-SD-S1": {good: 40}},
	}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, Legs: []routing.TourLeg{
			leg("X1-SD-S1", "X1-SD", sell(good, 20, 100)),
			leg("X1-SD-S2", "X1-SD", sell(good, 20, 100), sell(other, 20, 100)),
		}},
		{Feasible: false, InfeasibleReason: "no profitable tour"},
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	if _, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-SPENT", PlayerID: 1, ContainerID: "ctr-TOUR-SPENT",
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("tour returned error: %v", err)
	}

	requireDeferred(t, fx, "X1-SD-S1")
	requireNotDeferred(t, fx, "X1-SD-S2")
	if len(fx.sellCmds) != 3 {
		t.Fatalf("expected the refused tranche plus both of sink 2's, got %d dispatches", len(fx.sellCmds))
	}
	if fx.sellCmds[0].MinBidPerUnit != 85 {
		t.Fatalf("sink 1's first tranche must arm at planned−15%%, got %d", fx.sellCmds[0].MinBidPerUnit)
	}
	for _, c := range fx.sellCmds[1:] {
		if c.MinBidPerUnit != 0 {
			t.Fatalf("the spent budget leaves sink 2 unarmed (that is why it keeps its scan), got %d for %s", c.MinBidPerUnit, c.GoodSymbol)
		}
	}
}

func TestTourLegDeferralSide_Table(t *testing.T) {
	cases := []struct {
		name        string
		leg         routing.TourLeg
		discharging bool
		spent       map[string]bool
		want        string
	}{
		{"buys only", leg("W", "S", buy("G", 10, 100)), false, map[string]bool{}, shared.ArrivalScanSideBuy},
		{"sells only", leg("W", "S", sell("G", 10, 100)), false, map[string]bool{}, shared.ArrivalScanSideSell},
		{"both", leg("W", "S", buy("G", 10, 100), sell("H", 10, 100)), false, map[string]bool{}, shared.ArrivalScanSideMixed},
		{"discharging drops the buys, leaving a sell leg", leg("W", "S", buy("G", 10, 100), sell("H", 10, 100)), true, map[string]bool{}, shared.ArrivalScanSideSell},
		{"nothing deferred has no side", leg("W", "S", sell("G", 10, 0)), false, map[string]bool{}, ""},
		{"a spent sell defers nothing", leg("W", "S", buy("G", 10, 100), sell("H", 10, 100)), false, map[string]bool{"H": true}, ""},
	}
	for _, tc := range cases {
		if got := tourLegDeferralSide(tc.leg, tc.discharging, tc.spent); got != tc.want {
			t.Errorf("%s: tourLegDeferralSide = %q, want %q", tc.name, got, tc.want)
		}
	}
}

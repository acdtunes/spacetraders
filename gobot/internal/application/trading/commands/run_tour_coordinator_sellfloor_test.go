package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// --- Per-tranche SELL floor on the tour path ------------------------------
//
// The tour buy arms a ceiling at planned×(1+tourPriceTolerancePct/100); these pin the
// mirror on the bid plus the two bounds that make it affordable and safe — it arms only
// past tourACapTranches of visit depth, and refuses a hold at most once per tour.

const (
	sfGood    = "IRON_ORE"
	sfSystem  = "X1-AQ63"
	sfOrigin  = "X1-AQ63-A1"
	sfSink    = "X1-AQ63-SINK"
	sfSink2   = "X1-AQ63-SNK2"
	sfPlanned = 100 // plan basis for the sell; floor = 100 - 15% = 85
	sfFloor   = 85
	sfCrushed = 40 // live bid at the sale, far under the floor
	sfTV      = 20 // trade volume: one tranche, so the cap depth is 2*20 = 40 units
	sfHold    = 100
)

// sfFixture is a hull holding sfHold of sfGood at sfOrigin, with every named sink quoting
// sfPlanned in the CACHED row the leg gate reads. liveBid (per waypoint) is what the
// floor's live re-read returns at the sale — the divergence the guard exists to catch.
func sfFixture(liveBid map[string]map[string]int, sinks ...string) *tourFixture {
	fx := &tourFixture{
		cargo: map[string]int{sfGood: sfHold}, location: sfOrigin, cargoCap: 200,
		markets: map[string][]string{sfSystem: append([]string{sfOrigin}, sinks...)},
		bid:     map[string]map[string]int{},
		ask:     map[string]map[string]int{sfOrigin: {sfGood: 130}},
		tv:      map[string]map[string]int{},
		liveBid: liveBid,
	}
	for _, sink := range sinks {
		fx.bid[sink] = map[string]int{sfGood: sfPlanned}
		fx.ask[sink] = map[string]int{sfGood: sfPlanned + 30}
		fx.tv[sink] = map[string]int{sfGood: sfTV}
	}
	return fx
}

// sfPlanner gives each sink a leg of one-tranche sells, then an infeasible re-plan so the
// run winds down. Three tranches walks a visit past the two-tranche cap; two leaves it under.
func sfPlanner(tranches int, sinks ...string) *tourFakeRoutingClient {
	legs := make([]routing.TourLeg, 0, len(sinks))
	for _, sink := range sinks {
		trades := make([]routing.TourTrade, 0, tranches)
		for i := 0; i < tranches; i++ {
			trades = append(trades, sell(sfGood, sfTV, sfPlanned))
		}
		legs = append(legs, leg(sink, sfSystem, trades...))
	}
	return &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, ProjectedProfit: sfHold * sfPlanned, Legs: legs},
		{Feasible: false, InfeasibleReason: "no profitable tour"},
	}}
}

func sfRun(t *testing.T, fx *tourFixture, planner *tourFakeRoutingClient) (*RunTourCoordinatorResponse, *laneLogCapturingLogger) {
	t.Helper()
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	logger := &laneLogCapturingLogger{}
	resp, err := h.Handle(common.WithLogger(context.Background(), logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-SF", PlayerID: 1, ContainerID: "tour-run-TORWIND-SF",
		ModelArtifactPath: writeTourArtifact(t),
	})
	if err != nil {
		t.Fatalf("tour returned error: %v", err)
	}
	return tourResponse(t, resp), logger
}

// sfFloors is the per-tranche floor each dispatched sell carried, in dispatch order.
func sfFloors(fx *tourFixture) []int {
	out := make([]int, 0, len(fx.sellCmds))
	for _, c := range fx.sellCmds {
		out = append(out, c.MinBidPerUnit)
	}
	return out
}

func sfEqual(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func sfAbortLine(l *laneLogCapturingLogger) *laneLogEntry {
	for i := range l.entries {
		if l.entries[i].metadata["action"] == "tour_sell_floor_abort" {
			return &l.entries[i]
		}
	}
	return nil
}

// (a) A tranche PAST the declared per-sink depth whose live bid has collapsed below the
// plan's tolerance is not sold: it dispatches armed at planned−15%, the market takes
// nothing, and the cargo stays aboard for the next sink.
func TestTourSellFloor_RefusesADeepTrancheWhoseLiveBidCollapsedBelowTolerance(t *testing.T) {
	fx := sfFixture(map[string]map[string]int{sfSink: {sfGood: sfCrushed}}, sfSink)

	_, logger := sfRun(t, fx, sfPlanner(3, sfSink))

	if want := []int{0, 0, sfFloor}; !sfEqual(sfFloors(fx), want) {
		t.Fatalf("only the tranche past %d×trade_volume may arm the floor: want %v, got %v", tourACapTranches, want, sfFloors(fx))
	}
	if fx.sells != 2 {
		t.Fatalf("the two shallow tranches sell and the deep one is refused, got %d sales", fx.sells)
	}
	if fx.cargo[sfGood] != sfHold-2*sfTV {
		t.Fatalf("the refused tranche must stay aboard, got %d units left", fx.cargo[sfGood])
	}
	entry := sfAbortLine(logger)
	if entry == nil {
		t.Fatalf("expected a greppable tour_sell_floor_abort line, got %+v", logger.entries)
	}
	if entry.level != "WARNING" || !strings.Contains(entry.message, sfGood) {
		t.Fatalf("the abort line must be a WARNING naming the good, got %q at %s", entry.message, entry.level)
	}
	if entry.metadata["live_bid"] != sfCrushed || entry.metadata["floor"] != sfFloor {
		t.Fatalf("the abort line must carry the observed bid and the floor, got %+v", entry.metadata)
	}
}

// (b) NO-STRAND: the floor may refuse a good at most ONCE per tour. The second sink runs
// the same depth and still dispatches unarmed, so the hold always has an exit within a
// bounded number of legs and the guard can never iterate on itself.
func TestTourSellFloor_RefusedCargoAlwaysExitsUnarmedOnTheNextLeg(t *testing.T) {
	crushed := map[string]map[string]int{sfSink: {sfGood: sfCrushed}, sfSink2: {sfGood: sfCrushed}}
	fx := sfFixture(crushed, sfSink, sfSink2)

	_, _ = sfRun(t, fx, sfPlanner(3, sfSink, sfSink2))

	if len(fx.sellCmds) != 6 {
		t.Fatalf("expected the refused hold to be re-offered across both sinks, got %d dispatches", len(fx.sellCmds))
	}
	if fx.cargo[sfGood] != 0 {
		t.Fatalf("no-strand violated: %d units of %s never left the hull", fx.cargo[sfGood], sfGood)
	}
	if want := []int{0, 0, sfFloor, 0, 0, 0}; !sfEqual(sfFloors(fx), want) {
		t.Fatalf("after one refusal the good's budget is spent, so the second sink's deep tranche must dispatch unarmed: want %v, got %v", want, sfFloors(fx))
	}
	if fx.sells != 5 {
		t.Fatalf("every tranche but the single refusal must land, got %d sales", fx.sells)
	}
}

// (c) Within tolerance the floor is inert: the deep tranche is armed but never trips, and
// the leg books the same units and revenue it books with no floor at all.
func TestTourSellFloor_WithinTolerance_LeavesTheSaleUnchanged(t *testing.T) {
	fx := sfFixture(nil, sfSink) // live bid == the cached quote == the plan basis

	resp, logger := sfRun(t, fx, sfPlanner(3, sfSink))

	if want := []int{0, 0, sfFloor}; !sfEqual(sfFloors(fx), want) {
		t.Fatalf("the deep tranche must still arm on a healthy sale: want %v, got %v", want, sfFloors(fx))
	}
	if sfAbortLine(logger) != nil {
		t.Fatalf("a bid within tolerance must not trip the floor, got %+v", logger.entries)
	}
	if fx.sells != 3 || fx.cargo[sfGood] != sfHold-3*sfTV {
		t.Fatalf("every tranche must sell as before, got %d sales leaving %d units", fx.sells, fx.cargo[sfGood])
	}
	if resp.TotalRevenue != 3*sfTV*sfPlanned {
		t.Fatalf("revenue must be unchanged at %d, got %d", 3*sfTV*sfPlanned, resp.TotalRevenue)
	}
}

// (d) THE API BOUND: a visit that never reaches the declared depth dispatches every
// tranche unarmed, so the guard costs no live bid re-read on the common path — the
// leg-level gate's cached quote is left to decide, exactly as before.
func TestTourSellFloor_ShallowTranchesDispatchUnarmedSoTheyCostNoLiveScan(t *testing.T) {
	fx := sfFixture(map[string]map[string]int{sfSink: {sfGood: sfCrushed}}, sfSink)

	_, logger := sfRun(t, fx, sfPlanner(tourACapTranches, sfSink))

	if want := []int{0, 0}; !sfEqual(sfFloors(fx), want) {
		t.Fatalf("a visit under %d×trade_volume must never arm the floor: want %v, got %v", tourACapTranches, want, sfFloors(fx))
	}
	if sfAbortLine(logger) != nil {
		t.Fatalf("an unarmed tranche cannot trip the floor, got %+v", logger.entries)
	}
	if fx.sells != tourACapTranches {
		t.Fatalf("the shallow tranches sell exactly as before the floor existed, got %d sales", fx.sells)
	}
}

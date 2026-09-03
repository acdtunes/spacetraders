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
// mirror on the bid plus the three bounds that make it affordable and safe — it arms the
// first tranche of a leg that deferred its arrival scan, then only past
// defaultTourACapTranches of visit depth, and refuses a hold at most once per tour.

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

// (a) A tranche whose live bid has collapsed below the plan's tolerance is not sold: it
// dispatches armed at planned−15%, the market takes nothing, and the cargo stays aboard
// for the next sink. On a leg that deferred its arrival scan that is the FIRST tranche —
// the one the skipped scan would have refreshed (sp-htzl1.11); the refusal then spends
// the good's budget, so the tranches behind it dispatch unarmed exactly as before.
func TestTourSellFloor_RefusesATrancheWhoseLiveBidCollapsedBelowTolerance(t *testing.T) {
	fx := sfFixture(map[string]map[string]int{sfSink: {sfGood: sfCrushed}}, sfSink)

	_, logger := sfRun(t, fx, sfPlanner(3, sfSink))

	if want := []int{sfFloor, 0, 0}; !sfEqual(sfFloors(fx), want) {
		t.Fatalf("a deferred leg arms its first tranche and nothing after the refusal: want %v, got %v", want, sfFloors(fx))
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
	if want := []int{sfFloor, 0, 0, 0, 0, 0}; !sfEqual(sfFloors(fx), want) {
		t.Fatalf("after one refusal the good's budget is spent, so neither the second sink's first tranche nor its deep one may arm: want %v, got %v", want, sfFloors(fx))
	}
	if fx.sells != 5 {
		t.Fatalf("every tranche but the single refusal must land, got %d sales", fx.sells)
	}
	// And because the budget is spent, NOTHING will live-read the second sink: that leg must
	// keep the arrival scan the first one deferred, or its unarmed tranches sell blind off a
	// row no read refreshed since the plan was built.
	requireDeferred(t, fx, sfSink)
	requireNotDeferred(t, fx, sfSink2)
}

// (c) Within tolerance the floor is inert: the armed tranches never trip, and the leg books
// the same units and revenue it books with no floor at all. Both arming rules show here —
// the deferred leg's first tranche, then the depth rule past 2×trade_volume.
func TestTourSellFloor_WithinTolerance_LeavesTheSaleUnchanged(t *testing.T) {
	fx := sfFixture(nil, sfSink) // live bid == the cached quote == the plan basis

	resp, logger := sfRun(t, fx, sfPlanner(3, sfSink))

	if want := []int{sfFloor, 0, sfFloor}; !sfEqual(sfFloors(fx), want) {
		t.Fatalf("the first and the deep tranche must both arm on a healthy sale: want %v, got %v", want, sfFloors(fx))
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

// (d) THE API BOUND: a deferred leg buys its first tranche's floor with the arrival scan it
// skipped — ONE live bid read for the good, not one per tranche. The shallow tranches behind
// it stay unarmed until the depth rule engages, so the visit costs no read it did not save.
func TestTourSellFloor_DeferredLegArmsOnlyItsFirstTrancheSoItCostsOneLiveRead(t *testing.T) {
	fx := sfFixture(nil, sfSink)

	_, logger := sfRun(t, fx, sfPlanner(defaultTourACapTranches, sfSink))

	if want := []int{sfFloor, 0}; !sfEqual(sfFloors(fx), want) {
		t.Fatalf("the first tranche arms and the shallow one behind it does not (under %d×trade_volume): want %v, got %v", defaultTourACapTranches, want, sfFloors(fx))
	}
	if sfAbortLine(logger) != nil {
		t.Fatalf("a bid within tolerance must not trip the floor, got %+v", logger.entries)
	}
	if fx.sells != defaultTourACapTranches {
		t.Fatalf("both tranches must sell, got %d sales", fx.sells)
	}
}

// THE CHUNKING COMPOSITION: the market's per-transaction trade volume splits a
// dispatch into several API transactions BELOW this coordinator, inside the single command
// it sends — the cached trade volume the tranche was sized from is exactly what a 4604
// disproves. So one planned tranche stays one dispatch carrying one floor decision and one
// abort verdict: the good's one-refusal-per-tour budget is spent once by a chunked abort,
// not once per chunk, and the next sink's deep tranche dispatches unarmed exactly as it
// would after an unchunked refusal.
//
// Mutation targets: dropping the budget spend on a PARTIAL abort (the chunked shape — the
// unchunked one returns zero units) re-arms the second sink; dropping noteLegSale's
// aggregate disarms the first.
func TestTourSellFloor_ChunkedAbortSpendsTheGoodsBudgetOnceNotPerChunk(t *testing.T) {
	crushed := map[string]map[string]int{sfSink: {sfGood: sfCrushed}, sfSink2: {sfGood: sfCrushed}}
	fx := sfFixture(crushed, sfSink, sfSink2)
	// The market's live limit is half the cached trade volume the tranche was sized from,
	// so the deep dispatch is two API transactions: the first clears at the old bid and the
	// collapse aborts the second. One response, one verdict, a partial fill.
	fx.sellFloorPartial = map[string]int{sfGood: sfTV / 2}

	_, logger := sfRun(t, fx, sfPlanner(3, sfSink, sfSink2))

	if len(fx.sellCmds) != 6 {
		t.Fatalf("chunking happens below this coordinator: one dispatch per planned tranche, got %d", len(fx.sellCmds))
	}
	if want := []int{sfFloor, 0, 0, 0, 0, 0}; !sfEqual(sfFloors(fx), want) {
		t.Fatalf("a chunked abort spends the good's budget exactly once: want %v, got %v", want, sfFloors(fx))
	}
	aborts := 0
	for i := range logger.entries {
		if logger.entries[i].metadata["action"] == "tour_sell_floor_abort" {
			aborts++
		}
	}
	if aborts != 1 {
		t.Fatalf("a chunked dispatch carries exactly one abort verdict, got %d", aborts)
	}
	if fx.cargo[sfGood] != 0 {
		t.Fatalf("no-strand violated: %d units of %s never left the hull", fx.cargo[sfGood], sfGood)
	}
}

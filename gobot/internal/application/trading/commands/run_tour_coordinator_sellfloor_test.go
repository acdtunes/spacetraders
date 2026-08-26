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
// mirror on the bid, plus the property that makes refusing safe — a hold is refused at
// most once per tour, and every later sell of it dispatches unarmed.

const (
	sfGood    = "IRON_ORE"
	sfSystem  = "X1-AQ63"
	sfOrigin  = "X1-AQ63-A1"
	sfSink    = "X1-AQ63-SINK"
	sfSink2   = "X1-AQ63-SNK2"
	sfPlanned = 100 // plan basis for the sell; floor = 100 - 15% = 85
	sfFloor   = 85
	sfCrushed = 40 // live bid at the sale, far under the floor
	sfUnits   = 40
)

// sfFixture is a hull holding sfUnits of sfGood at sfOrigin, with every named sink
// quoting sfPlanned in the CACHED row the leg gate reads. liveBid (per waypoint) is
// what the floor's live re-read returns at the sale — the divergence the guard exists
// to catch.
func sfFixture(liveBid map[string]map[string]int, sinks ...string) *tourFixture {
	fx := &tourFixture{
		cargo: map[string]int{sfGood: sfUnits}, location: sfOrigin, cargoCap: 80,
		markets: map[string][]string{sfSystem: append([]string{sfOrigin}, sinks...)},
		bid:     map[string]map[string]int{},
		ask:     map[string]map[string]int{sfOrigin: {sfGood: 130}},
		tv:      map[string]map[string]int{},
		liveBid: liveBid,
	}
	for _, sink := range sinks {
		fx.bid[sink] = map[string]int{sfGood: sfPlanned}
		fx.ask[sink] = map[string]int{sfGood: sfPlanned + 30}
		fx.tv[sink] = map[string]int{sfGood: 100}
	}
	return fx
}

// sfPlanner returns a plan selling the whole hold at each sink in turn, then an
// infeasible re-plan so the run winds down instead of looping.
func sfPlanner(sinks ...string) *tourFakeRoutingClient {
	legs := make([]routing.TourLeg, 0, len(sinks))
	for _, sink := range sinks {
		legs = append(legs, leg(sink, sfSystem, sell(sfGood, sfUnits, sfPlanned)))
	}
	return &tourFakeRoutingClient{plans: []*routing.TourPlan{
		{Feasible: true, ProjectedProfit: sfUnits * sfPlanned, Legs: legs},
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

func sfAbortLine(l *laneLogCapturingLogger) *laneLogEntry {
	for i := range l.entries {
		if l.entries[i].metadata["action"] == "tour_sell_floor_abort" {
			return &l.entries[i]
		}
	}
	return nil
}

// (a) A tranche whose live bid has collapsed below the plan's tolerance is NOT sold:
// the sell dispatches armed at planned−15%, the market takes nothing, and the cargo
// stays aboard for the next sink.
func TestTourSellFloor_RefusesATrancheWhoseLiveBidCollapsedBelowTolerance(t *testing.T) {
	fx := sfFixture(map[string]map[string]int{sfSink: {sfGood: sfCrushed}}, sfSink)

	_, logger := sfRun(t, fx, sfPlanner(sfSink))

	if len(fx.sellCmds) != 1 {
		t.Fatalf("expected exactly one sell dispatch, got %d", len(fx.sellCmds))
	}
	if got := fx.sellCmds[0].MinBidPerUnit; got != sfFloor {
		t.Fatalf("the tour sell must arm the floor at planned−%d%% (%d), got %d", tourPriceTolerancePct, sfFloor, got)
	}
	if fx.sells != 0 {
		t.Fatalf("a below-floor bid must take no units, got %d sales", fx.sells)
	}
	if fx.cargo[sfGood] != sfUnits {
		t.Fatalf("the refused cargo must stay aboard, got %d of %d units", fx.cargo[sfGood], sfUnits)
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

// (b) NO-STRAND: the floor may refuse a good at most ONCE per tour. The second sink
// is dispatched unarmed (MinBidPerUnit 0 — the plain sell), so the hold always has an
// exit within a bounded number of legs and the guard can never iterate on itself.
func TestTourSellFloor_RefusedCargoAlwaysExitsUnarmedOnTheNextLeg(t *testing.T) {
	crushed := map[string]map[string]int{sfSink: {sfGood: sfCrushed}, sfSink2: {sfGood: sfCrushed}}
	fx := sfFixture(crushed, sfSink, sfSink2)

	_, _ = sfRun(t, fx, sfPlanner(sfSink, sfSink2))

	if len(fx.sellCmds) != 2 {
		t.Fatalf("expected the refused hold to be re-offered at the next sink, got %d dispatches", len(fx.sellCmds))
	}
	if fx.cargo[sfGood] != 0 {
		t.Fatalf("no-strand violated: %d units of %s never left the hull", fx.cargo[sfGood], sfGood)
	}
	if got := fx.sellCmds[0].MinBidPerUnit; got != sfFloor {
		t.Fatalf("the first sell of a good must be armed at %d, got %d", sfFloor, got)
	}
	if got := fx.sellCmds[1].MinBidPerUnit; got != 0 {
		t.Fatalf("after one refusal the good's budget is spent: the next sell must dispatch unarmed, got floor %d", got)
	}
	if fx.sells != 1 {
		t.Fatalf("exactly one sale must land (the unarmed one), got %d", fx.sells)
	}
}

// (c) Within tolerance the floor is inert: the sell is armed but never trips, and the
// leg books the same units, revenue and trade count it books with no floor at all.
func TestTourSellFloor_WithinTolerance_LeavesTheSaleUnchanged(t *testing.T) {
	fx := sfFixture(nil, sfSink) // live bid == the cached quote == the plan basis

	resp, logger := sfRun(t, fx, sfPlanner(sfSink))

	if got := fx.sellCmds[0].MinBidPerUnit; got != sfFloor {
		t.Fatalf("the floor must still be armed at %d on a healthy sale, got %d", sfFloor, got)
	}
	if sfAbortLine(logger) != nil {
		t.Fatalf("a bid within tolerance must not trip the floor, got %+v", logger.entries)
	}
	if fx.sells != 1 || fx.cargo[sfGood] != 0 {
		t.Fatalf("the whole hold must sell as before, got %d sales leaving %d units", fx.sells, fx.cargo[sfGood])
	}
	if resp.TotalRevenue != sfUnits*sfPlanned {
		t.Fatalf("revenue must be unchanged at %d, got %d", sfUnits*sfPlanned, resp.TotalRevenue)
	}
	if resp.TradesExecuted != 1 {
		t.Fatalf("the leg must book exactly one executed trade, got %d", resp.TradesExecuted)
	}
}

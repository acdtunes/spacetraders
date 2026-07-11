package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// sp-xwa1 - the --dest lane-targeting override. These tests cover the three
// acceptance bullets directly:
//   - a directed target PINS that lane instead of whatever the ranker would
//     otherwise have chosen, and the cross-system gate charge is waived for
//     it alone (TestRankLanesByCircuitRate_TargetDest_*, TestSelectLane_Directed_Pins*)
//   - the undirected auto-scan path is untouched - still surcharges every
//     cross-system lane's circuit time, still defers to trading.FirstDisciplinedLane
//     (TestSelectLane_Undirected_*)
//   - a directed lane still respects the sp-bp6f working-capital spend floor
//     (TestTradeRouteCoordinator_TargetDest_DirectedLane_StillRespectsSpendFloor)

// Reuses the same proportions as TestRankLanesByCircuitRate_CloseCall_SameSystemLaneWins
// (run_trade_route_coordinator_travel_test.go): a cross-system lane (X) whose
// value lead over the same-system lane Y (9,000 vs 8,000 per circuit, +12.5%)
// is smaller than the gate's ~17.6% time premium, so X's rate (~6,886/hr)
// trails Y's (7,200/hr). With no target, Y must still win - no regression.
// Once X becomes the operator's directed target it is ranked at the in-system
// baseline (9,000/1.111h = 8,100/hr > 7,200/hr) and wins outright, while Y's
// own economics never change.
func TestRankLanesByCircuitRate_TargetDest_WaivesSurchargeOnlyForTargetedLane(t *testing.T) {
	lanes := []trading.ArbitrageLane{
		{Good: "X", SourceWaypoint: "X1-AAA-1", DestWaypoint: "X1-BBB-1", SpreadPerUnit: 450, VolumeCap: 20, CappedSpread: 9000},
		{Good: "Y", SourceWaypoint: "X1-AAA-2", DestWaypoint: "X1-AAA-3", SpreadPerUnit: 400, VolumeCap: 20, CappedSpread: 8000},
	}

	t.Run("undirected: the gate surcharge still applies, same-system lane wins", func(t *testing.T) {
		ranked := rankLanesByCircuitRate(lanes, 0, "")
		if ranked[0].Good != "Y" {
			t.Fatalf("expected Y first (X's 9,000 over the surcharged circuit ~6,886/hr < Y's 7,200/hr), got %q first", ranked[0].Good)
		}
	})

	t.Run("directed at X: surcharge waived for X only, X wins", func(t *testing.T) {
		ranked := rankLanesByCircuitRate(lanes, 0, "X1-BBB-1")
		if ranked[0].Good != "X" {
			t.Fatalf("expected X first once ranked at the in-system baseline (8,100/hr > 7,200/hr), got %q first", ranked[0].Good)
		}
		if ranked[0].SpreadPerUnit != 450 {
			t.Fatalf("waiver is ranking-only - X's real SpreadPerUnit must stay 450, got %d", ranked[0].SpreadPerUnit)
		}
	})
}

// C outranks B on any scoring (CappedSpread 300000 vs 72000), but the
// operator directed the run at B's destination - selectLane must PIN B, not
// hand back whatever the ranker preferred.
func TestSelectLane_Directed_PinsTargetOverRankersTopChoice(t *testing.T) {
	rankedLanes := []trading.ArbitrageLane{
		{Good: "C", SourceWaypoint: "X1-HOME-1", DestWaypoint: "X1-HOME-2", SpreadPerUnit: 5000, VolumeCap: 60, CappedSpread: 300000},
		{Good: "B", SourceWaypoint: "X1-HOME-3", DestWaypoint: "X1-NEAR-1", SpreadPerUnit: 1200, VolumeCap: 60, CappedSpread: 72000},
	}

	lane, ok := selectLane(rankedLanes, "X1-NEAR-1")
	if !ok {
		t.Fatalf("expected selectLane to find the directed lane B")
	}
	if lane.Good != "B" {
		t.Fatalf("expected the directed lane B pinned despite C ranking first, got %q", lane.Good)
	}
}

// Same fixture, no target: must fall back to trading.FirstDisciplinedLane's
// plain ranked-order walk unchanged - proving the undirected path is
// byte-for-byte untouched by this lever.
func TestSelectLane_Undirected_DefersToFirstDisciplinedLane(t *testing.T) {
	rankedLanes := []trading.ArbitrageLane{
		{Good: "C", SourceWaypoint: "X1-HOME-1", DestWaypoint: "X1-HOME-2", SpreadPerUnit: 5000, VolumeCap: 60, CappedSpread: 300000},
		{Good: "B", SourceWaypoint: "X1-HOME-3", DestWaypoint: "X1-NEAR-1", SpreadPerUnit: 1200, VolumeCap: 60, CappedSpread: 72000},
	}

	lane, ok := selectLane(rankedLanes, "")
	if !ok {
		t.Fatalf("expected a lane to be selected")
	}
	if lane.Good != "C" {
		t.Fatalf("expected undirected selectLane to defer to the ranker's top choice C, got %q", lane.Good)
	}
}

// A directed target that cannot be serviced - either no lane goes there at
// all, or the one that does fails the floor discipline - must report
// ok=false, never silently substitute a different lane the operator didn't
// ask for (the same "fail rather than substitute" contract the
// batch-purchase ship-type guard established, sp-e7je).
func TestSelectLane_Directed_CannotService_ReturnsNotOKWithoutSubstituting(t *testing.T) {
	tests := []struct {
		name   string
		lanes  []trading.ArbitrageLane
		target string
	}{
		{
			name: "no lane goes to the target at all",
			lanes: []trading.ArbitrageLane{
				{Good: "C", SourceWaypoint: "X1-HOME-1", DestWaypoint: "X1-HOME-2", SpreadPerUnit: 5000, VolumeCap: 60, CappedSpread: 300000},
			},
			target: "X1-NOWHERE-1",
		},
		{
			name: "the matching lane fails the floor discipline",
			lanes: []trading.ArbitrageLane{
				{Good: "B", SourceWaypoint: "X1-HOME-3", DestWaypoint: "X1-NEAR-1", SpreadPerUnit: 400, VolumeCap: 60, CappedSpread: 24000},
			},
			target: "X1-NEAR-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lane, ok := selectLane(tc.lanes, tc.target)
			if ok {
				t.Fatalf("expected ok=false, got a silently-substituted lane %+v", lane)
			}
		})
	}
}

// A directed --dest still goes through the same coordinator execute() path as
// the undirected auto-scan, so the sp-bp6f working-capital spend floor must
// still trip before Leg 1's buy when the live treasury can't clear it - a
// target waypoint pins WHICH lane is chosen, it does not touch whether the
// guard runs at all. Mirrors the "default reserve" case of
// TestTradeRouteCoordinator_SpendFloor_AbortsBeforeBreachingBuy exactly
// (run_trade_route_coordinator_spendfloor_test.go), with TargetDest set to
// the fixture's own real lane destination so the directed path resolves to
// the identical lane the undirected scan would have found.
func TestTradeRouteCoordinator_TargetDest_DirectedLane_StillRespectsSpendFloor(t *testing.T) {
	ship := newTradeHauler(t, "TRADER-SFT1")
	apiClient := &sfFakeAPIClient{credits: 80000}
	handler, mediator := newSpendFloorHandler(ship, apiClient)

	ctx := auth.WithPlayerToken(context.Background(), "TOKEN-SFT1")
	resp, err := handler.Handle(ctx, &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
		TargetDest:   trDest,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	if !coord.SpendFloorAbort {
		t.Fatalf("expected SpendFloorAbort even for a directed lane, got %+v", coord)
	}
	if coord.ExitReason != exitReasonSpendFloor {
		t.Fatalf("expected exit reason %q, got %q", exitReasonSpendFloor, coord.ExitReason)
	}
	if coord.TreasuryAtAbort != 80000 {
		t.Fatalf("expected TreasuryAtAbort 80000, got %d", coord.TreasuryAtAbort)
	}
	if coord.ReserveFloor != defaultWorkingCapitalReserve {
		t.Fatalf("expected default reserve floor %d, got %d", defaultWorkingCapitalReserve, coord.ReserveFloor)
	}
	if coord.Visits != 0 {
		t.Fatalf("expected 0 visits - the guard must trip before Leg 1, got %d", coord.Visits)
	}
	if len(mediator.purchases) != 0 || len(mediator.sells) != 0 {
		t.Fatalf("expected zero trades, got %d buys / %d sells", len(mediator.purchases), len(mediator.sells))
	}
}

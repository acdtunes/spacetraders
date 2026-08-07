package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// sp-k4z5b, the acceptance criterion end-to-end: a consumer still refuses genuinely dead
// data while ADMITTING a row whose age is explained by normal rotation.
//
// The pairing with run_tour_coordinator_sinkfresh_test.go is the whole point. That file's
// TestTourSinkFresh_StaleSink_RefusesBuy runs the identical 2h-stale sink WITHOUT a
// rotation source and refuses the buy — which was correct when the map was small and
// became a 87%-throughput defect when it reached 4,389 markets. These two prove the
// refusal now tracks the map instead of a constant.

// A 2h-old sink row at the incident's map size is ONE rotation, not dead data. It is past
// the old 75-minute cap — the exact age the live gate was fail-closing on — and the buy
// must now execute in full.
func TestTourSinkFresh_RotationExplainedStaleness_StillBuys(t *testing.T) {
	fx := arbFixture(1000)
	fx.staleMarkets = map[string]bool{"X1-S1-B": true} // the sink's cached row is past the boot floor
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, _ := setupTourLedger(t)
	h.SetAbsorptionLedger(&sinkGateFakeLedger{Ledger: ledger, heldOverride: map[absorption.LaneKey]int{
		{Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell}: 40,
	}}, 0)
	h.SetSinkFreshness(75 * time.Minute) // the SAME boot floor the refusing test arms with
	h.SetMarketFreshness(NewMarketFreshness(incidentRotation(), nil, nil))

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	r := tourResponse(t, resp)
	require.Equal(t, 1, fx.buys,
		"a sink one rotation old is not dead data — refusing it is what cost ~87%% of trade throughput")
	require.Equal(t, int64(4000), r.TotalSpent, "buys the full planned 40 (40×100)")
	require.Equal(t, int64(8000), r.TotalRevenue, "sells the full 40 (40×200) — nothing stranded")
}

// And the money guard is STILL a money guard. Past the rotation bound the scanner has
// failed its own anti-starvation guarantee, so the row is genuinely dead and the buy is
// refused fail-closed exactly as before (RULINGS #4).
func TestTourSinkFresh_PastRotationBound_StillRefusesBuy(t *testing.T) {
	rotation := incidentRotation()
	bound := NewMarketFreshness(rotation, nil, nil).RotationBound(context.Background())
	require.Positive(t, bound)

	fx := arbFixture(1000)
	// Comfortably past the bound: no rotation at this map size can explain this row.
	fx.ageByWaypoint = map[string]time.Duration{"X1-S1-B": bound + 6*time.Hour}
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, _ := setupTourLedger(t)
	h.SetAbsorptionLedger(&sinkGateFakeLedger{Ledger: ledger, heldOverride: map[absorption.LaneKey]int{
		{Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell}: 40,
	}}, 0)
	h.SetSinkFreshness(75 * time.Minute)
	h.SetMarketFreshness(NewMarketFreshness(rotation, nil, nil))

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.Equal(t, 0, fx.buys, "a sink older than the rotation can explain is dead — fail closed")
	require.Equal(t, int64(0), tourResponse(t, resp).TotalSpent, "nothing spent on spec into a dead sink")
}

// The listing paths widen with it. This is the twin of
// TestTour_PlacementArmed_StalenessGateExcludesStaleListings: the identical fixture, whose
// candidate system's only listings are 2h old, must now REACH the pre-flight shortlist,
// because at 4,389 markets 2h is inside one rotation. Without this the reposition ranker
// goes blind to most of the map the moment charting outruns the budget.
func TestTour_RotationExplainedStaleListings_StillReachTheShortlist(t *testing.T) {
	fx := repositionFixture()
	fx.staleMarkets = map[string]bool{"X1-S2-A": true, "X1-S2-B": true} // S2's only listings are past the floor
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		if ship.CurrentSystem == "X1-S1" {
			if ship.Cargo == nil {
				return infeasibleTour() // E_s: home dead
			}
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1()
			}
			return infeasibleTour()
		}
		return infeasibleTour()
	}}
	h := newTourHandler(t, fx, planner, &seededTelemetry{rows: betaSeedRows(150000)})
	h.SetMarketFreshness(NewMarketFreshness(incidentRotation(), nil, nil))

	_, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-STALE", PlayerID: 1, ContainerID: "ctr-stale", Iterations: -1,
		PlacementScoreEnabled: true, ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.True(t, plannerVisitedSystem(planner.positions, "X1-S2"),
		"a candidate whose listings are one rotation old must still be priced; positions=%v", planner.positions)
}

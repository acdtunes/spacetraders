package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// The fleet-wide absorption sink cap is the live knob trade_fleet.acap_tranches, replacing a
// hard-coded 2 that a raised solver tranche cap breached on every plan. These pin that all
// four cap sites read ONE resolved value, and that an unset knob is still exactly 2.

// Resolution is the whole guard: unset AND any negative a config edit could produce must
// floor to the default, never shrink the cap the solver already plans against.
func TestResolveACapTranches_FloorsUnsetAndNegativeToTheDefault(t *testing.T) {
	require.Equal(t, 2, defaultTourACapTranches, "the shipped default is two tranches")
	for knob, want := range map[int]int{-4: 2, -1: 2, 0: 2, 1: 1, 2: 2, 4: 4} {
		require.Equalf(t, want, resolveACapTranches(knob), "acap_tranches %d", knob)
		require.Equalf(t, want, (&RunTourCoordinatorCommand{ACapTranches: knob}).aCapTranches(),
			"the command seam must resolve acap_tranches %d the same way", knob)
	}
	require.Equal(t, 2, (*RunTourCoordinatorCommand)(nil).aCapTranches(), "a nil command reads the default")
}

// SITE 1, the reservation ceiling: CapUnits is acap_tranches x trade_volume. The cap still
// bounds OTHER containers' outstanding depth only, so the plan's own 490 units are
// irrelevant to it (sp-6zqza) — the knob moves the ceiling, not the accounting.
func TestBuildTourReserveEntries_CapUnitsFollowTheKnob(t *testing.T) {
	h := &RunTourCoordinatorHandler{}
	plan := &routing.TourPlan{Feasible: true, Legs: []routing.TourLeg{
		leg("X1-S1-B", "X1-S1", sell("G1", 490, 200)),
	}}
	snapshot := []routing.TourGoodSnapshot{{Waypoint: "X1-S1-B", Good: "G1", TradeVolume: 70, Bid: 200}}

	for knob, wantTranches := range map[int]int{0: 2, -1: 2, 4: 4, 6: 6} {
		entries := h.buildTourReserveEntries(&RunTourCoordinatorCommand{ACapTranches: knob}, plan, snapshot)
		require.Len(t, entries, 1)
		require.Equalf(t, wantTranches*70, entries[0].CapUnits,
			"acap_tranches %d must cap the lane at %d tranches of trade_volume", knob, wantTranches)
	}
}

// SITE 2, the cap-binding metric's netted availability ceiling. 60 units against 150
// outstanding on a tv-100 lane is BOUND under two tranches (ceiling 50) and UNBOUND under
// four (ceiling 250): a ceiling still reading a hard-coded 2 would mislabel the raised
// fleet's every lane as cap-bound.
func TestClassifyCapBinding_CeilingFollowsTheKnob(t *testing.T) {
	plan := &routing.TourPlan{Legs: []routing.TourLeg{cbLeg("W1", cbSell("IRON", 60))}}
	snapshot := []routing.TourGoodSnapshot{cbSnap("W1", "IRON", 100)}
	view := []routing.TourMarketAbsorption{cbAbsorbed("W1", "IRON", absorption.SideSell, 150, 0)}

	require.Equal(t, []capBindingSample{{side: absorption.SideSell, outcome: "bound"}},
		classifyCapBinding(plan, snapshot, view, 0), "unset falls back to two tranches: ceiling 50, 60 units bind")
	require.Equal(t, []capBindingSample{{side: absorption.SideSell, outcome: "unbound"}},
		classifyCapBinding(plan, snapshot, view, 4), "four tranches lift the ceiling to 250, so 60 units are unbound")
}

// SITE 3, the sell floor's DEPTH rule. The floor arms only past acap_tranches x
// trade_volume into one visit's sink, so raising the knob must push the arming point out
// with it: at 4 the first FOUR tranches dispatch unarmed and only the fifth carries a floor.
// A DEPTH rule still reading the hard-coded 2 arms on the third.
func TestTourSellFloor_DepthRuleFollowsTheKnob(t *testing.T) {
	fx := sfFixture(map[string]map[string]int{sfSink: {sfGood: sfCrushed}}, sfSink)

	h := newTourHandler(t, fx, sfPlanner(5, sfSink), &tourFakeTelemetry{})
	_, err := h.Handle(common.WithLogger(context.Background(), &laneLogCapturingLogger{}), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-SF", PlayerID: 1, ContainerID: "tour-run-TORWIND-SF",
		ModelArtifactPath: writeTourArtifact(t), ACapTranches: 4,
	})
	require.NoError(t, err)

	require.Equal(t, []int{0, 0, 0, 0, sfFloor}, sfFloors(fx),
		"acap_tranches 4 arms the floor only past 4 x trade_volume into the visit")
	require.Equal(t, 4, fx.sells, "the four shallow tranches sell; the deep one is refused on the crushed bid")
}

// The same fixture under an UNSET knob is byte-identical to the shipped behaviour: the floor
// arms on the third tranche, past two tranches of depth.
func TestTourSellFloor_UnsetKnobKeepsTheTwoTrancheDepthRule(t *testing.T) {
	fx := sfFixture(map[string]map[string]int{sfSink: {sfGood: sfCrushed}}, sfSink)

	h := newTourHandler(t, fx, sfPlanner(5, sfSink), &tourFakeTelemetry{})
	_, err := h.Handle(common.WithLogger(context.Background(), &laneLogCapturingLogger{}), &RunTourCoordinatorCommand{
		ShipSymbol: "TORWIND-SF", PlayerID: 1, ContainerID: "tour-run-TORWIND-SF",
		ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.Equal(t, []int{0, 0, sfFloor, 0, 0}, sfFloors(fx),
		"an unset knob still arms on the third tranche, and the one-refusal budget disarms the rest")
}

// SITE 4, the firm-sink gate's LIVE depth re-read. The sink's live trade_volume collapsed to
// 10, so two tranches would bound the buy to 20 units (the shipped pin) — under four it
// absorbs the full firm 40 and the buy is unshrunk.
func TestTourSinkFresh_LiveDepthFollowsTheKnob(t *testing.T) {
	fx := arbFixture(1000)
	fx.tv["X1-S1-B"]["G1"] = 10
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, _ := setupTourLedger(t)
	h.SetAbsorptionLedger(&sinkGateFakeLedger{Ledger: ledger, heldOverride: map[absorption.LaneKey]int{
		{Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell}: 40,
	}}, 0)
	h.SetSinkFreshness(75 * time.Minute)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1",
		ModelArtifactPath: writeTourArtifact(t), ACapTranches: 4,
	})
	require.NoError(t, err)

	require.Equal(t, int64(4000), tourResponse(t, resp).TotalSpent,
		"4 x live tv 10 absorbs the full firm 40 units (40x100); a hard-coded 2 would have bound it to 2000")
}

// The launch log is how a deploy read confirms what a container actually runs — the 18:55Z
// mismatch was invisible because nothing printed the cap.
func TestTourCoordinator_LogsTheResolvedACapTranchesAtStart(t *testing.T) {
	fx := arbFixture(1000)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	logger := &laneLogCapturingLogger{}

	_, err := h.Handle(common.WithLogger(context.Background(), logger), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1",
		ModelArtifactPath: writeTourArtifact(t), ACapTranches: 4,
	})
	require.NoError(t, err)

	var found *laneLogEntry
	for i := range logger.entries {
		if logger.entries[i].metadata["action"] == "tour_acap_tranches" {
			found = &logger.entries[i]
		}
	}
	require.NotNil(t, found, "the tour must name its resolved sink cap once at start")
	require.Equal(t, 4, found.metadata["acap_tranches"])
	require.Contains(t, found.message, "acap_tranches=4")
}

// The coordinator hands its knob to every tour it launches; unset stays 0 so the launch spec
// is byte-identical to today and the tour keeps its own default.
func TestBuildTourLaunchSpec_CarriesACapTranchesToEveryTour(t *testing.T) {
	spec := buildTourLaunchSpec(&RunTradeFleetCoordinatorCommand{ACapTranches: 4}, "HULL-1", "trade", false, 150000)
	require.Equal(t, 4, spec.ACapTranches, "the launch spec carries the coordinator's cap")

	require.Equal(t, 0, buildTourLaunchSpec(&RunTradeFleetCoordinatorCommand{}, "HULL-1", "trade", false, 150000).ACapTranches,
		"an unset knob writes nothing, so the tour falls back to its own default")
}

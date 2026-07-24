package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// Hold the sink firm through the sell (sp-pcxju Part 2): a laden hull's re-plan must NOT
// release the sink reservation backing cargo already in the hold, or another engine crushes
// that sink before the hull can sell — the exact stranding this fix exists to stop. The
// release-before-(re)plan now PRESERVES the sink-side PLANNED rows for goods currently held
// and drops only the stale buy-side and the sinks for goods not yet bought.

// A hull holding G1, carrying a prior PLANNED buy-side hold AND the sink reservation backing
// that held G1, re-plans (the plan comes back infeasible, isolating the release path). The
// held-cargo sink must SURVIVE; the stale buy-side hold must drop.
func TestTourSinkHold_LadenReplan_PreservesHeldCargoSink(t *testing.T) {
	ledger, db := setupTourLedger(t)
	ctx := context.Background()

	// ctr-1's prior plan: a buy-side hold at A (G1 already bought — stale) and the sink at
	// B backing that held G1 (must be preserved so the hull can still sell it).
	_, ok, err := ledger.Reserve(ctx, 1, "ctr-1", "tour", []absorption.ReserveEntry{
		{Waypoint: "X1-S1-A", Good: "G1", Side: absorption.SideBuy, Units: 40, CapUnits: 400, TTL: time.Hour},
		{Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell, Units: 40, CapUnits: 400, TTL: time.Hour},
	})
	require.NoError(t, err)
	require.True(t, ok)

	fx := arbFixture(1000)
	fx.cargo = map[string]int{"G1": 40} // the hull is LADEN with the already-bought G1
	// The re-plan comes back infeasible: planAndReserve still runs its release-before-plan
	// first, so this isolates exactly what that release keeps vs drops.
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: false, InfeasibleReason: "isolating the release path"}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetAbsorptionLedger(ledger, 0)

	_, err = h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	rows := tourLedgerRows(t, db, "ctr-1")
	require.Len(t, rows, 1, "only the held-cargo sink survives; the stale buy-side hold was released")
	require.Equal(t, "PLANNED", rows[0].State)
	require.Equal(t, "X1-S1-B", rows[0].Waypoint)
	require.Equal(t, absorption.SideSell, rows[0].Side)
	require.Equal(t, "G1", rows[0].Good)
	require.Equal(t, 40, rows[0].Units, "the held cargo keeps its full guaranteed sell depth")
}

// The fresh path is byte-identical: a hull holding NOTHING releases every prior PLANNED row
// (nothing to preserve), exactly as before Part 2 — so an empty-hold re-adoption still
// de-dups its stale rows and never double-reserves.
func TestTourSinkHold_EmptyHold_ReleasesEverything(t *testing.T) {
	ledger, db := setupTourLedger(t)
	ctx := context.Background()

	_, ok, err := ledger.Reserve(ctx, 1, "ctr-1", "tour", []absorption.ReserveEntry{
		{Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell, Units: 40, CapUnits: 400, TTL: time.Hour},
	})
	require.NoError(t, err)
	require.True(t, ok)

	fx := arbFixture(1000) // empty cargo
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: false, InfeasibleReason: "isolating the release path"}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetAbsorptionLedger(ledger, 0)

	_, err = h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.Empty(t, tourLedgerRows(t, db, "ctr-1"), "an empty hold preserves nothing — every stale PLANNED row is released")
}

// With the held-cargo sink now PRESERVED across the re-plan, the fresh plan must NOT
// re-reserve that same sink on top of the preserved row, or the sink is double-counted and
// its sale records a DOUBLED crush (two EXECUTED shadows) that misleads every other engine.
// A laden re-plan that re-sells the held cargo at its preserved sink yields exactly ONE
// reservation lifecycle → ONE shadow.
func TestTourSinkHold_LadenReplan_ReSellPreservedSink_NoDoubleReserve(t *testing.T) {
	ledger, db := setupTourLedger(t)
	ctx := context.Background()

	// ctr-1 already holds the sink backing its held G1.
	_, ok, err := ledger.Reserve(ctx, 1, "ctr-1", "tour",
		[]absorption.ReserveEntry{{Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell, Units: 40, CapUnits: 2000, TTL: time.Hour}})
	require.NoError(t, err)
	require.True(t, ok)

	fx := arbFixture(1000)
	fx.cargo = map[string]int{"G1": 40} // laden — will sell the held G1 at its preserved sink
	// The re-plan liquidates the held cargo at the SAME sink it is already reserved into.
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{leg("X1-S1-B", "X1-S1", sell("G1", 40, 200))},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetAbsorptionLedger(ledger, 0)

	_, err = h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	rows := tourLedgerRows(t, db, "ctr-1")
	require.Len(t, rows, 1, "one shadow, not two — the preserved sink was re-used, not re-reserved")
	require.Equal(t, "EXECUTED", rows[0].State)
	require.Equal(t, 40, rows[0].Units, "a single 40u crush recorded, not a doubled 80")
}

// A preserved held-cargo sink must be netted OUT of THIS container's own plan request, so
// the solver plans INTO its own reserved sink (sells the held cargo there) instead of
// treating its own hold as occupied depth to route around — which, for a sink filled to its
// cap, would self-net to infeasible and strand the cargo. Another container's depth still
// nets in (the coordination the ledger exists for).
func TestTourSinkHold_LadenReplan_NetsOwnPreservedSinkOutOfPlanRequest(t *testing.T) {
	ledger, _ := setupTourLedger(t)
	ctx := context.Background()

	// This container's own held-cargo sink (G1) and a RIVAL's sink (G2) on the same market.
	_, ok, err := ledger.Reserve(ctx, 1, "ctr-1", "tour",
		[]absorption.ReserveEntry{{Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell, Units: 40, CapUnits: 2000, TTL: time.Hour}})
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, err = ledger.Reserve(ctx, 1, "rival", "idle-arb",
		[]absorption.ReserveEntry{{Waypoint: "X1-S1-B", Good: "G2", Side: absorption.SideSell, Units: 30, CapUnits: 2000, TTL: time.Hour}})
	require.NoError(t, err)
	require.True(t, ok)

	fx := arbFixture(1000)
	fx.cargo = map[string]int{"G1": 40} // laden — its G1 sink is preserved across the re-plan
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{Feasible: false, InfeasibleReason: "capture the plan request only"}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	h.SetAbsorptionLedger(ledger, 0)

	_, err = h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.NotEmpty(t, planner.absorptions, "the coordinator must call the planner at least once")
	var sawOwn, sawRival bool
	for _, a := range planner.absorptions[0] {
		if a.Good == "G1" && a.Waypoint == "X1-S1-B" {
			sawOwn = true
		}
		if a.Good == "G2" && a.Waypoint == "X1-S1-B" && a.PlannedUnits == 30 {
			sawRival = true
		}
	}
	require.False(t, sawOwn, "the hull's OWN preserved sink must be netted out so it plans INTO it: %+v", planner.absorptions[0])
	require.True(t, sawRival, "another container's depth must still net in")
}

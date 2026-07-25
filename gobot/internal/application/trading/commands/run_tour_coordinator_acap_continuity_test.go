package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// Cross-plan A-cap continuity. The per-plan tranche ladder is rebuilt
// from scratch every plan, so what a predecessor plan consumed at a sink has to reach the
// successor through the ledger or not at all. It reached it for TAGGED sinks only —
// untagged sales left no row — which is where consecutive plans lawfully rebuilt the D39
// ladder. These drive the whole coordinator against the REAL ledger, so the carry is
// proven across an actual plan boundary rather than asserted on a stub.

// untaggedArbFixture is arbFixture with both markets UNTAGGED (the ~24% of the live
// market universe whose activity the model never fit).
func untaggedArbFixture(tv int) *tourFixture {
	fx := arbFixture(tv)
	fx.activityByWaypoint = map[string]string{"X1-S1-A": "", "X1-S1-B": ""}
	return fx
}

// blindLedger fails every Outstanding read while leaving the rest of the ledger intact —
// the transient-DB-error shape the netting consult must survive without widening depth.
type blindLedger struct {
	absorption.Ledger
}

func (blindLedger) Outstanding(context.Context, int) (map[absorption.LaneKey]absorption.KeyOccupancy, error) {
	return nil, errors.New("ledger read failed")
}

// Two consecutive plans by the same hull: what the first sold into an untagged sink must
// be visible to the second as recovering depth. Without it the second plan sees virgin
// depth and re-takes the full ladder at a sink the fleet is still recovering.
func TestTourAbsorption_UntaggedSinkConsumptionCarriesIntoTheNextPlan(t *testing.T) {
	// trade_volume 40 makes the plan's 40-unit sale exactly ONE tranche, so the shadow
	// clears the half-tranche recovery floor. A token sale into a vast market is meant
	// to leave nothing blocking — that floor is doing its job, not hiding the carry.
	fx := untaggedArbFixture(40)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, _ := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, 0)
	cmd := &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	}

	_, err := h.Handle(context.Background(), cmd)
	require.NoError(t, err)
	_, err = h.Handle(context.Background(), cmd)
	require.NoError(t, err)

	require.Len(t, planner.absorptions, 2, "expected one plan per Handle")
	var recovering float64
	for _, a := range planner.absorptions[1] {
		if a.Waypoint == "X1-S1-B" && a.Good == "G1" && a.Side == absorption.SideSell {
			recovering = a.RecoveringUnits
		}
	}
	require.Greater(t, recovering, 0.0,
		"the first plan's realized consumption at the untagged sink must reach the second plan, "+
			"or consecutive plans rebuild the whole ladder there: %+v", planner.absorptions[1])
}

// Fail CLOSED (RULINGS #4 — the A-cap bounds BUY COMMITMENT). An unreadable ledger must
// not hand the planner a view that FREES depth. It degrades to no view at all, which
// leaves the solver's own per-plan A-cap and the conditional Reserve (fail-closed
// in-transaction) as the binding caps — bounded, never unbounded.
func TestTourAbsorption_LedgerReadFailureDegradesToThePerPlanCapNotUnbounded(t *testing.T) {
	fx := untaggedArbFixture(1000)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, db := setupTourLedger(t)
	ctx := context.Background()

	// A rival holds real outstanding depth at the sink — depth a working read would net.
	_, ok, err := ledger.Reserve(ctx, 1, "rival", "idle-arb", []absorption.ReserveEntry{{
		Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell,
		Units: 40, CapUnits: 4000, TTL: time.Hour,
	}})
	require.NoError(t, err)
	require.True(t, ok)
	h.SetAbsorptionLedger(blindLedger{Ledger: ledger}, 0)

	_, err = h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.NotEmpty(t, planner.absorptions)
	require.Empty(t, planner.absorptions[0],
		"an unreadable ledger must yield NO netting view — never a partial one that reports depth as free")

	// The hard cap still bound: the plan's own sink reservation was written under the
	// conditional Reserve, so commitment stayed capped despite the blind read.
	rows := tourLedgerRows(t, db, "ctr-1")
	require.NotEmpty(t, rows, "the plan must still have reserved through the fleet-wide cap")
}

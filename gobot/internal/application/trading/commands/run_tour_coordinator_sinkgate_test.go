package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// The execution-time firm-sink gate on the buy (sp-pcxju): a tour buys cargo ONLY up to
// the depth its OWN downstream sink reservation can still absorb, and refuses entirely
// when no firm sink is held — "we absolutely cannot buy cargo and not sell it." The gate
// ports achievableUnits' min-bound shape into the tour's plan-time-reserve model: the
// firm bound is this container's still-held PLANNED sell-depth (≤ CapUnits − others' by
// the ledger's cap invariant), so a deep freshly-reserved sink is a no-op (byte-identical,
// proven by the existing absorption suite staying green) and a lost/saturated sink is a
// hard refuse.

// sinkGateFakeLedger decorates the real DB-backed ledger to control ONLY the firm-sink
// read the buy gate consults, staging the "sink lost / unreadable at buy time" states a
// single forward Handle cannot otherwise produce (the plan-time Reserve would have blocked
// them). The ledger is a driven port, so overriding it at the boundary is the sanctioned
// test double; every other lifecycle call passes through to the real ledger.
type sinkGateFakeLedger struct {
	absorption.Ledger
	heldOverride map[absorption.LaneKey]int
	heldErr      error
}

func (l *sinkGateFakeLedger) HeldByContainer(ctx context.Context, containerID string, playerID int) (map[absorption.LaneKey]int, error) {
	if l.heldErr != nil {
		return nil, l.heldErr
	}
	return l.heldOverride, nil
}

// PARTIAL HEADROOM (end-to-end, real ledger): a plan that would buy 40 into a sink it only
// reserved 20 units of (an over-buy the executor must not honor) is bound to the firm 20 —
// the gate ties every buy to guaranteed sell depth, so nothing is stranded.
func TestTourSinkGate_PartialSink_BoundsBuyToFirmReservedDepth(t *testing.T) {
	fx := arbFixture(1000) // A: G1 ask 100; B: G1 bid 200; tv 1000
	// Over-buy plan: 40 bought at A, but only 20 of sink B is reserved (the sell leg).
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{{
		Feasible: true, Legs: []routing.TourLeg{
			leg("X1-S1-A", "X1-S1", buy("G1", 40, 100)),
			leg("X1-S1-B", "X1-S1", sell("G1", 20, 200)),
		},
	}}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, _ := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, 0)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	r := tourResponse(t, resp)
	require.Equal(t, 1, fx.buys, "exactly one buy executed")
	require.Equal(t, int64(2000), r.TotalSpent, "the buy is bound to the 20 units of firm sink depth (20×100), not the planned 40 (which would be 4000)")
	require.Equal(t, int64(4000), r.TotalRevenue, "the 20 firm-sink units all sold (20×200) — nothing stranded")
}

// SATURATED / LOST SINK → buy 0, spend nothing. At buy time the hull holds NO reservation
// for the good's sink (another engine has since filled it, or a re-plan dropped it): the
// executor refuses on spec rather than buy cargo it cannot guarantee selling.
func TestTourSinkGate_NoFirmSinkHeld_RefusesBuy(t *testing.T) {
	fx := arbFixture(1000)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}} // buy 40 / sell 40
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, _ := setupTourLedger(t)
	// The plan reserves its sink normally, but the read the gate consults reports the sink
	// as no longer held by this container (saturated by others / dropped).
	h.SetAbsorptionLedger(&sinkGateFakeLedger{Ledger: ledger, heldOverride: map[absorption.LaneKey]int{}}, 0)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.Equal(t, 0, fx.buys, "a good with no firm sink held is never bought")
	require.Equal(t, int64(0), tourResponse(t, resp).TotalSpent, "nothing is spent on spec")
}

// RULINGS #4 fail-closed: if the firm-sink read itself cannot run, the buy does NOT proceed
// — an unreadable guard never green-lights a spend.
func TestTourSinkGate_LedgerReadError_FailsClosed(t *testing.T) {
	fx := arbFixture(1000)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, _ := setupTourLedger(t)
	h.SetAbsorptionLedger(&sinkGateFakeLedger{Ledger: ledger, heldErr: context.DeadlineExceeded}, 0)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.Equal(t, 0, fx.buys, "an unreadable firm-sink guard fails closed — no buy")
	require.Equal(t, int64(0), tourResponse(t, resp).TotalSpent)
}

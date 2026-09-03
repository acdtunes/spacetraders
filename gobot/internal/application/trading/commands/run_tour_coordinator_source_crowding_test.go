package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// Source crowding across hulls. The trade fleet's anti-herd guards were all scoped to ONE
// hull, so nothing bounded how many DIFFERENT hulls routed to the same market to buy.
// These drive the FULL coordinator against the REAL ledger, so what one hull's purchase
// leaves behind is proven to reach the NEXT hull's plan rather than asserted on a stub.

// buySideRecovering reports the recovering depth a plan request carried for the source
// pool at `waypoint`.
func buySideRecovering(view []routing.TourMarketAbsorption, waypoint, good string) float64 {
	for _, a := range view {
		if a.Waypoint == waypoint && a.Good == good && a.Side == absorption.SideBuy {
			return a.RecoveringUnits
		}
	}
	return 0
}

// One hull buys; the NEXT hull's plan must see the source it drained. Without the buy-side
// shadow the follower reads untouched depth at a market the fleet just emptied, ranks it
// best again, and pays the ask its predecessor moved.
func TestTourSourceCrowding_OneHullsPurchaseReachesTheNextHullsPlan(t *testing.T) {
	ledger, _ := setupTourLedger(t)
	ctx := context.Background()

	// trade_volume 40 makes the plan's 40-unit buy exactly ONE tranche, so the pool clears
	// its half-tranche floor.
	leader := newTourHandler(t, arbFixture(40), &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}, &tourFakeTelemetry{})
	leader.SetAbsorptionLedger(ledger, 0)
	_, err := leader.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	followerPlanner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	follower := newTourHandler(t, arbFixture(40), followerPlanner, &tourFakeTelemetry{})
	follower.SetAbsorptionLedger(ledger, 0)
	_, err = follower.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-2", PlayerID: 1, ContainerID: "ctr-2", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.NotEmpty(t, followerPlanner.absorptions)
	require.Greater(t, buySideRecovering(followerPlanner.absorptions[0], "X1-S1-A", "G1"), 0.0,
		"the source the first hull drained must be de-ranked for the second: %+v", followerPlanner.absorptions[0])
}

// thinSourceFixture is the arb fixture with a THIN source and a deep sink, so only the
// SOURCE's depth can be what refuses a hull — the sell side of the same lane stays wide
// open and cannot be mistaken for the cause.
func thinSourceFixture(sourceTV int) *tourFixture {
	fx := arbFixture(sourceTV)
	fx.tv = map[string]map[string]int{"X1-S1-A": {"G1": sourceTV}, "X1-S1-B": {"G1": 100_000}}
	return fx
}

// The bound is fleet-wide, not per hull: once the fleet's purchases have taken a source's
// measured depth the next hull cannot reserve it — the case every per-hull guard passes.
func TestTourSourceCrowding_TheFleetCannotAllSourceOneMarket(t *testing.T) {
	ledger, _ := setupTourLedger(t)
	ctx := context.Background()
	artifact := writeTourArtifact(t)

	// Source trade_volume 40: the A-cap is 80 units of OTHERS' depth. Two hulls' loads fill
	// it, the third finds their shadows a hair under it and is admitted on that — its own
	// load is not the question (sp-6zqza) — and every hull after is refused.
	completed := 0
	for i := 0; i < 6; i++ {
		h := newTourHandler(t, thinSourceFixture(40), &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}, &tourFakeTelemetry{})
		h.SetAbsorptionLedger(ledger, 0)
		resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
			ShipSymbol: "TOUR-" + string(rune('A'+i)), PlayerID: 1,
			ContainerID: "ctr-" + string(rune('A'+i)), ModelArtifactPath: artifact,
		})
		require.NoError(t, err)
		if tourResponse(t, resp).Completed {
			completed++
		}
	}

	require.Equal(t, 3, completed,
		"the source serves the hulls its depth carries and refuses the rest of the fleet")
}

// The displaced hull FLIES. A bound that parked a hull whose best source was taken would
// cost more than the crowding it fixes, so the refusal must re-plan onto the next-best
// source and complete the tour from there.
func TestTourSourceCrowding_DisplacedHullRePlansOntoItsNextBestSource(t *testing.T) {
	ledger, _ := setupTourLedger(t)
	ctx := context.Background()
	artifact := writeTourArtifact(t)

	// Two sources for the same good: A is thin and about to be taken, C is untouched.
	fixture := func() *tourFixture {
		fx := thinSourceFixture(40)
		fx.markets = map[string][]string{"X1-S1": {"X1-S1-A", "X1-S1-B", "X1-S1-C"}}
		fx.ask["X1-S1-C"] = map[string]int{"G1": 100}
		fx.tv["X1-S1-C"] = map[string]int{"G1": 100_000}
		return fx
	}
	viaC := &routing.TourPlan{Feasible: true, ProjectedProfit: 4000, Legs: []routing.TourLeg{
		leg("X1-S1-C", "X1-S1", buy("G1", 40, 100)),
		leg("X1-S1-B", "X1-S1", sell("G1", 40, 200)),
	}}

	// Three hulls take the thin source past its depth: two fill it, the third is admitted on
	// their shadows a hair under the cap (sp-6zqza).
	for i := 0; i < 3; i++ {
		h := newTourHandler(t, fixture(), &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}, &tourFakeTelemetry{})
		h.SetAbsorptionLedger(ledger, 0)
		resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
			ShipSymbol: "TOUR-" + string(rune('A'+i)), PlayerID: 1,
			ContainerID: "ctr-" + string(rune('A'+i)), ModelArtifactPath: artifact,
		})
		require.NoError(t, err)
		require.True(t, tourResponse(t, resp).Completed)
	}

	// The fourth hull ranks the taken source best, is refused, and re-plans onto C.
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan(), viaC}}
	h := newTourHandler(t, fixture(), planner, &tourFakeTelemetry{})
	h.SetAbsorptionLedger(ledger, 0)
	resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-D", PlayerID: 1, ContainerID: "ctr-D", ModelArtifactPath: artifact,
	})
	require.NoError(t, err)

	require.True(t, tourResponse(t, resp).Completed,
		"a hull displaced from a crowded source must fall through to the next, not stall")
	require.GreaterOrEqual(t, planner.calls, 2, "the refusal must have driven a re-plan")
}

// A small fleet is untouched: the same source, the same rules, and every hull flies —
// the bound is depth, and a fleet that does not stack never reaches it.
func TestTourSourceCrowding_SmallFleetIsUnaffected(t *testing.T) {
	ledger, _ := setupTourLedger(t)
	ctx := context.Background()
	artifact := writeTourArtifact(t)

	for i := 0; i < 3; i++ {
		h := newTourHandler(t, arbFixture(1000), &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}, &tourFakeTelemetry{})
		h.SetAbsorptionLedger(ledger, 0)
		resp, err := h.Handle(ctx, &RunTourCoordinatorCommand{
			ShipSymbol: "TOUR-" + string(rune('A'+i)), PlayerID: 1,
			ContainerID: "ctr-" + string(rune('A'+i)), ModelArtifactPath: artifact,
		})
		require.NoError(t, err)
		require.True(t, tourResponse(t, resp).Completed,
			"a fleet small against the source's depth must fly unchanged")
	}
}

// Fail closed (RULINGS #4): when the fleet's own purchase volume cannot be read, the guard
// does not silently disappear. The netting view degrades to nothing rather than to a view
// that reports the drained source as free, and the conditional Reserve — which reads the
// pool inside its own transaction — remains the binding cap.
func TestTourSourceCrowding_UnreadableFleetVolumeDoesNotFreeTheSource(t *testing.T) {
	ledger, db := setupTourLedger(t)
	ctx := context.Background()

	// A rival holds real outstanding depth at the SOURCE — depth a working read would net.
	_, ok, err := ledger.Reserve(ctx, 1, "rival", "tour", []absorption.ReserveEntry{{
		Waypoint: "X1-S1-A", Good: "G1", Side: absorption.SideBuy,
		Units: 40, CapUnits: 4000, TTL: time.Hour,
	}})
	require.NoError(t, err)
	require.True(t, ok)

	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, arbFixture(1000), planner, &tourFakeTelemetry{})
	h.SetAbsorptionLedger(blindLedger{Ledger: ledger}, 0)

	_, err = h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.NotEmpty(t, planner.absorptions)
	require.Empty(t, planner.absorptions[0],
		"an unreadable ledger must yield NO netting view — never one that reports a drained source as free")
	require.NotEmpty(t, tourLedgerRows(t, db, "ctr-1"),
		"the plan must still have reserved through the fleet-wide cap")
}

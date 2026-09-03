package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// The execution-time SINK-FRESHNESS clause on the firm-sink buy gate (sp-tgll8, item 2).
// sp-pcxju proved the downstream sink is FIRM (reserved) and DEEP (enough units); this
// extends the SAME gate with the "FRESH" clause of the Admiral principle ("a buy must
// target a GUARANTEED, RESERVED, REACHABLE, FRESH, sufficiently-DEEP sink"): at BUY
// EXECUTION it re-reads the sink waypoint's LIVE market_data (the same cached market read
// observeGood/foreignMarketFresh use, aged against the same now.Sub(LastUpdated) notion the
// stocker/reposition freshness gates use) and refuses on stale data / shrinks on a sink
// whose live depth shrank since planning. Inert until SetSinkFreshness arms it (every
// existing ledger-wired tour test never calls it, so the absorption/sinkgate/sinkhold suites
// stay green — the byte-identical proof), and a no-op on a genuinely fresh, deep sink.

// STALE SINK → buy 0, spend nothing. The hull holds a FIRM reservation on the sink (so
// sp-pcxju alone would buy), but the sink's cached market_data is older than the freshness
// threshold: the "fresh" guarantee cannot be confirmed, so the buy is refused fail-closed
// (RULINGS #4 — never buy cargo whose sink we cannot confirm is live).
func TestTourSinkFresh_StaleSink_RefusesBuy(t *testing.T) {
	fx := arbFixture(1000)
	fx.staleMarkets = map[string]bool{"X1-S1-B": true}                       // the sink's cached row is 2h old (>75m)
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}} // buy 40 / sell 40
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, _ := setupTourLedger(t)
	// A firm 40-unit sink reservation is held (sp-pcxju would buy) — only staleness refuses.
	h.SetAbsorptionLedger(&sinkGateFakeLedger{Ledger: ledger, heldOverride: map[absorption.LaneKey]int{
		{Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell}: 40,
	}}, 0)
	h.SetSinkFreshness(75 * time.Minute)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.Equal(t, 0, fx.buys, "a buy into a STALE sink is refused — the fresh guarantee failed")
	require.Equal(t, int64(0), tourResponse(t, resp).TotalSpent, "nothing spent on spec into a stale sink")
}

// SHRUNK LIVE DEPTH → shrink the buy. The sink is FRESH but its live trade_volume dropped
// since planning, so the depth it can now absorb (defaultTourACapTranches × live tradeVolume) is
// below the firm reservation: the buy is bound to the live depth, not the stale reservation
// (mirrors sp-pcxju's shrink-to-firm-depth, re-checked against the live market).
func TestTourSinkFresh_ShrunkLiveDepth_ShrinksBuy(t *testing.T) {
	fx := arbFixture(1000)
	fx.tv["X1-S1-B"]["G1"] = 10                                              // sink live trade_volume collapsed: 2×10 = 20 absorbable now
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}} // buy 40 / sell 40
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, _ := setupTourLedger(t)
	h.SetAbsorptionLedger(&sinkGateFakeLedger{Ledger: ledger, heldOverride: map[absorption.LaneKey]int{
		{Waypoint: "X1-S1-B", Good: "G1", Side: absorption.SideSell}: 40, // firm 40 still held
	}}, 0)
	h.SetSinkFreshness(75 * time.Minute)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	require.Equal(t, 1, fx.buys, "exactly one (shrunk) buy executed")
	require.Equal(t, int64(2000), tourResponse(t, resp).TotalSpent,
		"the buy is bound to the live sink depth (2×tv=20 units × 100), not the firm 40 (which would be 4000)")
}

// FRESH + DEEP sink → byte-identical to sp-pcxju (the armed gate is a no-op). Proven
// end-to-end against the REAL ledger with the freshness gate ARMED: a fresh, deep sink buys
// and sells the full planned 40, exactly as it would with the freshness gate unarmed.
func TestTourSinkFresh_FreshDeepSink_ByteIdentical(t *testing.T) {
	fx := arbFixture(1000) // sink B fresh (observedAt=now), tv 1000 → 2×1000 absorbable
	planner := &tourFakeRoutingClient{plans: []*routing.TourPlan{arbPlan()}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})
	ledger, _ := setupTourLedger(t)
	h.SetAbsorptionLedger(ledger, 0)
	h.SetSinkFreshness(75 * time.Minute)

	resp, err := h.Handle(context.Background(), &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-1", PlayerID: 1, ContainerID: "ctr-1", ModelArtifactPath: writeTourArtifact(t),
	})
	require.NoError(t, err)

	r := tourResponse(t, resp)
	require.Equal(t, 1, fx.buys, "the fresh deep sink buys the full planned tranche")
	require.Equal(t, int64(4000), r.TotalSpent, "buys the full 40 (40×100) — the armed gate is a no-op")
	require.Equal(t, int64(8000), r.TotalRevenue, "sells the full 40 (40×200) — nothing stranded")
}

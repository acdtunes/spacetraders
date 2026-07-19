package capacity

// Behavioral tests for the GOVERN-phase capex EMITTER. The lane does NOT build
// a standalone capex governor/guard stack; instead the capital tier translates
// its tier-4 Actions into contract-delivery capacity demand and EMITS it to the
// already-built fleet autosizer (which owns the single guard stack that
// executes the buy). Every test drives the Governor port (Govern) and asserts
// observable outcomes: the GovernResult and the demand published at the
// driven-port boundary (CapitalDemandSink spy).
//
// Test budget (this file): 3 behaviors — (1) tier-4 → demand translation +
// cheap-tier passthrough + ROI evidence, (2) no-capital → fresh zero, (3) thin
// emitter with NO second guard stack — well within 5 behaviors × 2.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingSink is the driven-port boundary the emitter writes to — the seam
// the fleet autosizer consumes through its registered provider.
type recordingSink struct {
	emitted []CapitalDemand
}

func (s *recordingSink) EmitCapitalDemand(d CapitalDemand) { s.emitted = append(s.emitted, d) }

// B1 + B3 + B4(passthrough): the emitter sums the tier-4 Actions' HullDelta per
// role into one demand snapshot, hands the fleet autosizer the marginal ROI
// projection as evidence, and passes cheap (autonomous) tiers straight through
// to Approved.
func TestCapexEmitter_TranslatesCapitalActionsToDemandAndPassesCheapTiersThrough(t *testing.T) {
	sink := &recordingSink{}
	emitter := NewCapexEmitter(sink)

	reassign := Action{Tier: TierReuseIdle, Verb: VerbReassignHull, ShipSymbol: "SHIP-IDLE"}
	buyWarehouse := Action{
		Tier: TierCapital, Verb: VerbBuyHull, HullDelta: 1, WarehouseDelta: 1,
		GapKind: GapWarehouseShort, ProjectedPerHullCrHr: 900000, EstimatedCostCredits: 400000,
	}
	addCluster := Action{
		Tier: TierCapital, Verb: VerbAddCluster, HullDelta: 3,
		WarehouseDelta: 1, StockerDelta: 1, WorkerDelta: 1,
		ProjectedPerHullCrHr: 820000, EstimatedCostCredits: 1200000,
	}

	result, err := emitter.Govern(context.Background(),
		[]Action{reassign, buyWarehouse, addCluster},
		EconomicsSignals{FleetPerHullCrHr: 700000, FleetHullCount: 12}, DefaultCalibration())
	require.NoError(t, err)

	// Cheap tier passes through to Approved verbatim; capital tiers do NOT (the
	// reconciler's converge must never execute them — the fleet autosizer's path does).
	require.Equal(t, []Action{reassign}, result.Approved)
	require.Empty(t, result.Proposals, "the emitter mints no proposals — the capital path is sp-1txd, not the reconciler")

	// Exactly one demand snapshot published, summing the tier-4 hull deltas per role.
	require.Len(t, sink.emitted, 1)
	d := sink.emitted[0]
	require.True(t, d.Present)
	require.Equal(t, 4, d.Hulls)          // 1 + 3
	require.Equal(t, 2, d.WarehouseHulls) // 1 + 1
	require.Equal(t, 1, d.StockerHulls)
	require.Equal(t, 1, d.DeliveryHulls)
	// ROI evidence handed to the fleet autosizer's era-payback + realized-rate
	// guards: the MARGINAL (lowest) projection across the capital actions + the
	// fleet reference.
	require.Equal(t, 820000.0, d.MarginalProjectedCrHr)
	require.Equal(t, 700000.0, d.FleetPerHullCrHr)
	require.True(t, d.RateReadable)
}

// B2: a tick with no capital action still publishes a FRESH zero-gap demand —
// an omitted emit would leave a stale non-zero gap standing at the fleet autosizer.
func TestCapexEmitter_NoCapitalActions_PublishesFreshZeroDemand(t *testing.T) {
	sink := &recordingSink{}
	emitter := NewCapexEmitter(sink)

	cheap := []Action{
		{Tier: TierReuseIdle, Verb: VerbReassignHull, ShipSymbol: "S1"},
		{Tier: TierRebalance, Verb: VerbRebalanceWorkers, HubSymbol: "H1", Count: 2},
	}
	result, err := emitter.Govern(context.Background(), cheap,
		EconomicsSignals{FleetPerHullCrHr: 500000}, DefaultCalibration())
	require.NoError(t, err)

	require.Equal(t, cheap, result.Approved)
	require.Len(t, sink.emitted, 1)
	require.True(t, sink.emitted[0].Present)
	require.Equal(t, 0, sink.emitted[0].Hulls)
}

// B4 (falsifiable): the seam is a THIN emitter — it computes NO reserve-floor /
// 25%-treasury / payback budget. Proven behaviorally: even with treasury FAR
// below the reserve floor (where a budget-computing governor would refuse
// everything) the cheap tier is still approved AND the capital demand is still
// emitted, and no CapexBudget is ever computed.
func TestCapexEmitter_IsAThinEmitter_NoReserveFloorOr25PctOrPaybackMath(t *testing.T) {
	sink := &recordingSink{}
	emitter := NewCapexEmitter(sink)

	cheap := Action{Tier: TierBufferAdjust, Verb: VerbAdjustBufferCap, HubSymbol: "H1", Good: "IRON", UnitsCap: 50}
	capital := Action{
		Tier: TierCapital, Verb: VerbBuyHull, HullDelta: 1, WorkerDelta: 1, GapKind: GapWorkerShort,
		ProjectedPerHullCrHr: 600000, EstimatedCostCredits: 5000000,
	}
	// Treasury (1000) far below the 50000 reserve floor.
	econ := EconomicsSignals{TreasuryCredits: 1000, FleetPerHullCrHr: 550000, FleetHullCount: 3}

	result, err := emitter.Govern(context.Background(), []Action{cheap, capital}, econ, DefaultCalibration())
	require.NoError(t, err)

	require.Equal(t, []Action{cheap}, result.Approved, "no reserve-floor gate is applied to cheap tiers")
	require.Len(t, sink.emitted, 1)
	require.Equal(t, 1, sink.emitted[0].Hulls, "capital demand is emitted regardless of treasury — all money-gating is sp-1txd's")
	for _, dec := range result.Decisions {
		require.Equal(t, CapexBudget{}, dec.Budget, "the emitter computes no capex budget — that is sp-1txd's guard stack")
	}
}

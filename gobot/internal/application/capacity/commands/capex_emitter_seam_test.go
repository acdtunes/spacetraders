package commands

// Acceptance test for the capital-emit seam: drives the reconciler through its
// driving port (the command handler) and proves the CAPITAL tier's tier-4
// Actions are EMITTED to the fleet autosizer's demand path — while the
// reconciler's OWN converge neither executes nor proposes them (safety
// invariant 4 preserved) and the cheap autonomous tiers still flow.

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
)

// recordingCapitalSink is the driven-port boundary the GOVERN emitter writes to.
type recordingCapitalSink struct {
	mu      sync.Mutex
	emitted []capacity.CapitalDemand
}

func (s *recordingCapitalSink) EmitCapitalDemand(d capacity.CapitalDemand) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitted = append(s.emitted, d)
}

func (s *recordingCapitalSink) snapshot() []capacity.CapitalDemand {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capacity.CapitalDemand(nil), s.emitted...)
}

func TestCapacityReconciler_CapitalTierEmitsContractDeliveryDemandToAutosizerSeam(t *testing.T) {
	f := newLoopFixture()
	f.sensor.signals = capacity.Signals{
		Economics: capacity.EconomicsSignals{FleetPerHullCrHr: 700000, FleetHullCount: 10},
	}
	// DIFF yields one AUTONOMOUS tier-1 reuse + one tier-4 CAPITAL buy.
	f.differ.actions = []capacity.Action{
		{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, ShipSymbol: "SHIP-IDLE", GapKind: capacity.GapWorkerShort},
		{
			Tier: capacity.TierCapital, Verb: capacity.VerbBuyHull, HubSymbol: "X1-HUB",
			HullDelta: 1, WarehouseDelta: 1, GapKind: capacity.GapWarehouseShort,
			ProjectedPerHullCrHr: 900000, EstimatedCostCredits: 500000,
		},
	}

	sink := &recordingCapitalSink{}
	emitter := capacity.NewCapexEmitter(sink)
	h := NewRunCapacityReconcilerCoordinatorHandler(
		capacity.NewStaticDomain(capacity.ContractDeliveryDomainName, f.sensor, f.planner),
		f.differ, emitter, f.actuator, f.proposals, f.kill, f.clock,
	)

	outcomes := runTicks(t, h, reconcilerCmd(), 1, nil)

	// 1) The capital tier was EMITTED to the autosizer's demand path (class + count + evidence).
	emitted := sink.snapshot()
	require.Len(t, emitted, 1)
	require.Equal(t, 1, emitted[0].Hulls)
	require.Equal(t, 1, emitted[0].WarehouseHulls)
	require.Equal(t, 900000.0, emitted[0].MarginalProjectedCrHr)
	require.Equal(t, 700000.0, emitted[0].FleetPerHullCrHr)

	// 2) The reconciler's OWN converge neither executed nor proposed the capital
	//    action: the buy happens through the autosizer's guarded path (invariant 4).
	require.Empty(t, f.actuator.calls(capacity.VerbBuyHull), "capital must not reach the reconciler's ExecuteCapital")
	require.Empty(t, f.proposals.all(), "the emitter files no proposal — that is st-0h8")
	require.Empty(t, outcomes[0].ProposalsFiled)

	// 3) The cheap tier-1 DID execute (the emitter passes autonomous tiers straight through).
	require.Len(t, f.actuator.calls(capacity.VerbReassignHull), 1)
	require.Len(t, outcomes[0].ActionsExecuted, 1)
	require.Equal(t, capacity.VerbReassignHull, outcomes[0].ActionsExecuted[0].Verb)
	require.Empty(t, outcomes[0].Error, "the tick is clean — no converge failure")
}

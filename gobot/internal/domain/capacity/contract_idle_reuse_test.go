package capacity

// The reconciler must REPOSITION idle contract-dedicated haulers into depot
// roles BEFORE emitting capital-buy demand (Admiral: reuse before buy). An idle
// hull tagged "contract" is the contract op's OWN reserve hauler — repositioning
// it within the contract op is reuse, not poaching — so it belongs in the tier-1
// reuse pool alongside undedicated hulls. These behaviors pin the reuse-first +
// residual-only contract that keeps the fleet autosizer buying only the true
// residual.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// idleContractHauler is an idle contract-dedicated LIGHT hauler — the contract
// op's own reserve, cargo-capable, role HAULER (not the COMMAND frigate).
func idleContractHauler(ship string) HullUtilization {
	return HullUtilization{
		ShipSymbol:     ship,
		DedicatedFleet: "contract",
		Role:           "HAULER",
		Idle:           true,
		CargoCapacity:  haulCapableCargoUnits,
	}
}

// Behavior (reuse before buy): an uncovered hub whose whole depot desire —
// warehouse AND stocker AND delivery — is coverable by idle contract haulers is
// staffed entirely from tier-1 reassigns, raising ZERO capital-buy demand.
func TestLadderDiffer_ReusesIdleContractHaulersIntoAllDepotRoles(t *testing.T) {
	desired := DesiredTopology{Hubs: []DesiredHub{
		{HubSymbol: "X1-HUB-A", WarehouseCount: 1, StockerCount: 1, WorkerCount: 1},
	}}
	actual := TopologySignals{IdleHulls: []HullUtilization{
		idleContractHauler("CT-1"),
		idleContractHauler("CT-2"),
		idleContractHauler("CT-3"),
	}}

	actions := diffActions(t, desired, actual)

	reassigns := actionsByVerb(actions, VerbReassignHull)
	require.Len(t, reassigns, 3, "all three depot roles staffed from idle contract haulers")
	require.Empty(t, actionsByTier(actions, TierCapital),
		"idle contract haulers cover the whole desire — the reconciler emits NO capital-buy demand")

	roles := map[GapKind]bool{}
	for _, r := range reassigns {
		roles[r.GapKind] = true
	}
	require.True(t, roles[GapWarehouseShort], "warehouse role staffed from an idle contract hauler")
	require.True(t, roles[GapStockerShort], "stocker role staffed from an idle contract hauler")
	require.True(t, roles[GapWorkerShort], "delivery role staffed from an idle contract hauler")
}

// Behavior (residual-only): when the depot desire EXCEEDS the idle contract
// pool, tier-1 reuse consumes every idle first and only the RESIDUAL
// (desired − reused) reaches the capex emitter as capital demand — never the
// full gap.
func TestLadderDiffer_ContractIdleReuseEmitsOnlyResidualCapital(t *testing.T) {
	desired := DesiredTopology{Hubs: []DesiredHub{
		{HubSymbol: "X1-HUB-A", WarehouseCount: 2, StockerCount: 2}, // 4 depot roles
	}}
	actual := TopologySignals{IdleHulls: []HullUtilization{
		idleContractHauler("CT-1"),
		idleContractHauler("CT-2"),
	}}

	actions := diffActions(t, desired, actual)
	require.Len(t, actionsByVerb(actions, VerbReassignHull), 2, "both idle contract haulers are reused first")

	sink := &recordingSink{}
	_, err := NewCapexEmitter(sink).Govern(context.Background(), actions, EconomicsSignals{}, DefaultCalibration())
	require.NoError(t, err)
	require.Len(t, sink.emitted, 1)
	require.Equal(t, 2, sink.emitted[0].Hulls,
		"capital demand is the RESIDUAL (4 desired − 2 reused), not the full 4-hull gap")
}

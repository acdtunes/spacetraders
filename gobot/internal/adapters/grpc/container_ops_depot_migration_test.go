package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Item 3 (legacy migration = STOP-AND-APPLY): the legacy coordinator's warehouse hull,
// once RELEASED by stopping it (the existing claim-release machinery), is claimable by the
// declaratively-applied depot — and NOT before. This is the single-writer /
// no-double-claim invariant the whole stop->apply runbook rests on: the depot adopts the
// hull only after the legacy owner relinquishes it, so the two never hold it at once.
//
// It also exercises the runbook's key move — REUSE the legacy warehouse hull as the new
// depot's warehouse — so the migration preserves the stock already standing in that hull.
func TestMigration_ReleasedShipIsClaimableByAppliedDepot(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	ctx := context.Background()
	clock := shared.NewRealClock()

	const hull = "WH-CENTRAL"
	ship := newIdleTradeShip(t, hull, playerID)

	// The legacy contract-fleet coordinator holds the hull.
	require.NoError(t, ship.AssignToContainer("contract_fleet_coordinator-LEGACY", clock))
	require.True(t, ship.IsAssigned())

	// A depot cannot poach a STILL-claimed hull: a second claim is rejected. This is why
	// the migration MUST stop the legacy coordinator first — no double-claim.
	require.Error(t, ship.AssignToContainer("depot-central-worker", clock),
		"a hull still held by the legacy coordinator must not be double-claimed")

	// Stop the legacy coordinator == release its hull (the existing claim-release machinery).
	require.NoError(t, ship.Release("legacy_coordinator_stopped", clock))
	require.True(t, ship.IsIdle(), "the stopped coordinator's hull returns to idle, claimable")

	// Declaratively apply a depot that REUSES the released hull as its warehouse (Item 2
	// bulk apply — the runbook's "reuse the legacy warehouse hull to preserve stock").
	require.NoError(t, s.ApplyDepotTopology(ctx, playerID, DepotTopologySpec{Depots: []DepotSpec{
		{ID: "central", Warehouses: []ElementSpec{{Waypoint: "X1-CENTRAL-A1", ShipSymbol: hull}}},
	}}))

	// The applied depot references the hull, and the now-idle hull is claimable by the
	// depot's worker with no double-claim — the migration sequence is sound.
	reg, err := s.LoadDepotRegistry(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, reg.Depots(), 1)
	require.Equal(t, hull, reg.Depots()[0].Warehouses()[0].ShipSymbol,
		"the applied depot reuses the legacy warehouse hull")
	require.NoError(t, ship.AssignToContainer("depot-central-worker", clock),
		"after release + apply, the depot worker claims the released hull cleanly")
	require.Equal(t, "depot-central-worker", ship.ContainerID())
}

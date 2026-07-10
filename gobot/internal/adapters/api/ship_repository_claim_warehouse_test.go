package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The warehouse hull is a physically dedicated, pinned asset (RULINGS #7,
// sp-dchv Lane B): a hull dedicated to the "warehouse" fleet must reject a claim
// from any other operation, even while idle, so no gas/manufacturing/contract
// coordinator can poach the buffer hull out from under a running warehouse. The
// rejection leaves the row untouched — still idle, still claimable by warehouse.
func TestClaimShip_RejectsForeignOperationOnWarehouseHull(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "WAREHOUSE-HULL-1",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
		DedicatedFleet:   "warehouse",
	}).Error)

	err := repo.ClaimShip(context.Background(), "WAREHOUSE-HULL-1", "mfg-worker-1", playerID, "manufacturing")
	require.Error(t, err)

	var dedicated *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, err, &dedicated, "a foreign claim on a warehouse hull must fail with the typed dedication error")
	require.Equal(t, "warehouse", dedicated.Fleet)
	require.Equal(t, "manufacturing", dedicated.Operation)

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "WAREHOUSE-HULL-1").First(&model).Error)
	require.Equal(t, "idle", model.AssignmentStatus, "a rejected claim must not mutate the assignment")
	require.Nil(t, model.ContainerID, "a rejected claim must not attach a container")
}

// The mirror: the warehouse's own operation claims its dedicated hull exactly
// like any other claim — the dedication tag grants exclusivity to the warehouse
// fleet, it never blocks the warehouse itself.
func TestClaimShip_WarehouseOperationClaimsItsDedicatedHull(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "WAREHOUSE-HULL-1",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
		DedicatedFleet:   "warehouse",
	}).Error)
	seedContainerParent(t, db, "warehouse-X1-HOME-A1", playerID.Value())

	err := repo.ClaimShip(context.Background(), "WAREHOUSE-HULL-1", "warehouse-X1-HOME-A1", playerID, "warehouse")
	require.NoError(t, err, "the warehouse's own operation must claim its dedicated hull")

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "WAREHOUSE-HULL-1").First(&model).Error)
	require.Equal(t, "active", model.AssignmentStatus)
	require.NotNil(t, model.ContainerID)
	require.Equal(t, "warehouse-X1-HOME-A1", *model.ContainerID)
}

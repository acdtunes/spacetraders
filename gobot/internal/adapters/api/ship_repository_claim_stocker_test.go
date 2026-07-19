package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The stocker hull is a durably dedicated hauler (mirroring the
// warehouse RULINGS #7): a hull dedicated to the "stocker" fleet must reject a
// claim from any other operation, even while idle, so no factory/contract
// coordinator can poach the continuous-stocking hull between the stocker
// container's legs (crash, restart, or idle-gap). The rejection leaves the row
// untouched — still idle, still claimable by the stocker itself.
func TestClaimShip_RejectsForeignOperationOnStockerHull(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "STOCKER-HULL-1",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
		DedicatedFleet:   "stocker",
	}).Error)

	// The contract/factory pool claims through the operation-checked ClaimShip
	// with its own fleet identity ("contract"); against a stocker-dedicated hull
	// it must be rejected atomically — this is what stops the poach the moment
	// the stocker container ends.
	err := repo.ClaimShip(context.Background(), "STOCKER-HULL-1", "contract-worker-1", playerID, "contract")
	require.Error(t, err)

	var dedicated *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, err, &dedicated, "a foreign claim on a stocker hull must fail with the typed dedication error")
	require.Equal(t, "stocker", dedicated.Fleet)
	require.Equal(t, "contract", dedicated.Operation)

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "STOCKER-HULL-1").First(&model).Error)
	require.Equal(t, "idle", model.AssignmentStatus, "a rejected claim must not mutate the assignment")
	require.Nil(t, model.ContainerID, "a rejected claim must not attach a container")
}

// The mirror: the stocker's own operation claims its dedicated hull exactly like
// any other claim — the dedication tag grants exclusivity to the stocker fleet,
// it never blocks the stocker itself. This is the claim the stocker container's
// runner performs (operation="stocker" in its launch config).
func TestClaimShip_StockerOperationClaimsItsDedicatedHull(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "STOCKER-HULL-1",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
		DedicatedFleet:   "stocker",
	}).Error)
	seedContainerParent(t, db, "stocker-X1-HOME-A1", playerID.Value())

	err := repo.ClaimShip(context.Background(), "STOCKER-HULL-1", "stocker-X1-HOME-A1", playerID, "stocker")
	require.NoError(t, err, "the stocker's own operation must claim its dedicated hull")

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "STOCKER-HULL-1").First(&model).Error)
	require.Equal(t, "active", model.AssignmentStatus)
	require.NotNil(t, model.ContainerID)
	require.Equal(t, "stocker-X1-HOME-A1", *model.ContainerID)
}

// The durability guarantee end-to-end: a stocker-dedicated hull that
// is claimed, then released when its container ends (crash/restart/between legs),
// keeps its "stocker" dedication — only the container claim is dropped — and the
// NEXT stocker container re-claims it cleanly. Between release and re-claim the
// hull is idle-but-dedicated, so FindIdleLightHaulers still excludes it and no
// factory/contract coordinator can grab it. This is CONTINUOUS stocking without
// the captain babysitting the reservation.
func TestClaimShip_StockerDedicationSurvivesContainerEndAndReclaims(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "STOCKER-HULL-1",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
		DedicatedFleet:   "stocker",
	}).Error)
	seedContainerParent(t, db, "stocker-run-A", playerID.Value())
	seedContainerParent(t, db, "stocker-run-B", playerID.Value())

	// First stocker container claims and runs.
	require.NoError(t, repo.ClaimShip(context.Background(), "STOCKER-HULL-1", "stocker-run-A", playerID, "stocker"))

	// Container ends (crash/restart/between legs): the runner force-releases the
	// hull. ReleaseAllActive is the reconciliation-path release; it drops the
	// container claim without touching the dedication tag.
	released, err := repo.ReleaseAllActive(context.Background(), playerID, "container ended")
	require.NoError(t, err)
	require.Equal(t, 1, released)

	var afterRelease persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "STOCKER-HULL-1").First(&afterRelease).Error)
	require.Equal(t, "idle", afterRelease.AssignmentStatus, "the container claim must be dropped on container end")
	require.Nil(t, afterRelease.ContainerID, "the container claim must be dropped on container end")
	require.Equal(t, "stocker", afterRelease.DedicatedFleet,
		"the dedication must SURVIVE container end — only the container claim is released (sp-m92a)")

	// A foreign coordinator still cannot grab the idle-but-dedicated hull.
	foreignErr := repo.ClaimShip(context.Background(), "STOCKER-HULL-1", "contract-worker-9", playerID, "contract")
	var dedicated *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, foreignErr, &dedicated, "an idle-but-dedicated stocker hull must still reject a foreign claim")

	// The next stocker container (relaunch) re-claims the still-dedicated hull.
	require.NoError(t, repo.ClaimShip(context.Background(), "STOCKER-HULL-1", "stocker-run-B", playerID, "stocker"),
		"the relaunched stocker container must re-claim its still-dedicated hull")

	var afterReclaim persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "STOCKER-HULL-1").First(&afterReclaim).Error)
	require.Equal(t, "active", afterReclaim.AssignmentStatus)
	require.NotNil(t, afterReclaim.ContainerID)
	require.Equal(t, "stocker-run-B", *afterReclaim.ContainerID)
	require.Equal(t, "stocker", afterReclaim.DedicatedFleet)
}

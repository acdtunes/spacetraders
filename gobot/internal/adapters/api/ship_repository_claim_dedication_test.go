package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// newDedicationTestRepo builds a real-sqlite repository plus one player row.
// ClaimShip/AssignFleet only touch r.db (and the zero-value cache/clock the
// constructor defaults), so nil apiClient/waypoint/player deps are safe here.
func newDedicationTestRepo(t *testing.T) (*ShipRepository, *gorm.DB, shared.PlayerID) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	player := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)

	return NewShipRepository(nil, nil, nil, nil, db, nil), db, shared.MustNewPlayerID(player.ID)
}

// The atomic layer-2 guard (sp-l7h2): a FREE hull dedicated to another fleet
// must be rejected inside the claim transaction itself — the discovery-time
// exclude filter is only a pre-check and can race a concurrent `fleet assign`.
// The rejection must also leave the row untouched: still idle, still
// claimable by the right fleet.
func TestClaimShip_RejectsForeignOperationOnDedicatedHull(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-4",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
		DedicatedFleet:   "contract",
	}).Error)

	err := repo.ClaimShip(context.Background(), "TORWIND-4", "mfg-worker-1", playerID, "manufacturing")
	require.Error(t, err)

	var dedicated *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, err, &dedicated, "wrong-fleet claim must fail with the typed dedication error")
	require.Equal(t, "contract", dedicated.Fleet)
	require.Equal(t, "manufacturing", dedicated.Operation)

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-4").First(&model).Error)
	require.Equal(t, "idle", model.AssignmentStatus, "a rejected claim must not mutate the assignment")
	require.Nil(t, model.ContainerID, "a rejected claim must not attach a container")
}

// The dedicated fleet's own coordinator claims its hull exactly like any
// other claim — the tag grants exclusivity, it never blocks the owner.
func TestClaimShip_OwnFleetOperationClaimsDedicatedHull(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-4",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
		DedicatedFleet:   "contract",
	}).Error)

	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-4", "contract-worker-1", playerID, "contract"))

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-4").First(&model).Error)
	require.Equal(t, "active", model.AssignmentStatus)
	require.NotNil(t, model.ContainerID)
	require.Equal(t, "contract-worker-1", *model.ContainerID)
}

// Crash-recovery invariant: dedication is ownership of the NEXT acquisition,
// never eviction. A worker re-claiming its own hull mid-job must keep it even
// if the captain re-dedicated the ship to another fleet while the job ran —
// the new fleet takes over when this claim is released, not by yanking a hull
// out from under a running operation.
func TestClaimShip_SameContainerReclaimSurvivesRededication(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-4",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
		DedicatedFleet:   "contract",
	}).Error)

	// The contract worker takes the hull, then the captain re-dedicates it
	// to bulk_circuit while the job is still running.
	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-4", "contract-worker-1", playerID, "contract"))
	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-4", "bulk_circuit", playerID))

	// The worker restarts (crash recovery) and re-claims its own hull under
	// its original operation — this must remain an idempotent success.
	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-4", "contract-worker-1", playerID, "contract"),
		"same-container re-claim must survive a mid-job re-dedication")

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-4").First(&model).Error)
	require.Equal(t, "active", model.AssignmentStatus)
	require.Equal(t, "contract-worker-1", *model.ContainerID, "the running worker keeps its hull")
	require.Equal(t, "bulk_circuit", model.DedicatedFleet, "the new dedication stays persisted for the NEXT acquisition")
}

// Phase 1 must be additive: an undedicated hull stays claimable by every
// operation — including legacy callers that identify as "" — and a dedicated
// hull rejects even an undeclared claimant. Together these pin the guard to
// exactly one trigger: a non-empty tag that differs from the claimant.
func TestClaimShip_UndedicatedHullOpenToAllOperations(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-1",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-4",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
		DedicatedFleet:   "contract",
	}).Error)

	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-1", "legacy-worker-1", playerID, ""),
		"an undeclared operation must still claim general-pool hulls (additive change)")

	err := repo.ClaimShip(context.Background(), "TORWIND-4", "legacy-worker-2", playerID, "")
	var dedicated *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, err, &dedicated, "an undeclared operation must not take a dedicated hull")
	require.Contains(t, err.Error(), "an undeclared operation")
}

// releasedClaimFlipsToNewFleet completes the re-dedication story: after the
// original holder releases, the tag that was written mid-job governs the next
// acquisition — the old fleet is rejected, the new fleet claims.
func TestClaimShip_ReleasedHullHonorsNewDedication(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-4",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
		DedicatedFleet:   "contract",
	}).Error)

	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-4", "contract-worker-1", playerID, "contract"))
	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-4", "bulk_circuit", playerID))
	released, err := repo.ReleaseAllActive(context.Background(), playerID, "job complete")
	require.NoError(t, err)
	require.Equal(t, 1, released, "the worker's claim should be the only active assignment")

	err = repo.ClaimShip(context.Background(), "TORWIND-4", "contract-worker-2", playerID, "contract")
	var dedicated *shared.ShipDedicatedToOtherFleetError
	require.ErrorAs(t, err, &dedicated, "the old fleet must be rejected once the hull is free")
	require.Equal(t, "bulk_circuit", dedicated.Fleet)

	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-4", "bulk-worker-1", playerID, "bulk_circuit"),
		"the newly-dedicated fleet takes over on the next acquisition")
}

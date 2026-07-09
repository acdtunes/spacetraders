package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// AssignFleet is the single write path for the dedication tag (sp-l7h2): set
// persists the name, and fleet == "" clears it — returning the hull to the
// general pool. Both directions must land in the row itself, because that
// column is what ClaimShip's atomic guard reads.
func TestAssignFleet_SetsAndClearsDedication(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-19",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
	}).Error)

	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-19", "bulk_circuit", playerID))

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-19").First(&model).Error)
	require.Equal(t, "bulk_circuit", model.DedicatedFleet)

	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-19", "", playerID))

	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-19").First(&model).Error)
	require.Equal(t, "", model.DedicatedFleet, "clearing must return the hull to the general pool")
}

// Re-assigning the already-persisted value must succeed and leave the tag
// intact — the coordinator reconciles its --dedicated-ships flag on EVERY
// restart, so repeat assignment is the steady state, not an edge case.
// (Internally this path also skips the DB write; the observable contract is
// idempotent success.)
func TestAssignFleet_RepeatAssignmentIsIdempotent(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-4",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
		DedicatedFleet:   "contract",
	}).Error)

	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-4", "contract", playerID))
	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-4", "contract", playerID))

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-4").First(&model).Error)
	require.Equal(t, "contract", model.DedicatedFleet)
}

// A symbol that doesn't exist for the player must fail loudly — a typo'd
// `fleet assign --ship` or a stale --dedicated-ships entry (ship sold) must
// never silently succeed.
func TestAssignFleet_UnknownShipReturnsNotFound(t *testing.T) {
	repo, _, playerID := newDedicationTestRepo(t)

	err := repo.AssignFleet(context.Background(), "TORWIND-GONE", "contract", playerID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found for player")
}

// Dedication is ownership of the NEXT acquisition, orthogonal to current
// occupancy: tagging a hull mid-claim must succeed WITHOUT disturbing the
// live assignment — the running worker keeps its container, status and all.
func TestAssignFleet_DoesNotEvictActiveClaim(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	containerID := "mfg-worker-1"
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-7",
		PlayerID:         playerID.Value(),
		ContainerID:      &containerID,
		AssignmentStatus: "active",
	}).Error)

	require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-7", "bulk_circuit", playerID),
		"assigning a busy hull must succeed — the tag takes effect on release")

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-7").First(&model).Error)
	require.Equal(t, "bulk_circuit", model.DedicatedFleet)
	require.Equal(t, "active", model.AssignmentStatus, "the live claim must be untouched")
	require.NotNil(t, model.ContainerID)
	require.Equal(t, "mfg-worker-1", *model.ContainerID, "the holder must keep its container")
}

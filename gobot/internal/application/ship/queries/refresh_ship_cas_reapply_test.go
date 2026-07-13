package queries

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// refreshReapplyStubWaypoints forces modelToDomain's denormalized-coordinate
// fallback so the real repo needs no waypoint rows.
type refreshReapplyStubWaypoints struct{}

func (refreshReapplyStubWaypoints) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return nil, errors.New("stub: use denormalized fallback")
}

// This proves the sp-wa7c fix for refresh_ship's stale-claim release against a
// real, version-guarded repository: a concurrent writer commits a fresh cargo
// update on the same hull while the reconciler holds a snapshot. The migrated
// SaveWithRetry persist re-applies ONLY the release (assignment -> idle) on the
// fresh row, so the concurrent cargo update survives instead of being clobbered
// by the snapshot's stale cargo.
func TestReconcileStaleClaim_ReleaseDoesNotClobberFreshCargo(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	pid := shared.MustNewPlayerID(playerRow.ID)

	// The orphaned owner still needs a row to satisfy the ship->container FK; the
	// container reader (not the DB) decides orphan-ness in this test.
	deadCtr := "dead-ctr"
	require.NoError(t, db.Create(&persistence.ContainerModel{ID: deadCtr, PlayerID: playerRow.ID, Status: "COMPLETED"}).Error)

	assignedAt := time.Now().Add(-1 * time.Hour)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "STALE-1",
		PlayerID:         playerRow.ID,
		AssignmentStatus: "active",
		ContainerID:      &deadCtr,
		AssignedAt:       &assignedAt,
		NavStatus:        "IN_ORBIT",
		LocationSymbol:   "X1-TEST-A1",
		SystemSymbol:     "X1-TEST",
		FuelCurrent:      400,
		FuelCapacity:     1000,
		CargoCapacity:    200,
		CargoUnits:       100,
		CargoInventory:   `[{"symbol":"IRON_ORE","name":"Iron Ore","description":"x","units":100}]`,
		EngineSpeed:      10,
		Version:          1,
	}).Error)

	shipRepo := api.NewShipRepository(nil, nil, nil, refreshReapplyStubWaypoints{}, db, nil)
	// containerReader reports the owning container GONE -> IsClaimOrphaned == true.
	reader := &stubContainerStatusReader{found: false}
	handler := NewRefreshShipHandler(shipRepo, nil, reader, shared.NewRealClock())

	// The reconciler's snapshot: claimed by dead-ctr, cargo 100.
	snapshot, err := shipRepo.FindBySymbol(context.Background(), "STALE-1", pid)
	require.NoError(t, err)
	require.True(t, snapshot.IsAssigned())

	// Concurrent writer unloads 40 units (100 -> 60) and commits, keeping the claim
	// intact, bumping the row version behind the reconciler's back.
	other, err := shipRepo.FindBySymbol(context.Background(), "STALE-1", pid)
	require.NoError(t, err)
	require.NoError(t, other.RemoveCargo("IRON_ORE", 40))
	require.NoError(t, shipRepo.Save(context.Background(), other))

	require.NoError(t, handler.reconcileStaleClaim(context.Background(), snapshot, pid))

	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "STALE-1").First(&row).Error)

	// The concurrent cargo write SURVIVED the release (no clobber).
	require.Equal(t, 60, row.CargoUnits, "concurrent cargo unload must survive the stale-claim release")
	// The release was still applied on the fresh row.
	require.Equal(t, "idle", row.AssignmentStatus, "orphaned claim released on fresh state")
	require.Nil(t, row.ContainerID, "container link cleared")
}

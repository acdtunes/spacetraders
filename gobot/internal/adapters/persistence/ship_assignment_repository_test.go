package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"gorm.io/gorm"
)

func setupShipAssignmentRepo(t *testing.T) (*persistence.ShipAssignmentRepositoryGORM, int, *gorm.DB) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "TEST-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return persistence.NewShipAssignmentRepository(db), player.ID, db
}

func TestListActiveReturnsRoleAssignmentAndSyncedAtForEveryShip(t *testing.T) {
	repo, playerID, db := setupShipAssignmentRepo(t)
	ctx := context.Background()

	assignedAt := time.Now().Add(-90 * time.Second)
	idleSyncedAt := time.Now().Add(-4100 * time.Second)
	containerID := "CTR-1"

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: playerID, Role: "HAULER",
		ContainerID: &containerID, AssignmentStatus: "active", SyncedAt: assignedAt,
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-2", PlayerID: playerID, Role: "EXCAVATOR",
		AssignmentStatus: "idle", SyncedAt: idleSyncedAt,
	}).Error)

	infos, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, infos, 2)

	byShip := make(map[string]persistence.ShipAssignmentInfo, len(infos))
	for _, info := range infos {
		byShip[info.ShipSymbol] = info
	}

	require.Equal(t, "HAULER", byShip["SHIP-1"].Role)
	require.Equal(t, "CTR-1", byShip["SHIP-1"].ContainerID)
	require.WithinDuration(t, assignedAt, byShip["SHIP-1"].SyncedAt, time.Second)

	require.Equal(t, "EXCAVATOR", byShip["SHIP-2"].Role)
	require.Empty(t, byShip["SHIP-2"].ContainerID)
	require.WithinDuration(t, idleSyncedAt, byShip["SHIP-2"].SyncedAt, time.Second)
}

func TestListActiveScopesToPlayer(t *testing.T) {
	repo, playerID, db := setupShipAssignmentRepo(t)
	ctx := context.Background()

	otherPlayer := persistence.PlayerModel{AgentSymbol: "OTHER-AGENT", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&otherPlayer).Error)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-1", PlayerID: playerID, Role: "HAULER", SyncedAt: time.Now(),
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "SHIP-9", PlayerID: otherPlayer.ID, Role: "HAULER", SyncedAt: time.Now(),
	}).Error)

	infos, err := repo.ListActive(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	require.Equal(t, "SHIP-1", infos[0].ShipSymbol)
}

package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// newWarehouseRepoTest builds a real FK-enforcing SQLite repo plus one player
// row. NewTestConnection turns PRAGMA foreign_keys ON, so the
// storage_operations.player_id -> players.id constraint is live here exactly as
// it is in production Postgres.
func newWarehouseRepoTest(t *testing.T) (*persistence.StorageOperationRepository, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	player := persistence.PlayerModel{AgentSymbol: "WAREHOUSER", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)

	return persistence.NewStorageOperationRepository(db, nil), player.ID
}

// A warehouse operation must persist and reload through the SHARED
// storage_operations repository with its WAREHOUSE type and zero-extractor shape
// intact, under live FK enforcement. This is what the StorageRecoveryService
// reads on daemon restart (RULINGS #2) — no new table, no schema change.
func TestStorageOperationRepository_WarehouseRoundTripFKOn(t *testing.T) {
	repo, playerID := newWarehouseRepoTest(t)
	ctx := context.Background()

	op, err := storage.NewWarehouseOperation(
		"warehouse-X1-HOME-A1",
		playerID,
		"X1-HOME-A1",
		[]string{"HULL-STORE-1"},
		[]string{"IRON_ORE", "ALUMINUM"},
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, op.Start())

	// Create succeeds: the FK to the seeded player is satisfied.
	require.NoError(t, repo.Create(ctx, op))

	reloaded, err := repo.FindByID(ctx, "warehouse-X1-HOME-A1")
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	require.Equal(t, storage.OperationTypeWarehouse, reloaded.OperationType())
	require.Empty(t, reloaded.ExtractorShips(), "zero-extractor shape must survive persistence")
	require.Equal(t, []string{"HULL-STORE-1"}, reloaded.StorageShips())
	require.True(t, reloaded.IsRunning())
	require.True(t, reloaded.SupportsGood("IRON_ORE"))
	require.True(t, reloaded.SupportsGood("ALUMINUM"))
}

// Recovery finds a warehouse via FindRunning, which filters by STATUS only, not
// operation type — so the extractor-free warehouse is recovered by the exact
// same query that recovers gas operations. This is the mechanism behind the
// "recovery is free" claim.
func TestStorageOperationRepository_FindRunningReturnsWarehouse(t *testing.T) {
	repo, playerID := newWarehouseRepoTest(t)
	ctx := context.Background()

	op, err := storage.NewWarehouseOperation(
		"warehouse-X1-HOME-A1", playerID, "X1-HOME-A1",
		[]string{"HULL-STORE-1"}, []string{"IRON_ORE"}, nil,
	)
	require.NoError(t, err)
	require.NoError(t, op.Start())
	require.NoError(t, repo.Create(ctx, op))

	running, err := repo.FindRunning(ctx, playerID)
	require.NoError(t, err)
	require.Len(t, running, 1)
	require.Equal(t, "warehouse-X1-HOME-A1", running[0].ID())
	require.Equal(t, storage.OperationTypeWarehouse, running[0].OperationType())
}

// The FK is genuinely enforced: persisting a warehouse for a player that does
// not exist must be REJECTED, not silently accepted. This proves the round-trip
// above passed because the constraint was satisfied, not because enforcement
// was off (the gap this harness closes).
func TestStorageOperationRepository_WarehouseRejectsMissingPlayerFK(t *testing.T) {
	repo, playerID := newWarehouseRepoTest(t)
	ctx := context.Background()

	op, err := storage.NewWarehouseOperation(
		"warehouse-orphan", playerID+999, "X1-HOME-A1",
		[]string{"HULL-STORE-1"}, []string{"IRON_ORE"}, nil,
	)
	require.NoError(t, err)
	require.NoError(t, op.Start())

	err = repo.Create(ctx, op)
	require.Error(t, err, "a warehouse row for a non-existent player must violate the FK")
}

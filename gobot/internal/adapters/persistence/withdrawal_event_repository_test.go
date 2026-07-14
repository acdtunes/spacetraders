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

// A recorded warehouse→hauler draw reads back with every field intact and in draw
// order — the warehouse-ROI analysis (buffer hit-rate, served-from-buffer,
// contract-leg-avoided) depends on good/units/waypoint/ship/contract surviving the
// round-trip. The second draw serves no contract, exercising the nullable/empty
// contract id.
func TestWithdrawalEventRepository_RecordsAndListsInOrder(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewWithdrawalEventRepository(db)
	ctx := context.Background()

	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	contractDraw := storage.WithdrawalEvent{
		Good: "IRON_ORE", Units: 40, Waypoint: "X1-HOME-WH9", Ship: "TORWIND-1",
		ContractID: "ct-inv", PlayerID: 1, WithdrawnAt: base,
	}
	nonContractDraw := storage.WithdrawalEvent{
		Good: "COPPER_ORE", Units: 12, Waypoint: "X1-HOME-WH9", Ship: "TORWIND-2",
		ContractID: "", PlayerID: 1, WithdrawnAt: base.Add(time.Minute),
	}
	require.NoError(t, repo.Record(ctx, contractDraw))
	require.NoError(t, repo.Record(ctx, nonContractDraw))

	rows, err := repo.ListByPlayer(ctx, 1, time.Time{})
	require.NoError(t, err)
	require.Len(t, rows, 2)

	require.Equal(t, "IRON_ORE", rows[0].Good, "draws read back in the order they happened")
	require.Equal(t, 40, rows[0].Units)
	require.Equal(t, "X1-HOME-WH9", rows[0].Waypoint)
	require.Equal(t, "TORWIND-1", rows[0].Ship)
	require.Equal(t, "ct-inv", rows[0].ContractID)
	require.Equal(t, 1, rows[0].PlayerID)
	require.True(t, rows[0].WithdrawnAt.Equal(base))

	require.Equal(t, "COPPER_ORE", rows[1].Good)
	require.Equal(t, "", rows[1].ContractID, "a non-contract draw persists a nullable/empty contract id")
}

// ListByPlayer scopes to the player and honors the since window so a per-player,
// per-window ROI query never bleeds another player's or an older era's draws.
func TestWithdrawalEventRepository_ScopesByPlayerAndSince(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewWithdrawalEventRepository(db)
	ctx := context.Background()

	old := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)

	require.NoError(t, repo.Record(ctx, storage.WithdrawalEvent{
		Good: "G", Units: 1, Waypoint: "W", Ship: "H1", PlayerID: 1, WithdrawnAt: old}))
	require.NoError(t, repo.Record(ctx, storage.WithdrawalEvent{
		Good: "G", Units: 2, Waypoint: "W", Ship: "H1", PlayerID: 1, WithdrawnAt: recent}))
	require.NoError(t, repo.Record(ctx, storage.WithdrawalEvent{
		Good: "G", Units: 3, Waypoint: "W", Ship: "H2", PlayerID: 2, WithdrawnAt: recent}))

	// since excludes the old draw; player scoping excludes player 2's draw.
	rows, err := repo.ListByPlayer(ctx, 1, time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 2, rows[0].Units)
}

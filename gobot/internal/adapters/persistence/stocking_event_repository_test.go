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

// A recorded stocker→warehouse deposit reads back with every field intact and in deposit
// order — the stock-IN throughput/coverage analysis (units-stocked, goods-covered,
// source-provenance) depends on good/units/warehouse/source/ship surviving the round-trip.
// The second deposit is a resume deposit of prior-run cargo, exercising the empty/unknown
// source waypoint (the stock-IN analog of a non-contract draw's empty contract id).
func TestStockingEventRepository_RecordsAndListsInOrder(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewStockingEventRepository(db)
	ctx := context.Background()

	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	boughtDeposit := storage.StockingEvent{
		Good: "MEDICINE", Units: 40, WarehouseWaypoint: "X1-HOME-WH9", SourceWaypoint: "X1-FAR-M3",
		Ship: "TORWIND-11", PlayerID: 1, DepositedAt: base,
	}
	resumeDeposit := storage.StockingEvent{
		Good: "FUEL", Units: 12, WarehouseWaypoint: "X1-HOME-WH9", SourceWaypoint: "",
		Ship: "TORWIND-12", PlayerID: 1, DepositedAt: base.Add(time.Minute),
	}
	require.NoError(t, repo.Record(ctx, boughtDeposit))
	require.NoError(t, repo.Record(ctx, resumeDeposit))

	rows, err := repo.ListByPlayer(ctx, 1, time.Time{})
	require.NoError(t, err)
	require.Len(t, rows, 2)

	require.Equal(t, "MEDICINE", rows[0].Good, "deposits read back in the order they happened")
	require.Equal(t, 40, rows[0].Units)
	require.Equal(t, "X1-HOME-WH9", rows[0].WarehouseWaypoint)
	require.Equal(t, "X1-FAR-M3", rows[0].SourceWaypoint)
	require.Equal(t, "TORWIND-11", rows[0].Ship)
	require.Equal(t, 1, rows[0].PlayerID)
	require.True(t, rows[0].DepositedAt.Equal(base))

	require.Equal(t, "FUEL", rows[1].Good)
	require.Equal(t, "", rows[1].SourceWaypoint, "a resume deposit of prior-run cargo persists an empty/unknown source")
}

// ListByPlayer scopes to the player and honors the since window so a per-player, per-window
// stock-IN query never bleeds another player's or an older era's deposits.
func TestStockingEventRepository_ScopesByPlayerAndSince(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewStockingEventRepository(db)
	ctx := context.Background()

	old := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)

	require.NoError(t, repo.Record(ctx, storage.StockingEvent{
		Good: "G", Units: 1, WarehouseWaypoint: "W", Ship: "H1", PlayerID: 1, DepositedAt: old}))
	require.NoError(t, repo.Record(ctx, storage.StockingEvent{
		Good: "G", Units: 2, WarehouseWaypoint: "W", Ship: "H1", PlayerID: 1, DepositedAt: recent}))
	require.NoError(t, repo.Record(ctx, storage.StockingEvent{
		Good: "G", Units: 3, WarehouseWaypoint: "W", Ship: "H2", PlayerID: 2, DepositedAt: recent}))

	// since excludes the old deposit; player scoping excludes player 2's deposit.
	rows, err := repo.ListByPlayer(ctx, 1, time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 2, rows[0].Units)
}

package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func laneDebtRepo(t *testing.T, playerID int) (*persistence.LaneCooldownDebtRepositoryGORM, context.Context) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	return persistence.NewLaneCooldownDebtRepositoryGORM(db, playerID), context.Background()
}

func laneKey() trading.LaneKey {
	return trading.LaneKey{Source: "X1-KP46-A1", Dest: "X1-KP46-B2", Good: "IRON"}
}

// The table must be born from AutoMigrate — it carries no CREATE TABLE migration, so a model
// missing from AllModels() would leave every Save failing against a table that never existed.
func TestLaneCooldownDebt_RoundTripsThroughTheMigratedTable(t *testing.T) {
	repo, ctx := laneDebtRepo(t, 1)
	at := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	require.NoError(t, repo.Save(ctx, laneKey(), 0.0325, at))

	loaded, err := repo.LoadSince(ctx, at.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, laneKey(), loaded[0].Key)
	require.InDelta(t, 0.0325, loaded[0].Debt, 1e-9)
	require.WithinDuration(t, at, loaded[0].AccruedAt, time.Second)
}

// The ledger holds ONE decayed scalar per lane, so a second accrual REPLACES the row. A row per
// drain would restore the sum of every trade ever made on the lane, undecayed.
func TestLaneCooldownDebt_SecondAccrualReplacesTheLaneRow(t *testing.T) {
	repo, ctx := laneDebtRepo(t, 1)
	first := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	second := first.Add(30 * time.Minute)

	require.NoError(t, repo.Save(ctx, laneKey(), 0.0325, first))
	require.NoError(t, repo.Save(ctx, laneKey(), 0.0610, second))

	loaded, err := repo.LoadSince(ctx, first.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, loaded, 1, "each lane must occupy exactly one row")
	require.InDelta(t, 0.0610, loaded[0].Debt, 1e-9)
	require.WithinDuration(t, second, loaded[0].AccruedAt, time.Second)
}

// Debt decays as exp(-dt/tau), so a row older than the reload window restores a fraction of a
// percent while occupying a key the double-count guard would then refuse.
func TestLaneCooldownDebt_LoadSinceExcludesRowsOlderThanTheWindow(t *testing.T) {
	repo, ctx := laneDebtRepo(t, 1)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	stale := trading.LaneKey{Source: "X1-KP46-A1", Dest: "X1-KP46-C3", Good: "COPPER"}
	require.NoError(t, repo.Save(ctx, laneKey(), 0.0325, now))
	require.NoError(t, repo.Save(ctx, stale, 0.0400, now.Add(-100*time.Hour)))

	loaded, err := repo.LoadSince(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, laneKey(), loaded[0].Key)
}

// The ledger key carries no player dimension, so the SCOPE has to come from the store. Blending
// two agents' drains into one market's history is the silent correctness bug this prevents.
func TestLaneCooldownDebt_IsScopedToItsPlayer(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	ctx := context.Background()
	at := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	one := persistence.NewLaneCooldownDebtRepositoryGORM(db, 1)
	two := persistence.NewLaneCooldownDebtRepositoryGORM(db, 2)
	require.NoError(t, one.Save(ctx, laneKey(), 0.0325, at))

	loaded, err := two.LoadSince(ctx, at.Add(-time.Hour))
	require.NoError(t, err)
	require.Empty(t, loaded, "another player's lane debt must not be visible")
}

// A source-drain key names no destination. Those are reconstructed from the purchase rows by the
// cooldownreplay package; a second durable copy here is one free to drift from the rows it
// duplicates. The ledger filters them before calling — this is the store's own arm of that guard.
func TestLaneCooldownDebt_RefusesAKeyWithNoDestination(t *testing.T) {
	repo, ctx := laneDebtRepo(t, 1)
	at := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	require.NoError(t, repo.Save(ctx, trading.SourceDrainKey("X1-KP46-A1", "IRON"), 0.0325, at))

	loaded, err := repo.LoadSince(ctx, at.Add(-time.Hour))
	require.NoError(t, err)
	require.Empty(t, loaded, "a source-drain key must never reach the full-lane table")
}

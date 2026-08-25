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

func newJumpTollTestRepo(t *testing.T) (*persistence.GormJumpTollRepository, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "SP-TOLL", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return persistence.NewGormJumpTollRepository(db), player.ID
}

// A recorded hop round-trips: the window read returns exactly what the travel path measured,
// which is what makes the estimator a pure function of durable rows rather than of process
// memory (RULINGS #2 — a restart re-derives the toll from these, with nothing to reload).
func TestJumpTollRepo_RecordThenReadBack(t *testing.T) {
	repo, playerID := newJumpTollTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, repo.RecordJumpToll(ctx, playerID, "SHIP-1", "X1-AA", "X1-BB", trading.JumpTollSample{
		WaitSeconds: 1180, CooldownSeconds: 940, RecordedAt: now.Add(-5 * time.Minute),
	}))

	got, err := repo.RecentJumpTolls(ctx, playerID, now.Add(-time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, 1180, got[0].WaitSeconds)
	require.Equal(t, 940, got[0].CooldownSeconds)
	require.WithinDuration(t, now.Add(-5*time.Minute), got[0].RecordedAt, time.Second)
}

// The window read is scoped by player and by time, and bounded by the limit — a fleet that has
// been jumping for weeks must not drag its whole history into memory on every re-read.
func TestJumpTollRepo_ScopesByPlayerWindowAndLimit(t *testing.T) {
	repo, playerID := newJumpTollTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		require.NoError(t, repo.RecordJumpToll(ctx, playerID, "SHIP-1", "X1-AA", "X1-BB", trading.JumpTollSample{
			WaitSeconds: 900 + i, CooldownSeconds: 800, RecordedAt: now.Add(-time.Duration(i) * time.Minute),
		}))
	}
	require.NoError(t, repo.RecordJumpToll(ctx, playerID, "SHIP-1", "X1-AA", "X1-BB", trading.JumpTollSample{
		WaitSeconds: 4000, CooldownSeconds: 3000, RecordedAt: now.Add(-48 * time.Hour),
	}))
	require.NoError(t, repo.RecordJumpToll(ctx, playerID+7, "SHIP-9", "X1-AA", "X1-BB", trading.JumpTollSample{
		WaitSeconds: 5000, CooldownSeconds: 4000, RecordedAt: now,
	}))

	inWindow, err := repo.RecentJumpTolls(ctx, playerID, now.Add(-24*time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, inWindow, 5, "the out-of-window row and the other player's row must both be excluded")

	// Newest first, so a limit truncates the OLDEST rows — the ones the decay would have
	// discounted anyway.
	capped, err := repo.RecentJumpTolls(ctx, playerID, now.Add(-24*time.Hour), 2)
	require.NoError(t, err)
	require.Len(t, capped, 2)
	require.Equal(t, 900, capped[0].WaitSeconds)
	require.Equal(t, 901, capped[1].WaitSeconds)
}

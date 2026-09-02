package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func newClaimTestDB(t *testing.T) (*persistence.TradeClaimRegistryGORM, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "MVT-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return persistence.NewTradeClaimRegistry(db), player.ID
}

func TestTradeClaimRegistry_UpsertArriveReleaseInTransit(t *testing.T) {
	var _ mvt.ClaimRegistry = (*persistence.TradeClaimRegistryGORM)(nil)
	reg, pid := newClaimTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	_, ok, err := reg.Get(ctx, pid, "H1")
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, reg.Upsert(ctx, pid, "H1", "X1-B", now))
	require.NoError(t, reg.Upsert(ctx, pid, "H2", "X1-B", now))
	require.NoError(t, reg.Upsert(ctx, pid, "H3", "X1-C", now))
	inTransit, err := reg.InTransit(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"X1-B": 2, "X1-C": 1}, inTransit)

	require.NoError(t, reg.MarkArrived(ctx, pid, "H1", now.Add(time.Minute)))
	c, ok, err := reg.Get(ctx, pid, "H1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "X1-B", c.System)
	require.NotNil(t, c.ArrivedAt)
	inTransit, err = reg.InTransit(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"X1-B": 1, "X1-C": 1}, inTransit)

	// Re-claiming resets arrival.
	require.NoError(t, reg.Upsert(ctx, pid, "H1", "X1-D", now.Add(2*time.Minute)))
	c, _, _ = reg.Get(ctx, pid, "H1")
	require.Equal(t, "X1-D", c.System)
	require.Nil(t, c.ArrivedAt)

	require.NoError(t, reg.Release(ctx, pid, "H1"))
	_, ok, _ = reg.Get(ctx, pid, "H1")
	require.False(t, ok)
	require.NoError(t, reg.Release(ctx, pid, "H1")) // idempotent

	// Another player's rows are invisible.
	other, err := reg.InTransit(ctx, pid+1)
	require.NoError(t, err)
	require.Empty(t, other)
}

package cli

// Integration test (real GORM/sqlite, no mocks) for the contract store query behind
// `contract get`: the abbreviated id `contract list` prints must select its row, and an
// operator-typed LIKE metacharacter must be matched literally rather than as a pattern.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestGormContractStoreFindsContractsByIDPrefix(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.PlayerModel{
		ID: 5, AgentSymbol: "TORWIND", Token: "token", CreatedAt: time.Now().UTC(),
	}).Error)

	stored := []persistence.ContractModel{
		{ID: "cms1ww9it000108l4ce4gd2ka", PlayerID: 5, FactionSymbol: "COSMIC", Type: "PROCUREMENT", DeliveriesJSON: "[]"},
		{ID: "cms1v602e000308l4b1c8m3qd", PlayerID: 5, FactionSymbol: "COSMIC", Type: "PROCUREMENT", DeliveriesJSON: "[]"},
	}
	require.NoError(t, db.Create(&stored).Error)

	store := &gormContractStore{db: db}
	ctx := context.Background()

	abbreviated, err := store.FindContractsByIDPrefix(ctx, shortContractID("cms1ww9it000108l4ce4gd2ka"))
	require.NoError(t, err)
	require.Len(t, abbreviated, 1, "the abbreviated id contract list prints must select its row")
	require.Equal(t, "cms1ww9it000108l4ce4gd2ka", abbreviated[0].ID)

	full, err := store.FindContractsByIDPrefix(ctx, "cms1v602e000308l4b1c8m3qd")
	require.NoError(t, err)
	require.Len(t, full, 1)
	require.Equal(t, "cms1v602e000308l4b1c8m3qd", full[0].ID)

	wildcard, err := store.FindContractsByIDPrefix(ctx, "cms1_w9it")
	require.NoError(t, err)
	require.Empty(t, wildcard, "a LIKE metacharacter must be matched literally")

	unknown, err := store.FindContractsByIDPrefix(ctx, "cms9zzzzz")
	require.NoError(t, err)
	require.Empty(t, unknown)
}

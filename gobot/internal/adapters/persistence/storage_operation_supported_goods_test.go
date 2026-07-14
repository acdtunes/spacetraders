package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// sp-94du: UpdateSupportedGoods re-applies a freshly-recomputed receipt whitelist to an
// already-running warehouse via a TARGETED column update, so a redeployed cap selector reaches
// the running buffer. It must overwrite ONLY supported_goods — never disturb the live status or
// storage-ship registration — which is why it is a targeted update, not a full-row Save.

func mustRunningWarehouseWithShips(t *testing.T, playerID int) *storage.StorageOperation {
	t.Helper()
	op, err := storage.NewWarehouseOperation(
		"warehouse-sg-1", playerID, "X1-J58-WH",
		[]string{"HULL-STORE-1"}, []string{"ELECTRONICS", "EQUIPMENT", "MEDICINE"}, nil,
	)
	require.NoError(t, err)
	require.NoError(t, op.Start())
	return op
}

// A targeted supported_goods update overwrites the whitelist and reloads with the new set,
// while leaving the operation's RUNNING status and its storage hull untouched — the running
// coordinator keeps its ship registration; only the stocker-visible whitelist changes.
func TestStorageOperationRepository_UpdateSupportedGoodsReplacesWhitelistWithoutClobber(t *testing.T) {
	repo, playerID := newWarehouseRepoTest(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, mustRunningWarehouseWithShips(t, playerID)))

	require.NoError(t, repo.UpdateSupportedGoods(ctx, "warehouse-sg-1",
		[]string{"CLOTHING", "ASSAULT_RIFLES", "FIREARMS"}))

	reloaded, err := repo.FindByID(ctx, "warehouse-sg-1")
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	require.True(t, reloaded.SupportsGood("CLOTHING"))
	require.True(t, reloaded.SupportsGood("ASSAULT_RIFLES"))
	require.False(t, reloaded.SupportsGood("ELECTRONICS"), "the stale whitelist is REPLACED, not merged")
	require.True(t, reloaded.IsRunning(), "a targeted whitelist update must not disturb the RUNNING status")
	require.Equal(t, []string{"HULL-STORE-1"}, reloaded.StorageShips(),
		"a targeted whitelist update must not disturb the storage-ship registration")
}

// An empty whitelist is rejected and the persisted set is left standing: a warehouse with no
// supported goods would strand every deposit, so the update fails closed rather than blank the row.
func TestStorageOperationRepository_UpdateSupportedGoodsRejectsEmpty(t *testing.T) {
	repo, playerID := newWarehouseRepoTest(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, mustRunningWarehouseWithShips(t, playerID)))

	require.Error(t, repo.UpdateSupportedGoods(ctx, "warehouse-sg-1", nil))

	reloaded, err := repo.FindByID(ctx, "warehouse-sg-1")
	require.NoError(t, err)
	require.True(t, reloaded.SupportsGood("ELECTRONICS"), "a rejected empty update must leave the whitelist intact")
}

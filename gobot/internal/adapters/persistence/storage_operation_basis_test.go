package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// C1: the cost_basis column round-trips through the shared
// storage_operations repository, and is managed OUT-OF-BAND — a normal operation
// Update must not clobber it, and a basis write must not disturb the operation.

func mustWarehouseOp(t *testing.T, playerID int) *storage.StorageOperation {
	t.Helper()
	op, err := storage.NewWarehouseOperation(
		"warehouse-basis-1", playerID, "X1-HOME-A1",
		[]string{"HULL-STORE-1"}, []string{"CLOTHING", "EQUIPMENT"}, nil,
	)
	require.NoError(t, err)
	require.NoError(t, op.Start())
	return op
}

func TestStorageOperationRepository_CostBasisRoundTrip(t *testing.T) {
	repo, playerID := newWarehouseRepoTest(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, mustWarehouseOp(t, playerID)))

	require.NoError(t, repo.SaveOperationBasis(ctx, "warehouse-basis-1",
		map[string]int{"CLOTHING": 65, "EQUIPMENT": 120}))

	got, err := repo.LoadOperationBasis(ctx, "warehouse-basis-1")
	require.NoError(t, err)
	require.Equal(t, 65, got["CLOTHING"])
	require.Equal(t, 120, got["EQUIPMENT"])
}

// A full-row operation Update (e.g. a status transition) must not wipe the
// out-of-band cost_basis column.
func TestStorageOperationRepository_UpdateDoesNotClobberBasis(t *testing.T) {
	repo, playerID := newWarehouseRepoTest(t)
	ctx := context.Background()
	op := mustWarehouseOp(t, playerID)
	require.NoError(t, repo.Create(ctx, op))
	require.NoError(t, repo.SaveOperationBasis(ctx, "warehouse-basis-1", map[string]int{"CLOTHING": 65}))

	require.NoError(t, op.Stop())
	require.NoError(t, repo.Update(ctx, op)) // status change — must preserve basis

	got, err := repo.LoadOperationBasis(ctx, "warehouse-basis-1")
	require.NoError(t, err)
	require.Equal(t, 65, got["CLOTHING"], "operation Update must not clobber out-of-band cost_basis")
}

// A missing / unset basis loads as an empty map (fail-closed at the reader).
func TestStorageOperationRepository_LoadBasisEmptyWhenUnset(t *testing.T) {
	repo, playerID := newWarehouseRepoTest(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, mustWarehouseOp(t, playerID)))

	got, err := repo.LoadOperationBasis(ctx, "warehouse-basis-1")
	require.NoError(t, err)
	require.Empty(t, got)
}

package services

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// --- fakes ---------------------------------------------------------------

// fakeInvOpRepo stubs StorageOperationRepository with only FindByGood wired
// (embedding the interface makes every other method a nil panic we never hit).
type fakeInvOpRepo struct {
	storage.StorageOperationRepository
	ops []*storage.StorageOperation
	err error
}

func (r *fakeInvOpRepo) FindByGood(_ context.Context, _ int, good string) ([]*storage.StorageOperation, error) {
	if r.err != nil {
		return nil, r.err
	}
	var out []*storage.StorageOperation
	for _, op := range r.ops {
		if op.SupportsGood(good) {
			out = append(out, op)
		}
	}
	return out, nil
}

// fakeInvCoordinator stubs StorageCoordinator with GetTotalCargoAvailable keyed
// by operation ID; unlisted operations report 0.
type fakeInvCoordinator struct {
	storage.StorageCoordinator
	availableByOp map[string]int
}

func (c *fakeInvCoordinator) GetTotalCargoAvailable(operationID, _ string) int {
	return c.availableByOp[operationID]
}

func warehouseOp(t *testing.T, id, waypoint string, goods ...string) *storage.StorageOperation {
	t.Helper()
	op, err := storage.NewWarehouseOperation(id, 1, waypoint, []string{"WAREHOUSE-HULL-1"}, goods, nil)
	require.NoError(t, err)
	require.NoError(t, op.Start())
	return op
}

// --- tests ---------------------------------------------------------------

func TestInventoryFinder_InSystemWarehouseWithStock_Found(t *testing.T) {
	op := warehouseOp(t, "wh-home", "X1-HOME-H51", "ELECTRONICS")
	finder := NewStorageInventoryFinder(
		&fakeInvOpRepo{ops: []*storage.StorageOperation{op}},
		&fakeInvCoordinator{availableByOp: map[string]int{"wh-home": 404}},
	)

	src := finder.FindInSystemInventory(context.Background(), 1, "X1-HOME", "ELECTRONICS")

	require.NotNil(t, src, "an in-system warehouse holding the good must be found")
	require.Equal(t, "wh-home", src.OperationID)
	require.Equal(t, "X1-HOME-H51", src.StorageWaypoint)
	require.Equal(t, 404, src.UnitsAvailable)
}

func TestInventoryFinder_OtherSystemWarehouse_NotFound(t *testing.T) {
	// A warehouse holding the good but parked in a DIFFERENT system must never
	// be returned: contract withdrawal is a physical in-system hop (RULINGS #14).
	op := warehouseOp(t, "wh-foreign", "X1-FOREIGN-A1", "ELECTRONICS")
	finder := NewStorageInventoryFinder(
		&fakeInvOpRepo{ops: []*storage.StorageOperation{op}},
		&fakeInvCoordinator{availableByOp: map[string]int{"wh-foreign": 500}},
	)

	src := finder.FindInSystemInventory(context.Background(), 1, "X1-HOME", "ELECTRONICS")

	require.Nil(t, src, "an out-of-system warehouse must not be a contract inventory source (RULINGS #14)")
}

func TestInventoryFinder_NonWarehouseOperation_Ignored(t *testing.T) {
	// A gas/mining storage op in-system that happens to support the good is not a
	// contract inventory source — those buffers live at extraction sites.
	gasOp, err := storage.NewStorageOperation(
		"gas-home", 1, "X1-HOME-GAS", storage.OperationTypeGasSiphon,
		[]string{"EXT-1"}, []string{"STORE-1"}, []string{"ELECTRONICS"}, nil,
	)
	require.NoError(t, err)
	require.NoError(t, gasOp.Start())

	finder := NewStorageInventoryFinder(
		&fakeInvOpRepo{ops: []*storage.StorageOperation{gasOp}},
		&fakeInvCoordinator{availableByOp: map[string]int{"gas-home": 500}},
	)

	src := finder.FindInSystemInventory(context.Background(), 1, "X1-HOME", "ELECTRONICS")

	require.Nil(t, src, "only OperationTypeWarehouse buffers contract goods")
}

func TestInventoryFinder_ZeroAvailable_NotFound(t *testing.T) {
	// A warehouse whose unreserved availability is 0 (fully reserved or a stopped
	// hull whose ship is unregistered) reports no inventory → market path.
	op := warehouseOp(t, "wh-empty", "X1-HOME-H51", "ELECTRONICS")
	finder := NewStorageInventoryFinder(
		&fakeInvOpRepo{ops: []*storage.StorageOperation{op}},
		&fakeInvCoordinator{availableByOp: map[string]int{"wh-empty": 0}},
	)

	src := finder.FindInSystemInventory(context.Background(), 1, "X1-HOME", "ELECTRONICS")

	require.Nil(t, src, "zero unreserved units is not an inventory source")
}

func TestInventoryFinder_RepoError_FailsOpenToNil(t *testing.T) {
	finder := NewStorageInventoryFinder(
		&fakeInvOpRepo{err: errors.New("db unavailable")},
		&fakeInvCoordinator{availableByOp: map[string]int{}},
	)

	src := finder.FindInSystemInventory(context.Background(), 1, "X1-HOME", "ELECTRONICS")

	require.Nil(t, src, "a repository read error must fail open to nil (never park a contract on a warehouse read)")
}

func TestInventoryFinder_NoWarehouse_NotFound(t *testing.T) {
	finder := NewStorageInventoryFinder(
		&fakeInvOpRepo{ops: nil},
		&fakeInvCoordinator{availableByOp: map[string]int{}},
	)

	src := finder.FindInSystemInventory(context.Background(), 1, "X1-HOME", "ELECTRONICS")

	require.Nil(t, src)
}

func TestInventoryFinder_NilReceiverAndDeps_Safe(t *testing.T) {
	var nilFinder *StorageInventoryFinder
	require.Nil(t, nilFinder.FindInSystemInventory(context.Background(), 1, "X1-HOME", "ELECTRONICS"))

	require.Nil(t, NewStorageInventoryFinder(nil, nil).FindInSystemInventory(context.Background(), 1, "X1-HOME", "ELECTRONICS"))
}

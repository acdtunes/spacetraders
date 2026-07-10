package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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

// warehouseOpAtTime builds a RUNNING warehouse op with a controllable CreatedAt so a
// most-units tie can be broken deterministically by the newest operation.
func warehouseOpAtTime(t *testing.T, id, waypoint string, createdAt time.Time, goods ...string) *storage.StorageOperation {
	t.Helper()
	op, err := storage.NewWarehouseOperation(id, 1, waypoint, []string{"WAREHOUSE-HULL-1"}, goods, &shared.MockClock{CurrentTime: createdAt})
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

// TestInventoryFinder_MultipleWarehouses_SourcesFromFullest is the sp-5q2c multi-
// warehouse pin: two co-located in-system warehouses hold the good (40 + 100 units).
// The withdrawal targets the FULLEST hull (most units = fewest trips), and the
// reported availability is the SUM across both — the total on hand, so a sibling's
// stock is never invisible to the contract-sourcing gate.
func TestInventoryFinder_MultipleWarehouses_SourcesFromFullest(t *testing.T) {
	small := warehouseOp(t, "wh-small", "X1-HOME-H51", "ELECTRONICS")
	large := warehouseOp(t, "wh-large", "X1-HOME-H51", "ELECTRONICS")
	finder := NewStorageInventoryFinder(
		&fakeInvOpRepo{ops: []*storage.StorageOperation{small, large}},
		&fakeInvCoordinator{availableByOp: map[string]int{"wh-small": 40, "wh-large": 100}},
	)

	src := finder.FindInSystemInventory(context.Background(), 1, "X1-HOME", "ELECTRONICS")

	require.NotNil(t, src)
	require.Equal(t, "wh-large", src.OperationID, "withdrawal must target the fullest hull (most units)")
	require.Equal(t, 140, src.UnitsAvailable, "reported availability must SUM across the co-located group (40+100)")
}

// TestInventoryFinder_MultipleWarehouses_TieBreaksToNewest: on an equal-units tie the
// withdrawal targets the NEWEST operation (stable, and biased toward the live hull in
// a zombie-adjacent read).
func TestInventoryFinder_MultipleWarehouses_TieBreaksToNewest(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	older := warehouseOpAtTime(t, "wh-older", "X1-HOME-H51", t0, "ELECTRONICS")
	newer := warehouseOpAtTime(t, "wh-newer", "X1-HOME-H51", t0.Add(2*time.Hour), "ELECTRONICS")
	finder := NewStorageInventoryFinder(
		&fakeInvOpRepo{ops: []*storage.StorageOperation{older, newer}},
		&fakeInvCoordinator{availableByOp: map[string]int{"wh-older": 100, "wh-newer": 100}},
	)

	src := finder.FindInSystemInventory(context.Background(), 1, "X1-HOME", "ELECTRONICS")

	require.NotNil(t, src)
	require.Equal(t, "wh-newer", src.OperationID, "an equal-units tie must resolve to the newest operation")
	require.Equal(t, 200, src.UnitsAvailable)
}

// TestInventoryFinder_MultipleWarehouses_ExcludesOutOfSystemFromSum confirms an
// out-of-system warehouse holding the good is excluded from BOTH the pick and the sum
// (RULINGS #14) — only the in-system hull's units are reported.
func TestInventoryFinder_MultipleWarehouses_ExcludesOutOfSystemFromSum(t *testing.T) {
	home := warehouseOp(t, "wh-home", "X1-HOME-H51", "ELECTRONICS")
	foreign := warehouseOp(t, "wh-foreign", "X1-FOREIGN-A1", "ELECTRONICS")
	finder := NewStorageInventoryFinder(
		&fakeInvOpRepo{ops: []*storage.StorageOperation{home, foreign}},
		&fakeInvCoordinator{availableByOp: map[string]int{"wh-home": 40, "wh-foreign": 500}},
	)

	src := finder.FindInSystemInventory(context.Background(), 1, "X1-HOME", "ELECTRONICS")

	require.NotNil(t, src)
	require.Equal(t, "wh-home", src.OperationID)
	require.Equal(t, 40, src.UnitsAvailable, "the out-of-system warehouse must not be summed in")
}

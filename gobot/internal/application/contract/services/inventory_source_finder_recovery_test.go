package services

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// FindRunning extends the existing fakeInvOpRepo (defined in
// inventory_source_finder_test.go) so the same fake serves BOTH the finder
// (FindByGood) and the storage recovery service (FindRunning) — proving they read
// one shared world. Additive: existing finder tests never call it.
func (r *fakeInvOpRepo) FindRunning(_ context.Context, _ int) ([]*storage.StorageOperation, error) {
	if r.err != nil {
		return nil, r.err
	}
	var out []*storage.StorageOperation
	for _, op := range r.ops {
		if op.IsRunning() {
			out = append(out, op)
		}
	}
	return out, nil
}

// recoveryAPIStub reports a storage hull's live cargo for the recovery service.
type recoveryAPIStub struct {
	ports.APIClient
	ships map[string]*navigation.ShipData
}

func (s *recoveryAPIStub) GetShip(_ context.Context, symbol, _ string) (*navigation.ShipData, error) {
	if ship, ok := s.ships[symbol]; ok {
		return ship, nil
	}
	return nil, fmt.Errorf("ship %s not found", symbol)
}

// TestInventoryFinder_ReturnsWarehouseAfterStorageRecovery is the sp-o477
// end-to-end pin, wired through the REAL shared coordinator (not a stub). It
// reproduces the regression and its fix across the exact seam that broke:
//
//   - Before recovery, the coordinator is EMPTY (post-restart state), so the
//     contract StorageInventoryFinder returns nil for a good the warehouse
//     physically holds — the caller falls through to a market buy (the bug).
//   - Storage recovery re-seeds THAT SAME coordinator from the hull's live cargo.
//   - After recovery, the finder returns the in-system warehouse for the held
//     good, so the contract worker withdraws standing stock instead of buying.
//
// Because the finder and the recovery service share one coordinator instance
// here, this also guards the "re-seeds a different coordinator" failure mode: if
// recovery populated a second instance, the finder would still see nil and the
// AFTER assertion would fail.
func TestInventoryFinder_ReturnsWarehouseAfterStorageRecovery(t *testing.T) {
	ctx := context.Background()
	op := warehouseOp(t, "wh-home", "X1-HOME-H51", "ELECTRONICS") // ship WAREHOUSE-HULL-1
	repo := &fakeInvOpRepo{ops: []*storage.StorageOperation{op}}

	coordinator := storageApp.NewInMemoryStorageCoordinator()
	finder := NewStorageInventoryFinder(repo, coordinator)

	// BEFORE recovery: empty coordinator ⇒ finder nil ⇒ contract market-buys (bug).
	require.Nil(t, finder.FindInSystemInventory(ctx, 1, "X1-HOME", "ELECTRONICS"),
		"precondition: post-restart the empty coordinator makes standing stock invisible (sp-o477)")

	recovery := storageApp.NewStorageRecoveryService(repo, &recoveryAPIStub{
		ships: map[string]*navigation.ShipData{
			"WAREHOUSE-HULL-1": {
				Symbol:   "WAREHOUSE-HULL-1",
				Location: "X1-HOME-H51",
				Cargo: &navigation.CargoData{
					Capacity:  120,
					Units:     55,
					Inventory: []shared.CargoItem{{Symbol: "ELECTRONICS", Units: 55}},
				},
			},
		},
	}, coordinator)
	_, err := recovery.RecoverStorageOperations(ctx, 1, "token")
	require.NoError(t, err)

	// AFTER recovery: the finder now routes the contract to the warehouse.
	src := finder.FindInSystemInventory(ctx, 1, "X1-HOME", "ELECTRONICS")
	require.NotNil(t, src,
		"after storage recovery the finder must return the in-system warehouse (was nil pre-fix — sp-o477)")
	require.Equal(t, "wh-home", src.OperationID)
	require.Equal(t, "X1-HOME-H51", src.StorageWaypoint)
	require.Equal(t, 55, src.UnitsAvailable)
}

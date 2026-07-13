package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// TestRecovery_AlreadyRegisteredShipNotDoubleCounted guards the sp-o477
// idempotency contract. On restart the warehouse worker's own registration
// (run_warehouse.go) and this boot recovery can BOTH run, so recovery must be a
// no-op for an already-registered ship — never a second registration that doubles
// the coordinator's cargo availability. Re-running recovery is the direct proxy
// for "the ship is already registered": the second pass reports zero new ships
// and the available count is unchanged (not doubled).
func TestRecovery_AlreadyRegisteredShipNotDoubleCounted(t *testing.T) {
	op, err := storage.NewWarehouseOperation(
		"warehouse-X1-HOME-A1", 1, "X1-HOME-A1",
		[]string{"HULL-STORE-1"}, []string{"IRON_ORE"}, nil,
	)
	require.NoError(t, err)
	require.NoError(t, op.Start())

	repo := &stubWarehouseOpRepo{running: []*storage.StorageOperation{op}}
	apiClient := &stubWarehouseAPIClient{ships: map[string]*navigation.ShipData{
		"HULL-STORE-1": {
			Symbol:   "HULL-STORE-1",
			Location: "X1-HOME-A1",
			Cargo: &navigation.CargoData{
				Capacity:  120,
				Units:     30,
				Inventory: []shared.CargoItem{{Symbol: "IRON_ORE", Units: 30}},
			},
		},
	}}

	coordinator := NewInMemoryStorageCoordinator()
	svc := NewStorageRecoveryService(repo, apiClient, coordinator)

	// First pass: the hull is registered and its 30 units become available.
	first, err := svc.RecoverStorageOperations(context.Background(), 1, "token")
	require.NoError(t, err)
	require.Equal(t, 1, first.ShipsRegistered)
	require.Equal(t, 30, coordinator.GetTotalCargoAvailable("warehouse-X1-HOME-A1", "IRON_ORE"))

	// Second pass (the hull is now already registered): no new registration and
	// the available count stays 30 — recovery never double-counts standing stock.
	second, err := svc.RecoverStorageOperations(context.Background(), 1, "token")
	require.NoError(t, err)
	require.Equal(t, 0, second.ShipsRegistered,
		"an already-registered ship must not be registered again")
	require.Empty(t, second.Errors)
	require.Equal(t, 30, coordinator.GetTotalCargoAvailable("warehouse-X1-HOME-A1", "IRON_ORE"),
		"re-running recovery on an already-registered hull must not double the available cargo")
}

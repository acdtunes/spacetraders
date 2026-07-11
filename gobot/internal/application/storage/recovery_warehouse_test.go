package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// stubWarehouseOpRepo serves a fixed set of running operations. Recovery only
// ever calls FindRunning, so the rest of the interface is left to the embedded
// nil (it panics if called, surfacing any unexpected new dependency).
type stubWarehouseOpRepo struct {
	storage.StorageOperationRepository
	running []*storage.StorageOperation
}

func (s *stubWarehouseOpRepo) FindRunning(ctx context.Context, playerID int) ([]*storage.StorageOperation, error) {
	return s.running, nil
}

// stubWarehouseAPIClient reports each ship's LIVE cargo — the physical source of
// truth the recovery service reconstructs from (RULINGS #2). Only GetShip is
// needed; the rest of the API surface is the embedded nil.
type stubWarehouseAPIClient struct {
	ports.APIClient
	ships map[string]*navigation.ShipData
}

func (s *stubWarehouseAPIClient) GetShip(ctx context.Context, symbol, token string) (*navigation.ShipData, error) {
	if ship, ok := s.ships[symbol]; ok {
		return ship, nil
	}
	return nil, fmt.Errorf("ship %s not found", symbol)
}

// The load-bearing RULINGS #2 proof: after a daemon restart the in-memory
// coordinator is EMPTY, but a warehouse's persisted RUNNING row plus the storage
// hull's live API cargo must reconstruct the warehouse's inventory with no new
// table and no warehouse-specific recovery code. RecoverStorageOperations reads
// only StorageShips()/WaypointSymbol() and the live cargo — never extractors —
// so a zero-extractor warehouse rebuilds identically to a gas operation.
func TestRecovery_RebuildsWarehouseCargoFromLiveShipState(t *testing.T) {
	// A warehouse that, before the restart, had 30 IRON_ORE deposited into it.
	op, err := storage.NewWarehouseOperation(
		"warehouse-X1-HOME-A1",
		1,
		"X1-HOME-A1",
		[]string{"HULL-STORE-1"},
		[]string{"IRON_ORE"},
		nil,
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

	// Fresh coordinator = the post-restart state (all in-memory ships lost).
	coordinator := NewInMemoryStorageCoordinator()
	require.Equal(t, 0, coordinator.GetTotalCargoAvailable("warehouse-X1-HOME-A1", "IRON_ORE"),
		"precondition: nothing registered before recovery")

	svc := NewStorageRecoveryService(repo, apiClient, coordinator)
	result, err := svc.RecoverStorageOperations(context.Background(), 1, "token")
	require.NoError(t, err)
	require.Equal(t, 1, result.OperationsRecovered)
	require.Equal(t, 1, result.ShipsRegistered)
	require.Empty(t, result.Errors)

	// The warehouse's cargo is reconstructed and immediately queryable/withdrawable.
	require.Equal(t, 30, coordinator.GetTotalCargoAvailable("warehouse-X1-HOME-A1", "IRON_ORE"),
		"warehouse inventory must be rebuilt from the hull's live cargo after restart")
	_, registered := coordinator.GetStorageShipBySymbol("HULL-STORE-1")
	require.True(t, registered, "the warehouse hull must be re-registered with the coordinator")
}

// C1 (sp-64je): on restart, units are rebuilt from the live ship API but the
// cost basis is reloaded from durable storage and re-seeded. After recovery, the
// solver-facing GetCostBasis reports the persisted basis for the recovered stock.
func TestRecovery_ReseedsCostBasisForRecoveredStock(t *testing.T) {
	op, err := storage.NewWarehouseOperation(
		"warehouse-X1-HOME-A1", 1, "X1-HOME-A1",
		[]string{"HULL-STORE-1"}, []string{"CLOTHING"}, nil,
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
				Units:     40,
				Inventory: []shared.CargoItem{{Symbol: "CLOTHING", Units: 40}},
			},
		},
	}}

	coordinator := NewInMemoryStorageCoordinator()
	store := newFakeBasisStore()
	store.toLoad["warehouse-X1-HOME-A1"] = map[string]int{"CLOTHING": 65}
	coordinator.SetCostBasisStore(store)

	svc := NewStorageRecoveryService(repo, apiClient, coordinator)
	_, err = svc.RecoverStorageOperations(context.Background(), 1, "token")
	require.NoError(t, err)

	basis, known := coordinator.GetCostBasis("warehouse-X1-HOME-A1", "CLOTHING")
	require.True(t, known, "cost basis must be re-seeded from durable storage on recovery")
	require.Equal(t, 65, basis)
}

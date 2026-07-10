package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// --- stubs (embed the port; only the methods the handler exercises are real) ---

// stubWarehouseMediator satisfies common.Mediator. Send is never invoked in
// these tests because the hull is already parked at the home waypoint, so
// navigation is skipped — a call here would be a bug, and the embedded nil
// panics to surface it.
type stubWarehouseMediator struct {
	common.Mediator
}

type stubWarehouseShipRepo struct {
	navigation.ShipRepository
	ship *navigation.Ship
}

func (r *stubWarehouseShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

// stubWarehouseOpRepo is an in-memory storage-operation repo. FindByID/Create/
// Update are the getOrCreate path; the rest is the embedded nil.
type stubWarehouseOpRepo struct {
	storage.StorageOperationRepository
	ops map[string]*storage.StorageOperation
}

func newStubWarehouseOpRepo() *stubWarehouseOpRepo {
	return &stubWarehouseOpRepo{ops: make(map[string]*storage.StorageOperation)}
}

func (r *stubWarehouseOpRepo) FindByID(_ context.Context, id string) (*storage.StorageOperation, error) {
	return r.ops[id], nil // (nil, nil) when absent — the create path, matching the real repo
}

func (r *stubWarehouseOpRepo) Create(_ context.Context, op *storage.StorageOperation) error {
	r.ops[op.ID()] = op
	return nil
}

func (r *stubWarehouseOpRepo) Update(_ context.Context, op *storage.StorageOperation) error {
	r.ops[op.ID()] = op
	return nil
}

func newWarehouseTestHull(t *testing.T, symbol, waypoint string, capacity int, cargo []*shared.CargoItem) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	units := 0
	for _, item := range cargo {
		units += item.Units
	}
	cargoState, err := shared.NewCargo(capacity, units, cargo)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), location, fuel, 100, capacity, cargoState,
		9, "FRAME_FREIGHTER", "HAULER", nil, navigation.NavStatusInOrbit,
	)
	require.NoError(t, err)
	return ship
}

// The end-to-end Lane B acceptance at the lifecycle-owner seam: starting a
// warehouse on a hull persists a RUNNING operation row and registers the hull
// (seeded from its live cargo) with the shared coordinator; a manual deposit via
// the tour/trade deposit protocol grows inventory; GetTotalCargoAvailable
// reports it; and the EXISTING manufacturing STORAGE_ACQUIRE_DELIVER withdrawal
// protocol (WaitForCargo -> ConfirmTransfer) drains it — all against the shared
// machinery, unchanged.
func TestRunWarehouse_SetupThenDepositAndManufacturingWithdraw(t *testing.T) {
	ctx := context.Background()
	logger := common.LoggerFromContext(ctx)

	// A hull already parked at the home waypoint with 25 IRON_ORE aboard.
	ironOre, err := shared.NewCargoItem("IRON_ORE", "Iron Ore", "", 25)
	require.NoError(t, err)
	hull := newWarehouseTestHull(t, "HULL-STORE-1", "X1-HOME-A1", 120, []*shared.CargoItem{ironOre})

	coordinator := storageApp.NewInMemoryStorageCoordinator()
	opRepo := newStubWarehouseOpRepo()
	handler := NewRunWarehouseHandler(
		&stubWarehouseMediator{}, &stubWarehouseShipRepo{ship: hull}, opRepo, coordinator, nil,
	)

	cmd := &RunWarehouseCommand{
		ShipSymbol:     "HULL-STORE-1",
		WaypointSymbol: "X1-HOME-A1",
		PlayerID:       shared.MustNewPlayerID(1),
		ContainerID:    "warehouse-X1-HOME-A1",
		OperationID:    "warehouse-X1-HOME-A1",
		SupportedGoods: []string{"IRON_ORE", "ALUMINUM"},
	}

	loc, err := handler.setup(ctx, cmd, logger)
	require.NoError(t, err)
	require.Equal(t, "X1-HOME-A1", loc, "a hull already parked issues no navigation")

	// The operation row is persisted RUNNING (recovery + StorageSourceFinder need it).
	persisted := opRepo.ops["warehouse-X1-HOME-A1"]
	require.NotNil(t, persisted)
	require.True(t, persisted.IsRunning())
	require.Equal(t, storage.OperationTypeWarehouse, persisted.OperationType())

	// The hull is registered, seeded from its live cargo.
	require.Equal(t, 25, coordinator.GetTotalCargoAvailable("warehouse-X1-HOME-A1", "IRON_ORE"))

	// Deposit leg (tour/trade drops 40 ALUMINUM): reserve space -> confirm.
	depositShip, reserved, ok := coordinator.ReserveSpaceForDeposit("warehouse-X1-HOME-A1", 40)
	require.True(t, ok)
	require.Equal(t, 40, reserved)
	coordinator.ConfirmDeposit(depositShip.ShipSymbol(), "ALUMINUM", 40)
	require.Equal(t, 40, coordinator.GetTotalCargoAvailable("warehouse-X1-HOME-A1", "ALUMINUM"))

	// Withdrawal via the EXACT manufacturing executor protocol: WaitForCargo
	// reserves, ConfirmTransfer completes the transfer and drains inventory.
	storageShip, units, err := coordinator.WaitForCargo(ctx, "warehouse-X1-HOME-A1", "IRON_ORE", 10)
	require.NoError(t, err)
	require.Equal(t, 25, units, "WaitForCargo reserves all available to fill the hauler")
	require.NoError(t, storageShip.ConfirmTransfer("IRON_ORE", 25))
	require.Equal(t, 0, coordinator.GetTotalCargoAvailable("warehouse-X1-HOME-A1", "IRON_ORE"),
		"the warehouse is drained after a manufacturing-style withdrawal")
}

// A restart re-runs setup against an already-persisted operation row: it must be
// RESUMED (not duplicated) and the hull re-registered from live cargo — the
// idempotence RULINGS #2 requires. Here the operation already exists RUNNING,
// mimicking the post-restart repo state.
func TestRunWarehouse_SetupResumesExistingOperationIdempotently(t *testing.T) {
	ctx := context.Background()
	logger := common.LoggerFromContext(ctx)

	hull := newWarehouseTestHull(t, "HULL-STORE-1", "X1-HOME-A1", 120, nil)
	coordinator := storageApp.NewInMemoryStorageCoordinator()

	// Pre-seed a RUNNING warehouse op, as if it survived a restart.
	existing, err := storage.NewWarehouseOperation(
		"warehouse-X1-HOME-A1", 1, "X1-HOME-A1", []string{"HULL-STORE-1"}, []string{"IRON_ORE"}, nil,
	)
	require.NoError(t, err)
	require.NoError(t, existing.Start())
	opRepo := newStubWarehouseOpRepo()
	opRepo.ops["warehouse-X1-HOME-A1"] = existing

	handler := NewRunWarehouseHandler(
		&stubWarehouseMediator{}, &stubWarehouseShipRepo{ship: hull}, opRepo, coordinator, nil,
	)
	cmd := &RunWarehouseCommand{
		ShipSymbol: "HULL-STORE-1", WaypointSymbol: "X1-HOME-A1", PlayerID: shared.MustNewPlayerID(1),
		ContainerID: "warehouse-X1-HOME-A1", OperationID: "warehouse-X1-HOME-A1", SupportedGoods: []string{"IRON_ORE"},
	}

	_, err = handler.setup(ctx, cmd, logger)
	require.NoError(t, err)
	require.Len(t, opRepo.ops, 1, "resume must not create a duplicate operation row")
	require.Same(t, existing, opRepo.ops["warehouse-X1-HOME-A1"], "the existing row is reused")
	_, registered := coordinator.GetStorageShipBySymbol("HULL-STORE-1")
	require.True(t, registered, "the hull is re-registered on resume")
}

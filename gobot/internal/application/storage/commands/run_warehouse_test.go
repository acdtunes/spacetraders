package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	storageApp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// --- stubs (embed the port; only the methods the handler exercises are real) ---

// stubWarehouseMediator satisfies common.Mediator and records the navigation and
// orbit commands setup() sends. It mirrors the real handlers just enough to drive
// the tests: an OrbitShipCommand transitions its ship in place (EnsureInOrbit,
// exactly like tactics.OrbitShipHandler), and a NavigateRouteCommand returns the
// pre-positioned arrivedShip on success or a configured error to simulate a
// route-exec failure.
type stubWarehouseMediator struct {
	common.Mediator

	orbitCalls    int
	navCalls      int
	lastOrbitShip string
	arrivedShip   *navigation.Ship // returned from a successful NavigateRouteCommand
	navAlwaysErr  error            // when set, every navigate fails with this
	navResults    []error          // per-call navigate outcomes (nil = success); overrides navAlwaysErr per index
}

func (m *stubWarehouseMediator) Send(_ context.Context, req common.Request) (common.Response, error) {
	switch c := req.(type) {
	case *shipTypes.OrbitShipCommand:
		m.orbitCalls++
		if c.Ship != nil {
			m.lastOrbitShip = c.Ship.ShipSymbol()
			_, _ = c.Ship.EnsureInOrbit() // mirror the real handler's in-place transition
		}
		return &shipTypes.OrbitShipResponse{Status: "in_orbit"}, nil
	case *shipNav.NavigateRouteCommand:
		idx := m.navCalls
		m.navCalls++
		if idx < len(m.navResults) && m.navResults[idx] != nil {
			return nil, m.navResults[idx]
		}
		if m.navAlwaysErr != nil {
			return nil, m.navAlwaysErr
		}
		return &shipNav.NavigateRouteResponse{Ship: m.arrivedShip}, nil
	default:
		return nil, nil
	}
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
	return newWarehouseTestHullWithStatus(t, symbol, waypoint, capacity, cargo, navigation.NavStatusInOrbit)
}

func newWarehouseTestHullWithStatus(t *testing.T, symbol, waypoint string, capacity int, cargo []*shared.CargoItem, status navigation.NavStatus) *navigation.Ship {
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
		9, "FRAME_FREIGHTER", "HAULER", nil, status,
	)
	require.NoError(t, err)
	return ship
}

func newWarehouseCmd(ship, waypoint string, goods ...string) *RunWarehouseCommand {
	if len(goods) == 0 {
		goods = []string{"IRON_ORE"}
	}
	return &RunWarehouseCommand{
		ShipSymbol:     ship,
		WaypointSymbol: waypoint,
		PlayerID:       shared.MustNewPlayerID(1),
		ContainerID:    "warehouse-" + waypoint,
		OperationID:    "warehouse-" + waypoint,
		SupportedGoods: goods,
	}
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

	// A hull already parked+orbiting at the home waypoint with 25 IRON_ORE aboard.
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

// A hull already AT its home waypoint but DOCKED must be put into ORBIT before
// registration — a warehouse anchors its stock in orbit. No navigation is issued
// (it is already home).
func TestRunWarehouse_AlreadyAtWaypointDocked_OrbitsBeforeRegister(t *testing.T) {
	ctx := context.Background()
	logger := common.LoggerFromContext(ctx)

	hull := newWarehouseTestHullWithStatus(t, "HULL-STORE-1", "X1-HOME-A1", 120, nil, navigation.NavStatusDocked)
	med := &stubWarehouseMediator{}
	coordinator := storageApp.NewInMemoryStorageCoordinator()
	opRepo := newStubWarehouseOpRepo()
	handler := NewRunWarehouseHandler(med, &stubWarehouseShipRepo{ship: hull}, opRepo, coordinator, nil)

	loc, err := handler.setup(ctx, newWarehouseCmd("HULL-STORE-1", "X1-HOME-A1"), logger)
	require.NoError(t, err)
	require.Equal(t, "X1-HOME-A1", loc)

	require.Equal(t, navigation.NavStatusInOrbit, hull.NavStatus(), "the warehouse hull must be ORBITING after setup, not docked")
	require.GreaterOrEqual(t, med.orbitCalls, 1, "setup must orbit the hull via the daemon (OrbitShipCommand)")
	require.Equal(t, 0, med.navCalls, "a hull already at its waypoint issues no navigation")

	_, registered := coordinator.GetStorageShipBySymbol("HULL-STORE-1")
	require.True(t, registered)
}

// When the hull cannot be positioned at its home waypoint, setup must RETRY
// (bounded) and then FAIL LOUD with a durable BLOCKED operation state — never
// silently register the hull at the wrong location (a mis-anchored warehouse is
// non-functional).
func TestRunWarehouse_RouteExecFailure_FailsLoudNoMisAnchor(t *testing.T) {
	ctx := context.Background()
	logger := common.LoggerFromContext(ctx)

	// Hull is elsewhere; every attempt to route it home fails.
	hull := newWarehouseTestHullWithStatus(t, "HULL-STORE-1", "X1-ELSEWHERE-Z9", 120, nil, navigation.NavStatusDocked)
	med := &stubWarehouseMediator{navAlwaysErr: errors.New("route unreachable")}
	coordinator := storageApp.NewInMemoryStorageCoordinator()
	opRepo := newStubWarehouseOpRepo()
	// MockClock so the retry backoff is instant.
	handler := NewRunWarehouseHandler(med, &stubWarehouseShipRepo{ship: hull}, opRepo, coordinator, &shared.MockClock{CurrentTime: time.Now()})

	loc, err := handler.setup(ctx, newWarehouseCmd("HULL-STORE-1", "X1-HOME-A1"), logger)
	require.Error(t, err, "an unreachable home waypoint must surface a loud failure, not a silent success")
	require.Empty(t, loc)
	require.Contains(t, err.Error(), "route unreachable")

	require.GreaterOrEqual(t, med.navCalls, 2, "setup must retry the positioning before giving up")

	_, registered := coordinator.GetStorageShipBySymbol("HULL-STORE-1")
	require.False(t, registered, "a hull that never reached its home waypoint must NOT be registered")

	// The operation is durably marked FAILED with the reason (restart-resilient).
	blocked := opRepo.ops["warehouse-X1-HOME-A1"]
	require.NotNil(t, blocked)
	require.Equal(t, storage.OperationStatusFailed, blocked.Status(), "a blocked warehouse operation must be durably FAILED")
	require.Error(t, blocked.LastError())
	require.Contains(t, blocked.LastError().Error(), "route unreachable")
}

// A hull that must travel to a reachable home waypoint is navigated, then ORBITED
// at the target, then registered there.
func TestRunWarehouse_NavigatesThenOrbitsAtTarget(t *testing.T) {
	ctx := context.Background()
	logger := common.LoggerFromContext(ctx)

	hull := newWarehouseTestHullWithStatus(t, "HULL-STORE-1", "X1-ELSEWHERE-Z9", 120, nil, navigation.NavStatusDocked)
	// A successful navigate lands the hull DOCKED at the home waypoint.
	arrived := newWarehouseTestHullWithStatus(t, "HULL-STORE-1", "X1-HOME-A1", 120, nil, navigation.NavStatusDocked)
	med := &stubWarehouseMediator{arrivedShip: arrived}
	coordinator := storageApp.NewInMemoryStorageCoordinator()
	opRepo := newStubWarehouseOpRepo()
	handler := NewRunWarehouseHandler(med, &stubWarehouseShipRepo{ship: hull}, opRepo, coordinator, nil)

	loc, err := handler.setup(ctx, newWarehouseCmd("HULL-STORE-1", "X1-HOME-A1"), logger)
	require.NoError(t, err)
	require.Equal(t, "X1-HOME-A1", loc)
	require.Equal(t, 1, med.navCalls, "the hull must be navigated to its home waypoint")
	require.GreaterOrEqual(t, med.orbitCalls, 1, "the hull must be orbited at its home waypoint")
	require.Equal(t, navigation.NavStatusInOrbit, arrived.NavStatus(), "the hull must be ORBITING at the target after arrival")

	_, registered := coordinator.GetStorageShipBySymbol("HULL-STORE-1")
	require.True(t, registered)
}

// A transient in-transit positioning failure is resolved by the bounded retry:
// a navigate that fails once then succeeds ends with the warehouse registered and
// orbiting, not mis-anchored.
func TestRunWarehouse_TransientFailureThenSuccess_Recovers(t *testing.T) {
	ctx := context.Background()
	logger := common.LoggerFromContext(ctx)

	hull := newWarehouseTestHullWithStatus(t, "HULL-STORE-1", "X1-ELSEWHERE-Z9", 120, nil, navigation.NavStatusDocked)
	arrived := newWarehouseTestHullWithStatus(t, "HULL-STORE-1", "X1-HOME-A1", 120, nil, navigation.NavStatusDocked)
	med := &stubWarehouseMediator{
		arrivedShip: arrived,
		navResults:  []error{errors.New("API error (status 400): code 4214 ship is currently in-transit")}, // 1st fails, 2nd succeeds
	}
	coordinator := storageApp.NewInMemoryStorageCoordinator()
	opRepo := newStubWarehouseOpRepo()
	handler := NewRunWarehouseHandler(med, &stubWarehouseShipRepo{ship: hull}, opRepo, coordinator, &shared.MockClock{CurrentTime: time.Now()})

	loc, err := handler.setup(ctx, newWarehouseCmd("HULL-STORE-1", "X1-HOME-A1"), logger)
	require.NoError(t, err, "a transient in-transit failure must be resolved by the bounded retry")
	require.Equal(t, "X1-HOME-A1", loc)
	require.Equal(t, 2, med.navCalls, "the retry must re-issue the navigate after the transient failure")
	require.Equal(t, navigation.NavStatusInOrbit, arrived.NavStatus())

	_, registered := coordinator.GetStorageShipBySymbol("HULL-STORE-1")
	require.True(t, registered)

	require.True(t, opRepo.ops["warehouse-X1-HOME-A1"].IsRunning(), "a recovered warehouse operation stays RUNNING")
}

// A daemon shutdown mid-park surfaces as "context canceled". Cancellation is NOT a
// warehouse failure: setup must return the error without marking the operation
// FAILED (so it is re-adopted at next boot, RULINGS #2) and without registering the
// hull at the wrong location.
func TestRunWarehouse_ContextCanceled_NotMarkedFailed(t *testing.T) {
	ctx := context.Background()
	logger := common.LoggerFromContext(ctx)

	hull := newWarehouseTestHullWithStatus(t, "HULL-STORE-1", "X1-ELSEWHERE-Z9", 120, nil, navigation.NavStatusDocked)
	med := &stubWarehouseMediator{navAlwaysErr: context.Canceled}
	coordinator := storageApp.NewInMemoryStorageCoordinator()
	opRepo := newStubWarehouseOpRepo()
	handler := NewRunWarehouseHandler(med, &stubWarehouseShipRepo{ship: hull}, opRepo, coordinator, &shared.MockClock{CurrentTime: time.Now()})

	_, err := handler.setup(ctx, newWarehouseCmd("HULL-STORE-1", "X1-HOME-A1"), logger)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, med.navCalls, "cancellation must not be retried")

	_, registered := coordinator.GetStorageShipBySymbol("HULL-STORE-1")
	require.False(t, registered, "a canceled park must not register the hull")

	op := opRepo.ops["warehouse-X1-HOME-A1"]
	require.NotNil(t, op)
	require.NotEqual(t, storage.OperationStatusFailed, op.Status(), "a graceful shutdown must not durably fail the operation")
}

// Regression: a hull already correctly parked AND orbiting at its home waypoint
// issues no spurious navigation.
func TestRunWarehouse_AlreadyParkedAndOrbiting_NoSpuriousNav(t *testing.T) {
	ctx := context.Background()
	logger := common.LoggerFromContext(ctx)

	hull := newWarehouseTestHullWithStatus(t, "HULL-STORE-1", "X1-HOME-A1", 120, nil, navigation.NavStatusInOrbit)
	med := &stubWarehouseMediator{}
	coordinator := storageApp.NewInMemoryStorageCoordinator()
	opRepo := newStubWarehouseOpRepo()
	handler := NewRunWarehouseHandler(med, &stubWarehouseShipRepo{ship: hull}, opRepo, coordinator, nil)

	loc, err := handler.setup(ctx, newWarehouseCmd("HULL-STORE-1", "X1-HOME-A1"), logger)
	require.NoError(t, err)
	require.Equal(t, "X1-HOME-A1", loc)
	require.Equal(t, 0, med.navCalls, "an already-orbiting hull at home issues no spurious navigation")
	require.Equal(t, navigation.NavStatusInOrbit, hull.NavStatus())

	_, registered := coordinator.GetStorageShipBySymbol("HULL-STORE-1")
	require.True(t, registered)
}

// A container restart re-runs setup against a durably-FAILED (blocked) operation
// from a prior attempt. When the hull can now be positioned, the operation is
// reset to RUNNING and the warehouse comes up — the transient-failure recovery
// path that keeps a blocked op from being permanently stuck.
func TestRunWarehouse_RestartAfterFailure_ResetsAndRecovers(t *testing.T) {
	ctx := context.Background()
	logger := common.LoggerFromContext(ctx)

	// Pre-seed a FAILED warehouse op, as if a prior park attempt gave up.
	failed, err := storage.NewWarehouseOperation(
		"warehouse-X1-HOME-A1", 1, "X1-HOME-A1", []string{"HULL-STORE-1"}, []string{"IRON_ORE"}, nil,
	)
	require.NoError(t, err)
	require.NoError(t, failed.Start())
	require.NoError(t, failed.Fail(errors.New("prior park failed")))
	require.Equal(t, storage.OperationStatusFailed, failed.Status())

	opRepo := newStubWarehouseOpRepo()
	opRepo.ops["warehouse-X1-HOME-A1"] = failed

	// The hull is now home (docked); setup should orbit + register + un-fail.
	hull := newWarehouseTestHullWithStatus(t, "HULL-STORE-1", "X1-HOME-A1", 120, nil, navigation.NavStatusDocked)
	med := &stubWarehouseMediator{}
	coordinator := storageApp.NewInMemoryStorageCoordinator()
	handler := NewRunWarehouseHandler(med, &stubWarehouseShipRepo{ship: hull}, opRepo, coordinator, nil)

	loc, err := handler.setup(ctx, newWarehouseCmd("HULL-STORE-1", "X1-HOME-A1"), logger)
	require.NoError(t, err)
	require.Equal(t, "X1-HOME-A1", loc)
	require.True(t, opRepo.ops["warehouse-X1-HOME-A1"].IsRunning(), "a recovered warehouse op must be reset to RUNNING")
	require.Equal(t, navigation.NavStatusInOrbit, hull.NavStatus())

	_, registered := coordinator.GetStorageShipBySymbol("HULL-STORE-1")
	require.True(t, registered)
}

package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	storageapp "github.com/andrescamacho/spacetraders-go/internal/application/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// Warehouse-first construction sourcing. These tests pin the NEW PHASE-1.5
// seam in supplyTask: before buying a gate material at market (PHASE 2), the drain
// WITHDRAWS it from an in-system depot warehouse at zero cost, so the depot's stocker
// is the sole buyer→warehouse and the construction buy is only ever the RESIDUAL
// (RULINGS #4, no double-buy). Byte-identical when no warehouse is present.
//
// The finder under test is the SHARED contract StorageInventoryFinder — construction
// reuses it rather than a divergent parallel one — driven over the real in-memory
// storage coordinator so a real withdrawal decrements real warehouse stock.

const warehouseWP = "X1-TEST-WH1" // in testSystem (X1-TEST), distinct from the gate site

// --- fakes ---------------------------------------------------------------

// fakeConstructionOpRepo stubs StorageOperationRepository with only FindByGood, so the
// SHARED StorageInventoryFinder resolves a warehouse operation for the good.
type fakeConstructionOpRepo struct {
	storage.StorageOperationRepository
	ops []*storage.StorageOperation
	err error
}

func (r *fakeConstructionOpRepo) FindByGood(_ context.Context, _ int, good string) ([]*storage.StorageOperation, error) {
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

// invConstructionAPI stubs the APIClient methods the withdrawal seam uses:
// AlignAndTransferCargo reads both hulls' nav (GetShip) and aligns (Orbit/Dock),
// then TransferCargo moves the units. Unset nav defaults to IN_ORBIT (a no-op align).
type invConstructionAPI struct {
	domainPorts.APIClient
	transferCalls int
	lastUnits     int
	transferErr   error
	nav           map[string]string
}

func (a *invConstructionAPI) GetShip(_ context.Context, symbol, _ string) (*navigation.ShipData, error) {
	st, ok := a.nav[symbol]
	if !ok {
		st = string(navigation.NavStatusInOrbit)
	}
	return &navigation.ShipData{Symbol: symbol, NavStatus: st}, nil
}

func (a *invConstructionAPI) OrbitShip(_ context.Context, symbol, _ string) error {
	if a.nav == nil {
		a.nav = map[string]string{}
	}
	a.nav[symbol] = string(navigation.NavStatusInOrbit)
	return nil
}

func (a *invConstructionAPI) DockShip(_ context.Context, symbol, _ string) error {
	if a.nav == nil {
		a.nav = map[string]string{}
	}
	a.nav[symbol] = string(navigation.NavStatusDocked)
	return nil
}

func (a *invConstructionAPI) TransferCargo(_ context.Context, _, _, _ string, units int, _ string) (*domainPorts.TransferResult, error) {
	a.transferCalls++
	a.lastUnits = units
	if a.transferErr != nil {
		return nil, a.transferErr
	}
	return &domainPorts.TransferResult{}, nil
}

// invConstructionNavigator stubs the warehouse-leg navigator: it returns the hull so the
// withdrawal proceeds to the transfer without a live nav.
type invConstructionNavigator struct {
	ship  *navigation.Ship
	calls []string
}

func (n *invConstructionNavigator) NavigateAndDock(_ context.Context, _ string, dest string, _ shared.PlayerID) (*navigation.Ship, error) {
	n.calls = append(n.calls, dest)
	return n.ship, nil
}

func warehouseOpC(t *testing.T, id, waypoint, good string) *storage.StorageOperation {
	t.Helper()
	op, err := storage.NewWarehouseOperation(id, 1, waypoint, []string{"WH-HULL-1"}, []string{good}, nil)
	require.NoError(t, err)
	require.NoError(t, op.Start())
	return op
}

// wiredWarehouse builds a real in-memory coordinator holding `units` of `good` in a
// warehouse hull, plus the SHARED finder over it. Returns the coordinator so a test can
// re-read post-withdrawal availability (state-consistency / drain proofs).
func wiredWarehouse(t *testing.T, good string, units int) (*storageapp.InMemoryStorageCoordinator, *contractServices.StorageInventoryFinder) {
	t.Helper()
	coordinator := storageapp.NewInMemoryStorageCoordinator()
	whShip, err := storage.NewStorageShip("WH-HULL-1", warehouseWP, "wh-gate", 200, map[string]int{good: units})
	require.NoError(t, err)
	require.NoError(t, coordinator.RegisterStorageShip(whShip))
	opRepo := &fakeConstructionOpRepo{ops: []*storage.StorageOperation{warehouseOpC(t, "wh-gate", warehouseWP, good)}}
	return coordinator, contractServices.NewStorageInventoryFinder(opRepo, coordinator)
}

func withToken(ctx context.Context) context.Context {
	return common.WithPlayerToken(ctx, "test-token")
}

// --- tests ---------------------------------------------------------------

// RED-first (core): a gate material sitting in an in-system depot warehouse is WITHDRAWN
// at zero cost — the drain does NOT buy it at market. Today supplyTask always buys.
func TestConstructionSupply_WithdrawsFromWarehouse_SkipsMarketBuy(t *testing.T) {
	const good = "FAB_MATS"
	pipeline := newDrainPipeline(t, good, 40) // one hull-load bill
	task := readyConstructionTask(t, pipeline, good)
	hauler := newTestHauler(t, "HAULER-1", nil) // cap 40, empty

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hauler)
	coordinator, finder := wiredWarehouse(t, good, 40)
	api := &invConstructionAPI{}
	nav := &invConstructionNavigator{ship: hauler}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetInventorySource(finder, coordinator, api, nav)

	drained := handler.supplyTask(withToken(context.Background()), newDrainCommand(), testSystem, constructionLot{task: task, ship: hauler}, shared.MustNewPlayerID(1))

	require.True(t, drained, "the task drained via warehouse withdrawal")
	require.Equal(t, 1, api.transferCalls, "must WITHDRAW the material from the warehouse")
	require.Equal(t, 40, api.lastUnits, "withdraws the full covered bill")
	require.Empty(t, producer.produceGoods, "must NOT buy at market when the warehouse covers the bill (no double-buy, RULINGS #4)")
	require.Len(t, producer.deliverCalls, 1, "delivers the withdrawn units to the site")
	require.Equal(t, 0, coordinator.GetTotalCargoAvailable("wh-gate", good), "the warehouse stock is drained by the committed withdrawal")
}

// Byte-identical regression guard: with the feature wired but NO warehouse holding the
// good, the drain buys at market exactly as today — never withdraws. This is the
// arm-safety property that makes warehouse-first deploy-safe before the reconciler half.
func TestConstructionSupply_NoWarehouse_ByteIdenticalMarketBuy(t *testing.T) {
	const good = "FAB_MATS"
	pipeline := newDrainPipeline(t, good, 40)
	task := readyConstructionTask(t, pipeline, good)
	hauler := newTestHauler(t, "HAULER-1", nil)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hauler)
	// Finder wired, but the op repo holds NO warehouse for the good -> finder returns nil.
	coordinator := storageapp.NewInMemoryStorageCoordinator()
	finder := contractServices.NewStorageInventoryFinder(&fakeConstructionOpRepo{}, coordinator)
	api := &invConstructionAPI{}
	nav := &invConstructionNavigator{ship: hauler}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetInventorySource(finder, coordinator, api, nav)

	drained := handler.supplyTask(withToken(context.Background()), newDrainCommand(), testSystem, constructionLot{task: task, ship: hauler}, shared.MustNewPlayerID(1))

	require.True(t, drained)
	require.Equal(t, 0, api.transferCalls, "no warehouse -> no withdrawal")
	require.Empty(t, nav.calls, "no warehouse -> no warehouse-leg navigation")
	require.Equal(t, []string{good}, producer.produceGoods, "buys at market exactly as today (byte-identical)")
	require.Len(t, producer.deliverCalls, 1)
}

// Feature entirely unwired (no SetInventorySource): the withdrawal seam is inert and the
// drain buys as today — proves the nil-collaborator path is byte-identical.
func TestConstructionSupply_FinderUnwired_ByteIdenticalMarketBuy(t *testing.T) {
	const good = "FAB_MATS"
	pipeline := newDrainPipeline(t, good, 40)
	task := readyConstructionTask(t, pipeline, good)
	hauler := newTestHauler(t, "HAULER-1", nil)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hauler)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	// No SetInventorySource — warehouse-first disabled.

	drained := handler.supplyTask(withToken(context.Background()), newDrainCommand(), testSystem, constructionLot{task: task, ship: hauler}, shared.MustNewPlayerID(1))

	require.True(t, drained)
	require.Equal(t, []string{good}, producer.produceGoods, "unwired -> market buy as today")
	require.Len(t, producer.deliverCalls, 1)
}

// RED-first (partial coverage / no double-buy across ticks): the warehouse holds LESS
// than the bill. Tick 1 withdraws the covered units and does NOT buy; once drained, a
// later tick buys ONLY the residual. The covered units are never bought.
func TestConstructionSupply_PartialWarehouse_BuysOnlyResidual(t *testing.T) {
	const good = "FAB_MATS"
	pipeline := newDrainPipeline(t, good, 100) // bill 100
	hauler := newTestHauler(t, "HAULER-1", nil)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hauler)
	coordinator, finder := wiredWarehouse(t, good, 40) // only 40 of 100 buffered
	api := &invConstructionAPI{}
	nav := &invConstructionNavigator{ship: hauler}

	// Tick 1 uses its own ready task; both ticks share the pipeline + coordinator.
	task1 := readyConstructionTask(t, pipeline, good)
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task1}}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetInventorySource(finder, coordinator, api, nav)

	// Tick 1: withdraw the 40 buffered, deliver, NO buy.
	drained1 := handler.supplyTask(withToken(context.Background()), newDrainCommand(), testSystem, constructionLot{task: task1, ship: hauler}, shared.MustNewPlayerID(1))
	require.True(t, drained1)
	require.Equal(t, 1, api.transferCalls, "tick 1 withdraws the buffered units")
	require.Empty(t, producer.produceGoods, "tick 1 must NOT buy the covered units (no double-buy)")
	require.Equal(t, 0, coordinator.GetTotalCargoAvailable("wh-gate", good), "warehouse drained after tick 1")

	// Tick 2: warehouse now empty -> finder returns nil -> buy ONLY the residual (60 remaining).
	task2 := readyConstructionTask(t, pipeline, good)
	drained2 := handler.supplyTask(withToken(context.Background()), newDrainCommand(), testSystem, constructionLot{task: task2, ship: hauler}, shared.MustNewPlayerID(1))
	require.True(t, drained2)
	require.Equal(t, 1, api.transferCalls, "tick 2 attempts no further withdrawal (warehouse drained)")
	require.Equal(t, []string{good}, producer.produceGoods, "tick 2 buys the residual at market")
}

// RED-first (fail-closed): a warehouse transfer error must fall through to the market buy
// so the gate is never starved, and the reservation must be released (no lost inventory).
func TestConstructionSupply_WarehouseTransferError_FailsOpenToMarket(t *testing.T) {
	const good = "FAB_MATS"
	pipeline := newDrainPipeline(t, good, 40)
	task := readyConstructionTask(t, pipeline, good)
	hauler := newTestHauler(t, "HAULER-1", nil)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hauler)
	coordinator, finder := wiredWarehouse(t, good, 40)
	api := &invConstructionAPI{transferErr: errors.New("api 500: transfer failed")}
	nav := &invConstructionNavigator{ship: hauler}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetInventorySource(finder, coordinator, api, nav)

	drained := handler.supplyTask(withToken(context.Background()), newDrainCommand(), testSystem, constructionLot{task: task, ship: hauler}, shared.MustNewPlayerID(1))

	require.True(t, drained, "the gate is filled via the market fallback despite the warehouse error")
	require.GreaterOrEqual(t, api.transferCalls, 1, "withdrawal was attempted")
	require.Equal(t, []string{good}, producer.produceGoods, "fail-open: buys at market so the gate is never starved (RULINGS #4)")
	require.Equal(t, 40, coordinator.GetTotalCargoAvailable("wh-gate", good), "the failed withdrawal releases its reservation (no lost inventory)")
}

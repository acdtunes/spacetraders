package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- fakes ------------------------------------------------------------------------------------

type fakeWHContainerLister struct {
	models []*persistence.ContainerModel
	err    error
}

func (f *fakeWHContainerLister) ListByStatus(ctx context.Context, status container.ContainerStatus, playerID *int) ([]*persistence.ContainerModel, error) {
	return f.models, f.err
}

type fakeWHExportLocator struct {
	byGood map[string]string // good -> export waypoint; absent/"" => no export market (nil result)
	err    error
}

func (f *fakeWHExportLocator) FindExportMarket(ctx context.Context, good, systemSymbol string, playerID int) (*goodsServices.MarketLocatorResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	wp, ok := f.byGood[good]
	if !ok || wp == "" {
		return nil, nil
	}
	return &goodsServices.MarketLocatorResult{WaypointSymbol: wp}, nil
}

type fakeWHChainPnL struct {
	raw manufacturing.ChainPnLRaw
	err error
}

func (f *fakeWHChainPnL) ReadRealizedPnL(ctx context.Context, playerID int, since time.Time) (manufacturing.ChainPnLRaw, error) {
	return f.raw, f.err
}

type fakeWHShipRepo struct {
	navigation.ShipRepository
	all   []*navigation.Ship
	bySym map[string]*navigation.Ship
	err   error
}

func (r *fakeWHShipRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	return r.all, r.err
}

func (r *fakeWHShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.bySym[symbol], nil
}

type whStartCall struct {
	ship     string
	waypoint string
	goods    []string
	playerID int
}

type fakeWHDispatcher struct {
	started  []whStartCall
	stopped  []string
	startErr error
	stopErr  error
}

func (f *fakeWHDispatcher) StartWarehouse(ctx context.Context, shipSymbol, waypointSymbol string, supportedGoods []string, playerID int) (*WarehouseOperationResult, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.started = append(f.started, whStartCall{ship: shipSymbol, waypoint: waypointSymbol, goods: supportedGoods, playerID: playerID})
	return &WarehouseOperationResult{ContainerID: "warehouse-" + shipSymbol + "-abc", ShipSymbol: shipSymbol, WaypointSymbol: waypointSymbol}, nil
}

func (f *fakeWHDispatcher) StopContainer(containerID string) error {
	if f.stopErr != nil {
		return f.stopErr
	}
	f.stopped = append(f.stopped, containerID)
	return nil
}

// --- fixture helpers --------------------------------------------------------------------------

// standingFactoryModel builds a running standing goods_factory_coordinator container fixture for
// (good, system): iterations=-1 marks it a managed portfolio member.
func standingFactoryModel(id, good, system string) *persistence.ContainerModel {
	cfg, _ := json.Marshal(map[string]interface{}{
		"max_iterations": float64(-1),
		"target_good":    good,
		"system_symbol":  system,
	})
	return &persistence.ContainerModel{ID: id, ContainerType: "goods_factory_coordinator", Config: string(cfg)}
}

// warehouseContainerModel builds a running WAREHOUSE container fixture parking shipSymbol at waypoint.
func warehouseContainerModel(id, shipSymbol, waypoint string) *persistence.ContainerModel {
	cfg, _ := json.Marshal(map[string]interface{}{
		"ship_symbol":     shipSymbol,
		"waypoint_symbol": waypoint,
	})
	return &persistence.ContainerModel{ID: id, ContainerType: string(container.ContainerTypeWarehouse), Config: string(cfg)}
}

// realizedGood is a chain_pnl fixture row with a positive realized factory sell (proven earner).
func realizedGood(good string, factorySell int) manufacturing.ChainGoodFlow {
	return manufacturing.ChainGoodFlow{Good: good, FactorySell: factorySell}
}

func warehouseDedicatedShip(t *testing.T, symbol string, playerID int) *navigation.Ship {
	t.Helper()
	ship := newIdleTradeShip(t, symbol, playerID)
	ship.SetDedicatedFleet("warehouse")
	return ship
}

// --- WarehousePortfolioSource -----------------------------------------------------------------

// A running standing chain joins its in-system export waypoint (the warehouse's home) and its
// realized $/hr from chain_pnl, marked readable — the durable-target row the demand provider ranks.
func TestWarehousePortfolio_JoinsChainWithExportWaypointAndRealizedRate(t *testing.T) {
	lister := &fakeWHContainerLister{models: []*persistence.ContainerModel{
		standingFactoryModel("f1", "CLOTHING", "X1-FD2"),
	}}
	locator := &fakeWHExportLocator{byGood: map[string]string{"CLOTHING": "X1-FD2-A1"}}
	pnl := &fakeWHChainPnL{raw: manufacturing.ChainPnLRaw{Goods: []manufacturing.ChainGoodFlow{realizedGood("CLOTHING", 200000)}}}

	src := newWarehousePortfolioSource(lister, locator, pnl)
	chains, readable, err := src.RunningChains(context.Background(), 1)

	require.NoError(t, err)
	require.True(t, readable, "a readable chain set must not fail the pass closed")
	require.Len(t, chains, 1)
	require.Equal(t, "CLOTHING", chains[0].Good)
	require.Equal(t, "X1-FD2-A1", chains[0].ExportWaypoint)
	require.True(t, chains[0].RealizedReadable)
	require.InDelta(t, 100000.0, chains[0].RealizedPerHour, 0.001) // 200000 over 2h window
}

// RULINGS #4: an unreadable chain set (container-repo error) fails the WHOLE pass closed — no
// demand, no dispatch, no spend — never a partial portfolio.
func TestWarehousePortfolio_ContainerReadErrorFailsWholePassClosed(t *testing.T) {
	lister := &fakeWHContainerLister{err: errors.New("db down")}
	src := newWarehousePortfolioSource(lister, &fakeWHExportLocator{}, &fakeWHChainPnL{})
	chains, readable, err := src.RunningChains(context.Background(), 1)

	require.Error(t, err)
	require.False(t, readable)
	require.Empty(t, chains)
}

// A chain whose good has no in-system export market cannot host a warehouse, so it is dropped from
// the portfolio (not a whole-pass failure) — the rest of the portfolio stands.
func TestWarehousePortfolio_ChainWithNoExportMarketIsSkipped(t *testing.T) {
	lister := &fakeWHContainerLister{models: []*persistence.ContainerModel{
		standingFactoryModel("f1", "CLOTHING", "X1-FD2"),
		standingFactoryModel("f2", "FABRICS", "X1-FD2"),
	}}
	locator := &fakeWHExportLocator{byGood: map[string]string{"CLOTHING": "X1-FD2-A1"}} // FABRICS absent
	pnl := &fakeWHChainPnL{raw: manufacturing.ChainPnLRaw{Goods: []manufacturing.ChainGoodFlow{
		realizedGood("CLOTHING", 200000), realizedGood("FABRICS", 50000),
	}}}

	src := newWarehousePortfolioSource(lister, locator, pnl)
	chains, readable, err := src.RunningChains(context.Background(), 1)

	require.NoError(t, err)
	require.True(t, readable)
	require.Len(t, chains, 1)
	require.Equal(t, "CLOTHING", chains[0].Good)
}

// A chain with no realized P&L row (pre-realization / unread rate) is carried with
// RealizedReadable=false so the demand provider fails IT closed on the pay gate — an unproven
// earner never pulls a warehouse.
func TestWarehousePortfolio_UnreadableRealizedRateMarksChainUnproven(t *testing.T) {
	lister := &fakeWHContainerLister{models: []*persistence.ContainerModel{
		standingFactoryModel("f1", "CLOTHING", "X1-FD2"),
	}}
	locator := &fakeWHExportLocator{byGood: map[string]string{"CLOTHING": "X1-FD2-A1"}}
	pnl := &fakeWHChainPnL{raw: manufacturing.ChainPnLRaw{Goods: nil}} // no row for CLOTHING

	src := newWarehousePortfolioSource(lister, locator, pnl)
	chains, readable, err := src.RunningChains(context.Background(), 1)

	require.NoError(t, err)
	require.True(t, readable)
	require.Len(t, chains, 1)
	require.Equal(t, "X1-FD2-A1", chains[0].ExportWaypoint)
	require.False(t, chains[0].RealizedReadable, "no P&L row => unproven => fails the pay gate closed")
}

// One-shot factory runs (iterations != -1) and non-factory containers are not portfolio members.
func TestWarehousePortfolio_IgnoresNonStandingAndNonFactory(t *testing.T) {
	oneShot := standingFactoryModel("f-oneshot", "CLOTHING", "X1-FD2")
	cfg, _ := json.Marshal(map[string]interface{}{"max_iterations": float64(1), "target_good": "CLOTHING", "system_symbol": "X1-FD2"})
	oneShot.Config = string(cfg)
	other := &persistence.ContainerModel{ID: "gas1", ContainerType: "gas_coordinator", Config: "{}"}

	lister := &fakeWHContainerLister{models: []*persistence.ContainerModel{oneShot, other}}
	src := newWarehousePortfolioSource(lister, &fakeWHExportLocator{byGood: map[string]string{"CLOTHING": "X1-FD2-A1"}}, &fakeWHChainPnL{})
	chains, readable, err := src.RunningChains(context.Background(), 1)

	require.NoError(t, err)
	require.True(t, readable)
	require.Empty(t, chains)
}

// --- WarehouseHullSource ----------------------------------------------------------------------

// A warehouse-dedicated hull running a WAREHOUSE container reports the container's waypoint as its
// parked location (the coverage signal the dispatch step reads).
func TestWarehouseHulls_ParkedWaypointFromRunningContainer(t *testing.T) {
	ship := warehouseDedicatedShip(t, "WH-1", 1)
	shipRepo := &fakeWHShipRepo{all: []*navigation.Ship{ship}}
	lister := &fakeWHContainerLister{models: []*persistence.ContainerModel{
		warehouseContainerModel("warehouse-WH-1-abc", "WH-1", "X1-FD2-A1"),
	}}

	src := newWarehouseHullSource(lister, shipRepo)
	hulls, err := src.WarehouseHulls(context.Background(), 1)

	require.NoError(t, err)
	require.Len(t, hulls, 1)
	require.Equal(t, "WH-1", hulls[0].ShipSymbol)
	require.Equal(t, "X1-FD2-A1", hulls[0].ParkedWaypoint)
}

// A warehouse-dedicated hull with no running warehouse container is unplaced (ParkedWaypoint="") —
// part of the pool, available for the dispatch step to place.
func TestWarehouseHulls_IdleDedicatedHullIsUnplaced(t *testing.T) {
	ship := warehouseDedicatedShip(t, "WH-2", 1)
	shipRepo := &fakeWHShipRepo{all: []*navigation.Ship{ship}}
	lister := &fakeWHContainerLister{models: nil}

	src := newWarehouseHullSource(lister, shipRepo)
	hulls, err := src.WarehouseHulls(context.Background(), 1)

	require.NoError(t, err)
	require.Len(t, hulls, 1)
	require.Equal(t, "WH-2", hulls[0].ShipSymbol)
	require.Equal(t, "", hulls[0].ParkedWaypoint)
}

// Only warehouse-dedicated hulls count: a trade-dedicated hull and an undedicated hull are excluded.
func TestWarehouseHulls_ExcludesNonWarehouseHulls(t *testing.T) {
	wh := warehouseDedicatedShip(t, "WH-1", 1)
	trade := newIdleTradeShip(t, "TR-1", 1)
	trade.SetDedicatedFleet("trade")
	plain := newIdleTradeShip(t, "PL-1", 1)

	shipRepo := &fakeWHShipRepo{all: []*navigation.Ship{wh, trade, plain}}
	src := newWarehouseHullSource(&fakeWHContainerLister{}, shipRepo)
	hulls, err := src.WarehouseHulls(context.Background(), 1)

	require.NoError(t, err)
	require.Len(t, hulls, 1)
	require.Equal(t, "WH-1", hulls[0].ShipSymbol)
}

// A ship-repo error fails the buy path closed (the pool is unknowable).
func TestWarehouseHulls_ShipRepoErrorFailsClosed(t *testing.T) {
	shipRepo := &fakeWHShipRepo{err: errors.New("db down")}
	src := newWarehouseHullSource(&fakeWHContainerLister{}, shipRepo)
	_, err := src.WarehouseHulls(context.Background(), 1)
	require.Error(t, err)
}

// --- WarehouseDispatchBridge ------------------------------------------------------------------

// An idle warehouse hull is placed by starting a warehouse at the durable export waypoint carrying
// the co-exported goods list.
func TestWarehouseDispatch_IdleHullStartsWarehouse(t *testing.T) {
	ship := warehouseDedicatedShip(t, "WH-1", 1) // idle: no container assignment
	shipRepo := &fakeWHShipRepo{bySym: map[string]*navigation.Ship{"WH-1": ship}}
	disp := &fakeWHDispatcher{}

	bridge := newWarehouseDispatchBridge(disp, shipRepo)
	err := bridge.DispatchWarehouse(context.Background(), 1, "WH-1", "X1-FD2-A1", []string{"CLOTHING", "FABRICS"})

	require.NoError(t, err)
	require.Len(t, disp.started, 1)
	require.Equal(t, "WH-1", disp.started[0].ship)
	require.Equal(t, "X1-FD2-A1", disp.started[0].waypoint)
	require.Equal(t, []string{"CLOTHING", "FABRICS"}, disp.started[0].goods)
	require.Empty(t, disp.stopped, "an idle-hull placement must not stop anything")
}

// A hull stranded on a retired chain (holding a warehouse container elsewhere) is un-stranded by
// stopping its container — freeing it for next-tick re-placement (StartWarehouse takes only idle
// hulls). No new warehouse is started this tick.
func TestWarehouseDispatch_StrandedHullStopsItsContainer(t *testing.T) {
	ship := warehouseDedicatedShip(t, "WH-1", 1)
	require.NoError(t, ship.AssignToContainer("warehouse-WH-1-old", shared.NewRealClock()))
	shipRepo := &fakeWHShipRepo{bySym: map[string]*navigation.Ship{"WH-1": ship}}
	disp := &fakeWHDispatcher{}

	bridge := newWarehouseDispatchBridge(disp, shipRepo)
	err := bridge.DispatchWarehouse(context.Background(), 1, "WH-1", "X1-NEW-A1", []string{"CLOTHING"})

	require.NoError(t, err)
	require.Equal(t, []string{"warehouse-WH-1-old"}, disp.stopped)
	require.Empty(t, disp.started, "the stranded hull is freed this tick, placed next tick")
}

// A warehouse-dedicated hull busy on a NON-warehouse container is not disturbed — refuse loudly
// rather than stop another coordinator's operation.
func TestWarehouseDispatch_RefusesToDisturbNonWarehouseBusyHull(t *testing.T) {
	ship := warehouseDedicatedShip(t, "WH-1", 1)
	require.NoError(t, ship.AssignToContainer("gas_coordinator-OTHER", shared.NewRealClock()))
	shipRepo := &fakeWHShipRepo{bySym: map[string]*navigation.Ship{"WH-1": ship}}
	disp := &fakeWHDispatcher{}

	bridge := newWarehouseDispatchBridge(disp, shipRepo)
	err := bridge.DispatchWarehouse(context.Background(), 1, "WH-1", "X1-NEW-A1", []string{"CLOTHING"})

	require.Error(t, err)
	require.Empty(t, disp.started)
	require.Empty(t, disp.stopped)
}

// A missing hull is a hard error, never a silent no-op (dispatch feeds a placement path).
func TestWarehouseDispatch_ShipNotFoundErrors(t *testing.T) {
	shipRepo := &fakeWHShipRepo{bySym: map[string]*navigation.Ship{}}
	bridge := newWarehouseDispatchBridge(&fakeWHDispatcher{}, shipRepo)
	err := bridge.DispatchWarehouse(context.Background(), 1, "GHOST", "X1-NEW-A1", []string{"CLOTHING"})
	require.Error(t, err)
}

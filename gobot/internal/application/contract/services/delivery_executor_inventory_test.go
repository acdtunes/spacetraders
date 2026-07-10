package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// --- fakes ---------------------------------------------------------------

// invFakeFinder returns a fixed InventorySource (or nil for "no stock").
type invFakeFinder struct {
	src   *appContract.InventorySource
	calls int
}

func (f *invFakeFinder) FindInSystemInventory(_ context.Context, _ int, _, _ string) *appContract.InventorySource {
	f.calls++
	return f.src
}

// invFakeCoordinator stubs StorageCoordinator with GetStorageShipsForOperation.
type invFakeCoordinator struct {
	storage.StorageCoordinator
	ships map[string][]*storage.StorageShip
}

func (c *invFakeCoordinator) GetStorageShipsForOperation(operationID string) []*storage.StorageShip {
	return c.ships[operationID]
}

// invFakeAPI stubs the APIClient methods the withdrawal seam uses: TransferCargo
// (recording the call and flipping the shared transferred flag) plus the nav-state
// trio (GetShip/OrbitShip/DockShip) that AlignAndTransferCargo calls to align the
// visitor to the warehouse hull before the transfer (sp-5qs1). Unset nav defaults to
// IN_ORBIT, so a test that does not care about alignment sees a no-op align.
type invFakeAPI struct {
	domainPorts.APIClient
	transferCalls int
	lastUnits     int
	transferErr   error
	moved         *bool
	nav           map[string]string // symbol -> nav status; empty => IN_ORBIT
	orbitCalls    []string
	dockCalls     []string
}

func (a *invFakeAPI) GetShip(_ context.Context, symbol, _ string) (*navigation.ShipData, error) {
	st, ok := a.nav[symbol]
	if !ok {
		st = string(navigation.NavStatusInOrbit)
	}
	return &navigation.ShipData{Symbol: symbol, NavStatus: st}, nil
}

func (a *invFakeAPI) OrbitShip(_ context.Context, symbol, _ string) error {
	a.orbitCalls = append(a.orbitCalls, symbol)
	if a.nav == nil {
		a.nav = map[string]string{}
	}
	a.nav[symbol] = string(navigation.NavStatusInOrbit)
	return nil
}

func (a *invFakeAPI) DockShip(_ context.Context, symbol, _ string) error {
	a.dockCalls = append(a.dockCalls, symbol)
	if a.nav == nil {
		a.nav = map[string]string{}
	}
	a.nav[symbol] = string(navigation.NavStatusDocked)
	return nil
}

func (a *invFakeAPI) TransferCargo(_ context.Context, _, _, _ string, units int, _ string) (*domainPorts.TransferResult, error) {
	a.transferCalls++
	a.lastUnits = units
	if a.transferErr != nil {
		return nil, a.transferErr
	}
	if a.moved != nil {
		*a.moved = true
	}
	return &domainPorts.TransferResult{}, nil
}

// invFakeMediator drives navigate/orbit/dock plus deliver and (asserting it is
// never used for a stocked good) purchase.
type invFakeMediator struct {
	common.Mediator
	navShip         *navigation.Ship
	deliverContract *domainContract.Contract
	purchaseCalls   int
	deliverCalls    int
}

func (m *invFakeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	switch request.(type) {
	case *shipNav.NavigateRouteCommand:
		return &shipNav.NavigateRouteResponse{Status: "completed", Ship: m.navShip}, nil
	case *shipTypes.OrbitShipCommand:
		return nil, nil
	case *shipTypes.DockShipCommand:
		return nil, nil
	case *DeliverContractCommand:
		m.deliverCalls++
		return &DeliverContractResponse{Contract: m.deliverContract}, nil
	case *shipCargo.PurchaseCargoCommand:
		m.purchaseCalls++
		return &shipCargo.PurchaseCargoResponse{}, nil
	default:
		return nil, fmt.Errorf("unexpected mediator command in inventory test: %T", request)
	}
}

// movingShipRepo reports the hauler as empty until the cargo has been
// transferred (invFakeAPI flips moved), then as loaded — modeling the physical
// withdrawal without a live API.
type movingShipRepo struct {
	navigation.ShipRepository
	empty, loaded *navigation.Ship
	moved         *bool
}

func (r *movingShipRepo) current() *navigation.Ship {
	if r.moved != nil && *r.moved {
		return r.loaded
	}
	return r.empty
}
func (r *movingShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.current(), nil
}
func (r *movingShipRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.current(), nil
}

func storageShipWith(t *testing.T, units int) *storage.StorageShip {
	t.Helper()
	seed := map[string]int{}
	if units > 0 {
		seed["IRON_ORE"] = units
	}
	s, err := storage.NewStorageShip("WH-HULL-1", "X1-HOME-WH9", "wh-1", 1000, seed)
	require.NoError(t, err)
	return s
}

func ironDelivery(required int) domainContract.Delivery {
	return domainContract.Delivery{
		TradeSymbol:       "IRON_ORE",
		DestinationSymbol: "X1-HOME-D39",
		UnitsRequired:     required,
		UnitsFulfilled:    0,
	}
}

func infoContains(l *capturingLogger, want string) bool {
	for _, e := range l.entries {
		if e.level == "INFO" && strings.Contains(e.message, want) {
			return true
		}
	}
	return false
}

// --- withdrawal mechanics (trySourceFromInventory) -----------------------

func TestTrySourceFromInventory_CappedWithdrawal_TransfersNeedReleasesExcess(t *testing.T) {
	hauler := buildShipWithIronOre(t, 0) // capacity 40, empty
	storageShip := storageShipWith(t, 200)
	shipRepo := &reconcileFakeShipRepo{cached: hauler, server: hauler}
	med := &invFakeMediator{navShip: hauler}
	api := &invFakeAPI{}
	finder := &invFakeFinder{src: &appContract.InventorySource{OperationID: "wh-1", StorageWaypoint: "X1-HOME-WH9", UnitsAvailable: 200}}
	coord := &invFakeCoordinator{ships: map[string][]*storage.StorageShip{"wh-1": {storageShip}}}
	executor := NewDeliveryExecutor(med, shipRepo, NewCargoManager(med, shipRepo), WithInventorySource(finder, coord, api))

	logger := &capturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "tok"), logger)
	profit := &contractQueries.ProfitabilityResult{MarketPrices: map[string]int{"IRON_ORE": 100}}

	// Contract needs only 10 units; the hold has room for 40; the warehouse holds 200.
	withdrew, _, err := executor.trySourceFromInventory(ctx, "TORWIND-1", shared.MustNewPlayerID(1), hauler, ironDelivery(10), 10, profit)

	require.NoError(t, err)
	require.True(t, withdrew)
	require.Equal(t, 1, api.transferCalls)
	require.Equal(t, 10, api.lastUnits, "take is capped to the 10 units the contract needs, not the 200 on hand")
	require.Equal(t, 190, storageShip.GetAvailableCargo("IRON_ORE"), "10 moved, the other 190 reservation released for other workers")
	require.True(t, infoContains(logger, "Sourced 10 IRON_ORE from warehouse"), "honest withdrawal log")
	require.True(t, infoContains(logger, "market would have cost 1000"), "realized savings = market ask 100 × 10 units")
}

func TestTrySourceFromInventory_WarehouseDocked_DocksVisitorBeforeTransfer(t *testing.T) {
	// The withdrawal seam must align the visitor to the warehouse's nav state, not
	// assume orbit (sp-5qs1). A DOCKED warehouse hull docks the visitor before the
	// transfer so the first-ever withdrawal cannot 4271.
	hauler := buildShipWithIronOre(t, 0)
	storageShip := storageShipWith(t, 200)
	shipRepo := &reconcileFakeShipRepo{cached: hauler, server: hauler}
	med := &invFakeMediator{navShip: hauler}
	api := &invFakeAPI{nav: map[string]string{
		"WH-HULL-1": string(navigation.NavStatusDocked),
		"TORWIND-1": string(navigation.NavStatusInOrbit),
	}}
	finder := &invFakeFinder{src: &appContract.InventorySource{OperationID: "wh-1", StorageWaypoint: "X1-HOME-WH9", UnitsAvailable: 200}}
	coord := &invFakeCoordinator{ships: map[string][]*storage.StorageShip{"wh-1": {storageShip}}}
	executor := NewDeliveryExecutor(med, shipRepo, NewCargoManager(med, shipRepo), WithInventorySource(finder, coord, api))
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "tok"), &capturingLogger{})

	withdrew, _, err := executor.trySourceFromInventory(ctx, "TORWIND-1", shared.MustNewPlayerID(1), hauler, ironDelivery(10), 10, nil)

	require.NoError(t, err)
	require.True(t, withdrew)
	require.Equal(t, []string{"TORWIND-1"}, api.dockCalls, "visitor docked to match the docked warehouse hull, not the warehouse moved")
	require.Equal(t, 1, api.transferCalls)
}

func TestTrySourceFromInventory_DrainedMidFlight_FallsThroughToMarket(t *testing.T) {
	hauler := buildShipWithIronOre(t, 0)
	drained := storageShipWith(t, 0) // finder snapshot was stale; nothing left
	shipRepo := &reconcileFakeShipRepo{cached: hauler, server: hauler}
	med := &invFakeMediator{navShip: hauler}
	api := &invFakeAPI{}
	finder := &invFakeFinder{src: &appContract.InventorySource{OperationID: "wh-1", StorageWaypoint: "X1-HOME-WH9", UnitsAvailable: 200}}
	coord := &invFakeCoordinator{ships: map[string][]*storage.StorageShip{"wh-1": {drained}}}
	executor := NewDeliveryExecutor(med, shipRepo, NewCargoManager(med, shipRepo), WithInventorySource(finder, coord, api))
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "tok"), &capturingLogger{})

	withdrew, _, err := executor.trySourceFromInventory(ctx, "TORWIND-1", shared.MustNewPlayerID(1), hauler, ironDelivery(10), 10, nil)

	require.NoError(t, err, "a drained warehouse is not an error — it is a fall-through")
	require.False(t, withdrew)
	require.Equal(t, 0, api.transferCalls, "no transfer when there is nothing to reserve")
}

func TestTrySourceFromInventory_TransferError_FailsOpenAndReleasesReservation(t *testing.T) {
	hauler := buildShipWithIronOre(t, 0)
	storageShip := storageShipWith(t, 200)
	shipRepo := &reconcileFakeShipRepo{cached: hauler, server: hauler}
	med := &invFakeMediator{navShip: hauler}
	api := &invFakeAPI{transferErr: errors.New("boom")}
	finder := &invFakeFinder{src: &appContract.InventorySource{OperationID: "wh-1", StorageWaypoint: "X1-HOME-WH9", UnitsAvailable: 200}}
	coord := &invFakeCoordinator{ships: map[string][]*storage.StorageShip{"wh-1": {storageShip}}}
	executor := NewDeliveryExecutor(med, shipRepo, NewCargoManager(med, shipRepo), WithInventorySource(finder, coord, api))
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "tok"), &capturingLogger{})

	withdrew, _, err := executor.trySourceFromInventory(ctx, "TORWIND-1", shared.MustNewPlayerID(1), hauler, ironDelivery(10), 10, nil)

	require.Error(t, err, "a transfer failure surfaces so the caller logs it and falls through")
	require.False(t, withdrew)
	require.Equal(t, 200, storageShip.GetAvailableCargo("IRON_ORE"), "reservation fully released after a failed transfer")
}

func TestTrySourceFromInventory_NotWired_UsesMarketPath(t *testing.T) {
	hauler := buildShipWithIronOre(t, 0)
	shipRepo := &reconcileFakeShipRepo{cached: hauler, server: hauler}
	med := &invFakeMediator{navShip: hauler}
	executor := NewDeliveryExecutor(med, shipRepo, NewCargoManager(med, shipRepo)) // no inventory option
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "tok"), &capturingLogger{})

	withdrew, _, err := executor.trySourceFromInventory(ctx, "TORWIND-1", shared.MustNewPlayerID(1), hauler, ironDelivery(10), 10, nil)

	require.NoError(t, err)
	require.False(t, withdrew, "with no finder wired the executor uses the market path")
}

func TestReserveFromWarehouse_SecondReserverSeesUnitsGone(t *testing.T) {
	// The per-storage-ship reservation is atomic (TryReserveCargo holds the ship
	// mutex): the first reserver takes the units, a racing second sees none — no
	// double-claim (the sp-dchv Lane D reservation acceptance).
	storageShip := storageShipWith(t, 200)
	coord := &invFakeCoordinator{ships: map[string][]*storage.StorageShip{"wh-1": {storageShip}}}
	executor := NewDeliveryExecutor(nil, nil, nil, WithInventorySource(&invFakeFinder{}, coord, &invFakeAPI{}))

	s1, r1 := executor.reserveFromWarehouse("wh-1", "IRON_ORE")
	require.NotNil(t, s1)
	require.Equal(t, 200, r1)

	s2, r2 := executor.reserveFromWarehouse("wh-1", "IRON_ORE")
	require.Nil(t, s2, "all units already reserved by the first worker")
	require.Equal(t, 0, r2)
}

// --- full source-trip -> deliver flow (ProcessSingleDelivery) ------------

func TestProcessSingleDelivery_SourcesFromInventory_NoMarketBuy(t *testing.T) {
	moved := false
	empty := buildShipWithIronOre(t, 0)   // capacity 40, empty before withdrawal
	loaded := buildShipWithIronOre(t, 40) // 40 IRON_ORE aboard after withdrawal
	storageShip := storageShipWith(t, 40)

	shipRepo := &movingShipRepo{empty: empty, loaded: loaded, moved: &moved}
	api := &invFakeAPI{moved: &moved}
	med := &invFakeMediator{navShip: loaded, deliverContract: contractWithFulfilled(t, 40, 40)}
	finder := &invFakeFinder{src: &appContract.InventorySource{OperationID: "wh-1", StorageWaypoint: "X1-HOME-WH9", UnitsAvailable: 40}}
	coord := &invFakeCoordinator{ships: map[string][]*storage.StorageShip{"wh-1": {storageShip}}}
	executor := NewDeliveryExecutor(med, shipRepo, NewCargoManager(med, shipRepo), WithInventorySource(finder, coord, api))

	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "tok"), &capturingLogger{})
	profit := &contractQueries.ProfitabilityResult{MarketPrices: map[string]int{"IRON_ORE": 100}, CheapestMarketWaypoint: "X1-MARKET-M1"}
	initial := contractWithFulfilled(t, 40, 0)

	got, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), initial, ironDelivery(40), profit, &RunWorkflowResponse{}, nil)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 0, med.purchaseCalls, "a fully-stocked good must never touch the market")
	require.Equal(t, 1, api.transferCalls, "withdrawn once from the warehouse")
	require.Equal(t, 1, med.deliverCalls, "then delivered to the contract")
	require.Equal(t, 0, storageShip.GetAvailableCargo("IRON_ORE"), "warehouse decremented by the 40 withdrawn")
}

// contractWithFulfilled builds a single-delivery IRON_ORE PROCUREMENT contract
// with the delivery at fulfilled/required, for driving the deliver loop.
func contractWithFulfilled(t *testing.T, required, fulfilled int) *domainContract.Contract {
	t.Helper()
	terms := domainContract.Terms{
		Payment: domainContract.Payment{OnAccepted: 50_000, OnFulfilled: 50_000},
		Deliveries: []domainContract.Delivery{{
			TradeSymbol:       "IRON_ORE",
			DestinationSymbol: "X1-HOME-D39",
			UnitsRequired:     required,
			UnitsFulfilled:    fulfilled,
		}},
		Deadline: "2026-07-20T00:00:00Z",
	}
	c, err := domainContract.NewContract("ct-inv", shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT", terms, nil)
	require.NoError(t, err)
	return c
}

package commands

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	testSystem          = "X1-TEST"
	testFactoryWaypoint = "X1-TEST-FACTORY"
	testIronWaypoint    = "X1-TEST-IRONMKT"
	testOutputGood      = "FAB_PLATE"
	testInputGood       = "IRON"
	testContainerID     = "goods_factory-FAB_PLATE-test1"
)

// factoryFakeMediator records purchase/sell dispatches (and the context each sell
// was sent with) and answers every other command with a no-op success. Any
// behavioral assertion happens in the tests, at this driven-port boundary.
type factoryFakeMediator struct {
	mu           sync.Mutex
	purchases    []*shipCargo.PurchaseCargoCommand
	sells        []*shipCargo.SellCargoCommand
	sellContexts []context.Context
}

func (m *factoryFakeMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.purchases = append(m.purchases, cmd)
		return &shipCargo.PurchaseCargoResponse{
			TotalCost:        cmd.Units * 10,
			UnitsAdded:       cmd.Units,
			TransactionCount: 1,
		}, nil
	case *shipCargo.SellCargoCommand:
		m.sells = append(m.sells, cmd)
		m.sellContexts = append(m.sellContexts, ctx)
		return &shipCargo.SellCargoResponse{
			TotalRevenue:     cmd.Units * 8,
			UnitsSold:        cmd.Units,
			TransactionCount: 1,
		}, nil
	default:
		// Navigation, docking, etc. succeed silently.
		return nil, nil
	}
}

func (m *factoryFakeMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}

func (m *factoryFakeMediator) RegisterMiddleware(middleware common.Middleware) {}

func (m *factoryFakeMediator) purchasedUnitsOf(good string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, p := range m.purchases {
		if p.GoodSymbol == good {
			total += p.Units
		}
	}
	return total
}

// factoryFakeShipRepo embeds the interface so unimplemented methods panic,
// keeping the fake honest about what the coordinator actually uses.
type factoryFakeShipRepo struct {
	navigation.ShipRepository
	mu    sync.Mutex
	ships map[string]*navigation.Ship
	order []string
}

func (r *factoryFakeShipRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ships := make([]*navigation.Ship, 0, len(r.order))
	for _, symbol := range r.order {
		ships = append(ships, r.ships[symbol])
	}
	return ships, nil
}

func (r *factoryFakeShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ships[symbol], nil
}

func (r *factoryFakeShipRepo) Save(ctx context.Context, ship *navigation.Ship) error {
	return nil
}

func (r *factoryFakeShipRepo) FindByContainer(ctx context.Context, containerID string, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	return nil, nil
}

// factoryFakeMarketRepo serves a two-market system: a factory exporting
// FAB_PLATE (fed by IRON) and a raw market selling IRON.
type factoryFakeMarketRepo struct {
	market.MarketRepository
}

func (r *factoryFakeMarketRepo) FindFactoryForGood(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.FactoryResult, error) {
	if goodSymbol == testOutputGood {
		return &market.FactoryResult{
			WaypointSymbol: testFactoryWaypoint,
			TradeSymbol:    goodSymbol,
			SellPrice:      100,
			Supply:         "MODERATE",
			Activity:       "GROWING",
		}, nil
	}
	return nil, nil
}

func (r *factoryFakeMarketRepo) FindBestMarketForBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.BestBuyingMarketResult, error) {
	if goodSymbol == testInputGood {
		return &market.BestBuyingMarketResult{
			WaypointSymbol: testIronWaypoint,
			TradeSymbol:    goodSymbol,
			SellPrice:      10,
			Supply:         "HIGH",
			Activity:       "STRONG",
			TradeType:      market.TradeTypeExport,
		}, nil
	}
	return nil, nil
}

func (r *factoryFakeMarketRepo) FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.CheapestMarketResult, error) {
	switch goodSymbol {
	case testOutputGood:
		return &market.CheapestMarketResult{
			WaypointSymbol: testFactoryWaypoint,
			TradeSymbol:    goodSymbol,
			SellPrice:      100,
			Supply:         "MODERATE",
		}, nil
	case testInputGood:
		return &market.CheapestMarketResult{
			WaypointSymbol: testIronWaypoint,
			TradeSymbol:    goodSymbol,
			SellPrice:      10,
			Supply:         "HIGH",
		}, nil
	}
	return nil, nil
}

func (r *factoryFakeMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "GROWING"
	switch waypointSymbol {
	case testFactoryWaypoint:
		output, err := market.NewTradeGood(testOutputGood, &supply, &activity, 80, 100, 20, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*output}, time.Now())
	case testIronWaypoint:
		input, err := market.NewTradeGood(testInputGood, &supply, &activity, 8, 10, 10, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*input}, time.Now())
	}
	return nil, nil
}

type factoryFakeClock struct{}

func (c *factoryFakeClock) Now() time.Time        { return time.Now() }
func (c *factoryFakeClock) Sleep(d time.Duration) {}

func newTestHauler(t *testing.T, symbol string, inventory []*shared.CargoItem) *navigation.Ship {
	t.Helper()

	units := 0
	for _, item := range inventory {
		units += item.Units
	}
	cargo, err := shared.NewCargo(40, units, inventory)
	if err != nil {
		t.Fatalf("failed to build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("failed to build fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(testFactoryWaypoint, 0, 0)
	if err != nil {
		t.Fatalf("failed to build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		waypoint,
		fuel,
		100,
		40,
		cargo,
		30,
		"FRAME_LIGHT_FREIGHTER",
		"HAULER",
		nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("failed to build ship: %v", err)
	}
	return ship
}

// runFactoryCoordinator drives the coordinator through its driving port with a
// FAB_PLATE <- IRON supply chain. The first hauler already carries IRON, so the
// level-0 worker delivers (sells) it to the factory; the level-1 worker then
// fabricates FAB_PLATE at the factory.
func runFactoryCoordinator(t *testing.T) (*factoryFakeMediator, *RunFactoryCoordinatorResponse, error) {
	t.Helper()

	ironItem, err := shared.NewCargoItem(testInputGood, testInputGood, "", 10)
	if err != nil {
		t.Fatalf("failed to build cargo item: %v", err)
	}
	shipWithIron := newTestHauler(t, "CRAFTY-2", []*shared.CargoItem{ironItem})
	emptyShip := newTestHauler(t, "CRAFTY-3", nil)

	shipRepo := &factoryFakeShipRepo{
		ships: map[string]*navigation.Ship{
			shipWithIron.ShipSymbol(): shipWithIron,
			emptyShip.ShipSymbol():    emptyShip,
		},
		order: []string{shipWithIron.ShipSymbol(), emptyShip.ShipSymbol()},
	}
	marketRepo := &factoryFakeMarketRepo{}
	fakeMediator := &factoryFakeMediator{}
	clock := &factoryFakeClock{}

	resolver := mfgServices.NewSupplyChainResolver(
		map[string][]string{testOutputGood: {testInputGood}},
		marketRepo,
	)
	marketLocator := mfgServices.NewMarketLocator(marketRepo, nil, nil, nil)

	handler := NewRunFactoryCoordinatorHandler(
		fakeMediator,
		shipRepo,
		marketRepo,
		resolver,
		marketLocator,
		clock,
	)

	cmd := &RunFactoryCoordinatorCommand{
		PlayerID:     1,
		TargetGood:   testOutputGood,
		SystemSymbol: testSystem,
		ContainerID:  testContainerID,
	}

	resp, err := handler.Handle(context.Background(), cmd)
	coordResp, _ := resp.(*RunFactoryCoordinatorResponse)
	return fakeMediator, coordResp, err
}

// Ledger linkage (#24): every factory delivery sale must be dispatched with the
// factory operation context so the sell handler links the ledger transaction to
// the factory container.
func TestFactoryCoordinator_DeliverySale_CarriesFactoryOperationContext(t *testing.T) {
	fakeMediator, _, err := runFactoryCoordinator(t)
	if err != nil {
		t.Fatalf("coordinator failed: %v", err)
	}

	if len(fakeMediator.sells) == 0 {
		t.Fatal("expected at least one delivery sale, got none")
	}
	for i, sellCtx := range fakeMediator.sellContexts {
		opCtx := shared.OperationContextFromContext(sellCtx)
		if !opCtx.IsValid() {
			t.Fatalf("sale %d (%s) dispatched without a valid operation context - ledger transaction will not be linked to the factory operation", i, fakeMediator.sells[i].GoodSymbol)
		}
		if opCtx.ContainerID != testContainerID {
			t.Fatalf("sale %d linked to container %q, want %q", i, opCtx.ContainerID, testContainerID)
		}
	}
}

// Double purchasing (#25): once a child worker has bought IRON and delivered it
// to the factory, the fabrication worker must not buy IRON again - it only
// polls the factory and purchases the fabricated output.
func TestFactoryCoordinator_ParallelFabrication_DoesNotRepurchaseDeliveredInputs(t *testing.T) {
	fakeMediator, resp, err := runFactoryCoordinator(t)
	if err != nil {
		t.Fatalf("coordinator failed: %v", err)
	}
	if resp == nil || !resp.Completed {
		t.Fatalf("expected completed coordinator run, got %+v", resp)
	}

	if units := fakeMediator.purchasedUnitsOf(testInputGood); units != 0 {
		t.Fatalf("fabrication worker re-purchased %d units of %s that the child worker already delivered to the factory - double spend", units, testInputGood)
	}
	if units := fakeMediator.purchasedUnitsOf(testOutputGood); units == 0 {
		t.Fatalf("expected the fabrication worker to purchase the fabricated %s output, got no purchase", testOutputGood)
	}
}

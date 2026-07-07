package commands

import (
	"context"
	"errors"
	"reflect"
	"strings"
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
//
// The gate fields model a fleet whose haulers are momentarily all busy: while
// gated, FindAllByPlayer reports an empty fleet, so FindIdleLightHaulers finds
// zero idle haulers. This drives the wait-for-idle-gap path (sp-vmrj) without
// constructing assigned ships.
type factoryFakeShipRepo struct {
	navigation.ShipRepository
	mu    sync.Mutex
	ships map[string]*navigation.Ship
	order []string

	findAllCalls   int              // number of FindAllByPlayer calls so far
	emptyUntilCall int              // report an empty fleet while findAllCalls <= this
	alwaysEmpty    bool             // report an empty fleet on every call
	onFindAll      func(callNum int) // hook fired on each call (e.g. to cancel ctx)
}

func (r *factoryFakeShipRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findAllCalls++
	if r.onFindAll != nil {
		r.onFindAll(r.findAllCalls)
	}
	if r.alwaysEmpty || r.findAllCalls <= r.emptyUntilCall {
		return []*navigation.Ship{}, nil
	}
	ships := make([]*navigation.Ship, 0, len(r.order))
	for _, symbol := range r.order {
		ships = append(ships, r.ships[symbol])
	}
	return ships, nil
}

func (r *factoryFakeShipRepo) findAllCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.findAllCalls
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

// factoryFixture wires the coordinator to its driven ports for a
// FAB_PLATE <- IRON supply chain, exposing the fakes so tests can gate ship
// discovery or drive the run with a cancellable context.
type factoryFixture struct {
	handler  *RunFactoryCoordinatorHandler
	shipRepo *factoryFakeShipRepo
	mediator *factoryFakeMediator
	cmd      *RunFactoryCoordinatorCommand
}

// newFactoryFixture builds a coordinator with two idle haulers: the first
// already carries IRON (its level-0 worker delivers it to the factory) and the
// second is empty; the level-1 worker fabricates FAB_PLATE at the factory.
func newFactoryFixture(t *testing.T) *factoryFixture {
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

	return &factoryFixture{
		handler:  handler,
		shipRepo: shipRepo,
		mediator: fakeMediator,
		cmd:      cmd,
	}
}

// runFactoryCoordinator drives the coordinator through its driving port with a
// FAB_PLATE <- IRON supply chain and both haulers immediately idle.
func runFactoryCoordinator(t *testing.T) (*factoryFakeMediator, *RunFactoryCoordinatorResponse, error) {
	t.Helper()
	f := newFactoryFixture(t)
	resp, err := f.handler.Handle(context.Background(), f.cmd)
	coordResp, _ := resp.(*RunFactoryCoordinatorResponse)
	return f.mediator, coordResp, err
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

// Impatience crash (sp-vmrj): the goods factory used to crash unrecoverably
// ("no idle hauler ships available for production") the instant it found every
// hauler momentarily busy at launch. A factory that holds a market at MODERATE+
// is long-lived, so it must instead poll for the next idle gap — like the fleet
// coordinator does — and acquire the first hauler that frees.
func TestFactoryCoordinator_ZeroIdleHaulersAtLaunch_WaitsAndAcquires(t *testing.T) {
	f := newFactoryFixture(t)
	// The first two discovery polls see an empty fleet (every hauler
	// coordinator-assigned); the third reveals the now-idle haulers.
	f.shipRepo.emptyUntilCall = 2

	resp, err := f.handler.Handle(context.Background(), f.cmd)
	if err != nil {
		t.Fatalf("factory crashed on a transient zero-idle moment at launch instead of waiting: %v", err)
	}
	coordResp, _ := resp.(*RunFactoryCoordinatorResponse)
	if coordResp == nil || !coordResp.Completed {
		t.Fatalf("expected the factory to wait, acquire an idle hauler, and complete; got %+v", coordResp)
	}
	if calls := f.shipRepo.findAllCallCount(); calls < 3 {
		t.Fatalf("expected the factory to poll for idle haulers at least 3 times before acquiring, got %d", calls)
	}
}

// newFactoryHandlerWithClock builds a coordinator wired to the standard fakes
// but with the caller's choice of clock. Passing nil mirrors the daemon's
// production wiring (main.go: "nil = use RealClock"), which every sibling
// coordinator honours by substituting a RealClock in its constructor.
func newFactoryHandlerWithClock(t *testing.T, clock shared.Clock) *RunFactoryCoordinatorHandler {
	t.Helper()
	marketRepo := &factoryFakeMarketRepo{}
	shipRepo := &factoryFakeShipRepo{ships: map[string]*navigation.Ship{}}
	resolver := mfgServices.NewSupplyChainResolver(
		map[string][]string{testOutputGood: {testInputGood}},
		marketRepo,
	)
	marketLocator := mfgServices.NewMarketLocator(marketRepo, nil, nil, nil)
	return NewRunFactoryCoordinatorHandler(
		&factoryFakeMediator{},
		shipRepo,
		marketRepo,
		resolver,
		marketLocator,
		clock,
	)
}

// P0 regression (sp-bt6o): the daemon wires the factory coordinator with a nil
// clock ("nil = use RealClock", main.go), exactly like every sibling
// coordinator. The factory constructor forgot to substitute a RealClock, so
// h.clock stayed nil and the parallel claim path dereferenced it —
// ship.AssignToContainer -> clock.Now() (ship.go:444) — SIGSEGV'ing the whole
// daemon (fleet-wide outage, three panics in the log). Claiming a genuinely
// idle hauler must assign it to the factory container without panicking.
func TestClaimShipForFactory_NilClockFromDaemonWiring_DoesNotPanic(t *testing.T) {
	handler := newFactoryHandlerWithClock(t, nil) // mirror main.go's nil clock
	ship := newTestHauler(t, "CRAFTY-9", nil)     // a genuinely idle hauler
	shipsUsed := map[string]bool{}
	var mu sync.Mutex

	// Before the fix this SIGSEGVs at clock.Now() on the nil clock.
	handler.claimShipForFactory(context.Background(), testContainerID, ship, shipsUsed, &mu)

	if !ship.IsAssigned() {
		t.Fatal("expected the idle hauler to be claimed and assigned to the factory container")
	}
	if ship.ContainerID() != testContainerID {
		t.Fatalf("expected the hauler assigned to %q, got %q", testContainerID, ship.ContainerID())
	}
}

// Defense-in-depth (sp-bt6o): a nil ship must degrade to a skipped claim, never
// a SIGSEGV. The claim path is reported unclaimable so the worker skips the node.
func TestClaimShipForFactory_NilShip_SkippedNotPanicked(t *testing.T) {
	handler := newFactoryHandlerWithClock(t, &factoryFakeClock{})
	shipsUsed := map[string]bool{}
	var mu sync.Mutex

	claimed := handler.claimShipForFactory(context.Background(), testContainerID, nil, shipsUsed, &mu)

	if claimed {
		t.Fatal("a nil ship must never be reported as claimed")
	}
}

// Root correctness (sp-bt6o): claimability is re-validated at claim time. A ship
// another coordinator grabbed since discovery (a stale-snapshot TOCTOU) must be
// skipped, not clobbered — the factory must not steal a hull mid-task, and must
// not panic doing so.
func TestClaimShipForFactory_ShipOwnedByAnotherContainer_SkippedNotClobbered(t *testing.T) {
	handler := newFactoryHandlerWithClock(t, &factoryFakeClock{})
	ship := newTestHauler(t, "CRAFTY-7", nil)

	const otherContainer = "contract-work-CRAFTY-7-other"
	if err := ship.AssignToContainer(otherContainer, shared.NewRealClock()); err != nil {
		t.Fatalf("failed to pre-assign ship to another container: %v", err)
	}

	shipsUsed := map[string]bool{}
	var mu sync.Mutex

	claimed := handler.claimShipForFactory(context.Background(), testContainerID, ship, shipsUsed, &mu)

	if claimed {
		t.Fatal("a ship owned by another container must not be claimed by the factory")
	}
	if ship.ContainerID() != otherContainer {
		t.Fatalf("factory clobbered another coordinator's assignment: ship now on %q, want %q", ship.ContainerID(), otherContainer)
	}
}

// Happy path preserved (vmrj): a genuinely idle hauler is still claimed and
// assigned, and is reported usable.
func TestClaimShipForFactory_IdleShip_Claimed(t *testing.T) {
	handler := newFactoryHandlerWithClock(t, &factoryFakeClock{})
	ship := newTestHauler(t, "CRAFTY-5", nil)
	shipsUsed := map[string]bool{}
	var mu sync.Mutex

	claimed := handler.claimShipForFactory(context.Background(), testContainerID, ship, shipsUsed, &mu)

	if !claimed {
		t.Fatal("expected a genuinely idle hauler to be claimed")
	}
	if !ship.IsAssigned() || ship.ContainerID() != testContainerID {
		t.Fatalf("expected the hauler assigned to %q, got assigned=%v container=%q", testContainerID, ship.IsAssigned(), ship.ContainerID())
	}
	if !shipsUsed[ship.ShipSymbol()] {
		t.Fatal("expected the claimed hauler to be recorded in shipsUsed")
	}
}

// The wait-for-idle loop is bounded only by container shutdown (context
// cancellation), never a timeout. When the container is cancelled while it is
// still waiting for a hauler, it must exit cleanly with the context error and
// must never emit the old fatal "no idle hauler" crash.
func TestFactoryCoordinator_ContextCancelledWhileWaiting_ExitsWithoutFatalCrash(t *testing.T) {
	f := newFactoryFixture(t)
	f.shipRepo.alwaysEmpty = true // the fleet never frees a hauler

	ctx, cancel := context.WithCancel(context.Background())
	f.shipRepo.onFindAll = func(callNum int) {
		if callNum >= 3 {
			cancel() // container shuts down after a few fruitless polls
		}
	}

	resp, err := f.handler.Handle(ctx, f.cmd)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled when the container is cancelled while waiting for a hauler, got %v", err)
	}
	if coordResp, _ := resp.(*RunFactoryCoordinatorResponse); coordResp != nil &&
		strings.Contains(coordResp.Error, "no idle hauler") {
		t.Fatalf("factory emitted the impatience-crash error while merely waiting for a hauler: %q", coordResp.Error)
	}
}

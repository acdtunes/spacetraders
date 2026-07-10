package services

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests reproduce goods-factory feeder crash #3 (sp-n7yp): the buy step
// races the async dock. The ONLY faked collaborator is the ShipRepository (the
// DB/API boundary) and the market; the real DockShipHandler, runStateTransition,
// LoadShip and domain EnsureDocked all execute, so the tests exercise the actual
// dock-persistence path rather than a re-implemented one.

const (
	dockRaceShip     = "FEEDER-1"
	dockRaceOrigin   = "X1-DR-ORIGIN"
	dockRaceMarketWP = "X1-DR-IRONMKT"
	dockRaceGood     = "IRON"
)

// dockRaceShipRepo persists a ship's nav status as a primitive and rebuilds a
// fresh Ship on every FindBySymbol — mirroring modelToDomain reading DB columns.
// Rebuilding (rather than sharing a pointer) is essential: it prevents a caller's
// in-memory EnsureDocked mutation from silently leaking into "the DB" and masking
// the bug. Dock models the real API dock + persist (sets DOCKED). Embedding the
// interface makes any unused method panic, keeping the fake honest.
type dockRaceShipRepo struct {
	navigation.ShipRepository
	mu             sync.Mutex
	location       string
	navStatus      navigation.NavStatus
	dockAPICalls   int
	syncAPICalls   int
	cargoUnits     int
	cargoCapacity  int
	cargoInventory []*shared.CargoItem
}

func (r *dockRaceShipRepo) buildShip() *navigation.Ship {
	waypoint, err := shared.NewWaypoint(r.location, 0, 0)
	if err != nil {
		panic(err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		panic(err)
	}
	cargo, err := shared.NewCargo(r.cargoCapacity, r.cargoUnits, r.cargoInventory)
	if err != nil {
		panic(err)
	}
	ship, err := navigation.NewShip(
		dockRaceShip,
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
		r.navStatus,
	)
	if err != nil {
		panic(err)
	}
	return ship
}

func (r *dockRaceShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buildShip(), nil
}

// Dock models the concrete repo: unconditionally hits the API and persists DOCKED.
func (r *dockRaceShipRepo) Dock(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dockAPICalls++
	r.navStatus = navigation.NavStatusDocked
	return nil
}

func (r *dockRaceShipRepo) SyncShipFromAPI(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.syncAPICalls++
	return r.buildShip(), nil
}

func (r *dockRaceShipRepo) Save(ctx context.Context, ship *navigation.Ship) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.navStatus = ship.NavStatus()
	return nil
}

func (r *dockRaceShipRepo) arriveInOrbit(destination string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.location = destination
	r.navStatus = navigation.NavStatusInOrbit
}

func (r *dockRaceShipRepo) status() navigation.NavStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.navStatus
}

func (r *dockRaceShipRepo) dockCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dockAPICalls
}

func (r *dockRaceShipRepo) syncCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.syncAPICalls
}

// fillCargo (sp-mu6u) preloads the hold with the given items so buildShip
// reports a full/partially-full cargo hold, reproducing the crash precondition:
// an input BUY attempted while the hull is already carrying cargo.
func (r *dockRaceShipRepo) fillCargo(items []*shared.CargoItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cargoInventory = items
	units := 0
	for _, item := range items {
		units += item.Units
	}
	r.cargoUnits = units
}

// removeCargo (sp-mu6u) mirrors a successful sell: it mutates the persisted
// inventory/units the same way the real API would, so a follow-up
// FindBySymbol/buildShip reflects the freed space.
func (r *dockRaceShipRepo) removeCargo(symbol string, units int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	remaining := make([]*shared.CargoItem, 0, len(r.cargoInventory))
	for _, item := range r.cargoInventory {
		if item.Symbol != symbol {
			remaining = append(remaining, item)
			continue
		}
		left := item.Units - units
		if left > 0 {
			updated, err := shared.NewCargoItem(item.Symbol, item.Name, item.Description, left)
			if err != nil {
				panic(err)
			}
			remaining = append(remaining, updated)
		}
	}
	r.cargoInventory = remaining
	r.cargoUnits -= units
	if r.cargoUnits < 0 {
		r.cargoUnits = 0
	}
}

// dockRaceMediator routes DockShipCommand to the REAL DockShipHandler (so the
// stale-in-memory-DOCKED no-op is exercised for real), models navigation arrival
// as "in orbit at destination", and answers PurchaseCargoCommand either by
// modeling the real docked-precondition or by a scripted per-attempt sequence.
type dockRaceMediator struct {
	repo        *dockRaceShipRepo
	dockHandler *tactics.DockShipHandler

	mu             sync.Mutex
	navCalls       int
	purchaseCalls  int
	purchaseScript []error // per-attempt outcome; nil entry = success, missing = model real precondition
	sellCalls      int
	sellShouldFail bool // sp-mu6u: model a market that won't import the onboard good
}

func (m *dockRaceMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipTypes.DockShipCommand:
		return m.dockHandler.Handle(ctx, cmd)

	case *shipNav.NavigateRouteCommand:
		m.mu.Lock()
		m.navCalls++
		m.mu.Unlock()
		// Model arrival: NavigateRouteCommand ends with ship.Arrive() -> IN_ORBIT.
		m.repo.arriveInOrbit(cmd.Destination)
		return nil, nil

	case *shipCargo.PurchaseCargoCommand:
		m.mu.Lock()
		idx := m.purchaseCalls
		m.purchaseCalls++
		scripted := idx < len(m.purchaseScript)
		var scriptedErr error
		if scripted {
			scriptedErr = m.purchaseScript[idx]
		}
		m.mu.Unlock()

		if scripted {
			if scriptedErr != nil {
				return nil, scriptedErr
			}
			return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * 10, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
		}

		// Default: faithfully model cargo_transaction.go's validateShipDocked —
		// reload from the repo and reject if not actually docked. This is the exact
		// error string and precondition that crashed the container.
		ship, err := m.repo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return nil, err
		}
		if !ship.IsDocked() {
			return nil, fmt.Errorf("ship must be docked to perform cargo transactions")
		}
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * 10, UnitsAdded: cmd.Units, TransactionCount: 1}, nil

	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		m.sellCalls++
		shouldFail := m.sellShouldFail
		m.mu.Unlock()

		if shouldFail {
			return nil, fmt.Errorf("market does not import %s", cmd.GoodSymbol)
		}
		m.repo.removeCargo(cmd.GoodSymbol, cmd.Units)
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * 5, UnitsSold: cmd.Units, TransactionCount: 1}, nil

	default:
		return nil, nil
	}
}

func (m *dockRaceMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}

func (m *dockRaceMediator) RegisterMiddleware(middleware common.Middleware) {}

func (m *dockRaceMediator) purchaseAttempts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.purchaseCalls
}

func (m *dockRaceMediator) sellAttempts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sellCalls
}

// dockRaceMarketRepo serves a single raw market selling dockRaceGood.
type dockRaceMarketRepo struct {
	market.MarketRepository
}

func (r *dockRaceMarketRepo) FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.CheapestMarketResult, error) {
	if goodSymbol == dockRaceGood {
		return &market.CheapestMarketResult{
			WaypointSymbol: dockRaceMarketWP,
			TradeSymbol:    goodSymbol,
			SellPrice:      10,
			Supply:         "HIGH",
		}, nil
	}
	return nil, nil
}

// FindAllMarketsInSystem lists this harness's single market so the trade-type-aware
// FindExportMarket (sp-9mkf) can iterate it; GetMarketData is consistent with
// FindCheapestMarketSelling, so the sourced market is unchanged.
func (r *dockRaceMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{dockRaceMarketWP}, nil
}

func (r *dockRaceMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	if waypointSymbol != dockRaceMarketWP {
		return nil, nil
	}
	supply := "HIGH"
	activity := "STRONG"
	good, err := market.NewTradeGood(dockRaceGood, &supply, &activity, 8, 10, 10, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
}

// FindBestMarketBuying backs the crushed-sink guard's resale-bid lookup
// (bp6f #3). This harness's harvest cost is fixed at 10/unit (GetMarketData
// above); a bid of 100 keeps every pre-existing harvest-mode test in this
// package (which know nothing about the guard) clearly profitable, so they
// continue to harvest exactly as before it existed.
func (r *dockRaceMarketRepo) FindBestMarketBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.BestMarketBuyingResult, error) {
	if goodSymbol == dockRaceGood {
		return &market.BestMarketBuyingResult{
			WaypointSymbol: dockRaceMarketWP,
			TradeSymbol:    goodSymbol,
			PurchasePrice:  100,
			Supply:         "HIGH",
		}, nil
	}
	return nil, nil
}

type dockRaceClock struct{}

func (c *dockRaceClock) Now() time.Time        { return time.Now() }
func (c *dockRaceClock) Sleep(d time.Duration) {}

func newDockRaceExecutor(t *testing.T, purchaseScript []error) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
	t.Helper()

	repo := &dockRaceShipRepo{
		location:      dockRaceOrigin,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 40,
	}
	mediator := &dockRaceMediator{
		repo:           repo,
		dockHandler:    tactics.NewDockShipHandler(repo),
		purchaseScript: purchaseScript,
	}
	marketRepo := &dockRaceMarketRepo{}
	marketLocator := NewMarketLocator(marketRepo, nil, nil, nil)

	executor := NewProductionExecutorWithConfig(
		mediator,
		repo,
		marketRepo,
		marketLocator,
		&dockRaceClock{},
		[]time.Duration{time.Millisecond},
		nil, // apiClient: this harness's tests predate the spend floor; nil keeps it disabled
	)
	return executor, repo, mediator
}

// Fix (a): NavigateAndDock must not return until the ship is ACTUALLY docked
// (persisted via the API), not merely flipped to DOCKED in memory. The old poll
// loop pre-mutated the ship with EnsureDocked, which made the follow-up
// DockShipCommand a no-op inside runStateTransition (stateChanged=false), so the
// API dock never fired and the DB stayed IN_ORBIT — the buy then reloaded
// IN_ORBIT and crashed.
func TestNavigateAndDock_ArrivesInOrbit_ConfirmsPersistedDockBeforeReturning(t *testing.T) {
	executor, repo, _ := newDockRaceExecutor(t, nil)

	ship, err := executor.NavigateAndDock(context.Background(), dockRaceShip, dockRaceMarketWP, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("NavigateAndDock returned error: %v", err)
	}
	if ship == nil || !ship.IsDocked() {
		t.Fatalf("returned ship must be DOCKED, got status=%v", statusOf(ship))
	}
	if repo.status() != navigation.NavStatusDocked {
		t.Fatalf("persisted ship state must be DOCKED before returning, got %v", repo.status())
	}
	if repo.dockCalls() < 1 {
		t.Fatalf("expected a real API dock (repo.Dock) before returning, got %d dock calls", repo.dockCalls())
	}
}

// Fix (b): a transient "ship must be docked" at the buy step must trigger a
// re-dock + retry (mirroring the reactive 4214/4244 handling in
// NegotiateContractHandler), not propagate up and crash the container.
func TestBuyGood_TransientMustBeDocked_RedocksAndRetries(t *testing.T) {
	// First purchase attempt fails transiently; the retry (after a real re-dock)
	// succeeds.
	script := []error{fmt.Errorf("ship must be docked to perform cargo transactions"), nil}
	executor, repo, mediator := newDockRaceExecutor(t, script)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("ProduceGood must recover from a transient dock error, got: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("expected a successful purchase after retry, got %+v", result)
	}
	if mediator.purchaseAttempts() != 2 {
		t.Fatalf("expected exactly 2 purchase attempts (1 transient + 1 retry), got %d", mediator.purchaseAttempts())
	}
	if repo.syncCalls() < 1 {
		t.Fatalf("expected an API resync before re-dock (clears stale DOCKED cache), got %d", repo.syncCalls())
	}
}

// Fix (b) guard: a genuine, non-transient purchase failure must surface
// immediately and must NOT be retried or trigger a re-dock.
func TestBuyGood_GenuineFailure_NotRetried(t *testing.T) {
	script := []error{fmt.Errorf("purchase rejected: insufficient credits")}
	executor, repo, mediator := newDockRaceExecutor(t, script)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	_, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err == nil {
		t.Fatalf("expected a genuine purchase failure to surface, got nil")
	}
	if !strings.Contains(err.Error(), "insufficient credits") {
		t.Fatalf("expected the genuine error to surface verbatim, got: %v", err)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("genuine failures must not be retried, got %d purchase attempts", mediator.purchaseAttempts())
	}
	if repo.syncCalls() != 0 {
		t.Fatalf("genuine failures must not trigger a re-dock, got %d resyncs", repo.syncCalls())
	}
}

// Fix (b) bound: a persistent transient dock error must terminate after a bounded
// number of retries (never infinite-loop), then surface an error.
func TestBuyGood_PersistentMustBeDocked_BoundedRetries(t *testing.T) {
	// Every attempt is transient — the retry must give up after the bound.
	script := make([]error, 16)
	for i := range script {
		script[i] = fmt.Errorf("ship must be docked to perform cargo transactions")
	}
	executor, repo, mediator := newDockRaceExecutor(t, script)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	_, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err == nil {
		t.Fatalf("expected an error after exhausting bounded dock retries, got nil")
	}
	attempts := mediator.purchaseAttempts()
	if attempts != productionDockRetryLimit+1 {
		t.Fatalf("expected exactly %d purchase attempts (initial + %d retries), got %d", productionDockRetryLimit+1, productionDockRetryLimit, attempts)
	}
	_ = repo
}

func statusOf(ship *navigation.Ship) navigation.NavStatus {
	if ship == nil {
		return ""
	}
	return ship.NavStatus()
}

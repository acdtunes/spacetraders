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
	mu           sync.Mutex
	location     string
	navStatus    navigation.NavStatus
	dockAPICalls int
	syncAPICalls int
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
	cargo, err := shared.NewCargo(40, 0, nil)
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

type dockRaceClock struct{}

func (c *dockRaceClock) Now() time.Time        { return time.Now() }
func (c *dockRaceClock) Sleep(d time.Duration) {}

func newDockRaceExecutor(t *testing.T, purchaseScript []error) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
	t.Helper()

	repo := &dockRaceShipRepo{
		location:  dockRaceOrigin,
		navStatus: navigation.NavStatusDocked,
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
	result, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil)
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
	_, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil)
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
	_, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil)
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

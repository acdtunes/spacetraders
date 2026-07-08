package commands

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// sp-2sam hazard b — the stale-ask guard. The lane is ranked from a market cache
// that can be many minutes stale (the live run ranked RIFLES off a 13m-old E41 ask
// whose live value had moved 3.6x that day, a -196k manual-loss precedent). Before
// the first buy — now that the hull is docked at the source — the coordinator
// re-reads the source ask live and ABORTS if it has run away from the basis the lane
// was ranked on, rather than buy into a fill the ranked spread no longer describes.

const (
	staleSystem = "X1-STALE"
	staleSrc    = "X1-STALE-EXPORT" // BUY here; the hull is docked HERE (= circuit source)
	staleDst    = "X1-STALE-IMPORT" // SELL here
	staleGood   = "ASSAULT_RIFLES"

	staleRankedAsk = 500  // the STALE ask the lane was ranked on (basis)
	staleStartBid  = 2608 // 2608 − 500 = 2108/u — clears the floor on the ranked basis
	staleDstAsk    = 2700
	staleVol       = 60
	staleDecay     = 600
)

// staleFixture serves the source ask as STALE until a live refresh flips it to the
// live value — modelling a cache that was written minutes ago and a fresh API scan
// that reveals the moved price. The importer bid decays with fills so a proceeding
// circuit terminates.
type staleFixture struct {
	mu         sync.Mutex
	liveSrcAsk int  // the ask a live refresh reveals
	refreshed  bool // set by the refresher; before it, the source shows the stale ranked ask
	sellCount  int
}

func (f *staleFixture) srcAsk() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refreshed {
		return f.liveSrcAsk
	}
	return staleRankedAsk
}

func (f *staleFixture) markRefreshed() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed = true
}

func (f *staleFixture) destBid() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return staleStartBid - staleDecay*f.sellCount
}

func (f *staleFixture) recordSell() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sellCount++
}

// staleRefresher stands in for the live MarketScanner: on success it flips the
// source market to its live ask; a non-nil err models a scan that could not run.
type staleRefresher struct {
	fixture *staleFixture
	err     error
	calls   int
}

func (r *staleRefresher) ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	if waypointSymbol == staleSrc {
		r.fixture.markRefreshed()
	}
	return nil
}

type staleMediator struct {
	mu        sync.Mutex
	fixture   *staleFixture
	purchases []*shipCargo.PurchaseCargoCommand
	sells     []*shipCargo.SellCargoCommand
}

func (m *staleMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.mu.Lock()
		m.purchases = append(m.purchases, cmd)
		m.mu.Unlock()
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * m.fixture.srcAsk(), UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		m.sells = append(m.sells, cmd)
		m.mu.Unlock()
		bid := m.fixture.destBid()
		m.fixture.recordSell()
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * bid, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil
	}
}

func (m *staleMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *staleMediator) RegisterMiddleware(middleware common.Middleware) {}

type staleMarketRepo struct {
	market.MarketRepository
	fixture *staleFixture
}

func (r *staleMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{staleSrc, staleDst}, nil
}

func (r *staleMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "STRONG"
	switch waypointSymbol {
	case staleSrc:
		good, err := market.NewTradeGood(staleGood, &supply, &activity, r.fixture.srcAsk()-20, r.fixture.srcAsk(), staleVol, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	case staleDst:
		good, err := market.NewTradeGood(staleGood, &supply, &activity, r.fixture.destBid(), staleDstAsk, staleVol, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	}
	return nil, nil
}

func newStaleHarness(t *testing.T, ship *navigation.Ship, fixture *staleFixture, refresher MarketRefresher) (*RunTradeRouteCoordinatorHandler, *staleMediator) {
	t.Helper()
	mediator := &staleMediator{fixture: fixture}
	marketRepo := &staleMarketRepo{fixture: fixture}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, refresher)
	return handler, mediator
}

// When a live re-read of the source ask (taken at the source before the first buy)
// shows the ask has run away from the ranked basis, the circuit must ABORT before
// buying — reporting WHY (StaleAskAbort), spending nothing, and releasing the hull.
func TestTradeRouteCoordinator_StaleAskMovedBeyondTolerance_AbortsBeforeFirstBuy(t *testing.T) {
	fixture := &staleFixture{liveSrcAsk: 1800} // 500 -> 1800 = 3.6x, far beyond 30%
	ship := newDiscHauler(t, "STALE-1", staleSrc)
	handler, mediator := newStaleHarness(t, ship, fixture, &staleRefresher{fixture: fixture})

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: staleSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("a stale-ask abort must be a clean exit, not an error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)
	if !coord.Completed {
		t.Fatalf("expected a completed run, got %+v", coord)
	}

	// The lane was still SELECTED (RIFLES clears the floor on the ranked basis) — the
	// abort is a runtime basis check, not a selection failure.
	if coord.Good != staleGood {
		t.Fatalf("expected the ranked lane %q selected, got %q", staleGood, coord.Good)
	}
	if !coord.StaleAskAbort {
		t.Fatal("live ask moved 3.6x from the ranked basis; StaleAskAbort must be set so the run reports WHY it bought nothing")
	}
	if coord.RankedSourceAsk != staleRankedAsk || coord.LiveSourceAsk != 1800 {
		t.Fatalf("expected ranked=%d live=1800 reported, got ranked=%d live=%d", staleRankedAsk, coord.RankedSourceAsk, coord.LiveSourceAsk)
	}
	if coord.Visits != 0 || coord.UnitsTraded != 0 {
		t.Fatalf("a stale-ask abort must fly zero and buy nothing, got %d visits / %d units", coord.Visits, coord.UnitsTraded)
	}
	if len(mediator.purchases) != 0 {
		t.Fatalf("aborting on a moved basis must not buy: %d purchases fired", len(mediator.purchases))
	}
	if coord.TotalCost != 0 {
		t.Fatalf("aborting before the first buy must spend nothing, spent %d", coord.TotalCost)
	}
	if !ship.IsIdle() {
		t.Fatalf("the hull must be released after a stale-ask abort, still on %q", ship.ContainerID())
	}
}

// When the live re-read is within tolerance, the guard is transparent: the basis is
// confirmed and the circuit flies its visits normally.
func TestTradeRouteCoordinator_StaleAskWithinTolerance_ProceedsAndFlies(t *testing.T) {
	fixture := &staleFixture{liveSrcAsk: 550} // 500 -> 550 = +10%, within the 30% band
	ship := newDiscHauler(t, "STALE-2", staleSrc)
	handler, mediator := newStaleHarness(t, ship, fixture, &staleRefresher{fixture: fixture})

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: staleSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)
	if coord.StaleAskAbort {
		t.Fatalf("a +10%% ask move is within the %d%% tolerance; the circuit must proceed, not abort", staleRankedAsk)
	}
	if coord.Good != staleGood {
		t.Fatalf("expected the ranked lane %q, got %q", staleGood, coord.Good)
	}
	if coord.Visits < 1 {
		t.Fatalf("a within-tolerance basis must fly >=1 visit, got %d", coord.Visits)
	}
	if len(mediator.purchases) < 1 {
		t.Fatal("a within-tolerance basis must actually buy")
	}
	if coord.NetProfit <= 0 {
		t.Fatalf("expected a net-positive circuit, got net %d", coord.NetProfit)
	}
	if !ship.IsIdle() {
		t.Fatalf("expected the ship released to idle, still on %q", ship.ContainerID())
	}
}

// Fail-open: a refresher that cannot scan (transient error) must NOT strand an
// otherwise-good circuit. Only a CONFIRMED move aborts; an unverifiable basis
// proceeds on the ranked economics.
func TestTradeRouteCoordinator_StaleAskGuard_FailsOpenWhenRefreshErrors(t *testing.T) {
	fixture := &staleFixture{liveSrcAsk: 1800} // would abort IF the refresh had revealed it
	ship := newDiscHauler(t, "STALE-3", staleSrc)
	refresher := &staleRefresher{fixture: fixture, err: fmt.Errorf("daemon scan unavailable")}
	handler, mediator := newStaleHarness(t, ship, fixture, refresher)

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: staleSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)
	if refresher.calls == 0 {
		t.Fatal("the guard must have attempted a live refresh")
	}
	if coord.StaleAskAbort {
		t.Fatal("an unverifiable basis (refresh failed) must NOT abort — the guard fails open on infrastructure gaps")
	}
	if coord.Visits < 1 || len(mediator.purchases) < 1 {
		t.Fatalf("fail-open must let the circuit fly on the ranked basis, got %d visits / %d purchases", coord.Visits, len(mediator.purchases))
	}
}

package commands

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
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// SECONDARY zero-visit hardening (NOT the sp-2sam root cause — that is the navigate
// self-collision reproduced in run_trade_route_coordinator_selfcollision_test.go and
// fixed in the CLI runner). This pins a DISTINCT latent zero-visit the "too ideal"
// fixtures hid: an idle hull that is not EMPTY.
//
// The defect: runCircuit sized the tranche to the hull's TOTAL capacity
//
//	cargoSpace := ship.CargoCapacity() - held
//
// not its AVAILABLE space. An idle hull is not necessarily empty (a factory hauler
// benched mid-task, a pool hull with leftover cargo). When it carries residual cargo,
// buyUnits is sized past the free hold, and the real purchase precondition
// (PurchaseStrategy.ValidatePreconditions: units > AvailableCargoSpace) rejects the
// oversized buy — 'Purchase failed - ending circuit', zero visits, zero credits spent.
// The existing fakes never bit because they fly an EMPTY hull through a mediator whose
// purchase ALWAYS succeeds regardless of cargo space. The fix sizes to AvailableCargoSpace.

const (
	zvSystem = "X1-ZV"
	zvSrc    = "X1-ZV-EXPORT" // exporter: BUY here; the idle hull is docked HERE (= circuit source)
	zvDst    = "X1-ZV-IMPORT" // importer: SELL here as its bid decays
	zvGood   = "ASSAULT_RIFLES"

	zvSrcAsk   = 500  // basis (source ask we pay)
	zvStartBid = 2608 // 2608 − 500 = 2108/u spread — CLEARS the floor, like the live RIFLES lane
	zvDstAsk   = 2700
	zvSrcVol   = 60 // source is a real export with deep volume → VisitTranche is never the limiter
	zvDstVol   = 60
	zvBidDecay = 600 // importer fills → bid decays per completed sell so the circuit terminates
)

// zvFixture is the shared onboard-cargo + bid-decay simulator. onboard models the
// hull's actual hold the way the live API does: a purchase that would push it past
// capacity is REFUSED (the precondition the empty-hull fakes skip); each sell frees
// space and walks the importer bid down toward the floor.
type zvFixture struct {
	mu        sync.Mutex
	capacity  int
	onboard   int // units currently in the hold (starts at the residual cargo)
	sellCount int
}

func (f *zvFixture) destBid() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return zvStartBid - zvBidDecay*f.sellCount
}

// tryBuy enforces the live purchase precondition: reject a buy that overflows the
// hold, exactly as PurchaseStrategy.ValidatePreconditions does against the ship's
// AvailableCargoSpace(). Returns the error the coordinator surfaces as 'Purchase
// failed - ending circuit'.
func (f *zvFixture) tryBuy(units int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	available := f.capacity - f.onboard
	if units > available {
		return fmt.Errorf("insufficient cargo space: need %d, have %d", units, available)
	}
	f.onboard += units
	return nil
}

func (f *zvFixture) recordSell(units int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onboard -= units
	if f.onboard < 0 {
		f.onboard = 0
	}
	f.sellCount++
}

// zvMediator is live-faithful where the old fakes were ideal: its purchase honours
// the hull's real free space instead of always succeeding.
type zvMediator struct {
	mu        sync.Mutex
	fixture   *zvFixture
	failBuy   error // when set, every purchase fails with this error (models an API buy rejection)
	purchases []*shipCargo.PurchaseCargoCommand
	sells     []*shipCargo.SellCargoCommand
}

func (m *zvMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		if m.failBuy != nil {
			return nil, m.failBuy
		}
		if err := m.fixture.tryBuy(cmd.Units); err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.purchases = append(m.purchases, cmd)
		m.mu.Unlock()
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * zvSrcAsk, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		m.sells = append(m.sells, cmd)
		m.mu.Unlock()
		bid := m.fixture.destBid()
		m.fixture.recordSell(cmd.Units)
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * bid, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil // navigate, dock, etc. succeed silently
	}
}

func (m *zvMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *zvMediator) RegisterMiddleware(middleware common.Middleware) {}

// zvMarketRepo serves the single clearing RIFLES lane: a deep-volume exporter and
// an importer whose bid decays with fills.
type zvMarketRepo struct {
	market.MarketRepository
	fixture *zvFixture
}

func (r *zvMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{zvSrc, zvDst}, nil
}

func (r *zvMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "STRONG"
	switch waypointSymbol {
	case zvSrc:
		good, err := market.NewTradeGood(zvGood, &supply, &activity, zvSrcAsk-20, zvSrcAsk, zvSrcVol, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	case zvDst:
		good, err := market.NewTradeGood(zvGood, &supply, &activity, r.fixture.destBid(), zvDstAsk, zvDstVol, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	}
	return nil, nil
}

// newResidualHauler builds an idle hauler that is NOT empty: it still carries
// residualUnits of leftover cargo, leaving capacity−residualUnits of free hold.
func newResidualHauler(t *testing.T, symbol string, capacity, residualUnits int) *navigation.Ship {
	t.Helper()
	var items []*shared.CargoItem
	if residualUnits > 0 {
		item, err := shared.NewCargoItem("RESERVED_GOODS", "Reserved", "leftover cargo from a prior task", residualUnits)
		if err != nil {
			t.Fatalf("cargo item: %v", err)
		}
		items = []*shared.CargoItem{item}
	}
	cargo, err := shared.NewCargo(capacity, residualUnits, items)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(zvSrc, 0, 0) // docked AT the circuit source, like the live T8-at-E41 case
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, capacity, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

func newZvHarness(t *testing.T, ship *navigation.Ship, residualUnits int) (*RunTradeRouteCoordinatorHandler, *zvMediator) {
	t.Helper()
	fixture := &zvFixture{capacity: ship.CargoCapacity(), onboard: residualUnits}
	mediator := &zvMediator{fixture: fixture}
	marketRepo := &zvMarketRepo{fixture: fixture}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, nil, nil)
	return handler, mediator
}

// A genuinely idle hull that still carries residual cargo must SIZE ITS BUY TO THE
// FREE HOLD and fly >=1 profitable visit. With the total-capacity bug the coordinator
// asks to buy an 18u tranche into a hull with only 10u free, the purchase precondition
// refuses it, and the circuit exits at iteration 0 with zero visits. This is a distinct
// zero-visit path from the sp-2sam self-collision root cause: RED before the fix, GREEN
// after.
func TestTradeRouteCoordinator_ResidualCargoHull_SizesBuyToAvailableSpace(t *testing.T) {
	const capacity, residual = 40, 30 // 10u free
	ship := newResidualHauler(t, "T8", capacity, residual)
	handler, mediator := newZvHarness(t, ship, residual)

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: zvSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord, ok := resp.(*RunTradeRouteCoordinatorResponse)
	if !ok || coord == nil {
		t.Fatalf("unexpected response %T", resp)
	}

	// Selection is correct (this is not the sh6w bug): the clearing RIFLES lane is chosen.
	if coord.Good != zvGood || coord.SourceWaypoint != zvSrc || coord.DestWaypoint != zvDst {
		t.Fatalf("wrong lane selected: good=%q source=%q dest=%q", coord.Good, coord.SourceWaypoint, coord.DestWaypoint)
	}

	// THE reproduction assertion: a selected, floor-clearing lane on a non-empty hull
	// must still fly. Before the fix buyUnits=18 into 10u free → purchase refused → 0.
	if coord.Visits < 1 {
		t.Fatalf("expected >=1 profitable visit on a residual-cargo hull, got %d visits / %d units "+
			"(runCircuit sized the buy to CargoCapacity, not AvailableCargoSpace, so the oversized "+
			"buy was refused and the circuit flew zero — sp-2sam)", coord.Visits, coord.UnitsTraded)
	}

	// No buy may ever exceed the hull's free space (the precondition the fix respects).
	for i, p := range mediator.purchases {
		if p.Units > capacity-residual {
			t.Fatalf("purchase %d bought %d units into a hull with only %d free — overflow (sp-2sam)", i, p.Units, capacity-residual)
		}
	}
	if coord.NetProfit <= 0 {
		t.Fatalf("expected a net-positive circuit, got net %d (cost %d, revenue %d)", coord.NetProfit, coord.TotalCost, coord.TotalRevenue)
	}
	if !ship.IsIdle() {
		t.Fatalf("expected the ship released to idle after the run, still assigned to %q", ship.ContainerID())
	}
}

// A selected lane that flies zero because a post-gate leg failed must SURFACE the
// reason, not return a bare 'Visits: 0'. This is the anti-mystery guard: three
// zero-visit bugs (r3cl, sh6w, sp-2sam) each needed a live re-run to discover WHY the
// loop stopped, because the failing leg's reason never reached the caller. Here an
// empty hull's buy is rejected at the source and AbortReason must name the failed leg.
func TestTradeRouteCoordinator_PostGateFailure_SurfacesAbortReason(t *testing.T) {
	ship := newResidualHauler(t, "T9", 40, 0) // EMPTY hull — not the cargo-space path; the buy itself is rejected
	handler, mediator := newZvHarness(t, ship, 0)
	// Force the buy to fail the way a live API rejection would (market/credits/etc.).
	mediator.failBuy = fmt.Errorf("market rejected purchase: good not sold here (4602)")

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: zvSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("a mid-circuit leg failure must be a clean exit, not an error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)

	// The lane was selected (Good set) but flew zero — exactly the shape that has been
	// an unexplained mystery three times over.
	if coord.Good != zvGood {
		t.Fatalf("expected the lane %q selected, got %q", zvGood, coord.Good)
	}
	if coord.Visits != 0 {
		t.Fatalf("expected 0 visits after the buy was rejected, got %d", coord.Visits)
	}
	if coord.AbortReason == "" {
		t.Fatal("a selected lane that flew zero MUST report AbortReason so the run self-diagnoses (sp-2sam) — got empty")
	}
	if !strings.Contains(coord.AbortReason, "purchase") || !strings.Contains(coord.AbortReason, "4602") {
		t.Fatalf("AbortReason must name the failed leg and carry the underlying error, got %q", coord.AbortReason)
	}
	if !ship.IsIdle() {
		t.Fatalf("the hull must be released after a mid-circuit abort, still on %q", ship.ContainerID())
	}
}

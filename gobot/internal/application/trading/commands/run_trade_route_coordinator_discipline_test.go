package commands

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// This file is the sp-sh6w regression: the scan ranks lanes by volume-capped
// spread, but the executor refuses any lane whose per-unit spread is below the
// bid-floor (trading.MinBidMargin). When the top capped-spread lane is sub-floor,
// the old coordinator selected it and flew ZERO visits. The fix selects the deepest
// lane that clears the floor instead — or, when none does, reports it cleanly.

const (
	discSystem = "X1-DISC"
	discSrc    = "X1-DISC-EXPORT" // exporter: we BUY here (both goods)
	discDst    = "X1-DISC-IMPORT" // importer: we SELL here (both goods)
	discDock   = "X1-DISC-DOCK"   // where the idle hull starts — NOT the circuit source

	// FOOD is the top CAPPED-spread lane but its per-unit spread is BELOW the floor:
	// 780/u × 60 vol = 46800 capped, yet 780 < 1000 → the executor would refuse it.
	discFoodGood   = "FOOD"
	discFoodSrcBid = 200
	discFoodSrcAsk = 220
	discFoodDstBid = 1000 // 1000 − 220 = 780/u (SUB-FLOOR)
	discFoodDstAsk = 1050
	discFoodVol    = 60

	// ASSAULT_RIFLES ranks LOWER by capped spread (2108/u × 20 = 42160) but CLEARS
	// the floor (2108 ≥ 1000) — the lane the executor must actually select and fly.
	discRiflesGood     = "ASSAULT_RIFLES"
	discRiflesSrcBid   = 480
	discRiflesSrcAsk   = 500
	discRiflesStartBid = 2608 // 2608 − 500 = 2108/u (CLEARS the floor)
	discRiflesDstAsk   = 2700
	discRiflesVol      = 20
	discRiflesDecay    = 600 // importer bid decay per completed sell
)

// discFixture shares the ASSAULT_RIFLES importer bid between the market repo (which
// decays it as the importer fills) and the mediator (which increments the fill count
// on each sell), so the circuit walks the bid down through the floor and stops.
type discFixture struct {
	mu        sync.Mutex
	sellCount int
}

func (f *discFixture) riflesDestBid() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return discRiflesStartBid - discRiflesDecay*f.sellCount
}

func (f *discFixture) recordRiflesSell() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sellCount++
}

// discMediator prices buys/sells per good from the same numbers the markets show,
// so the flown lane's economics are coherent; navigation/docking no-op.
type discMediator struct {
	mu        sync.Mutex
	fixture   *discFixture
	purchases []*shipCargo.PurchaseCargoCommand
	sells     []*shipCargo.SellCargoCommand
}

func (m *discMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.mu.Lock()
		m.purchases = append(m.purchases, cmd)
		m.mu.Unlock()
		ask := discFoodSrcAsk
		if cmd.GoodSymbol == discRiflesGood {
			ask = discRiflesSrcAsk
		}
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * ask, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		m.sells = append(m.sells, cmd)
		m.mu.Unlock()
		bid := discFoodDstBid
		if cmd.GoodSymbol == discRiflesGood {
			bid = m.fixture.riflesDestBid()
			m.fixture.recordRiflesSell() // importer fills → dest bid decays next visit
		}
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * bid, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil // navigate, dock, etc. succeed silently
	}
}

func (m *discMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *discMediator) RegisterMiddleware(middleware common.Middleware) {}

// discMarketRepo serves a two-market system trading FOOD and (unless subFloorOnly)
// ASSAULT_RIFLES: an exporter with fixed asks and an importer whose ASSAULT_RIFLES
// bid decays with fills.
type discMarketRepo struct {
	market.MarketRepository
	fixture      *discFixture
	subFloorOnly bool // when true, only FOOD (sub-floor) trades — no clearing lane exists
}

func (r *discMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{discSrc, discDst}, nil
}

func (r *discMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "STRONG"
	switch waypointSymbol {
	case discSrc:
		goods := []market.TradeGood{}
		food, err := market.NewTradeGood(discFoodGood, &supply, &activity, discFoodSrcBid, discFoodSrcAsk, discFoodVol, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		goods = append(goods, *food)
		if !r.subFloorOnly {
			rifles, err := market.NewTradeGood(discRiflesGood, &supply, &activity, discRiflesSrcBid, discRiflesSrcAsk, discRiflesVol, market.TradeTypeExport)
			if err != nil {
				return nil, err
			}
			goods = append(goods, *rifles)
		}
		return market.NewMarket(waypointSymbol, goods, time.Now())
	case discDst:
		goods := []market.TradeGood{}
		food, err := market.NewTradeGood(discFoodGood, &supply, &activity, discFoodDstBid, discFoodDstAsk, discFoodVol, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		goods = append(goods, *food)
		if !r.subFloorOnly {
			rifles, err := market.NewTradeGood(discRiflesGood, &supply, &activity, r.fixture.riflesDestBid(), discRiflesDstAsk, discRiflesVol, market.TradeTypeImport)
			if err != nil {
				return nil, err
			}
			goods = append(goods, *rifles)
		}
		return market.NewMarket(waypointSymbol, goods, time.Now())
	}
	return nil, nil
}

func newDiscHauler(t *testing.T, symbol, atWaypoint string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(atWaypoint, 0, 0)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

type discHarness struct {
	handler  *RunTradeRouteCoordinatorHandler
	mediator *discMediator
	shipRepo *trFakeShipRepo
	ship     *navigation.Ship
}

func newDiscHarness(t *testing.T, ship *navigation.Ship, subFloorOnly bool) *discHarness {
	t.Helper()
	fixture := &discFixture{}
	mediator := &discMediator{fixture: fixture}
	marketRepo := &discMarketRepo{fixture: fixture, subFloorOnly: subFloorOnly}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, nil, nil, nil)
	return &discHarness{handler: handler, mediator: mediator, shipRepo: shipRepo, ship: ship}
}

// The executor must SKIP the top capped-spread lane (FOOD, sub-floor) and select the
// deeper-disciplined ASSAULT_RIFLES lane that clears the bid-floor, then fly ≥1
// profitable visit — even though the hull starts at a neutral dock, not the circuit
// source (mirroring the live TORWIND-8-at-E41 case). This is the sp-sh6w bug: the old
// selection picked FOOD and MarginAlive killed it on visit 0 → zero units, net zero.
func TestTradeRouteCoordinator_SelectsLaneThatClearsFloor_NotTopCappedSubFloor(t *testing.T) {
	ship := newDiscHauler(t, "TORWIND-8", discDock)
	h := newDiscHarness(t, ship, false)

	resp, err := h.handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: discSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord, ok := resp.(*RunTradeRouteCoordinatorResponse)
	if !ok || coord == nil {
		t.Fatalf("unexpected response %T", resp)
	}
	if !coord.Completed {
		t.Fatalf("expected a completed run, got %+v", coord)
	}

	// The disciplined lane — not the sub-floor FOOD the scan ranked #1 — must be flown.
	if coord.Good != discRiflesGood {
		t.Fatalf("expected the disciplined lane %q, got %q: the executor picked the top capped-spread lane instead of the one that clears the floor (sp-sh6w)", discRiflesGood, coord.Good)
	}
	if coord.SourceWaypoint != discSrc || coord.DestWaypoint != discDst {
		t.Fatalf("wrong circuit: source=%q dest=%q", coord.SourceWaypoint, coord.DestWaypoint)
	}
	if coord.NoDisciplinedLane {
		t.Fatal("a lane cleared the floor; NoDisciplinedLane must be false")
	}

	// The core anti-silent-zero assertion: a selected lane must fly ≥1 visit.
	if coord.Visits < 1 {
		t.Fatalf("expected >=1 profitable visit, got %d (a selected lane must never fly zero)", coord.Visits)
	}
	// ASSAULT_RIFLES bid decays 2608→2008 (alive, ≥1500), then 1408 (<1500, dead): 2 visits.
	if coord.Visits != 2 {
		t.Fatalf("expected 2 visits before the bid-floor kills the ASSAULT_RIFLES margin, got %d", coord.Visits)
	}
	if coord.UnitsTraded != 36 {
		t.Fatalf("expected 36 units traded (2 visits x 18u), got %d", coord.UnitsTraded)
	}
	if coord.NetProfit <= 0 {
		t.Fatalf("expected a net-positive circuit, got net %d (cost %d, revenue %d)", coord.NetProfit, coord.TotalCost, coord.TotalRevenue)
	}

	// No visit may exceed the 18u tranche cap, and FOOD must never trade.
	for i, p := range h.mediator.purchases {
		if p.Units > 18 {
			t.Fatalf("purchase %d bought %d units, exceeding the 18u tranche cap", i, p.Units)
		}
		if p.GoodSymbol != discRiflesGood {
			t.Fatalf("purchase %d traded %q, not the disciplined lane %q", i, p.GoodSymbol, discRiflesGood)
		}
	}

	if !ship.IsIdle() {
		t.Fatalf("expected the ship released to idle after the run, still assigned to %q", ship.ContainerID())
	}
}

// When profitable lanes exist but NONE clears the bid-floor, the coordinator must
// exit cleanly reporting 'no disciplined lane' — never a silent zero-visit success
// (Good set but nothing flown). The old code selected the sub-floor FOOD lane, set
// Good, then flew zero: indistinguishable from a real circuit that traded nothing.
func TestTradeRouteCoordinator_NoLaneClearsFloor_ReportsNoDisciplinedLane(t *testing.T) {
	ship := newDiscHauler(t, "TORWIND-9", discDock)
	h := newDiscHarness(t, ship, true) // subFloorOnly: only FOOD (780/u) exists

	resp, err := h.handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: discSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("no disciplined lane must be a clean exit, not an error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)
	if !coord.Completed {
		t.Fatalf("expected a completed run, got %+v", coord)
	}

	if !coord.NoDisciplinedLane {
		t.Fatal("profitable-but-sub-floor lanes existed; NoDisciplinedLane must be set so the run reports WHY it flew nothing")
	}
	if coord.Good != "" {
		t.Fatalf("no lane was flyable under discipline; Good must be empty (not a selected lane), got %q", coord.Good)
	}
	if coord.Visits != 0 {
		t.Fatalf("expected 0 visits, got %d", coord.Visits)
	}
	if coord.BestSubFloorSpread != 780 {
		t.Fatalf("expected the best standing spread (780/u) reported, got %d", coord.BestSubFloorSpread)
	}
	if len(h.mediator.purchases) != 0 || len(h.mediator.sells) != 0 {
		t.Fatalf("no disciplined lane must mean no trades: %d buys / %d sells", len(h.mediator.purchases), len(h.mediator.sells))
	}
	if !ship.IsIdle() {
		t.Fatalf("ship must be released even when no lane clears the floor, still on %q", ship.ContainerID())
	}
}

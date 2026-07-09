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

// End-to-end proof of sp-pnx0: the full Handle() flow (not just scanLanes in
// isolation) must select a hull-appropriate lane, not merely the deepest raw
// CappedSpread. The fixture reproduces the incident shape: THINGOOD is a thin,
// deep-spread lane (a light ship's ideal — but a 225-hold heavy would crush its
// vol-20 market); DEEPGOOD is a modest-spread, deep lane a heavy hull can
// actually absorb. Numbers mirror the domain-layer proof in arbitrage_lane_test.go.
const (
	hwSystem = "X1-HW"

	hwThinSource = "X1-HW-THIN-S" // exporter: buy THINGOOD here at ask 1000
	hwThinDest   = "X1-HW-THIN-D" // importer: sell THINGOOD here, bid starts 9000
	hwDeepSource = "X1-HW-DEEP-S" // exporter: buy DEEPGOOD here at ask 500
	hwDeepDest   = "X1-HW-DEEP-D" // importer: sell DEEPGOOD here, bid starts 1500

	hwThinGood = "THINGOOD"
	hwDeepGood = "DEEPGOOD"

	// THINGOOD: spread/u 8000 (9000-1000), volume cap 20 -> capped 160000.
	// Basis 1000 + MinBidMargin 1000 = floor 2000.
	hwThinAsk      = 1000
	hwThinStartBid = 9000
	hwThinVolume   = 20
	// Decay drops the bid to 1500 after one sell: below THINGOOD's own 2000
	// floor, but still positive (avoids a negative-bid market-data rejection).
	hwThinDecay = 7500

	// DEEPGOOD: spread/u 1000 (1500-500), volume cap 150 -> capped 150000.
	// Basis 500 + MinBidMargin 1000 = floor 1500 (bid clears it exactly).
	hwDeepAsk      = 500
	hwDeepStartBid = 1500
	hwDeepVolume   = 150
	// Decay drops the bid to 900 after one sell: below DEEPGOOD's own 1500
	// floor, still positive.
	hwDeepDecay = 600
)

// hwFixture's sellCount gates BOTH goods' dest-bid decay identically (not just
// whichever good was actually sold). This is deliberate: once the executor has
// traded ANY lane once, a second circuit's re-scan must see EVERY lane sub-floor
// and cleanly terminate the run — never silently swap to the other good's lane,
// which would corrupt this test's assertion on the FIRST (hold-fit) selection.
type hwFixture struct {
	mu   sync.Mutex
	sold bool
}

func (f *hwFixture) recordSell() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sold = true
}

func (f *hwFixture) hasSold() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sold
}

// hwMarketRepo serves a single system with two independent goods, each with its
// own exporter/importer pair, so RankSpreads sees two real candidate lanes to
// choose between.
type hwMarketRepo struct {
	market.MarketRepository
	fixture *hwFixture
}

func (r *hwMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{hwThinSource, hwThinDest, hwDeepSource, hwDeepDest}, nil
}

func (r *hwMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "STRONG"
	decayed := r.fixture.hasSold()

	switch waypointSymbol {
	case hwThinSource:
		good, err := market.NewTradeGood(hwThinGood, &supply, &activity, hwThinAsk-100, hwThinAsk, hwThinVolume, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	case hwThinDest:
		bid := hwThinStartBid
		if decayed {
			bid -= hwThinDecay
		}
		good, err := market.NewTradeGood(hwThinGood, &supply, &activity, bid, hwThinStartBid+100, hwThinVolume, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	case hwDeepSource:
		good, err := market.NewTradeGood(hwDeepGood, &supply, &activity, hwDeepAsk-100, hwDeepAsk, hwDeepVolume, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	case hwDeepDest:
		bid := hwDeepStartBid
		if decayed {
			bid -= hwDeepDecay
		}
		good, err := market.NewTradeGood(hwDeepGood, &supply, &activity, bid, hwDeepStartBid+100, hwDeepVolume, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	}
	return nil, nil
}

// hwFakeMediator no-ops navigate/dock (mirroring trFakeMediator) and echoes
// units on buy/sell with trivial economics — this fixture is about lane
// SELECTION, not circuit economics (already covered by
// TestTradeRouteCoordinator_RunsDisciplinedCircuitUntilMarginDies).
type hwFakeMediator struct {
	mu      sync.Mutex
	fixture *hwFixture
}

func (m *hwFakeMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.fixture.recordSell()
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil // navigate, dock, etc. succeed silently
	}
}

func (m *hwFakeMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *hwFakeMediator) RegisterMiddleware(middleware common.Middleware) {}

// newHoldWeightShip builds a ship of the given cargo capacity, otherwise
// mirroring newTradeHauler's fixture shape.
func newHoldWeightShip(t *testing.T, symbol string, capacity int) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(capacity, 0, nil)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(hwThinSource, 0, 0)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, capacity, cargo, 30,
		"FRAME_HEAVY_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

// TestTradeRouteCoordinator_HeavyHullPrefersDeepLaneOverThinOne is the sp-pnx0
// regression proof: a 225-cargo heavy hull must select DEEPGOOD over THINGOOD,
// even though THINGOOD has the deeper raw CappedSpread (160000 > 150000) — the
// exact incident shape (TORWIND-19-style heavy sent onto a vol-20 lane).
func TestTradeRouteCoordinator_HeavyHullPrefersDeepLaneOverThinOne(t *testing.T) {
	fixture := &hwFixture{}
	mediator := &hwFakeMediator{fixture: fixture}
	marketRepo := &hwMarketRepo{fixture: fixture}
	ship := newHoldWeightShip(t, "HEAVY-1", 225)
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, nil, &trFakeClock{}, nil)

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: hwSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord, ok := resp.(*RunTradeRouteCoordinatorResponse)
	if !ok || coord == nil {
		t.Fatalf("unexpected response %T", resp)
	}
	if coord.Good != hwDeepGood {
		t.Fatalf("expected a 225-hold heavy hull to select DEEPGOOD (THINGOOD's vol-20 lane would be crushed), got %q", coord.Good)
	}
	if coord.SourceWaypoint != hwDeepSource || coord.DestWaypoint != hwDeepDest {
		t.Fatalf("expected DEEPGOOD's lane endpoints, got source=%q dest=%q", coord.SourceWaypoint, coord.DestWaypoint)
	}
}

// TestTradeRouteCoordinator_LightHullStillPrefersThinLane proves the fix does
// not simply invert the ranking: a 20-cargo light ship (matching THINGOOD's own
// volume cap) must still select THINGOOD — both lanes saturate hold-fit to 1.0
// at a 20-cap hull, so the real CappedSpread order (160000 > 150000) decides.
func TestTradeRouteCoordinator_LightHullStillPrefersThinLane(t *testing.T) {
	fixture := &hwFixture{}
	mediator := &hwFakeMediator{fixture: fixture}
	marketRepo := &hwMarketRepo{fixture: fixture}
	ship := newHoldWeightShip(t, "LIGHT-1", 20)
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, nil, &trFakeClock{}, nil)

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: hwSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord, ok := resp.(*RunTradeRouteCoordinatorResponse)
	if !ok || coord == nil {
		t.Fatalf("unexpected response %T", resp)
	}
	if coord.Good != hwThinGood {
		t.Fatalf("expected a 20-hold light hull to still select THINGOOD (both lanes saturate hold-fit, real CappedSpread order holds), got %q", coord.Good)
	}
}

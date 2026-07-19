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
)

// Gate-crossing circuits must LOOP until a margin-exit or starvation-exit, never
// one-and-done. A hull that flies one lane to its bid-floor and then idles wastes
// duty cycle — the gap that matters here is DUTY CYCLE, not per-trade economics.
// These tests pin that the coordinator re-scans and commits to a SECOND
// still-viable lane the moment the first dies, accumulates totals across both
// circuits, and reports why the whole run eventually stopped.

const (
	loopSystem = "X1-LOOP"
	loopSrc    = "X1-LOOP-EXPORT" // exporter: we BUY here (both goods)
	loopDst    = "X1-LOOP-IMPORT" // importer: we SELL here (both goods)
	loopDock   = "X1-LOOP-DOCK"   // where the idle hull starts — neutral, not the source

	// WIDGET ranks FIRST (higher initial spread) and dies after 2 visits.
	loopWidgetGood     = "WIDGET"
	loopWidgetSrcBid   = 480
	loopWidgetSrcAsk   = 500
	loopWidgetStartBid = 2608 // spread 2108/u, clears the 1500 floor (ask+1000)
	loopWidgetDstAsk   = 2700
	loopWidgetVol      = 20
	loopWidgetDecay    = 600 // 2608->2008 (alive) -> 1408 (<1500, dead): 2 visits

	// GADGET ranks SECOND initially (lower spread) and is untouched while WIDGET
	// flies, so it is still fully fresh when the outer loop re-scans after WIDGET
	// dies — the lane the loop must pick up next, proving the re-scan happened.
	loopGadgetGood     = "GADGET"
	loopGadgetSrcBid   = 380
	loopGadgetSrcAsk   = 400
	loopGadgetStartBid = 1900 // spread 1500/u, clears the 1400 floor (ask+1000)
	loopGadgetDstAsk   = 2100
	loopGadgetVol      = 20
	loopGadgetDecay    = 400 // 1900->1500 (alive) -> 1100 (<1400, dead): 2 visits
)

// loopFixture tracks each good's OWN independent fill count, so WIDGET decaying
// never touches GADGET's bid (and vice versa) — GADGET must read as fully fresh
// until the outer loop actually starts flying it.
type loopFixture struct {
	mu              sync.Mutex
	widgetSellCount int
	gadgetSellCount int
}

func (f *loopFixture) widgetDestBid() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return loopWidgetStartBid - loopWidgetDecay*f.widgetSellCount
}

func (f *loopFixture) gadgetDestBid() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return loopGadgetStartBid - loopGadgetDecay*f.gadgetSellCount
}

func (f *loopFixture) recordWidgetSell() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.widgetSellCount++
}

func (f *loopFixture) recordGadgetSell() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gadgetSellCount++
}

// loopMediator prices buys/sells per good from the same numbers the markets
// show, so each flown circuit's economics are coherent; navigate/dock/jump-gate
// lookups no-op (fail open), mirroring the sibling fixtures in this package.
type loopMediator struct {
	mu        sync.Mutex
	fixture   *loopFixture
	purchases []*shipCargo.PurchaseCargoCommand
	sells     []*shipCargo.SellCargoCommand
}

func (m *loopMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.mu.Lock()
		m.purchases = append(m.purchases, cmd)
		m.mu.Unlock()
		ask := loopWidgetSrcAsk
		if cmd.GoodSymbol == loopGadgetGood {
			ask = loopGadgetSrcAsk
		}
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * ask, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		m.sells = append(m.sells, cmd)
		m.mu.Unlock()
		var bid int
		if cmd.GoodSymbol == loopGadgetGood {
			bid = m.fixture.gadgetDestBid()
			m.fixture.recordGadgetSell()
		} else {
			bid = m.fixture.widgetDestBid()
			m.fixture.recordWidgetSell()
		}
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * bid, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil // navigate, dock, jump-gate lookup, etc. succeed silently / fail open
	}
}

func (m *loopMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *loopMediator) RegisterMiddleware(middleware common.Middleware) {}

// loopMarketRepo serves a two-good, two-market system: WIDGET and GADGET both
// export at loopSrc and import at loopDst, each decaying independently as it
// sells — so a re-scan after WIDGET's margin dies finds GADGET untouched.
type loopMarketRepo struct {
	market.MarketRepository
	fixture *loopFixture
}

func (r *loopMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{loopSrc, loopDst}, nil
}

func (r *loopMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "STRONG"
	switch waypointSymbol {
	case loopSrc:
		widget, err := market.NewTradeGood(loopWidgetGood, &supply, &activity, loopWidgetSrcBid, loopWidgetSrcAsk, loopWidgetVol, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		gadget, err := market.NewTradeGood(loopGadgetGood, &supply, &activity, loopGadgetSrcBid, loopGadgetSrcAsk, loopGadgetVol, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*widget, *gadget}, time.Now())
	case loopDst:
		widget, err := market.NewTradeGood(loopWidgetGood, &supply, &activity, r.fixture.widgetDestBid(), loopWidgetDstAsk, loopWidgetVol, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		gadget, err := market.NewTradeGood(loopGadgetGood, &supply, &activity, r.fixture.gadgetDestBid(), loopGadgetDstAsk, loopGadgetVol, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*widget, *gadget}, time.Now())
	}
	return nil, nil
}

func newLoopHarness(t *testing.T, ship *navigation.Ship) (*RunTradeRouteCoordinatorHandler, *loopMediator) {
	t.Helper()
	fixture := &loopFixture{}
	mediator := &loopMediator{fixture: fixture}
	marketRepo := &loopMarketRepo{fixture: fixture}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, nil, nil, nil)
	return handler, mediator
}

// Once WIDGET's margin dies, the coordinator must re-scan and commit
// to GADGET — a second still-viable lane — rather than idling the hull after
// one circuit. Totals must accumulate across BOTH circuits, and the run must
// report why it eventually stopped (both lanes now sub-floor).
func TestTradeRouteCoordinator_MarginDeath_ReScansAndFliesNextViableLane(t *testing.T) {
	ship := newDiscHauler(t, "TORWIND-8", loopDock)
	handler, mediator := newLoopHarness(t, ship)

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: loopSystem,
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

	// The loop must have committed to TWO distinct lanes, not one-and-done.
	if coord.Circuits != 2 {
		t.Fatalf("expected the loop to commit to 2 circuits (WIDGET then GADGET), got %d", coord.Circuits)
	}

	// Visits/units accumulate across BOTH circuits: 2 visits x 18u each = 4 / 72.
	if coord.Visits != 4 {
		t.Fatalf("expected 4 total visits across both circuits (2+2), got %d", coord.Visits)
	}
	if coord.UnitsTraded != 72 {
		t.Fatalf("expected 72 total units traded (36+36), got %d", coord.UnitsTraded)
	}

	// Both goods must actually have been bought — proof the loop really flew a
	// SECOND lane, not just WIDGET twice.
	sawWidget, sawGadget := false, false
	for _, p := range mediator.purchases {
		switch p.GoodSymbol {
		case loopWidgetGood:
			sawWidget = true
		case loopGadgetGood:
			sawGadget = true
		}
	}
	if !sawWidget || !sawGadget {
		t.Fatalf("expected purchases of both WIDGET and GADGET, got widget=%v gadget=%v (purchases=%+v)", sawWidget, sawGadget, mediator.purchases)
	}

	// Exact economics prove cross-circuit accumulation, not just per-circuit totals:
	// WIDGET: cost 2*18*500=18000, revenue 18*2608+18*2008=83088, net 65088.
	// GADGET: cost 2*18*400=14400, revenue 18*1900+18*1500=61200, net 46800.
	// Combined: cost 32400, revenue 144288, net 111888.
	if coord.TotalCost != 32400 {
		t.Fatalf("expected combined cost 32400, got %d", coord.TotalCost)
	}
	if coord.TotalRevenue != 144288 {
		t.Fatalf("expected combined revenue 144288, got %d", coord.TotalRevenue)
	}
	if coord.NetProfit != 111888 {
		t.Fatalf("expected combined net profit 111888, got %d", coord.NetProfit)
	}

	// Both lanes are now sub-floor: the run must report a clean margin-exhausted
	// stop, not an error, and not a false "no disciplined lane ever" claim — a
	// lane WAS flown, twice.
	if coord.ExitReason != exitReasonMarginExhausted {
		t.Fatalf("expected ExitReason %q, got %q", exitReasonMarginExhausted, coord.ExitReason)
	}
	if coord.NoDisciplinedLane {
		t.Fatal("two lanes were flown; NoDisciplinedLane must be false")
	}
	if coord.AbortReason != "" {
		t.Fatalf("a clean margin-exhausted stop must not set AbortReason, got %q", coord.AbortReason)
	}
}

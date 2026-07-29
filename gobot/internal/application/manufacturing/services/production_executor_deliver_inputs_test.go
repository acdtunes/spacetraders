package services

import (
	"context"
	"fmt"
	"reflect"
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

// A hold carrying one good the factory won't take disabled the hull permanently:
// deliverInputs offered the ENTIRE inventory unfiltered and hard-returned on the first
// rejection, so the sourcing step aborted, no cleanup ran, and nothing reconciles a hold
// between lots — every subsequent lot aborted at the same item. Four gate haulers were
// stuck this way carrying EXPLOSIVES / ELECTRONICS the next factory did not import.
//
// Driven through ProduceGood (the driving port) over the real fabricateGood path, so the
// delivery, the market-listing read and the revenue accounting are all the production ones.

const (
	diFactoryWP = "X1-DI-FAB"
	diSystem    = "X1-DI"
	diOutput    = "ELECTRONICS"      // the factory's own product (EXPORT)
	diInput     = "SILICON_CRYSTALS" // an input the factory imports
	diPoison    = "EXPLOSIVES"       // not listed at the factory at all
	diOutputAsk = 50                 // harvest cost per unit
	diOutputVol = 10                 // factory trade volume => 10 units harvested
	diSellBid   = 5                  // revenue per unit delivered
)

// diMarketRepo serves a single factory: it EXPORTS diOutput at MODERATE supply (so the
// already-stocked shortcut is skipped and the fabricate path runs) and IMPORTS diInput.
// Goods absent from this listing are absent at the market, exactly as the API reports.
type diMarketRepo struct {
	market.MarketRepository
	extraGoods []market.TradeGood // additional listings for this factory
}

func (r *diMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{diFactoryWP}, nil
}

func (r *diMarketRepo) FindBestMarketBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.BestMarketBuyingResult, error) {
	return nil, nil
}

func (r *diMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	if waypointSymbol != diFactoryWP {
		return nil, nil
	}
	return market.NewMarket(waypointSymbol, append(r.listings(), r.extraGoods...), time.Now())
}

func (r *diMarketRepo) listings() []market.TradeGood {
	return []market.TradeGood{
		*mustTradeGood(diOutput, "MODERATE", diOutputAsk, diOutputAsk, diOutputVol, market.TradeTypeExport),
		*mustTradeGood(diInput, "SCARCE", diSellBid, diSellBid, 100, market.TradeTypeImport),
	}
}

func mustTradeGood(symbol, supply string, ask, bid, volume int, tradeType market.TradeType) *market.TradeGood {
	activity := "STRONG"
	good, err := market.NewTradeGood(symbol, &supply, &activity, ask, bid, volume, tradeType)
	if err != nil {
		panic(err)
	}
	return good
}

// diMediator models the API's sell contract: a good the market does not list is refused
// with the wrapper the cargo handler produces, and a successful sell removes the units
// from the persisted hold. Purchases (the output harvest) are priced at diOutputAsk.
type diMediator struct {
	repo        *dockRaceShipRepo
	dockHandler *tactics.DockShipHandler
	listed      map[string]bool
	rejectSell  map[string]error // goods the API refuses even though they are listed

	mu    sync.Mutex
	sells []sellRecord
}

type sellRecord struct {
	good  string
	units int
}

func (m *diMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipTypes.DockShipCommand:
		return m.dockHandler.Handle(ctx, cmd)

	case *shipNav.NavigateRouteCommand:
		m.repo.arriveInOrbit(cmd.Destination)
		return nil, nil

	case *shipCargo.PurchaseCargoCommand:
		return &shipCargo.PurchaseCargoResponse{
			TotalCost: cmd.Units * diOutputAsk, UnitsAdded: cmd.Units, TransactionCount: 1,
		}, nil

	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		m.sells = append(m.sells, sellRecord{good: cmd.GoodSymbol, units: cmd.Units})
		m.mu.Unlock()

		if err, refused := m.rejectSell[cmd.GoodSymbol]; refused {
			return nil, err
		}
		if !m.listed[cmd.GoodSymbol] {
			return nil, fmt.Errorf("partial failure: failed to sell cargo after 0 successful transactions "+
				"(0 units processed, 0 credits): API error 400: market does not list %s", cmd.GoodSymbol)
		}
		m.repo.removeCargo(cmd.GoodSymbol, cmd.Units)
		return &shipCargo.SellCargoResponse{
			TotalRevenue: cmd.Units * diSellBid, UnitsSold: cmd.Units, TransactionCount: 1,
		}, nil

	default:
		return nil, nil
	}
}

func (m *diMediator) Register(reflect.Type, common.RequestHandler) error { return nil }
func (m *diMediator) RegisterMiddleware(common.Middleware)               {}

func (m *diMediator) sellsOf(good string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, s := range m.sells {
		if s.good == good {
			total += s.units
		}
	}
	return total
}

func (m *diMediator) sellAttemptsFor(good string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, s := range m.sells {
		if s.good == good {
			count++
		}
	}
	return count
}

// newDeliverInputsExecutor docks a hauler at the factory carrying hold and wires the
// executor over it. The market's own listings decide which sells the API will accept;
// rejectSell refuses a listed good anyway, modelling an API rejection the listing cannot
// predict.
func newDeliverInputsExecutor(
	t *testing.T,
	marketRepo *diMarketRepo,
	hold []*shared.CargoItem,
	rejectSell map[string]error,
) (*ProductionExecutor, *dockRaceShipRepo, *diMediator) {
	t.Helper()

	shipRepo := &dockRaceShipRepo{
		location:      diFactoryWP,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 40, // the hull capacity dockRaceShipRepo builds its ships with
	}
	shipRepo.fillCargo(hold)

	listed := map[string]bool{diOutput: true, diInput: true}
	for _, good := range marketRepo.extraGoods {
		listed[good.Symbol()] = true
	}

	mediator := &diMediator{
		repo:        shipRepo,
		dockHandler: tactics.NewDockShipHandler(shipRepo),
		listed:      listed,
		rejectSell:  rejectSell,
	}
	executor := NewProductionExecutorWithConfig(
		mediator, shipRepo, marketRepo, NewMarketLocator(marketRepo, nil, nil, nil),
		&dockRaceClock{}, []time.Duration{time.Millisecond}, nil,
	)
	return executor, shipRepo, mediator
}

// deliverInputsCtx marks the run construction-supply so the resale-margin guards (which
// govern a different decision) stay out of the way of the delivery under test.
func deliverInputsCtx() context.Context {
	return shared.WithConstructionSupply(common.WithLogger(context.Background(), &dwellCapturingLogger{}))
}

func heldUnits(repo *dockRaceShipRepo, good string) int {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, item := range repo.cargoInventory {
		if item.Symbol == good {
			return item.Units
		}
	}
	return 0
}

// THE REGRESSION. A hold mixing an input the factory imports with a good it does not list
// must deliver the sellable one and complete the step. Under the hard-return this aborted
// at EXPLOSIVES with "failed to deliver inputs" and the hull never sourced again.
func TestProduceGood_MixedHold_DeliversSellableInputAndDoesNotAbort(t *testing.T) {
	executor, repo, mediator := newDeliverInputsExecutor(t, &diMarketRepo{},
		[]*shared.CargoItem{mustCargoItem(diInput, 20), mustCargoItem(diPoison, 15)}, nil)

	node := goods.NewSupplyChainNode(diOutput, goods.AcquisitionFabricate)
	result, err := executor.ProduceGood(deliverInputsCtx(), repo.buildShip(), node, diSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("a good this factory will not take must not abort the sourcing step: %v", err)
	}
	if got := mediator.sellsOf(diInput); got != 20 {
		t.Fatalf("the imported input must be delivered in full: expected 20 units sold, got %d", got)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("the step must go on to harvest the output, got %+v", result)
	}

	// Revenue from the partial delivery is accounted, not discarded: the harvest cost
	// (diOutputVol units at diOutputAsk) is offset by what the input sale earned.
	wantCost := diOutputVol*diOutputAsk - 20*diSellBid
	if result.TotalCost != wantCost {
		t.Fatalf("delivery revenue must offset the harvest cost: expected TotalCost %d, got %d", wantCost, result.TotalCost)
	}

	// Not jettisoned — the unsellable good rides on.
	if got := heldUnits(repo, diPoison); got != 15 {
		t.Fatalf("the good this market will not buy must stay aboard: expected 15 units of %s, got %d", diPoison, got)
	}
}

// A hold with NOTHING this factory takes is a no-op, not an error: identical to docking
// empty, which the step has always accepted. The run proceeds to the harvest.
func TestProduceGood_WhollyUnsellableHold_IsNoOpNotError(t *testing.T) {
	executor, repo, mediator := newDeliverInputsExecutor(t, &diMarketRepo{},
		[]*shared.CargoItem{mustCargoItem(diPoison, 15)}, nil)

	node := goods.NewSupplyChainNode(diOutput, goods.AcquisitionFabricate)
	result, err := executor.ProduceGood(deliverInputsCtx(), repo.buildShip(), node, diSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("a wholly unsellable hold must be a no-op, not an error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("the step must still harvest the output, got %+v", result)
	}
	if result.TotalCost != diOutputVol*diOutputAsk {
		t.Fatalf("with nothing delivered the cost is the bare harvest: expected %d, got %d", diOutputVol*diOutputAsk, result.TotalCost)
	}
	if got := mediator.sellAttemptsFor(diPoison); got != 0 {
		t.Fatalf("an unlisted good must not even be offered, got %d sell attempts", got)
	}
}

// The factory's OWN product aboard must not be dumped at the factory: an EXPORT listing is
// its export bid, and selling into it ladders that bid down against us.
func TestProduceGood_FactoryOwnOutputAboard_IsNotDumpedAtTheFactory(t *testing.T) {
	executor, repo, mediator := newDeliverInputsExecutor(t, &diMarketRepo{},
		[]*shared.CargoItem{mustCargoItem(diOutput, 12), mustCargoItem(diInput, 20)}, nil)

	node := goods.NewSupplyChainNode(diOutput, goods.AcquisitionFabricate)
	_, err := executor.ProduceGood(deliverInputsCtx(), repo.buildShip(), node, diSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("delivering alongside the factory's own product must not error: %v", err)
	}
	if got := mediator.sellAttemptsFor(diOutput); got != 0 {
		t.Fatalf("the factory's own export must never be sold back to it, got %d sell attempts", got)
	}
	if got := mediator.sellsOf(diInput); got != 20 {
		t.Fatalf("the imported input must still be delivered, got %d units", got)
	}
	if got := heldUnits(repo, diOutput); got != 12 {
		t.Fatalf("the fabricated output must stay aboard for its resale sink, got %d units", got)
	}
}

// A listed good the API refuses anyway (the over-trade-volume 400 the handler surfaces as
// "0 successful transactions") is held aboard and the rest of the hold still delivers —
// the tolerance does not depend on the listing filter predicting the refusal.
func TestProduceGood_ListedGoodRefusedByAPI_IsToleratedNotFatal(t *testing.T) {
	refused := mustTradeGood(diPoison, "SCARCE", diSellBid, diSellBid, 20, market.TradeTypeImport)
	rejection := map[string]error{
		diPoison: fmt.Errorf("partial failure: failed to sell cargo after 0 successful transactions " +
			"(0 units processed, 0 credits): API error 400: trade volume exceeded"),
	}
	executor, repo, mediator := newDeliverInputsExecutor(t,
		&diMarketRepo{extraGoods: []market.TradeGood{*refused}},
		[]*shared.CargoItem{mustCargoItem(diPoison, 20), mustCargoItem(diInput, 20)}, rejection)

	node := goods.NewSupplyChainNode(diOutput, goods.AcquisitionFabricate)
	result, err := executor.ProduceGood(deliverInputsCtx(), repo.buildShip(), node, diSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("an API-refused sell must not abort the sourcing step: %v", err)
	}
	if got := mediator.sellAttemptsFor(diPoison); got != 1 {
		t.Fatalf("a listed good must be offered exactly once, got %d attempts", got)
	}
	if got := mediator.sellsOf(diInput); got != 20 {
		t.Fatalf("the rest of the hold must still deliver, got %d units of %s", got, diInput)
	}
	wantCost := diOutputVol*diOutputAsk - 20*diSellBid
	if result == nil || result.TotalCost != wantCost {
		t.Fatalf("only the delivered good's revenue counts: expected TotalCost %d, got %+v", wantCost, result)
	}
	if got := heldUnits(repo, diPoison); got != 20 {
		t.Fatalf("the refused good must stay aboard, got %d units", got)
	}
}

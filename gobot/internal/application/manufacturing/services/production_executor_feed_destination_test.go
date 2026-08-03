package services

import (
	"context"
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

// sp-b27a2, driven through the real ProduceGood -> fabricateGood path. The incident was a hauler
// navigated to the factory that EXPORTS the good being produced, on the assumption that the same
// factory imports that good's inputs. Across chains it does not: IRON_ORE (FAB_MATS chain) was
// taken to the ADVANCED_CIRCUITRY exporter, which imports nothing from that chain, and the hauler
// then sat at 80/80 unable to deliver or dump.
//
// The observable under test is the NAVIGATE, not the sell. deliverInputs already holds cargo the
// local market will not buy (sp-w2qg5) — by the time it runs the hull is already at the wrong
// waypoint, so it is the victim of the mis-route rather than its cause.

const (
	fdSystem    = "X1-FD"
	fdFactoryWP = "X1-FD-FACTORY"
	fdOrigin    = "X1-FD-ORIGIN"
	fdOutput    = "ELECTRONICS" // recipe: SILICON_CRYSTALS + COPPER (goods.ExportToImportMap)
)

// fdMarketRepo serves one factory exporting fdOutput plus one source market per input. The
// factory's own IMPORT listings are the variable under test: importsInputs switches the
// destination between a same-chain factory that will take the feedstock and a wrong-chain one
// that will not, with nothing else about the fixture changing.
type fdMarketRepo struct {
	market.MarketRepository
	importsInputs bool
	inputs        []feedInputSpec
}

func (r *fdMarketRepo) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	waypoints := []string{fdFactoryWP}
	for _, in := range r.inputs {
		waypoints = append(waypoints, in.waypoint)
	}
	return waypoints, nil
}

// No resale sink: these runs are construction-supply, which scopes out the resale-margin guards
// and isolates the navigate decision under test.
func (r *fdMarketRepo) FindBestMarketBuying(_ context.Context, _, _ string, _ int) (*market.BestMarketBuyingResult, error) {
	return nil, nil
}

func (r *fdMarketRepo) GetMarketData(_ context.Context, waypointSymbol string, _ int) (*market.Market, error) {
	activity := "STRONG"
	if waypointSymbol == fdFactoryWP {
		// MODERATE, not HIGH/ABUNDANT: the already-stocked shortcut must not fire, or the run
		// never reaches the feed path at all.
		supply := "MODERATE"
		output, err := market.NewTradeGood(fdOutput, &supply, &activity, 50, 50, 10, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		rows := []market.TradeGood{*output}
		if r.importsInputs {
			for _, in := range r.inputs {
				inputSupply := in.supply
				listed, err := market.NewTradeGood(in.good, &inputSupply, &activity, in.ask, in.ask, in.tradeVolume, market.TradeTypeImport)
				if err != nil {
					return nil, err
				}
				rows = append(rows, *listed)
			}
		}
		return market.NewMarket(waypointSymbol, rows, time.Now())
	}
	for _, in := range r.inputs {
		if in.waypoint == waypointSymbol {
			supply := in.supply
			source, err := market.NewTradeGood(in.good, &supply, &activity, in.ask, in.ask, in.tradeVolume, market.TradeTypeExport)
			if err != nil {
				return nil, err
			}
			return market.NewMarket(waypointSymbol, []market.TradeGood{*source}, time.Now())
		}
	}
	return nil, nil
}

// fdMediator records every waypoint the executor actually navigated to. That list is the
// falsifier: the guard's whole job is that the factory never appears in it.
type fdMediator struct {
	repo        *dockRaceShipRepo
	dockHandler *tactics.DockShipHandler
	mu          sync.Mutex
	navigatedTo []string
	sold        []string
	spent       int
}

func (m *fdMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipTypes.DockShipCommand:
		return m.dockHandler.Handle(ctx, cmd)
	case *shipNav.NavigateRouteCommand:
		m.mu.Lock()
		m.navigatedTo = append(m.navigatedTo, cmd.Destination)
		m.mu.Unlock()
		m.repo.arriveInOrbit(cmd.Destination)
		return nil, nil
	case *shipCargo.PurchaseCargoCommand:
		cost := cmd.Units * 10
		m.mu.Lock()
		m.spent += cost
		m.mu.Unlock()
		return &shipCargo.PurchaseCargoResponse{TotalCost: cost, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		m.sold = append(m.sold, cmd.GoodSymbol)
		m.mu.Unlock()
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * 5, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil
	}
}

func (m *fdMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }
func (m *fdMediator) RegisterMiddleware(common.Middleware)                   {}

func (m *fdMediator) navigatedToFactory() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, destination := range m.navigatedTo {
		if destination == fdFactoryWP {
			return true
		}
	}
	return false
}

func (m *fdMediator) sellCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sold)
}

func (m *fdMediator) creditsSpent() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spent
}

func newFeedDestinationRun(t *testing.T, importsInputs bool) (*ProductionExecutor, *fdMediator, *navigation.Ship) {
	t.Helper()
	repo := &fdMarketRepo{
		importsInputs: importsInputs,
		inputs: []feedInputSpec{
			{good: "SILICON_CRYSTALS", waypoint: "X1-FD-SILICON", supply: "MODERATE", tradeVolume: 100, ask: 10},
			{good: "COPPER", waypoint: "X1-FD-COPPER", supply: "MODERATE", tradeVolume: 100, ask: 10},
		},
	}
	// The hull is LOADED with the inputs before it decides where to go — that is the whole
	// hazard. An empty hold would make the "nothing was delivered" assertion below vacuous:
	// deliverInputs sells nothing from an empty hold whether the guard fires or not.
	shipRepo := &dockRaceShipRepo{
		location: fdOrigin, navStatus: navigation.NavStatusDocked,
		cargoCapacity: 400, cargoUnits: 20,
		cargoInventory: []*shared.CargoItem{
			{Symbol: "SILICON_CRYSTALS", Name: "SILICON_CRYSTALS", Units: 10},
			{Symbol: "COPPER", Name: "COPPER", Units: 10},
		},
	}
	mediator := &fdMediator{repo: shipRepo, dockHandler: tactics.NewDockShipHandler(shipRepo)}
	executor := NewProductionExecutorWithConfig(
		mediator, shipRepo, repo, NewMarketLocator(repo, nil, nil, nil), &dockRaceClock{},
		[]time.Duration{time.Millisecond}, nil,
	)
	return executor, mediator, shipRepo.buildShip()
}

func feedDestinationChain() *goods.SupplyChainNode {
	root := goods.NewSupplyChainNode(fdOutput, goods.AcquisitionFabricate)
	root.AddChild(goods.NewSupplyChainNode("SILICON_CRYSTALS", goods.AcquisitionBuy))
	root.AddChild(goods.NewSupplyChainNode("COPPER", goods.AcquisitionBuy))
	return root
}

func feedDestinationCtx() context.Context {
	return shared.WithConstructionSupply(common.WithLogger(context.Background(), &dwellCapturingLogger{}))
}

// THE FIX. The factory exports ELECTRONICS but imports nothing, so the inputs about to be carried
// there can neither be delivered nor dumped. The run must refuse the navigate outright — not
// navigate and hope, and not silently substitute another waypoint, which is how feedstock ends up
// somewhere it cannot be used.
func TestFabricate_RefusesToNavigateToAFactoryThatCannotAcceptTheInputs(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, false)

	result, err := executor.ProduceGood(feedDestinationCtx(), ship, feedDestinationChain(), fdSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("a refused feed destination must park the run, not error it: %v", err)
	}
	if mediator.navigatedToFactory() {
		t.Fatalf("the hauler was navigated to %s, which imports none of the inputs it is carrying — this is the sp-b27a2 stranding", fdFactoryWP)
	}
	if mediator.sellCount() != 0 {
		t.Fatalf("nothing may be delivered at a destination the run refused to navigate to, got %d sells", mediator.sellCount())
	}
	if result == nil || result.QuantityAcquired != 0 {
		t.Fatalf("a refused feed destination must yield a zero-quantity result, got %+v", result)
	}
	// The refusal lands AFTER the inputs were bought, unlike every earlier park in this function,
	// so the run must report the credits it really spent. Zeroing it here would understate the
	// chain's cost to the caller that sums these results.
	if spent := mediator.creditsSpent(); spent == 0 {
		t.Fatal("fixture is inert: the run bought no inputs, so it cannot show whether their cost is reported honestly")
	} else if result.TotalCost != spent {
		t.Fatalf("a refused run reported TotalCost %d but actually spent %d on inputs already aboard", result.TotalCost, spent)
	}
}

// The guard's subject is the cargo this run is HAULING, not the good's recipe. A childless
// FABRICATE node acquires nothing — it delivers whatever is already aboard and harvests — so there
// is no feedstock to strand and the trip must go ahead, even though the factory imports neither of
// the two goods ELECTRONICS's recipe names.
//
// Judging such a run against goods.ExportToImportMap instead parks it over goods it was never
// carrying, and would also reverse sp-w2qg5: unsellable cargo aboard rides on, it does not veto
// the trip. The four TestProduceGood_* delivery tests are the ones that would break, but they
// break with a delivery-shaped message; this test names the cause.
func TestFabricate_ChildlessNodeIsJudgedOnWhatItHauls_NotOnTheRecipe(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, false)

	childless := goods.NewSupplyChainNode(fdOutput, goods.AcquisitionFabricate)
	if _, err := executor.ProduceGood(feedDestinationCtx(), ship, childless, fdSystem, 1, nil, false); err != nil {
		t.Fatalf("a run that hauls no inputs must not be refused: %v", err)
	}
	if !mediator.navigatedToFactory() {
		t.Fatalf("a childless node hauls nothing, so nothing can strand at %s — the guard is judging the recipe instead of the cargo", fdFactoryWP)
	}
}

// The falsifier for a blanket refusal. Identical fixture except the factory now IMPORTS both
// inputs — the same-chain case that is the overwhelming majority in production — and the run must
// proceed to the factory and deliver. Without this, a guard that always refuses would pass the
// test above and silently park every fabrication in the game.
func TestFabricate_NavigatesWhenTheFactoryImportsTheInputs(t *testing.T) {
	executor, mediator, ship := newFeedDestinationRun(t, true)

	if _, err := executor.ProduceGood(feedDestinationCtx(), ship, feedDestinationChain(), fdSystem, 1, nil, false); err != nil {
		t.Fatalf("a factory that imports every input must be fed without error: %v", err)
	}
	if !mediator.navigatedToFactory() {
		t.Fatalf("the hauler never reached %s even though it imports every input being carried — the guard is refusing the same-chain case it must allow", fdFactoryWP)
	}
	if mediator.sellCount() == 0 {
		t.Fatal("no input was delivered at a factory that imports every one of them")
	}
}

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

// sp-to2v — fabrication efficiency, driven through the real fabricateGood/ProduceGood path. These
// pin the executor's OBSERVABLE feeding behavior when the policy is engaged: the ample input is
// pulled down to the scarce (limiting) input's flow rather than greedily piled on (#2), each
// delivery is saturation-capped (#3), the scarcest input is fed FIRST (#4a), a non-responsive
// output is BUY-OR-SKIPed instead of fed (#4b), and — with the policy OFF — feeding is byte-identical
// to today's greedy single-trade-volume tranche.

const (
	fpFactoryWP = "X1-FP-FAB"
	fpScarceWP  = "X1-FP-SCARCE"
	fpAmpleWP   = "X1-FP-AMPLE"
	fpSystem    = "X1-FP"
	fpScarce    = "SILICON_CRYSTALS" // the limiting (scarcer) input in the analyst's COPPER-into-ELECTRONICS example
	fpAmple     = "COPPER"           // the ample input greedily over-fed today
)

type feedInputSpec struct {
	good, waypoint, supply string
	tradeVolume, ask       int
}

// feedingMarketRepo prices a fabrication: a factory exporting outputGood (at a non-abundant supply so
// the fabricate/feed path runs, not the already-stocked shortcut) plus one source market per input,
// each with its own supply/trade-volume so the balanced-feed planner sizes the tranche to the
// scarcest. No resale sink is served (nil FindBestMarketBuying) — the feeding runs are construction-
// supply, which scopes out the resale-margin guards, isolating the feeding behavior under test.
type feedingMarketRepo struct {
	market.MarketRepository
	outputGood   string
	factoryWP    string
	outputSupply string
	fabAsk       int
	inputs       []feedInputSpec
}

func (r *feedingMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	wps := []string{r.factoryWP}
	for _, in := range r.inputs {
		wps = append(wps, in.waypoint)
	}
	return wps, nil
}

func (r *feedingMarketRepo) FindBestMarketBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.BestMarketBuyingResult, error) {
	return nil, nil
}

func (r *feedingMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	activity := "STRONG"
	if waypointSymbol == r.factoryWP {
		supply := r.outputSupply
		g, err := market.NewTradeGood(r.outputGood, &supply, &activity, r.fabAsk, r.fabAsk, 10, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*g}, time.Now())
	}
	for _, in := range r.inputs {
		if in.waypoint == waypointSymbol {
			supply := in.supply
			g, err := market.NewTradeGood(in.good, &supply, &activity, in.ask, in.ask, in.tradeVolume, market.TradeTypeExport)
			if err != nil {
				return nil, err
			}
			return market.NewMarket(waypointSymbol, []market.TradeGood{*g}, time.Now())
		}
	}
	return nil, nil
}

// feedingMediator records the per-good purchase quantities and order so a test can observe how much
// of each input the executor bought and in which order. Navigation and docking route through the
// real handlers (as the dock-race fakes do); purchases return the requested units so the recorded
// quantity is exactly what buyGood asked for.
type feedingMediator struct {
	repo        *dockRaceShipRepo
	dockHandler *tactics.DockShipHandler
	mu          sync.Mutex
	purchases   []purchaseRecord
}

type purchaseRecord struct {
	good  string
	units int
}

func (m *feedingMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipTypes.DockShipCommand:
		return m.dockHandler.Handle(ctx, cmd)
	case *shipNav.NavigateRouteCommand:
		m.repo.arriveInOrbit(cmd.Destination)
		return nil, nil
	case *shipCargo.PurchaseCargoCommand:
		m.mu.Lock()
		m.purchases = append(m.purchases, purchaseRecord{good: cmd.GoodSymbol, units: cmd.Units})
		m.mu.Unlock()
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * 10, UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * 5, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil
	}
}

func (m *feedingMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *feedingMediator) RegisterMiddleware(common.Middleware) {}

func (m *feedingMediator) unitsFor(good string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, p := range m.purchases {
		if p.good == good {
			total += p.units
		}
	}
	return total
}

func (m *feedingMediator) firstIndexOf(good string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, p := range m.purchases {
		if p.good == good {
			return i
		}
	}
	return -1
}

func (m *feedingMediator) totalPurchases() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.purchases)
}

func newFeedingExecutor(t *testing.T, repo *feedingMarketRepo) (*ProductionExecutor, *feedingMediator) {
	t.Helper()
	shipRepo := &dockRaceShipRepo{location: dockRaceOrigin, navStatus: navigation.NavStatusDocked, cargoCapacity: 400}
	mediator := &feedingMediator{repo: shipRepo, dockHandler: tactics.NewDockShipHandler(shipRepo)}
	marketLocator := NewMarketLocator(repo, nil, nil, nil)
	executor := NewProductionExecutorWithConfig(
		mediator, shipRepo, repo, marketLocator, &dockRaceClock{}, []time.Duration{time.Millisecond}, nil,
	)
	return executor, mediator
}

func twoInputChain(output string) *goods.SupplyChainNode {
	root := goods.NewSupplyChainNode(output, goods.AcquisitionFabricate)
	root.AddChild(goods.NewSupplyChainNode(fpScarce, goods.AcquisitionBuy))
	root.AddChild(goods.NewSupplyChainNode(fpAmple, goods.AcquisitionBuy))
	return root
}

// balancedFeedingRepo: SILICON MODERATE (supply-aware avail 0.40*100=40, the limiter) + COPPER
// ABUNDANT (avail 0.80*100=80), feeding a responsive ELECTRONICS factory.
func balancedFeedingRepo() *feedingMarketRepo {
	return &feedingMarketRepo{
		outputGood: "ELECTRONICS", factoryWP: fpFactoryWP, outputSupply: "MODERATE", fabAsk: 50,
		inputs: []feedInputSpec{
			{good: fpScarce, waypoint: fpScarceWP, supply: "MODERATE", tradeVolume: 100, ask: 10},
			{good: fpAmple, waypoint: fpAmpleWP, supply: "ABUNDANT", tradeVolume: 100, ask: 10},
		},
	}
}

func feedingCtx(policy bool) context.Context {
	ctx := shared.WithConstructionSupply(common.WithLogger(context.Background(), &dwellCapturingLogger{}))
	if policy {
		// saturation window [5,200] so the balanced tranche (=limiter 40) passes through un-clamped and
		// the ample input is visibly pulled from its full trade volume down to the limiter's flow.
		ctx = WithFeedingPolicy(ctx, 200, 5, nil, false)
	}
	return ctx
}

// #2 (the ~4x lever): with the policy engaged, the ample COPPER input is fed the SAME balanced
// tranche as the limiting SILICON input (both = the limiter's 40-unit flow), NOT its full 100-unit
// trade volume — proving inputs are fed balanced-to-the-scarcest, not greedily piled onto the ample
// one.
func TestFeeding_BalancedToLimiting_CapsAmpleInput(t *testing.T) {
	executor, mediator := newFeedingExecutor(t, balancedFeedingRepo())

	_, err := executor.ProduceGood(feedingCtx(true), balancedFeedingRepo0Ship(), twoInputChain("ELECTRONICS"), fpSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("balanced feeding must not error: %v", err)
	}
	if got := mediator.unitsFor(fpAmple); got != 40 {
		t.Fatalf("the ample COPPER input must be balanced down to the limiting flow (40), not greedily bought at its full trade volume (100), got %d", got)
	}
	if got := mediator.unitsFor(fpScarce); got != 40 {
		t.Fatalf("the limiting SILICON input should be fed its balanced tranche (40), got %d", got)
	}
}

// Regression / OFF path: with NO policy stamped, feeding is byte-identical to today — the ample
// COPPER input is bought at its full trade volume (100), the greedy behavior the policy improves on.
func TestFeeding_PolicyOff_GreedyByteIdentical(t *testing.T) {
	executor, mediator := newFeedingExecutor(t, balancedFeedingRepo())

	_, err := executor.ProduceGood(feedingCtx(false), balancedFeedingRepo0Ship(), twoInputChain("ELECTRONICS"), fpSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("greedy feeding must not error: %v", err)
	}
	if got := mediator.unitsFor(fpAmple); got != 100 {
		t.Fatalf("with the policy OFF the ample input must keep the greedy full-trade-volume buy (100), got %d", got)
	}
}

// #4a (taproot-first): the scarcer SILICON input gates everything above it, so with the policy
// engaged it is fed BEFORE the ample COPPER input.
func TestFeeding_TaprootFirst_ScarcestInputFedFirst(t *testing.T) {
	executor, mediator := newFeedingExecutor(t, balancedFeedingRepo())

	_, err := executor.ProduceGood(feedingCtx(true), balancedFeedingRepo0Ship(), twoInputChain("ELECTRONICS"), fpSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("taproot-first feeding must not error: %v", err)
	}
	scarceIdx, ampleIdx := mediator.firstIndexOf(fpScarce), mediator.firstIndexOf(fpAmple)
	if scarceIdx < 0 || ampleIdx < 0 || scarceIdx > ampleIdx {
		t.Fatalf("the scarcest (taproot) input must be fed first: SILICON@%d must precede COPPER@%d", scarceIdx, ampleIdx)
	}
}

// #3 (saturation cap): with BOTH inputs ample but a small saturation window (max 30), each per-input
// delivery is capped at the saturation ceiling (30) — Δactivity rolls off past saturation, so a hull
// never dumps a node past it.
func TestFeeding_SaturationCap_CapsBothAmpleInputs(t *testing.T) {
	repo := &feedingMarketRepo{
		outputGood: "ELECTRONICS", factoryWP: fpFactoryWP, outputSupply: "MODERATE", fabAsk: 50,
		inputs: []feedInputSpec{
			{good: fpScarce, waypoint: fpScarceWP, supply: "ABUNDANT", tradeVolume: 100, ask: 10}, // avail 80
			{good: fpAmple, waypoint: fpAmpleWP, supply: "ABUNDANT", tradeVolume: 100, ask: 10},   // avail 80
		},
	}
	executor, mediator := newFeedingExecutor(t, repo)
	ctx := WithFeedingPolicy(shared.WithConstructionSupply(common.WithLogger(context.Background(), &dwellCapturingLogger{})), 30, 5, nil, false)

	_, err := executor.ProduceGood(ctx, balancedFeedingRepo0Ship(), twoInputChain("ELECTRONICS"), fpSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("saturation-capped feeding must not error: %v", err)
	}
	if got := mediator.unitsFor(fpAmple); got != 30 {
		t.Fatalf("an ample input must be capped at the saturation ceiling (30), got %d", got)
	}
	if got := mediator.unitsFor(fpScarce); got != 30 {
		t.Fatalf("both ample inputs saturation-cap at 30, got %d for the second", got)
	}
}

// #4b (feed-responsive only): a NON-responsive output (EQUIPMENT) does not respond to feeding, so
// with the policy engaged the executor BUYS-OR-SKIPs it — it does NOT haul inputs to feed the
// factory (zero input purchases, a zero-spend result).
func TestFeeding_NonResponsiveGood_BuyOrSkipNotFed(t *testing.T) {
	repo := &feedingMarketRepo{
		outputGood: "EQUIPMENT", factoryWP: fpFactoryWP, outputSupply: "MODERATE", fabAsk: 50,
		inputs: []feedInputSpec{
			{good: fpScarce, waypoint: fpScarceWP, supply: "MODERATE", tradeVolume: 100, ask: 10},
			{good: fpAmple, waypoint: fpAmpleWP, supply: "ABUNDANT", tradeVolume: 100, ask: 10},
		},
	}
	executor, mediator := newFeedingExecutor(t, repo)

	result, err := executor.ProduceGood(feedingCtx(true), balancedFeedingRepo0Ship(), twoInputChain("EQUIPMENT"), fpSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("a non-responsive buy-or-skip must not error: %v", err)
	}
	if mediator.totalPurchases() != 0 {
		t.Fatalf("a non-responsive good must NOT be fed (zero input hauls), got %d purchases", mediator.totalPurchases())
	}
	if result == nil || result.QuantityAcquired != 0 {
		t.Fatalf("a skipped non-responsive good must yield a zero-spend result, got %+v", result)
	}
}

// Positive: a responsive output (ELECTRONICS) IS fed — the inputs are actually hauled — so the
// buy-or-skip short-circuit is scoped strictly to the non-responsive set.
func TestFeeding_ResponsiveGood_IsFed(t *testing.T) {
	executor, mediator := newFeedingExecutor(t, balancedFeedingRepo())

	_, err := executor.ProduceGood(feedingCtx(true), balancedFeedingRepo0Ship(), twoInputChain("ELECTRONICS"), fpSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("a responsive good must be fed without error: %v", err)
	}
	if mediator.unitsFor(fpScarce) <= 0 || mediator.unitsFor(fpAmple) <= 0 {
		t.Fatalf("a responsive good must have BOTH inputs fed, got SILICON=%d COPPER=%d", mediator.unitsFor(fpScarce), mediator.unitsFor(fpAmple))
	}
}

// balancedFeedingRepo0Ship builds a fresh DOCKED hauler at the origin with a large empty hold so the
// hold never binds the feed sizing under test.
func balancedFeedingRepo0Ship() *navigation.Ship {
	repo := &dockRaceShipRepo{location: dockRaceOrigin, navStatus: navigation.NavStatusDocked, cargoCapacity: 400}
	return repo.buildShip()
}

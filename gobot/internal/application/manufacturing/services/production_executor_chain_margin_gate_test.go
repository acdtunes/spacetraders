package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-iv65 fix-shape, 2nd half — the chain-level negative-margin gate. Even below the
// per-buy ceiling, a fabrication whose summed input ask already exceeds its output's resale
// bid loses money every cycle. This suite pins inputRoundMarginParked (the executor-level,
// live-at-buy-time re-check of ChainMarginGuard's launch-time verdict) and its wiring into
// fabricateGood: PARK the input round when underwater, resume when the bid recovers, and
// fail OPEN on an unpriceable sink so intermediate feeds (delivered to a parent, never
// resold) are not over-parked.

const (
	cgOutput = "ADVANCED_CIRCUITRY"
	cgInput1 = "ELECTRONICS"
	cgInput2 = "MICROPROCESSORS"
	cgSrcWP  = "X1-CG-SRC"
	cgSinkWP = "X1-CG-SINK"
	cgFabWP  = "X1-CG-FAB"
	cgSystem = "X1-CG"
)

// chainGateMarketRepo prices a 2-input fabrication chain across three waypoints: a source
// exporting the inputs, a factory exporting the output at MODERATE supply (so the fabricate
// path is taken, not the already-stocked shortcut), and a resale sink buying the output.
type chainGateMarketRepo struct {
	market.MarketRepository
	sinkBid   int            // output resale bid (FindBestMarketBuying for the output)
	inputAsks map[string]int // input good -> export ask (SellPrice)
	fabAsk    int            // factory's export ask for the output
	noSink    bool           // model an unpriceable resale sink
}

func (r *chainGateMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{cgSrcWP, cgSinkWP, cgFabWP}, nil
}

func (r *chainGateMarketRepo) FindBestMarketBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.BestMarketBuyingResult, error) {
	if goodSymbol == cgOutput && !r.noSink {
		return &market.BestMarketBuyingResult{
			WaypointSymbol: cgSinkWP,
			TradeSymbol:    goodSymbol,
			PurchasePrice:  r.sinkBid,
			Supply:         "HIGH",
		}, nil
	}
	return nil, nil
}

func (r *chainGateMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "STRONG"
	switch waypointSymbol {
	case cgSrcWP:
		goodsList := make([]market.TradeGood, 0, len(r.inputAsks))
		for sym, ask := range r.inputAsks {
			g, err := market.NewTradeGood(sym, &supply, &activity, ask, ask, 10, market.TradeTypeExport)
			if err != nil {
				return nil, err
			}
			goodsList = append(goodsList, *g)
		}
		return market.NewMarket(waypointSymbol, goodsList, time.Now())
	case cgSinkWP:
		// The output as an IMPORT good so FindImportMarket's scannedTradeGood resolves a
		// trade volume; the bid itself comes from FindBestMarketBuying above.
		g, err := market.NewTradeGood(cgOutput, &supply, &activity, r.sinkBid, r.sinkBid, 10, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*g}, time.Now())
	case cgFabWP:
		// The factory EXPORTS the output at MODERATE supply → Step 0's FindExportMarket
		// resolves here and collectExistingFactorySupply proceeds to fabricate.
		g, err := market.NewTradeGood(cgOutput, &supply, &activity, r.fabAsk, r.fabAsk, 10, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*g}, time.Now())
	default:
		return nil, nil
	}
}

func newChainGateExecutor(t *testing.T, repo *chainGateMarketRepo) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
	t.Helper()
	shipRepo := &dockRaceShipRepo{location: dockRaceOrigin, navStatus: navigation.NavStatusDocked, cargoCapacity: 40}
	mediator := &dockRaceMediator{repo: shipRepo, dockHandler: tactics.NewDockShipHandler(shipRepo)}
	marketLocator := NewMarketLocator(repo, nil, nil, nil)
	executor := NewProductionExecutorWithConfig(
		mediator, shipRepo, repo, marketLocator, &dockRaceClock{}, []time.Duration{time.Millisecond}, nil,
	)
	return executor, shipRepo, mediator
}

func cgChain() *goods.SupplyChainNode {
	root := goods.NewSupplyChainNode(cgOutput, goods.AcquisitionFabricate)
	root.AddChild(goods.NewSupplyChainNode(cgInput1, goods.AcquisitionBuy))
	root.AddChild(goods.NewSupplyChainNode(cgInput2, goods.AcquisitionBuy))
	return root
}

// The ADV_CIRC shape below the ceiling: two inputs at ~19k each sum to ~37.7k against a
// ~7.5k output bid — fabricating loses ~30k/cycle. The gate must PARK and name the numbers.
func TestInputRoundMargin_ParksWhenSummedInputAskExceedsOutputBid(t *testing.T) {
	repo := &chainGateMarketRepo{sinkBid: 7500, fabAsk: 7000, inputAsks: map[string]int{cgInput1: 19000, cgInput2: 18700}}
	executor, _, _ := newChainGateExecutor(t, repo)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	if !executor.inputRoundMarginParked(ctx, cgChain(), cgSystem, 1) {
		t.Fatalf("summed input ask 37700 above output bid 7500 must PARK the input round")
	}
	warns := logger.entriesWithLevel("WARNING")
	if !spendFloorWarnContains(warns, "37700") || !spendFloorWarnContains(warns, "7500") {
		t.Fatalf("expected a WARNING carrying the summed ask (37700) and output bid (7500), got: %+v", warns)
	}
	if !spendFloorWarnContains(warns, cgOutput) {
		t.Fatalf("expected the park WARNING to name the output %s, got: %+v", cgOutput, warns)
	}
}

// A healthy spread — cheap inputs, high output bid — must PROCEED: sum 3500 < bid 7500.
func TestInputRoundMargin_ProceedsWhenSummedInputAskUnderOutputBid(t *testing.T) {
	repo := &chainGateMarketRepo{sinkBid: 7500, fabAsk: 7000, inputAsks: map[string]int{cgInput1: 2000, cgInput2: 1500}}
	executor, _, _ := newChainGateExecutor(t, repo)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	if executor.inputRoundMarginParked(ctx, cgChain(), cgSystem, 1) {
		t.Fatalf("summed input ask 3500 below output bid 7500 must PROCEED")
	}
	if len(logger.entriesWithLevel("WARNING")) != 0 {
		t.Fatalf("a clearing chain must log no park WARNING, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// Recovery: the same chain parks while the sink bid is crushed (3000 < sum 3500) and
// resumes once the bid recovers above the summed input ask — the gate re-checks LIVE, so a
// recovered sink un-parks the chain without any restart.
func TestInputRoundMargin_ParksThenResumesWhenBidRecovers(t *testing.T) {
	crushed := &chainGateMarketRepo{sinkBid: 3000, fabAsk: 2800, inputAsks: map[string]int{cgInput1: 2000, cgInput2: 1500}}
	executor, _, _ := newChainGateExecutor(t, crushed)
	if !executor.inputRoundMarginParked(common.WithLogger(context.Background(), &dwellCapturingLogger{}), cgChain(), cgSystem, 1) {
		t.Fatalf("a crushed sink bid 3000 below summed ask 3500 must PARK")
	}

	recovered := &chainGateMarketRepo{sinkBid: 9000, fabAsk: 2800, inputAsks: map[string]int{cgInput1: 2000, cgInput2: 1500}}
	executor2, _, _ := newChainGateExecutor(t, recovered)
	if executor2.inputRoundMarginParked(common.WithLogger(context.Background(), &dwellCapturingLogger{}), cgChain(), cgSystem, 1) {
		t.Fatalf("a recovered sink bid 9000 above summed ask 3500 must RESUME (proceed)")
	}
}

// An unpriceable resale sink must fail OPEN (proceed), not closed: a root resale chain with
// no sink is already parked upstream by ChainMarginGuard at launch, and failing closed here
// would over-park intermediate feeds delivered to a parent factory (never resold) — the
// "guard rejects a class" fleet-killer.
func TestInputRoundMargin_ProceedsWhenSinkUnpriceable_FailOpen(t *testing.T) {
	repo := &chainGateMarketRepo{noSink: true, fabAsk: 7000, inputAsks: map[string]int{cgInput1: 19000, cgInput2: 18700}}
	executor, _, _ := newChainGateExecutor(t, repo)
	logger := &dwellCapturingLogger{}

	if executor.inputRoundMarginParked(common.WithLogger(context.Background(), logger), cgChain(), cgSystem, 1) {
		t.Fatalf("an unpriceable sink must fail OPEN (proceed), not park")
	}
	if len(logger.entriesWithLevel("WARNING")) != 0 {
		t.Fatalf("a fail-open (no-sink) proceed must log no park WARNING, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// Wiring: an underwater chain driven through the real fabricateGood path (ProduceGood on a
// FABRICATE node) must yield a zero-spend result and never fly to a market to buy inputs.
func TestFabricateGood_ChainMargin_ParksUnderwaterChainZeroSpend(t *testing.T) {
	repo := &chainGateMarketRepo{sinkBid: 7500, fabAsk: 7000, inputAsks: map[string]int{cgInput1: 19000, cgInput2: 18700}}
	executor, shipRepo, mediator := newChainGateExecutor(t, repo)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result, err := executor.ProduceGood(ctx, shipRepo.buildShip(), cgChain(), cgSystem, 1, nil, false)
	if err != nil {
		t.Fatalf("an underwater chain must park gracefully, not error: %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0 {
		t.Fatalf("a parked chain must yield a zero-spend result, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a parked chain must dispatch ZERO input purchases, got %d", mediator.purchaseAttempts())
	}
	if shipRepo.dockCalls() != 0 {
		t.Fatalf("a parked chain must not fly to any market to buy inputs (0 docks), got %d", shipRepo.dockCalls())
	}
}

// Wiring, negative: a construction-supply run (inputsOnly) has no resale sink and must NOT
// be gated on resale margin — it is governed by the construction pipeline's economics. The
// gate must be scoped out even when the resale bid would be underwater.
func TestFabricateGood_ChainMargin_InputsOnlyBypassesGate(t *testing.T) {
	repo := &chainGateMarketRepo{sinkBid: 7500, fabAsk: 7000, inputAsks: map[string]int{cgInput1: 19000, cgInput2: 18700}}
	executor, shipRepo, _ := newChainGateExecutor(t, repo)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	// inputsOnly=true: the resale negative-margin gate must be skipped (construction supply
	// has no resale sink). The run proceeds into fabrication; we only assert the gate did not
	// fire — its park WARNING carries the unique phrase "refusing the input round".
	_, _ = executor.ProduceGood(ctx, shipRepo.buildShip(), cgChain(), cgSystem, 1, nil, true)
	if spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "refusing the input round") {
		t.Fatalf("inputs-only must bypass the resale negative-margin gate, but it fired: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// sp-qmp8: the construction-supply drive harvests the output INTO the hauler (inputsOnly=false)
// and delivers it to the gate — never resells it — so the resale negative-margin gate must be
// scoped out even though inputsOnly is false, signalled by WithConstructionSupply. Without this
// scoping, an underwater resale bid would wrongly park the gate fill (the regression this bead
// fixes, via a different park reason). The INPUT buys still pass the money-guard stack; here we
// only assert the resale gate did not fire.
func TestFabricateGood_ChainMargin_ConstructionSupplyBypassesGate(t *testing.T) {
	repo := &chainGateMarketRepo{sinkBid: 7500, fabAsk: 7000, inputAsks: map[string]int{cgInput1: 19000, cgInput2: 18700}}
	executor, shipRepo, _ := newChainGateExecutor(t, repo)
	logger := &dwellCapturingLogger{}
	ctx := shared.WithConstructionSupply(common.WithLogger(context.Background(), logger))

	// inputsOnly=false (harvest into hauler) BUT construction-supply: the resale gate must be
	// skipped. Its park WARNING carries the unique phrase "refusing the input round".
	_, _ = executor.ProduceGood(ctx, shipRepo.buildShip(), cgChain(), cgSystem, 1, nil, false)
	if spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "refusing the input round") {
		t.Fatalf("construction supply must bypass the resale negative-margin gate, but it fired: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// sp-qmp8 (RULINGS #4): construction supply scopes out ONLY the resale-margin guards — the INPUT
// buys must STILL pass the money-guard stack. Driving the real fabricate path under construction
// supply with a live treasury below the working-capital reserve, the input purchase must be
// PARKED by the sp-9aoc spend floor (not spent blind). Proves the fabricate input legs the drain
// now sources are money-guarded exactly like every other factory input buy.
func TestFabricateGood_ConstructionSupply_InputBuysStillMoneyGuarded(t *testing.T) {
	repo := &chainGateMarketRepo{sinkBid: 7500, fabAsk: 7000, inputAsks: map[string]int{cgInput1: 19000, cgInput2: 18700}}
	shipRepo := &dockRaceShipRepo{location: dockRaceOrigin, navStatus: navigation.NavStatusDocked, cargoCapacity: 40}
	mediator := &dockRaceMediator{repo: shipRepo, dockHandler: tactics.NewDockShipHandler(shipRepo)}
	marketLocator := NewMarketLocator(repo, nil, nil, nil)
	// Live treasury 40000 < 50000 reserve → every input buy breaches. apiClient WIRED so the
	// working-capital floor is ACTIVE (the shared chainGate helper leaves it nil/disabled).
	executor := NewProductionExecutorWithConfig(
		mediator, shipRepo, repo, marketLocator, &dockRaceClock{}, []time.Duration{time.Millisecond},
		&spendFloorFakeAPIClient{credits: 40000},
	)
	logger := &dwellCapturingLogger{}
	ctx := shared.WithConstructionSupply(common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-QMP8"), logger))

	_, _ = executor.ProduceGood(ctx, shipRepo.buildShip(), cgChain(), cgSystem, 1, nil, false)

	warns := logger.entriesWithLevel("WARNING")
	if !spendFloorWarnContains(warns, "working-capital reserve") {
		t.Fatalf("construction-supply input buys must still be gated by the working-capital floor, got: %+v", warns)
	}
	if !spendFloorWarnContains(warns, cgInput1) && !spendFloorWarnContains(warns, cgInput2) {
		t.Fatalf("expected the spend-floor park to name a fabrication input (%s/%s), got: %+v", cgInput1, cgInput2, warns)
	}
}

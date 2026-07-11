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
)

// sp-a5j7 Phase 2 (wedx restoration): buyGood selects its buy source SUPPLY-FIRST — the
// original SupplyChainResolver design (FindExportMarketBySupplyPriority) the runtime path had
// bypassed for price-first. This suite drives buyGood (via ProduceGood with a BUY node) across
// a MULTI-market system and pins: a SCARCE source is INELIGIBLE while a healthy one exists (the
// ADV_CIRC incident shape); supply outranks price among eligibles; a degraded source is
// re-sourced, not ridden down; the rescue clause buys a depleted market only within the 1.2x
// cap (fail-closed without a median); and the era-end / disabled escape hatches revert to
// price-first.

// srcSpec is one market's listing of the test good.
type srcSpec struct {
	waypoint    string
	supply      string // SCARCE/LIMITED/MODERATE/HIGH/ABUNDANT
	ask         int    // sell_price = what we pay to buy
	tradeVolume int    // 0 -> 10
	activity    string // "" -> STRONG
	tradeType   market.TradeType
}

// multiSourceMarketRepo serves a configurable set of markets all listing dockRaceGood, so a
// test can pin supply-first selection, cross-market median, and re-sourcing. The source list is
// mutable (degradeSource) to model a market's supply degrading mid-chain.
type multiSourceMarketRepo struct {
	market.MarketRepository
	sources []srcSpec
}

func (r *multiSourceMarketRepo) degradeSource(waypoint, newSupply string) {
	for i := range r.sources {
		if r.sources[i].waypoint == waypoint {
			r.sources[i].supply = newSupply
		}
	}
}

func (r *multiSourceMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	wps := make([]string, 0, len(r.sources))
	for _, s := range r.sources {
		wps = append(wps, s.waypoint)
	}
	return wps, nil
}

func (r *multiSourceMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	for _, s := range r.sources {
		if s.waypoint != waypointSymbol {
			continue
		}
		supply := s.supply
		activity := s.activity
		if activity == "" {
			activity = "STRONG"
		}
		tv := s.tradeVolume
		if tv == 0 {
			tv = 10
		}
		tt := s.tradeType
		if tt == "" {
			tt = market.TradeTypeExport
		}
		good, err := market.NewTradeGood(dockRaceGood, &supply, &activity, s.ask, s.ask, tv, tt)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	}
	return nil, nil
}

// FindBestMarketBuying is unused by the input-buy path (buyGood never re-sells an input); return
// nothing so an accidental call is a clean no-source rather than an embedded-interface panic.
func (r *multiSourceMarketRepo) FindBestMarketBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.BestMarketBuyingResult, error) {
	return nil, nil
}

func newMultiSourceExecutor(t *testing.T, repo *multiSourceMarketRepo, reader InputPriceHistoryReader) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
	t.Helper()
	shipRepo := &dockRaceShipRepo{
		location:      dockRaceOrigin,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 40,
	}
	mediator := &dockRaceMediator{
		repo:        shipRepo,
		dockHandler: tactics.NewDockShipHandler(shipRepo),
	}
	marketLocator := NewMarketLocator(repo, nil, nil, nil)
	executor := NewProductionExecutorWithConfig(
		mediator, shipRepo, repo, marketLocator, &dockRaceClock{},
		[]time.Duration{time.Millisecond}, nil,
	)
	if reader != nil {
		executor.SetPriceHistoryReader(reader)
	}
	return executor, shipRepo, mediator
}

func produceBuy(t *testing.T, executor *ProductionExecutor, shipRepo *dockRaceShipRepo, ctx context.Context) *ProductionResult {
	t.Helper()
	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, shipRepo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("ProduceGood returned error: %v", err)
	}
	return result
}

// ACCEPTANCE (ADV_CIRC incident shape, captain's evidence line): a SCARCE source must be
// INELIGIBLE for a full tranche while a healthy source exists — the buy sources from the HIGH
// market, never the SCARCE one. Every input blowup this era began at a SCARCE/LIMITED source.
func TestSelectSource_ScarceIneligibleWhenHealthyExists_ADVCIRC(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-SCARCE", supply: supplyScarce, ask: 4000},
		{waypoint: "X1-DR-HIGH", supply: supplyHigh, ask: 5000},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("expected a purchase from the healthy source, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase, got %d", mediator.purchaseAttempts())
	}
	if result.WaypointSymbol != "X1-DR-HIGH" {
		t.Fatalf("supply-first must source from the HIGH market, never the cheaper SCARCE one; got %s", result.WaypointSymbol)
	}
}

// Supply outranks price among eligible sources (wedx ranking: supply DESC > activity > price
// ASC): an ABUNDANT source is picked over a CHEAPER MODERATE one.
func TestSelectSource_SupplyOutranksPrice(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-MOD", supply: supplyModerate, ask: 100},
		{waypoint: "X1-DR-ABUND", supply: supplyAbundant, ask: 200},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	result := produceBuy(t, executor, shipRepo, common.WithLogger(context.Background(), &dwellCapturingLogger{}))

	if result == nil || mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected a purchase, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if result.WaypointSymbol != "X1-DR-ABUND" {
		t.Fatalf("supply-first must pick ABUNDANT@200 over the cheaper MODERATE@100; got %s", result.WaypointSymbol)
	}
}

// Re-source (wedx (d), the furnace fix): a source that degrades below MODERATE is no longer
// selected — the next buy re-sources to the still-healthy market instead of riding the degraded
// one down. buyGood re-runs the supply-first selector every call, so re-sourcing is structural.
func TestSelectSource_ReSourcesDegradedSource(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-A", supply: supplyModerate, ask: 100},
		{waypoint: "X1-DR-B", supply: supplyHigh, ask: 200},
	}}
	executor, shipRepo, _ := newMultiSourceExecutor(t, repo, nil)
	ctx := common.WithLogger(context.Background(), &dwellCapturingLogger{})

	first := produceBuy(t, executor, shipRepo, ctx)
	if first.WaypointSymbol != "X1-DR-B" {
		t.Fatalf("first buy must pick the HIGH source B; got %s", first.WaypointSymbol)
	}
	// B ladders down to SCARCE under our draw — it must drop out of eligibility.
	repo.degradeSource("X1-DR-B", supplyScarce)
	second := produceBuy(t, executor, shipRepo, ctx)
	if second.WaypointSymbol != "X1-DR-A" {
		t.Fatalf("after B degrades to SCARCE the buy must RE-SOURCE to the still-healthy A; got %s", second.WaypointSymbol)
	}
}

// Rescue clause (wedx (a)): with NO eligible source, a SCARCE market is bought ONLY within the
// rescue cap (rescueMultiplier x trailing median). Ask within cap -> rescue buy.
func TestSelectSource_RescueWithinCapBuys(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-SCARCE", supply: supplyScarce, ask: 5000},
	}}
	reader := &fakePriceHistoryReader{sellPrices: []int{4800, 4800, 4800}} // median 4800 -> cap 5760
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, reader)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("a rescue buy within the cap must proceed, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "RESCUE buy") {
		t.Fatalf("expected a RESCUE-buy WARNING, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// Rescue over the cap parks: a depleted market whose ask exceeds 1.2x the trailing median is the
// ladder the whole restoration exists to refuse — PARK, zero spend.
func TestSelectSource_RescueOverCapParks(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-SCARCE", supply: supplyScarce, ask: 19000},
	}}
	reader := &fakePriceHistoryReader{sellPrices: []int{4800, 4800, 4800}} // median 4800 -> cap 5760
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, reader)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("a rescue over the cap must PARK, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("INFO"), "rescue REFUSED") {
		t.Fatalf("expected a rescue-refused INFO park, got: %+v", logger.entriesWithLevel("INFO"))
	}
}

// Rescue fails CLOSED without a trailing median: with no price-history reader, a depleted-market
// buy cannot be validated, so it is refused rather than blind-bought (RULINGS #4).
func TestSelectSource_RescueNoMedianFailsClosed(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-SCARCE", supply: supplyScarce, ask: 5000},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil) // no reader
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("a rescue with no median must fail closed (PARK), got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "fail-closed") {
		t.Fatalf("expected a fail-closed WARNING, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// The RULINGS #5 escape hatch: disabled sourcing reverts to pure PRICE-FIRST — it picks the
// cheapest source even when it is SCARCE (the pre-restoration behavior, for an emergency override).
func TestSelectSource_DisabledIsPriceFirst(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-SCARCE", supply: supplyScarce, ask: 100},
		{waypoint: "X1-DR-HIGH", supply: supplyHigh, ask: 5000},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	ctx := WithInputSourcing(common.WithLogger(context.Background(), &dwellCapturingLogger{}), 0, false, true) // disabled

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || mediator.purchaseAttempts() != 1 {
		t.Fatalf("disabled sourcing must proceed price-first, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if result.WaypointSymbol != "X1-DR-SCARCE" {
		t.Fatalf("disabled sourcing must pick the cheapest (SCARCE@100) price-first; got %s", result.WaypointSymbol)
	}
}

// Era-end mode (wedx (3)(i)): < T-6h, sourcing flips to price-first — mean-reversion has no time
// to work, so the cheapest ask that clears margin now beats waiting for supply to regenerate.
func TestSelectSource_EraEndIsPriceFirst(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-SCARCE", supply: supplyScarce, ask: 100},
		{waypoint: "X1-DR-HIGH", supply: supplyHigh, ask: 5000},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	logger := &dwellCapturingLogger{}
	ctx := WithInputSourcing(common.WithLogger(context.Background(), logger), 0, true, false) // era-end

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || mediator.purchaseAttempts() != 1 || result.WaypointSymbol != "X1-DR-SCARCE" {
		t.Fatalf("era-end must source price-first (cheapest SCARCE@100), got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("INFO"), "Era-end mode") {
		t.Fatalf("expected an Era-end mode INFO, got: %+v", logger.entriesWithLevel("INFO"))
	}
}

// Buy-side absorption (sp-a5j7 acceptance): no single input tranche exceeds the market's
// trade_volume — a 40-unit hold at a trade_volume of 10 acquires exactly one 10-unit tranche.
func TestSelectSource_Absorption_TrancheCappedAtTradeVolume(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-HIGH", supply: supplyHigh, ask: 100, tradeVolume: 10},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	result := produceBuy(t, executor, shipRepo, common.WithLogger(context.Background(), &dwellCapturingLogger{}))

	if result == nil || mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly one purchase, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if result.QuantityAcquired != 10 {
		t.Fatalf("an input tranche must be capped at trade_volume (10), not the 40-unit hold; got %d", result.QuantityAcquired)
	}
}

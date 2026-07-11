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

// sp-a5j7 (Admiral design point, iv65 completion): the D39/ADVANCED_CIRCUITRY ladder was a
// SUPPLY event — each 43u input buy exceeded the source's trade_volume increment, drove its
// supply toward SCARCE, and the ask repriced as the SYMPTOM. iv65's price ceiling caught the
// damage AFTER the ask laddered (the LAGGING backstop); this suite drives buyGood (via
// ProduceGood with a BUY node) and asserts the LEADING supply-state gate: PARK an input buy
// into a depleted (SCARCE) market UNLESS the feed leg still clears at the live ask; WARN and
// proceed on LIMITED; proceed on MODERATE+; fail CLOSED (park) when supply is unreadable; and
// prove the iv65 ceiling still backstops independently of this gate.

// supplyGateMarketRepo prices a single EXPORT source for dockRaceGood at a configurable supply
// level + ask (SellPrice — what we PAY), and a single importer (the feed-leg delivery target)
// at a configurable bid, so a test can pin every branch of the gate. A supply of "" models an
// unscanned/unreadable supply (nil on the trade good → fail closed). importBid<=0 or
// noImporter models an unpriceable delivery (margin exception cannot be granted).
type supplyGateMarketRepo struct {
	market.MarketRepository
	supply      string
	ask         int
	importBid   int
	noImporter  bool
	tradeVolume int // 0 → default 10
}

func (r *supplyGateMarketRepo) tv() int {
	if r.tradeVolume == 0 {
		return 10
	}
	return r.tradeVolume
}

func (r *supplyGateMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{dockRaceMarketWP}, nil
}

func (r *supplyGateMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	if waypointSymbol != dockRaceMarketWP {
		return nil, nil
	}
	var supplyPtr *string
	if r.supply != "" {
		s := r.supply
		supplyPtr = &s // empty string → nil supply on the trade good (models unreadable)
	}
	activity := "STRONG"
	good, err := market.NewTradeGood(dockRaceGood, supplyPtr, &activity, r.ask, r.ask, r.tv(), market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
}

// FindBestMarketBuying is the feed-leg delivery target the margin exception prices against.
func (r *supplyGateMarketRepo) FindBestMarketBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.BestMarketBuyingResult, error) {
	if r.noImporter || goodSymbol != dockRaceGood {
		return nil, nil
	}
	return &market.BestMarketBuyingResult{
		WaypointSymbol: dockRaceMarketWP,
		TradeSymbol:    goodSymbol,
		PurchasePrice:  r.importBid,
		Supply:         "HIGH",
	}, nil
}

// newSupplyGateExecutor wires the ship/mediator/clock harness (docked, 40-cap hold) against a
// supplyGateMarketRepo pinned to the given supply/ask/importBid, optionally wiring a
// price-history reader so the iv65 ceiling is ACTIVE too. apiClient stays nil so the
// working-capital floor is OFF — this suite isolates the supply gate (and, when wired, the
// ceiling).
func newSupplyGateExecutor(t *testing.T, repoCfg *supplyGateMarketRepo, reader InputPriceHistoryReader) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
	t.Helper()

	repo := &dockRaceShipRepo{
		location:      dockRaceOrigin,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 40,
	}
	mediator := &dockRaceMediator{
		repo:        repo,
		dockHandler: tactics.NewDockShipHandler(repo),
	}
	marketLocator := NewMarketLocator(repoCfg, nil, nil, nil)

	executor := NewProductionExecutorWithConfig(
		mediator,
		repo,
		repoCfg,
		marketLocator,
		&dockRaceClock{},
		[]time.Duration{time.Millisecond},
		nil, // apiClient nil → spend floor off, isolating the supply gate
	)
	if reader != nil {
		executor.SetPriceHistoryReader(reader)
	}
	return executor, repo, mediator
}

func supplyGateBuy(t *testing.T, executor *ProductionExecutor, repo *dockRaceShipRepo, ctx context.Context) *ProductionResult {
	t.Helper()
	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a supply-gated buy must be graceful, not an error: got %v", err)
	}
	return result
}

// ACCEPTANCE (the D39 incident shape): an input buy against a SCARCE market whose feed leg is
// underwater at the live ask must PARK — zero spend, no dispatch, and (checked pre-nav) no
// flight to the market — with a supply-gate line naming the good and the SCARCE state. No ctx
// config is stamped, proving the gate defaults ON at the SCARCE park level.
func TestBuyGood_SupplyGate_ParksScarceMarket_D39(t *testing.T) {
	// SCARCE source, laddered ask 19000, importer pays only 7000 → feed leg underwater → park.
	repoCfg := &supplyGateMarketRepo{supply: supplyScarce, ask: 19000, importBid: 7000}
	executor, repo, mediator := newSupplyGateExecutor(t, repoCfg, nil)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := supplyGateBuy(t, executor, repo, ctx)
	if result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0 {
		t.Fatalf("a supply-gate park must yield a zero-spend result, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a supply-gate park must dispatch ZERO purchases, got %d", mediator.purchaseAttempts())
	}
	if repo.dockCalls() != 0 {
		t.Fatalf("a supply-gate park must be refused BEFORE flying to the market (0 docks), got %d", repo.dockCalls())
	}
	infos := logger.entriesWithLevel("INFO")
	if !spendFloorWarnContains(infos, "supply") || !spendFloorWarnContains(infos, dockRaceGood) {
		t.Fatalf("expected an INFO supply-gate park naming the good and supply, got: %+v", infos)
	}
	if !spendFloorWarnContains(infos, supplyScarce) {
		t.Fatalf("expected the supply-gate park to name the SCARCE state in the text, got: %+v", infos)
	}
}

// The margin exception: a SCARCE market whose feed leg STILL clears at the live ask (the
// importer will pay at least what we pay) proceeds — the chain margin holds despite the
// degraded state, so we must not over-park a still-profitable buy. One real purchase.
func TestBuyGood_SupplyGate_ScarceButFeedLegClears_Proceeds(t *testing.T) {
	// SCARCE, ask 5000, importer bid 6000 (>= ask) → feed leg clears → proceed despite SCARCE.
	repoCfg := &supplyGateMarketRepo{supply: supplyScarce, ask: 5000, importBid: 6000}
	executor, repo, mediator := newSupplyGateExecutor(t, repoCfg, nil)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := supplyGateBuy(t, executor, repo, ctx)
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("a SCARCE buy whose feed leg clears must proceed to a real purchase, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase for the margin exception, got %d", mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("INFO"), "clears") {
		t.Fatalf("expected an INFO noting the margin exception (feed leg clears), got: %+v", logger.entriesWithLevel("INFO"))
	}
}

// A SCARCE market with NO priceable importer cannot prove the margin clears, so the park
// stands (fail the exception closed): PARK is the gate's default and the exception is granted
// only on positive proof (the spec's "unless the chain margin still clears").
func TestBuyGood_SupplyGate_ScarceUnpriceableImporter_ParksClosed(t *testing.T) {
	repoCfg := &supplyGateMarketRepo{supply: supplyScarce, ask: 5000, noImporter: true}
	executor, repo, mediator := newSupplyGateExecutor(t, repoCfg, nil)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := supplyGateBuy(t, executor, repo, ctx)
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("a SCARCE buy with no priceable importer must park, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("INFO"), "supply") {
		t.Fatalf("expected a supply-gate park line, got INFO: %+v", logger.entriesWithLevel("INFO"))
	}
}

// LIMITED supply (one state above the default SCARCE park floor) WARNS and PROCEEDS: the buy
// happens, but an operator-visible WARNING flags the market is one step from SCARCE.
func TestBuyGood_SupplyGate_LimitedWarnsAndProceeds(t *testing.T) {
	repoCfg := &supplyGateMarketRepo{supply: supplyLimited, ask: 5000, importBid: 3000} // bid<ask, irrelevant to LIMITED
	executor, repo, mediator := newSupplyGateExecutor(t, repoCfg, nil)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := supplyGateBuy(t, executor, repo, ctx)
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("a LIMITED buy must proceed to a real purchase, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "LIMITED") {
		t.Fatalf("expected a WARNING naming LIMITED supply, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// MODERATE supply proceeds silently — no park, no warn. Proves the gate is not over-aggressive
// above its park/warn levels.
func TestBuyGood_SupplyGate_ModerateProceedsSilently(t *testing.T) {
	repoCfg := &supplyGateMarketRepo{supply: supplyModerate, ask: 5000, importBid: 3000}
	executor, repo, mediator := newSupplyGateExecutor(t, repoCfg, nil)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := supplyGateBuy(t, executor, repo, ctx)
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("a MODERATE buy must proceed, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if spendFloorWarnContains(logger.entriesWithLevel("INFO"), "supply") ||
		spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "supply") {
		t.Fatalf("a MODERATE buy must not log a supply-gate line, got INFO=%+v WARNING=%+v", logger.entriesWithLevel("INFO"), logger.entriesWithLevel("WARNING"))
	}
}

// Unreadable supply (nil on the trade good → empty level) fails CLOSED: the buy is parked,
// never dispatched. A guard blind to its own signal must not spend (RULINGS #4).
func TestBuyGood_SupplyGate_UnreadableSupplyFailsClosed(t *testing.T) {
	repoCfg := &supplyGateMarketRepo{supply: "", ask: 5000, importBid: 9000} // empty supply, feed leg would clear
	executor, repo, mediator := newSupplyGateExecutor(t, repoCfg, nil)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := supplyGateBuy(t, executor, repo, ctx)
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("an unreadable-supply buy must fail closed (park), got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "fail-closed") {
		t.Fatalf("expected a fail-closed WARNING for unreadable supply, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// The RULINGS #5 off switch: a disabled gate proceeds even into a SCARCE market with an
// underwater feed leg (which would otherwise park). Proves the emergency disable works.
func TestBuyGood_SupplyGate_DisableFlagProceeds(t *testing.T) {
	repoCfg := &supplyGateMarketRepo{supply: supplyScarce, ask: 19000, importBid: 7000}
	executor, repo, mediator := newSupplyGateExecutor(t, repoCfg, nil)
	ctx := WithInputSupplyGate(common.WithLogger(context.Background(), &dwellCapturingLogger{}), "", true) // disabled

	result := supplyGateBuy(t, executor, repo, ctx)
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("a disabled supply gate must proceed to a real purchase, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
}

// A configurable park level TIGHTENS the gate: with park level raised to LIMITED, a LIMITED
// market (feed leg underwater) now PARKS rather than merely warning. Proves the ctx-threaded
// threshold reaches the guard.
func TestBuyGood_SupplyGate_ConfigurableParkLevelParksLimited(t *testing.T) {
	repoCfg := &supplyGateMarketRepo{supply: supplyLimited, ask: 19000, importBid: 7000}
	executor, repo, mediator := newSupplyGateExecutor(t, repoCfg, nil)
	logger := &dwellCapturingLogger{}
	ctx := WithInputSupplyGate(common.WithLogger(context.Background(), logger), supplyLimited, false)

	result := supplyGateBuy(t, executor, repo, ctx)
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("a LIMITED buy must park when the park level is raised to LIMITED, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("INFO"), "supply") {
		t.Fatalf("expected a supply-gate park line for the tightened LIMITED floor, got INFO: %+v", logger.entriesWithLevel("INFO"))
	}
}

// The iv65 price ceiling still backstops INDEPENDENTLY: a buy that CLEARS the supply gate (via
// the margin exception on a SCARCE market whose feed leg clears) is STILL parked by the ceiling
// when the ask exceeds the trailing-median ceiling. Guards never weaken (RULINGS #4) — clearing
// one does not bypass the other.
func TestBuyGood_SupplyGate_PriceCeilingStillBackstops(t *testing.T) {
	// SCARCE + importer bid 20000 (>= ask 19000) → supply gate GRANTS the margin exception.
	// But median 4750 → ceiling 7125, and ask 19000 > 7125 → the ceiling parks it anyway.
	repoCfg := &supplyGateMarketRepo{supply: supplyScarce, ask: 19000, importBid: 20000}
	reader := &fakePriceHistoryReader{sellPrices: []int{4900, 4500, 5000, 4600, 4750}}
	executor, repo, mediator := newSupplyGateExecutor(t, repoCfg, reader)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := supplyGateBuy(t, executor, repo, ctx)
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("the ceiling must still backstop a ladder-priced ask even when the supply gate clears, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	infos := logger.entriesWithLevel("INFO")
	// Supply gate cleared via the margin exception...
	if !spendFloorWarnContains(infos, "clears") {
		t.Fatalf("expected the supply gate to log its margin-clear exception, got INFO: %+v", infos)
	}
	// ...and the ceiling independently parked the buy.
	if !spendFloorWarnContains(infos, "ceiling") {
		t.Fatalf("expected the iv65 ceiling to still park the ladder-priced ask, got INFO: %+v", infos)
	}
}

// Buy-side absorption (sp-a5j7 point 2 / acceptance): no single input tranche exceeds the
// market's trade_volume for the good. With a 40-unit hold and a trade_volume of 10, the buy
// must acquire exactly one 10-unit tranche — never the full 40 the hold could hold — so a
// single cycle can never push the market more than one absorption increment.
func TestBuyGood_Absorption_TrancheCappedAtTradeVolume(t *testing.T) {
	repoCfg := &supplyGateMarketRepo{supply: supplyHigh, ask: 100, importBid: 200, tradeVolume: 10}
	executor, repo, mediator := newSupplyGateExecutor(t, repoCfg, nil)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := supplyGateBuy(t, executor, repo, ctx)
	if result == nil || mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly one purchase, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if result.QuantityAcquired != 10 {
		t.Fatalf("an input tranche must be capped at trade_volume (10), not the 40-unit hold; got %d units", result.QuantityAcquired)
	}
}

package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-iv65 (P1 money-integrity): the factory input buyer had NO price ceiling. The
// ADVANCED_CIRCUITRY chain bought ELECTRONICS+MICROPROCESSORS inputs at ~19k/u — 4x
// market, chasing its own supply ladder up — to fabricate a ~7k/u output: −6.6M in 3h.
// This suite drives buyGood (via ProduceGood with a BUY node) and asserts the per-buy
// ceiling: PARK the input (zero-spend result, no dispatch, no flight) when the live ask
// exceeds the trailing-median × multiplier, PARK fail-CLOSED when the median is
// unavailable/unreadable, and — the optional-port contract every other test relies on —
// proceed unchanged when no price-history reader is wired.

// ceilingMarketRepo prices a single EXPORT market for dockRaceGood at a configurable ask
// (SellPrice — the price we PAY to buy), so a test can pin the ADV_CIRC ladder shape:
// a live ask of 19000 against a ~4750 trailing median.
type ceilingMarketRepo struct {
	market.MarketRepository
	ask int
}

func (r *ceilingMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{dockRaceMarketWP}, nil
}

func (r *ceilingMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	if waypointSymbol != dockRaceMarketWP {
		return nil, nil
	}
	supply := "HIGH"
	activity := "STRONG"
	// purchasePrice (the bid) is irrelevant to the ceiling; sellPrice is the ask we pay.
	good, err := market.NewTradeGood(dockRaceGood, &supply, &activity, r.ask, r.ask, 10, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
}

// fakePriceHistoryReader returns a scripted trailing ask series (or an error) for the
// ceiling's median. sellPrices is the historical ask series; an empty slice models "no
// history in window" (median unavailable → fail closed). err models the live read failing.
type fakePriceHistoryReader struct {
	sellPrices []int
	err        error
	calls      int
}

func (r *fakePriceHistoryReader) GetPriceHistory(ctx context.Context, waypointSymbol, goodSymbol string, since time.Time, limit int) ([]*market.MarketPriceHistory, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	out := make([]*market.MarketPriceHistory, 0, len(r.sellPrices))
	for _, sp := range r.sellPrices {
		h, err := market.NewMarketPriceHistory(waypointSymbol, goodSymbol, shared.MustNewPlayerID(1), sp, sp, nil, nil, 10)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// newCeilingExecutor mirrors newDockRaceExecutor but prices the input at a configurable ask
// and (optionally) wires a price-history reader so the ceiling is ACTIVE. apiClient stays
// nil so the working-capital floor is OFF — this suite isolates the ceiling.
func newCeilingExecutor(t *testing.T, ask int, reader InputPriceHistoryReader) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
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
	marketRepo := &ceilingMarketRepo{ask: ask}
	marketLocator := NewMarketLocator(marketRepo, nil, nil, nil)

	executor := NewProductionExecutorWithConfig(
		mediator,
		repo,
		marketRepo,
		marketLocator,
		&dockRaceClock{},
		[]time.Duration{time.Millisecond},
		nil, // apiClient nil → spend floor off, so ONLY the ceiling can park
	)
	if reader != nil {
		executor.SetPriceHistoryReader(reader)
	}
	return executor, repo, mediator
}

// ADV_CIRC pin: a live ask 4x the trailing median must PARK — zero spend, no dispatch, and
// (checked pre-nav) no flight to the market. The exact leak shape: 19000 ask vs a 4750
// median → ceiling 7125 at the default 1.5x. No ctx config is stamped, proving the default
// multiplier is ON without the captain naming it.
func TestBuyGood_InputPriceCeiling_ParksLadderPricedInput_ADVCIRC(t *testing.T) {
	// sorted [4500,4600,4750,4900,5000] → median 4750, ceiling int(4750*1.5)=7125.
	reader := &fakePriceHistoryReader{sellPrices: []int{4900, 4500, 5000, 4600, 4750}}
	executor, repo, mediator := newCeilingExecutor(t, 19000, reader)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a ceiling-parked buy must be graceful, not an error: got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0 {
		t.Fatalf("a ceiling-parked buy must yield a zero-spend result, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a ceiling-parked buy must dispatch ZERO purchases, got %d", mediator.purchaseAttempts())
	}
	if repo.dockCalls() != 0 {
		t.Fatalf("a ceiling-parked buy must be refused BEFORE flying to the market (0 docks), got %d", repo.dockCalls())
	}
	// The park cause must be legible in the INFO message TEXT (sp-iqyq): good/ask/median/ceiling.
	infos := logger.entriesWithLevel("INFO")
	if !spendFloorWarnContains(infos, "ceiling") || !spendFloorWarnContains(infos, dockRaceGood) {
		t.Fatalf("expected an INFO park naming the good and the ceiling, got: %+v", infos)
	}
	if !spendFloorWarnContains(infos, "19000") || !spendFloorWarnContains(infos, "4750") {
		t.Fatalf("expected the INFO park to carry the ask (19000) and median (4750) in the text, got: %+v", infos)
	}
}

// An ask comfortably under the ceiling proceeds exactly as before the guard existed: one
// real purchase, a non-zero result, no park line. Proves the guard is not over-aggressive.
func TestBuyGood_InputPriceCeiling_ProceedsWhenAskUnderCeiling(t *testing.T) {
	// median 4750, ceiling 7125; ask 6000 < 7125 → proceed.
	reader := &fakePriceHistoryReader{sellPrices: []int{4500, 4750, 5000}}
	executor, repo, mediator := newCeilingExecutor(t, 6000, reader)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("an under-ceiling buy must proceed, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("expected a successful purchase under the ceiling, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase for an under-ceiling buy, got %d", mediator.purchaseAttempts())
	}
	if spendFloorWarnContains(logger.entriesWithLevel("INFO"), "ladder-chase refused") {
		t.Fatalf("an under-ceiling buy must not log a ceiling park, got: %+v", logger.entriesWithLevel("INFO"))
	}
}

// No trailing history in the window (0 samples) must fail CLOSED: the buy is parked, never
// dispatched. A guard whose whole job is refusing to overpay must not let a buy through
// because it went blind (RULINGS #4) — the ask here (6000) would clear any real ceiling.
func TestBuyGood_InputPriceCeiling_FailsClosedWhenMedianUnavailable(t *testing.T) {
	reader := &fakePriceHistoryReader{sellPrices: []int{}} // no samples in window
	executor, repo, mediator := newCeilingExecutor(t, 6000, reader)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a fail-closed ceiling park must be graceful, got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0 {
		t.Fatalf("a fail-closed ceiling park must yield a zero-spend result, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a fail-closed ceiling park must dispatch ZERO purchases, got %d", mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "fail-closed") {
		t.Fatalf("expected a fail-closed WARNING (median unavailable), got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// The history read itself erroring must also fail CLOSED — same discipline as the spend
// floor's blind-read park.
func TestBuyGood_InputPriceCeiling_FailsClosedWhenReaderErrors(t *testing.T) {
	reader := &fakePriceHistoryReader{err: errors.New("price history DB unavailable")}
	executor, repo, mediator := newCeilingExecutor(t, 6000, reader)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a blind price-history read must park gracefully, got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("a fail-closed ceiling park must dispatch ZERO purchases and zero-spend, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "fail-closed") {
		t.Fatalf("expected a fail-closed WARNING (read error), got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// No reader wired (e.priceHistory == nil) fails OPEN: the ceiling is simply unavailable, so
// even a wildly-over-market ask proceeds. This is the optional-port contract every other
// test in this package relies on by wiring nothing — an explicit guard so a future change
// that made nil fail-closed (silently parking every factory buy in the suite) is caught.
func TestBuyGood_InputPriceCeiling_FailsOpenWhenNoReaderWired(t *testing.T) {
	executor, repo, mediator := newCeilingExecutor(t, 19000, nil) // ask 19000, no ceiling wired

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("with no reader wired the ceiling must fail open and proceed, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("a fail-open (no-reader) buy must proceed to a real purchase, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
}

// The emergency disable flag turns the guard OFF even with a wired reader and a
// ladder-priced ask: the buy proceeds. Proves the RULINGS #5 off-switch works.
func TestBuyGood_InputPriceCeiling_DisableFlagProceeds(t *testing.T) {
	reader := &fakePriceHistoryReader{sellPrices: []int{4750, 4750, 4750}}
	executor, repo, mediator := newCeilingExecutor(t, 19000, reader) // would park at default
	logger := &dwellCapturingLogger{}
	ctx := WithInputPriceCeiling(common.WithLogger(context.Background(), logger), 0, true) // disabled

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a disabled ceiling must proceed, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("a disabled ceiling must proceed to a real purchase, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
}

// A custom multiplier from config is honored: an ask that clears the default 1.5x ceiling
// parks under a tighter 1.2x. Proves the ctx-threaded multiplier reaches the guard.
func TestBuyGood_InputPriceCeiling_CustomMultiplierHonored(t *testing.T) {
	// median 4750; default ceiling 7125 (6000 clears), 1.2x ceiling 5700 (6000 parks).
	reader := &fakePriceHistoryReader{sellPrices: []int{4500, 4750, 5000}}
	executor, repo, mediator := newCeilingExecutor(t, 6000, reader)
	logger := &dwellCapturingLogger{}
	ctx := WithInputPriceCeiling(common.WithLogger(context.Background(), logger), 1.2, false)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a tighter-multiplier park must be graceful, got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("ask 6000 must park under a 1.2x ceiling (5700), got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
}

// A STABLE-priced market records exactly ONE on-change history row. That single sample must
// be a usable median (guard priceable), NOT treated as "unavailable" and parked — otherwise
// every stable-priced input would park forever (the "guard rejects a class" fleet-killer).
func TestBuyGood_InputPriceCeiling_SingleSampleStableMarketIsPriceable(t *testing.T) {
	reader := &fakePriceHistoryReader{sellPrices: []int{4750}}      // one stable on-change row
	executor, repo, mediator := newCeilingExecutor(t, 5000, reader) // 5000 < ceiling 7125
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a single-sample stable market must be priceable, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("a single-sample stable market under the ceiling must proceed, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "fail-closed") {
		t.Fatalf("a single valid sample must NOT fail closed, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

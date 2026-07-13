package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// sp-2me2 (P1): the construction-supply drain used to carry ONE ~trade-volume tranche (~20u) per
// round-trip because buyGood did a single market buy of min(availableSpace, tradeVolume) and
// returned — a single SpaceTraders market buy is itself capped at trade_volume, so filling the
// ~80u hold needs a LOOP of tranches. With a hull-fill target stamped on ctx (only the drain does
// this — WithHullFillTarget), buyGood now tops the hold up toward hull capacity, bounded by the
// material's outstanding bill and, fail-CLOSED, by the per-iteration money/price guards. Every
// OTHER buyGood caller (goods-factory inputs) stamps NO target and keeps the single-tranche
// behavior unchanged (RULINGS #2) — pinned by TestBuyGood_NoFillTarget_SingleTranchePreserved and
// the pre-existing TestSelectSource_Absorption_TrancheCappedAtTradeVolume.

// produceBuyWithFill drives buyGood (via ProduceGood with a BUY node) with a hull-fill target
// stamped on ctx, mirroring what the construction drain does before sourcing a material.
func produceBuyWithFill(t *testing.T, executor *ProductionExecutor, ship *navigation.Ship, ctx context.Context, billRemaining int, fraction float64) *ProductionResult {
	t.Helper()
	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	ctx = WithHullFillTarget(ctx, billRemaining, fraction)
	result, err := executor.ProduceGood(ctx, ship, node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("ProduceGood returned error: %v", err)
	}
	return result
}

// Acceptance core: a hull-fill buy tops the hold up toward hull capacity via MULTIPLE market
// buys (each capped at trade_volume), not a single tranche. Capacity 40, trade_volume 10 => four
// 10-unit tranches fill the hold; the loop stops when the hold is full.
func TestBuyGood_HullFill_BuysMultipleTranchesToFillHold(t *testing.T) {
	executor, repo, mediator := newDockRaceExecutor(t, nil) // cargoCapacity 40, market trade_volume 10, ask 10
	// A bill far larger than the hull so the STOP is the hull filling, not the bill.
	result := produceBuyWithFill(t, executor, repo.buildShip(), context.Background(), 1000, 0)

	if result == nil || result.QuantityAcquired != 40 {
		t.Fatalf("expected the hold filled to capacity (40 units), got %+v", result)
	}
	if mediator.purchaseAttempts() != 4 {
		t.Fatalf("filling a 40-unit hold at trade_volume 10 must take 4 tranche buys, got %d", mediator.purchaseAttempts())
	}
}

// The loop STOPS at the material's remaining bill, never over-buying past demand. Bill 25 with a
// 40-unit hull and trade_volume 10 => 10 + 10 + 5 = 25 units over exactly 3 buys.
func TestBuyGood_HullFill_StopsAtRemainingBill(t *testing.T) {
	executor, repo, mediator := newDockRaceExecutor(t, nil)
	result := produceBuyWithFill(t, executor, repo.buildShip(), context.Background(), 25, 0)

	if result == nil || result.QuantityAcquired != 25 {
		t.Fatalf("expected exactly the 25-unit bill acquired (not the 40-unit hull), got %+v", result)
	}
	if mediator.purchaseAttempts() != 3 {
		t.Fatalf("a 25-unit bill at trade_volume 10 must take 3 buys (10+10+5), got %d", mediator.purchaseAttempts())
	}
}

// Regression: a bill SMALLER than one trade_volume still does EXACTLY one buy of the bill — no
// over-buy past demand, no wasted second tranche. Bill 7 => a single 7-unit buy.
func TestBuyGood_HullFill_SmallBillDoesExactlyOneBuy(t *testing.T) {
	executor, repo, mediator := newDockRaceExecutor(t, nil)
	result := produceBuyWithFill(t, executor, repo.buildShip(), context.Background(), 7, 0)

	if result == nil || result.QuantityAcquired != 7 {
		t.Fatalf("expected exactly the 7-unit bill acquired, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("a sub-trade-volume bill must do exactly ONE buy (no over-buy), got %d", mediator.purchaseAttempts())
	}
}

// sequentialCreditsAPIClient returns a scripted live-treasury figure per GetAgent call (clamping
// to the last), modelling treasury depleting as the fill loop spends. It lets a test drive the
// per-iteration working-capital floor to trip PART-WAY through a fill.
type sequentialCreditsAPIClient struct {
	domainPorts.APIClient
	credits []int
	calls   int
}

func (c *sequentialCreditsAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	i := c.calls
	if i >= len(c.credits) {
		i = len(c.credits) - 1
	}
	c.calls++
	return &player.AgentData{Credits: c.credits[i]}, nil
}

// The money guard is re-checked EACH iteration against live treasury and fails CLOSED under the
// loop (RULINGS #4): once the NEXT tranche would breach the working-capital reserve the loop stops
// and delivers what is already aboard — it never forces the buy. Each 10-unit tranche costs 100;
// the reserve is 50000. Credits 50200 -> 50100 -> 50050: the first two tranches clear (…-100 >=
// 50000), the third would breach, so the fill stops at 20 units after 2 buys.
func TestBuyGood_HullFill_StopsWhenMoneyGuardTrips(t *testing.T) {
	api := &sequentialCreditsAPIClient{credits: []int{50200, 50100, 50050}}
	executor, repo, mediator := newSpendFloorExecutor(t, api)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-2ME2"), logger)

	result := produceBuyWithFill(t, executor, repo.buildShip(), ctx, 1000, 0)

	if result == nil || result.QuantityAcquired != 20 {
		t.Fatalf("expected the fill to stop at 20 units when the reserve would be breached, got %+v", result)
	}
	if mediator.purchaseAttempts() != 2 {
		t.Fatalf("expected exactly 2 tranche buys before the money guard stopped the loop, got %d", mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "working-capital reserve") {
		t.Fatalf("expected a WARNING naming the working-capital reserve when the fill stopped, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// The loop STOPS when the market stock is exhausted: a tranche that comes back empty ("0 units
// processed", the drained-market signal) after its bounded retries ends the fill with what is
// already aboard, rather than looping forever against a dry market. First tranche succeeds (10
// units); the second is empty => the fill stops at 10.
func TestBuyGood_HullFill_StopsWhenMarketStockExhausted(t *testing.T) {
	emptyErr := fmt.Errorf("partial failure: purchase returned 0 units processed")
	// [success, then empty for every retry of the 2nd tranche]
	script := []error{nil, emptyErr, emptyErr, emptyErr, emptyErr, emptyErr}
	executor, repo, mediator := newDockRaceExecutor(t, script)

	result := produceBuyWithFill(t, executor, repo.buildShip(), context.Background(), 1000, 0)

	if result == nil || result.QuantityAcquired != 10 {
		t.Fatalf("expected the fill to stop at 10 units when the market ran dry, got %+v", result)
	}
	if mediator.purchaseAttempts() < 2 {
		t.Fatalf("expected at least a successful tranche plus an empty tranche attempt, got %d", mediator.purchaseAttempts())
	}
}

// ladderingMarketRepo serves one EXPORT source whose ask CLIMBS with each completed purchase
// (modelling a market laddering under our own draw) plus a stable low-ask peer that anchors the
// cross-market eligible median low, so the laddered source ask crosses the price ceiling mid-fill.
type ladderingMarketRepo struct {
	market.MarketRepository
	mediator                     *dockRaceMediator
	sourceWP, peerWP             string
	baseAsk, step, peerAsk, tvol int
}

func (r *ladderingMarketRepo) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	return []string{r.sourceWP, r.peerWP}, nil
}

func (r *ladderingMarketRepo) GetMarketData(_ context.Context, waypointSymbol string, _ int) (*market.Market, error) {
	supply := supplyHigh
	activity := "STRONG"
	ask := r.peerAsk
	if waypointSymbol == r.sourceWP {
		ask = r.baseAsk + r.step*r.mediator.purchaseAttempts() // ladders as we buy
	}
	good, err := market.NewTradeGood(dockRaceGood, &supply, &activity, ask, ask, r.tvol, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
}

func (r *ladderingMarketRepo) FindBestMarketBuying(_ context.Context, _, _ string, _ int) (*market.BestMarketBuyingResult, error) {
	return nil, nil
}

// The per-tranche price ceiling is re-checked EACH iteration so the loop does NOT ladder-chase a
// rising ask (RULINGS #4): once the source's live ask exceeds the cross-market ceiling mid-fill,
// the loop stops and delivers what is aboard (parks the rest). The source starts cheapest (picked
// supply-first), then ladders past the ceiling after the first buy; the fill stops at 10 units.
func TestBuyGood_HullFill_StopsWhenPriceCeilingTrips(t *testing.T) {
	shipRepo := &dockRaceShipRepo{location: dockRaceOrigin, navStatus: navigation.NavStatusDocked, cargoCapacity: 40}
	mediator := &dockRaceMediator{repo: shipRepo, dockHandler: tactics.NewDockShipHandler(shipRepo)}
	repo := &ladderingMarketRepo{
		mediator: mediator,
		sourceWP: "X1-DR-LADDER", peerWP: "X1-DR-PEER",
		baseAsk: 100, step: 1000, peerAsk: 120, tvol: 10,
	}
	marketLocator := NewMarketLocator(repo, nil, nil, nil)
	executor := NewProductionExecutorWithConfig(
		mediator, shipRepo, repo, marketLocator, &dockRaceClock{}, []time.Duration{time.Millisecond}, nil,
	)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := produceBuyWithFill(t, executor, shipRepo.buildShip(), ctx, 1000, 0)

	if result == nil || result.QuantityAcquired != 10 {
		t.Fatalf("expected the fill to stop at 10 units when the ask laddered past the ceiling, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly one buy before the price ceiling stopped the loop, got %d", mediator.purchaseAttempts())
	}
}

// Behavior-preserving guard (RULINGS #2): with NO hull-fill target on ctx — the goods-factory
// input path — buyGood does EXACTLY one tranche buy capped at trade_volume, leaving room for the
// factory's other inputs, exactly as before sp-2me2. Complements the pre-existing absorption test.
func TestBuyGood_NoFillTarget_SingleTranchePreserved(t *testing.T) {
	executor, repo, mediator := newDockRaceExecutor(t, nil)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("ProduceGood returned error: %v", err)
	}
	if result == nil || result.QuantityAcquired != 10 {
		t.Fatalf("the no-fill (factory input) path must buy exactly one trade_volume tranche (10), got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("the no-fill path must dispatch exactly 1 purchase, got %d", mediator.purchaseAttempts())
	}
}

package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-a5j7 Phase 2 / hzz5 X4: the input price ceiling is the BACKSTOP to the supply-first
// selector, and its BASELINE is now the median ask of ELIGIBLE (MODERATE+) sources
// CROSS-MARKET (EligibleSourceMedianAsk) — NOT the good's per-waypoint trailing median.
//
// WHY THE BASELINE CHANGED (the iv65 live failure): a per-waypoint trailing median is
// SELF-POISONING — a laddering source drags its own trailing median up behind it, so the 1.5x
// ceiling chases the ladder and never fires (KA42: ELECTRONICS laddered 8,973->12,976/u with
// ZERO parks because the 24h KA42 median was self-inflated). The eligible cross-market median
// is un-poisonable: a source that ladders degrades out of MODERATE+ supply and therefore out of
// the median. This suite pins the cross-market ceiling and the poisoning-shape park.

// fakePriceHistoryReader returns a scripted trailing ask series (or an error) — the trailing
// median source for the sourcing RESCUE cap (a depleted-market buy is validated against it).
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

// errFindAllMarketRepo errors on FindAllMarketsInSystem so EligibleSourceMedianAsk fails — the
// ceiling's fail-closed path (RULINGS #4), tested directly since the selector would otherwise
// surface the same read error first.
type errFindAllMarketRepo struct {
	market.MarketRepository
}

func (r *errFindAllMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return nil, errors.New("market DB unavailable")
}

// KA42 POISONING SHAPE (hzz5 X4 pin): a source the selector picks (top supply) whose ask is
// anomalously above its HEALTHY PEERS must PARK — because the ceiling's baseline is the eligible
// CROSS-MARKET median (~4,800), not the source's own self-inflated median (~12,976). The old
// per-waypoint baseline would have set the ceiling at ~19,464 (1.5x its own 12,976) and NEVER
// fired; the cross-market baseline sets it at 7,200 and parks the ladder.
func TestInputPriceCeiling_ParksSourceOverCrossMarketMedian_KA42(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-KA42", supply: supplyAbundant, ask: 12976}, // reads top-supply, priced like a ladder
		{waypoint: "X1-DR-B", supply: supplyHigh, ask: 4800},
		{waypoint: "X1-DR-C", supply: supplyHigh, ask: 4700},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("a source over the eligible cross-market ceiling must PARK, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	infos := logger.entriesWithLevel("INFO")
	if !spendFloorWarnContains(infos, "cross-market median") || !spendFloorWarnContains(infos, "12976") {
		t.Fatalf("expected a cross-market ceiling park naming the ask, got: %+v", infos)
	}
	if !spendFloorWarnContains(infos, "4800") {
		t.Fatalf("expected the park to carry the eligible cross-market median (4800), not the self-poisoned ~12976, got: %+v", infos)
	}
}

// A pick within the cross-market ceiling proceeds — the guard is not over-aggressive when the
// chosen source is priced in line with its healthy peers.
func TestInputPriceCeiling_ProceedsUnderCrossMarketMedian(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-A", supply: supplyHigh, ask: 4800},
		{waypoint: "X1-DR-B", supply: supplyHigh, ask: 4900},
		{waypoint: "X1-DR-C", supply: supplyHigh, ask: 5000},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("an in-line pick must proceed, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if spendFloorWarnContains(logger.entriesWithLevel("INFO"), "ladder-chase refused") {
		t.Fatalf("an in-line pick must not log a ceiling park, got: %+v", logger.entriesWithLevel("INFO"))
	}
}

// A tighter multiplier parks a pick the default 1.5x would clear — proving the ctx-threaded
// input_price_ceiling_multiplier reaches the cross-market ceiling.
func TestInputPriceCeiling_CustomMultiplierHonored(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-PICK", supply: supplyAbundant, ask: 6000}, // top-supply pick
		{waypoint: "X1-DR-B", supply: supplyHigh, ask: 4800},
		{waypoint: "X1-DR-C", supply: supplyHigh, ask: 4700},
	}}
	// eligible median 4800: default ceiling 7200 (6000 clears), 1.2x ceiling 5760 (6000 parks).
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	ctx := WithInputPriceCeiling(common.WithLogger(context.Background(), &dwellCapturingLogger{}), 1.2, false)

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("ask 6000 must park under a 1.2x cross-market ceiling (5760), got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
}

// The emergency disable flag turns the ceiling OFF even for an over-median pick: the buy proceeds.
func TestInputPriceCeiling_DisableFlagProceeds(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-PICK", supply: supplyAbundant, ask: 12976},
		{waypoint: "X1-DR-B", supply: supplyHigh, ask: 4800},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	ctx := WithInputPriceCeiling(common.WithLogger(context.Background(), &dwellCapturingLogger{}), 0, true) // disabled

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("a disabled ceiling must proceed, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
}

// The eligible-median read itself failing must fail CLOSED (PARK) — a guard whose job is
// refusing to overpay must not go blind (RULINGS #4). Tested directly on inputPriceCeilingParked
// because the selector surfaces the same read error before the ceiling on the buyGood path.
func TestInputPriceCeiling_FailsClosedOnReadError(t *testing.T) {
	repo := &errFindAllMarketRepo{}
	shipRepo := &dockRaceShipRepo{location: dockRaceOrigin, navStatus: navigation.NavStatusDocked, cargoCapacity: 40}
	executor := NewProductionExecutorWithConfig(
		&dockRaceMediator{repo: shipRepo}, shipRepo, repo, NewMarketLocator(repo, nil, nil, nil),
		&dockRaceClock{}, []time.Duration{time.Millisecond}, nil,
	)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	if !executor.inputPriceCeilingParked(ctx, "X1-DR-X", dockRaceGood, "X1-DR", 1, 5000) {
		t.Fatalf("a blind eligible-median read must fail CLOSED (park), got proceed")
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "fail-closed") {
		t.Fatalf("expected a fail-closed WARNING, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

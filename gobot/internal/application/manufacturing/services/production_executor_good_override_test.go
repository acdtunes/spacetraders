package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// sp-sdyo gate 2 (INPUT-PRICE-CEILING) — per-good ladder-chase multiplier override, plus the
// money-integrity guardrail. The per-good override tunes THIS good's per-tranche ceiling only; it
// must NOT bypass the structural inputRoundMarginParked round-gate nor the sp-9aoc solvency floor,
// and it is hard-capped so a fat-finger can loosen but never disable the ceiling (RULINGS #4).
// dockRaceGood ("IRON") is the good produceBuy sources, so overrides key on it.

// SURGICAL UNSTICK (acceptance): a pick the GLOBAL 1.5x ceiling would PARK proceeds under a per-good
// {priceCeilingMult:3.0} override — the stuck bottleneck is bought past the global ceiling while the
// guard stays at 1.5x for every other good.
func TestInputCeilingOverride_ProceedsPastGlobalCeiling(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-PICK", supply: supplyAbundant, ask: 10000}, // top-supply pick, priced like a ladder
		{waypoint: "X1-DR-B", supply: supplyHigh, ask: 4800},
		{waypoint: "X1-DR-C", supply: supplyHigh, ask: 4700},
	}}
	// eligible median 4800: global 1.5x ceiling 7200 (10000 PARKS); per-good 3.0 ceiling 14400 (10000 CLEARS).
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	overrides := manufacturing.GoodGatingOverrides{dockRaceGood: {PriceCeilingMult: 3.0}}
	ctx := WithGoodGatingOverrides(common.WithLogger(context.Background(), &dwellCapturingLogger{}), overrides)

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("ask 10000 must PROCEED under a per-good 3.0 ceiling (14400), got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
}

// REGRESSION: with an override for a DIFFERENT good, the non-overridden dockRaceGood is
// byte-identical to today — the same ask still PARKS under the untouched global 1.5x ceiling.
func TestInputCeilingOverride_NonOverriddenGoodUnchanged(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-PICK", supply: supplyAbundant, ask: 10000},
		{waypoint: "X1-DR-B", supply: supplyHigh, ask: 4800},
		{waypoint: "X1-DR-C", supply: supplyHigh, ask: 4700},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	// The override targets some OTHER good; dockRaceGood is not in the map, so it keeps the global ceiling.
	overrides := manufacturing.GoodGatingOverrides{"SOME_OTHER_GOOD": {PriceCeilingMult: 3.0}}
	logger := &dwellCapturingLogger{}
	ctx := WithGoodGatingOverrides(common.WithLogger(context.Background(), logger), overrides)

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("a non-overridden good must still PARK under the global 1.5x ceiling (7200), got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("INFO"), "ladder-chase refused") {
		t.Fatalf("the non-overridden park must log the usual ceiling refusal, got: %+v", logger.entriesWithLevel("INFO"))
	}
}

// GUARDRAIL (RULINGS #4): a fat-finger override (1000x) is HARD-CAPPED at 5x, so a pick above the
// 5x ceiling STILL PARKS. If the cap were absent, a 1000x ceiling (~4.8M) would clear a 30000 ask —
// the cap is what refuses it, proving the override can loosen but never disable the guard.
func TestInputCeilingOverride_HardCapRefusesFatFinger(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-PICK", supply: supplyAbundant, ask: 30000},
		{waypoint: "X1-DR-B", supply: supplyHigh, ask: 4800},
		{waypoint: "X1-DR-C", supply: supplyHigh, ask: 4700},
	}}
	// eligible median 4800: capped 5x ceiling 24000 (30000 PARKS). An uncapped 1000x would be 4.8M (would clear).
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	overrides := manufacturing.GoodGatingOverrides{dockRaceGood: {PriceCeilingMult: 1000.0}}
	ctx := WithGoodGatingOverrides(common.WithLogger(context.Background(), &dwellCapturingLogger{}), overrides)

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("a fat-finger 1000x override must be capped to 5x (24000) so ask 30000 still PARKS, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
}

// GUARDRAIL (acceptance): the structural per-round negative-margin gate is INDEPENDENT of the price
// override. With an aggressive per-good override stamped on ctx, inputRoundMarginParked STILL fires
// for a structurally-underwater chain (summed input ask > output resale bid) — the price knob raises
// the per-tranche ceiling only, never the round gate, so the sp-iv65 bleed stays prevented.
func TestInputRoundMargin_StillParksUnderAggressiveOverride(t *testing.T) {
	repo := &chainGateMarketRepo{sinkBid: 7500, fabAsk: 7000, inputAsks: map[string]int{cgInput1: 19000, cgInput2: 18700}}
	executor, _, _ := newChainGateExecutor(t, repo)
	overrides := manufacturing.GoodGatingOverrides{
		cgOutput: {Strategy: "prefer-buy", PriceCeilingMult: 5.0},
		cgInput1: {PriceCeilingMult: 5.0},
		cgInput2: {PriceCeilingMult: 5.0},
	}
	ctx := WithGoodGatingOverrides(common.WithLogger(context.Background(), &dwellCapturingLogger{}), overrides)

	if !executor.inputRoundMarginParked(ctx, cgChain(), cgSystem, 1) {
		t.Fatalf("an aggressive price override must NOT bypass the structural round gate: underwater chain must still PARK")
	}
}

// GUARDRAIL (acceptance): the sp-9aoc solvency floor is INDEPENDENT of the price override. With an
// aggressive per-good override stamped, an input buy that would drop live treasury below the
// working-capital reserve STILL parks fail-closed — the treasury floor stays hard for both
// overridden and non-overridden goods.
func TestSolvencyFloor_StillParksUnderAggressiveOverride(t *testing.T) {
	// 40000 - 100 (the harness's fixed input-buy cost) = 39900 < 50000 reserve -> breach.
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 40000})
	logger := &dwellCapturingLogger{}
	overrides := manufacturing.GoodGatingOverrides{dockRaceGood: {PriceCeilingMult: 5.0}}
	ctx := WithGoodGatingOverrides(common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-SDYO"), logger), overrides)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a breaching buy under an aggressive override must park gracefully, not error: %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || mediator.purchaseAttempts() != 0 {
		t.Fatalf("the solvency floor must still PARK a treasury breach despite the override, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "working-capital reserve") {
		t.Fatalf("expected the solvency-floor park WARNING even under the override, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

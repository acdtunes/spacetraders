package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// sp-agzj (P1 money-guard): the factory input-buy spend floor is UNIFIED with the
// fleet's per-run working-capital reserve — the effective floor is max(50000,
// configured), the 50k an immutable lower bound (RULINGS #5). Before this, factories
// enforced a hardcoded 50k while the fleet reserved 1M, so a factory legally rode the
// balance down to a ~617k trough. These tests drive buyGood with the reserve stamped on
// ctx (WithConfiguredReserve, as the coordinator does from the working_capital_reserve
// launch-config key) and the same deterministic 100-credit buy as the base floor suite.

// A configured reserve ABOVE the 50k default is honored: a treasury that clears the old
// 50k floor but NOT the configured 1M must PARK. This proves the fleet reserve config
// actually reaches the factory floor (the hardcoded 50k would have proceeded here).
func TestBuyGood_ReserveUnify_ConfiguredReserveHonoredAbove50k(t *testing.T) {
	// 500000 - 100 = 499900: clears 50k, but < 1,000,000 configured reserve -> breach.
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 500000})
	logger := &dwellCapturingLogger{}
	ctx := WithConfiguredReserve(common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-AGZJ"), logger), 1_000_000)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a breaching input buy must park gracefully, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0 {
		t.Fatalf("a buy breaching the 1M configured reserve must yield a zero-spend park, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a buy breaching the configured 1M reserve must dispatch ZERO purchases (the old hardcoded 50k floor would have let it through), got %d", mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "1000000") {
		t.Fatalf("expected the park WARNING to report the effective 1,000,000 reserve, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// A configured reserve BELOW the 50k lower bound is clamped UP to 50k — never weakened
// (RULINGS #5). A treasury that clears the raw 10k config but not the 50k floor must
// PARK, proving the per-run knob cannot lower the floor beneath its immutable bound.
func TestBuyGood_ReserveUnify_ConfiguredBelow50kClampedTo50k(t *testing.T) {
	// 45000 - 100 = 44900: clears the raw 10k config, but < 50000 lower bound -> breach.
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 45000})
	logger := &dwellCapturingLogger{}
	ctx := WithConfiguredReserve(common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-AGZJ"), logger), 10_000)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a clamped-floor park must be graceful, got error: %v", err)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a sub-50k configured reserve must be clamped UP to 50k and PARK this buy (44900 < 50000); a raw-10k floor would have proceeded, got %d purchases", mediator.purchaseAttempts())
	}
	if result == nil || result.QuantityAcquired != 0 {
		t.Fatalf("expected a zero-spend park, got %+v", result)
	}
}

// A configured reserve of 0 (absent) leaves the standing 50k floor exactly as before: a
// treasury clearing 50k proceeds. Guards against the unification accidentally raising or
// breaking the default path every existing factory relies on.
func TestBuyGood_ReserveUnify_ConfigZeroKeepsDefault50k(t *testing.T) {
	// 500000 - 100 = 499900 >= 50000 default -> proceed, exactly as pre-sp-agzj.
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 500000})
	logger := &dwellCapturingLogger{}
	ctx := WithConfiguredReserve(common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-AGZJ"), logger), 0)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a config-0 buy clearing the 50k default must proceed, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("a config-0 buy clearing the 50k default must proceed to a real purchase, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase (default 50k floor cleared), got %d", mediator.purchaseAttempts())
	}
}

// Regression: a treasury comfortably clearing even a RAISED (1M) floor proceeds to a real
// purchase — the unified floor is not over-aggressive once the fleet reserve is applied.
func TestBuyGood_ReserveUnify_AmpleBalanceProceedsUnderRaisedFloor(t *testing.T) {
	// 5,000,000 - 100 = 4,999,900 >= 1,000,000 configured reserve -> proceed.
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 5_000_000})
	logger := &dwellCapturingLogger{}
	ctx := WithConfiguredReserve(common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-AGZJ"), logger), 1_000_000)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("an ample-balance buy under a raised floor must proceed, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("expected a real purchase when the treasury clears the raised reserve, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase under the raised-but-cleared floor, got %d", mediator.purchaseAttempts())
	}
}

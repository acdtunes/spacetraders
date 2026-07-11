package services

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// sp-yqx4 (P1 deadlock fix): with a treasury-percent stamped (production factories resolve
// it to 40% by default), the factory input-buy floor becomes max(50k, min(reserve, pct% ×
// live treasury)) instead of the flat absolute reserve. A reserve above the treasury no
// longer parks every factory buy — the same deadlock that idled the tour fleet. These tests
// drive buyGood through the REAL spendFloorBreached seam with the pct set (via the shared
// common.EffectiveReserveFloor resolver); the sp-agzj reserve-unify suite (no pct) proves
// the absolute floor is untouched when the counter-cyclical mode is off. Harness economics
// are the deterministic 100-credit input buy (min(40,10)=10 units × 10/unit).

// Proportional unblocks a sub-2.5M treasury the absolute would have parked: at 500k with a
// 1M configured reserve and 40%, the floor resolves to 200k, so the 100-credit input buy
// (499,900 remaining ≥ 200k) PROCEEDS — where the flat 1M floor parked it (499,900 < 1M),
// the deadlock this bead removes. The counter-cyclical INFO must fire (the watch signal).
func TestBuyGood_ProportionalFloor_ProceedsWhereAbsoluteWouldPark(t *testing.T) {
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 500_000})
	logger := &dwellCapturingLogger{}
	ctx := common.WithReserveTreasuryPct(
		WithConfiguredReserve(common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-YQX4"), logger), 1_000_000),
		40,
	)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a proportional-floor input buy must proceed, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("at 500k treasury the 40%% proportional floor (200k) admits the buy where the 1M absolute floor would park it — expected a real purchase, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase (proportional floor cleared), got %d", mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("INFO"), "200000") {
		t.Fatalf("the counter-cyclical INFO must report the 200,000 proportional floor engaging; got: %+v", logger.entriesWithLevel("INFO"))
	}
}

// The immutable 50k bound parks even with the proportional floor active (RULINGS #5): at
// 50,050 treasury the 40% term (20,020) is clamped UP to 50k, so the 100-credit buy
// (49,950 remaining < 50k) still PARKS. The floor is never weakened below its non-tunable
// lower bound, no matter how low the treasury or how the pct resolves.
func TestBuyGood_ProportionalFloor_Immutable50kStillParks(t *testing.T) {
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 50_050})
	logger := &dwellCapturingLogger{}
	ctx := common.WithReserveTreasuryPct(
		WithConfiguredReserve(common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-YQX4"), logger), 1_000_000),
		40,
	)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("an immutable-floor park must be graceful, got error: %v", err)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("49,950 remaining is below the immutable 50k floor — the buy must PARK even with the pct active, got %d purchases", mediator.purchaseAttempts())
	}
	if result == nil || result.QuantityAcquired != 0 {
		t.Fatalf("expected a zero-spend park, got %+v", result)
	}
}

// Above 2.5M the configured absolute binds exactly as pre-sp-yqx4: at 3M with a 1M reserve
// and 40%, the proportional term (1.2M) exceeds the absolute, so the floor stays 1M and the
// counter-cyclical INFO must NOT fire. Guards against the proportional path altering
// high-treasury factory behavior.
func TestBuyGood_ProportionalFloor_AbsoluteBindsAbove2p5M(t *testing.T) {
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 3_000_000})
	logger := &dwellCapturingLogger{}
	ctx := common.WithReserveTreasuryPct(
		WithConfiguredReserve(common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-YQX4"), logger), 1_000_000),
		40,
	)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("an absolute-bind buy must proceed, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("at 3M treasury the 1M absolute binds and the buy proceeds, got %+v / %d purchases", result, mediator.purchaseAttempts())
	}
	if spendFloorWarnContains(logger.entriesWithLevel("INFO"), "Counter-cyclical") {
		t.Fatalf("above 2.5M the absolute binds — the counter-cyclical floor must NOT engage, but an INFO fired: %+v", logger.entriesWithLevel("INFO"))
	}
}

// Fail-closed is NOT weakened by the pct (RULINGS #4): an unreadable live treasury still
// parks the input buy even with the proportional floor active — the guard never computes a
// lowered floor against a treasury it could not read.
func TestBuyGood_ProportionalFloor_UnreadableParksFailClosed(t *testing.T) {
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{err: errors.New("simulated live-read failure")})
	logger := &dwellCapturingLogger{}
	ctx := common.WithReserveTreasuryPct(
		WithConfiguredReserve(common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-YQX4"), logger), 1_000_000),
		40,
	)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("an unreadable treasury must park gracefully, not error: %v", err)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a blind factory floor must dispatch ZERO purchases even with the pct set (fail-closed), got %d", mediator.purchaseAttempts())
	}
	if result == nil || result.QuantityAcquired != 0 {
		t.Fatalf("expected a zero-spend fail-closed park, got %+v", result)
	}
}

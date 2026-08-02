package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-65xqo (P1 money-guard, RULINGS #4 floor-everywhere): the fabricated-OUTPUT buy
// (purchaseFabricatedOutput — the two LARGEST gate purchases, FAB_MATS / ADVANCED_CIRCUITRY)
// executed bounded only by a UNITS cap (min(cargo, tradeVolume)) with NO treasury check, while
// the INPUT buys (buyGood) were already floored by the 50k working-capital reserve and
// serialized by the cross-container concurrent cap. This suite drives the output buy
// directly (the gate_output_drain idiom) with a live apiClient wired so the SAME floor primitives
// are active, and asserts the output buy is now floored IDENTICALLY to buyGood: PARK (zero-spend,
// zero-dispatch) when the buy would drop live treasury below the reserve, PROCEED + RELEASE the
// reservation when it clears, and PARK on the cross-container concurrent cap. No new floor, no new
// knob — it reuses spendFloorBreached / reserveConcurrentSpendOrPark / releaseSpendReservation.
//
// Economics are deterministic: the buy is min(cargo=400, tradeVolume=43)=43 units at unitPrice 10
// (the harvest cost the caller threads from tradeGood.SellPrice) = a 430-credit projected cost, so
// the treasury figures below are chosen relative to that fixed cost and the 50000 reserve. The buy
// is trivially affordable in every case — the ONLY thing that can refuse it is the money guard.

const (
	outputFloorCargo       = 400
	outputFloorTradeVolume = 43
	outputFloorUnitPrice   = 10 // per-unit harvest cost -> projected cost 43 * 10 = 430
)

// newOutputFloorExecutor mirrors newGateFillExecutor (ship DOCKED at the factory with an empty hold)
// but injects a live apiClient so the working-capital floor is ACTIVE on the output-buy path — the
// same fork newSpendFloorExecutor makes for the input path.
func newOutputFloorExecutor(t *testing.T, apiClient domainPorts.APIClient) (*ProductionExecutor, *dockRaceMediator) {
	t.Helper()
	repo := &dockRaceShipRepo{
		location:      gateFillTestFactoryWP,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: outputFloorCargo,
	}
	mediator := &dockRaceMediator{repo: repo, dockHandler: tactics.NewDockShipHandler(repo)}
	executor := NewProductionExecutorWithConfig(
		mediator, repo, nil, NewMarketLocator(nil, nil, nil, nil),
		&dockRaceClock{},
		[]time.Duration{time.Millisecond}, apiClient,
	)
	return executor, mediator
}

func buyFabricatedOutput(executor *ProductionExecutor, ctx context.Context) (int, int, error) {
	return executor.purchaseFabricatedOutput(ctx, harvestRun{good: dockRaceGood, waypointSymbol: gateFillTestFactoryWP, shipSymbol: dockRaceShip, playerID: shared.MustNewPlayerID(1)}, outputFloorTradeVolume, outputFloorUnitPrice)
}

// A treasury far above the 430-credit buy cost but below the 50000 reserve must PARK the output
// harvest: the buy is trivially affordable, so the ONLY reason to refuse it is the working-capital
// floor. Proves the floor — not affordability — stops the drain on the OUTPUT path, closing the
// sp-65xqo gap where the two largest gate buys could execute below the reserve.
func TestPurchaseFabricatedOutput_SpendFloor_ParksWhenBuyWouldBreachReserve(t *testing.T) {
	// 40000 - 430 = 39570 < 50000 reserve -> breach.
	executor, mediator := newOutputFloorExecutor(t, &spendFloorFakeAPIClient{credits: 40000})
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-65XQO"), logger)

	units, cost, err := buyFabricatedOutput(executor, ctx)
	if err != nil {
		t.Fatalf("a breaching output harvest must be parked gracefully, not surfaced as an error: got %v", err)
	}
	if units != 0 || cost != 0 {
		t.Fatalf("a parked (spend-floor) output buy must yield a zero-spend result, got units=%d cost=%d", units, cost)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a breaching output buy must dispatch ZERO purchases (no spend below the floor), got %d", mediator.purchaseAttempts())
	}
	warns := logger.entriesWithLevel("WARNING")
	if !spendFloorWarnContains(warns, "working-capital reserve") {
		t.Fatalf("expected a WARNING naming the working-capital reserve breach, got: %+v", warns)
	}
	if !spendFloorWarnContains(warns, dockRaceGood) || !spendFloorWarnContains(warns, gateFillTestFactoryWP) {
		t.Fatalf("expected the park WARNING to name the good %s and factory %s, got: %+v", dockRaceGood, gateFillTestFactoryWP, warns)
	}
}

// A treasury that comfortably clears the reserve after the buy must proceed exactly as before the
// floor existed: one real purchase for the full tv, a non-zero result — and the concurrent-spend
// reservation it took must be RELEASED after the buy so the held budget returns to the pool. Proves
// the guard is not over-aggressive once wired and that it composes with the cross-container cap.
func TestPurchaseFabricatedOutput_SpendFloor_ProceedsAndReleasesWhenTreasuryClears(t *testing.T) {
	// 500000 - 430 = 499570 >= 50000 reserve -> proceed.
	executor, mediator := newOutputFloorExecutor(t, &spendFloorFakeAPIClient{credits: 500000})
	ledger := &fakeSpendLedger{reserveOK: true, reserveID: "RES-65XQO"}
	executor.SetSpendLedger(ledger)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-65XQO"), logger)

	units, _, err := buyFabricatedOutput(executor, ctx)
	if err != nil {
		t.Fatalf("a clearing output harvest must proceed, got error: %v", err)
	}
	if units != outputFloorTradeVolume {
		t.Fatalf("a clearing output buy must harvest the full tv=%d unchanged, got %d", outputFloorTradeVolume, units)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase for a clearing output buy, got %d", mediator.purchaseAttempts())
	}
	if len(ledger.released) != 1 || ledger.released[0] != "RES-65XQO" {
		t.Fatalf("the output buy's concurrent-spend reservation must be released exactly once after completion, got %v", ledger.released)
	}
	if spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "working-capital reserve") {
		t.Fatalf("a clearing output buy must not log a spend-floor park WARNING, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// A treasury that clears the per-container floor but a ledger that rejects the reservation must PARK
// the output buy: the buy is affordable and clears the point check, so the ONLY reason to refuse it
// is the cross-container concurrent cap. Proves the output path is serialized through the SAME shared
// ledger as buyGood, so N factories harvesting concurrently cannot collectively breach the reserve.
func TestPurchaseFabricatedOutput_ConcurrentCap_ParksWhenReservationRejected(t *testing.T) {
	executor, mediator := newOutputFloorExecutor(t, &spendFloorFakeAPIClient{credits: 500000})
	ledger := &fakeSpendLedger{reserveOK: false}
	executor.SetSpendLedger(ledger)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-65XQO"), logger)

	units, cost, err := buyFabricatedOutput(executor, ctx)
	if err != nil {
		t.Fatalf("a cap-parked output buy must be graceful, not an error: got %v", err)
	}
	if units != 0 || cost != 0 {
		t.Fatalf("a cap-parked output buy must yield a zero-spend result, got units=%d cost=%d", units, cost)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a cap-parked output buy must dispatch ZERO purchases, got %d", mediator.purchaseAttempts())
	}
	if ledger.reserveCalls != 1 {
		t.Fatalf("the cap gate must consult the ledger exactly once, got %d calls", ledger.reserveCalls)
	}
	if len(ledger.released) != 0 {
		t.Fatalf("a rejected reservation is rolled back by the ledger — the executor must not Release it, got %v", ledger.released)
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "concurrent spend cap") {
		t.Fatalf("expected a WARNING naming the concurrent spend cap, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// sp-w3he: the cross-container concurrent spend cap sits in buyGood as a SECOND gate after
// the per-buy floor (sp-9aoc). This suite drives buyGood (via ProduceGood with a BUY node)
// through the same dock-race harness the floor suite uses, wiring a live apiClient whose
// treasury comfortably CLEARS the floor (so the cap gate is actually reached) plus a fake
// ledger, and asserts the executor<->ledger contract: PARK on rejection, PARK fail-closed on
// ledger error, and RELEASE the reservation after the buy completes. The ledger's own
// concurrency/atomicity is proven at the DB level in the persistence suite.

// fakeSpendLedger is a scripted SpendReservationLedger: reserveOK/reserveErr drive the
// Reserve outcome, and it records every reservation id it is asked to Release so a test can
// prove the executor releases after the buy.
type fakeSpendLedger struct {
	reserveOK    bool
	reserveErr   error
	reserveID    string
	reserveCalls int
	released     []string
}

func (f *fakeSpendLedger) Reserve(_ context.Context, _ int, _ string, _, _, _ int) (string, bool, error) {
	f.reserveCalls++
	if f.reserveErr != nil {
		return "", false, f.reserveErr
	}
	if !f.reserveOK {
		return "", false, nil
	}
	return f.reserveID, true, nil
}

func (f *fakeSpendLedger) Release(_ context.Context, id string) error {
	f.released = append(f.released, id)
	return nil
}

func (f *fakeSpendLedger) ExpireStale(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}

// A treasury that clears the floor but a ledger that rejects the reservation must PARK: the
// buy is affordable and clears the per-container floor, so the ONLY reason to refuse it is the
// cross-container concurrent cap. Zero spend, zero dispatch, and the cause named in the log.
func TestBuyGood_ConcurrentCap_ParksWhenReservationRejected(t *testing.T) {
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 500000})
	ledger := &fakeSpendLedger{reserveOK: false}
	executor.SetSpendLedger(ledger)

	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-W3HE"), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a cap-parked buy must be graceful, not an error: got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0 {
		t.Fatalf("a cap-parked buy must yield a zero-spend result, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a cap-parked buy must dispatch ZERO purchases, got %d", mediator.purchaseAttempts())
	}
	if ledger.reserveCalls != 1 {
		t.Fatalf("the cap gate must consult the ledger exactly once, got %d calls", ledger.reserveCalls)
	}
	if len(ledger.released) != 0 {
		t.Fatalf("a rejected reservation is rolled back by the ledger — the executor must not Release it, got %v", ledger.released)
	}
	warns := logger.entriesWithLevel("WARNING")
	if !spendFloorWarnContains(warns, "concurrent spend cap") {
		t.Fatalf("expected a WARNING naming the concurrent spend cap, got: %+v", warns)
	}
	if !spendFloorWarnContains(warns, dockRaceGood) || !spendFloorWarnContains(warns, dockRaceMarketWP) {
		t.Fatalf("expected the park WARNING to name the good %s and market %s, got: %+v", dockRaceGood, dockRaceMarketWP, warns)
	}
}

// The reservation must be RELEASED after the buy completes so the budget it held returns to
// the pool. A cleared floor + an accepting ledger yields one real purchase, and the executor
// must Release exactly the reservation id the ledger handed back.
func TestBuyGood_ConcurrentCap_ReleasesReservationAfterSuccessfulBuy(t *testing.T) {
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 500000})
	ledger := &fakeSpendLedger{reserveOK: true, reserveID: "RES-W3HE"}
	executor.SetSpendLedger(ledger)

	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-W3HE"), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("an accepted reservation must let the buy proceed, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("expected a successful purchase when floor and cap both clear, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase, got %d", mediator.purchaseAttempts())
	}
	if len(ledger.released) != 1 || ledger.released[0] != "RES-W3HE" {
		t.Fatalf("the buy's reservation must be released exactly once after completion, got %v", ledger.released)
	}
}

// A ledger error must fail CLOSED — park the buy, dispatch nothing. A cap that let a buy
// through when its own bookkeeping failed would defeat its purpose.
func TestBuyGood_ConcurrentCap_ParksFailClosedOnLedgerError(t *testing.T) {
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 500000})
	ledger := &fakeSpendLedger{reserveErr: errors.New("ledger unavailable")}
	executor.SetSpendLedger(ledger)

	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-W3HE"), logger)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a ledger error must park gracefully, not surface an error: got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 || result.TotalCost != 0 {
		t.Fatalf("a fail-closed cap park must yield a zero-spend result, got %+v", result)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a fail-closed cap park must dispatch ZERO purchases, got %d", mediator.purchaseAttempts())
	}
	if !spendFloorWarnContains(logger.entriesWithLevel("WARNING"), "fail-closed") {
		t.Fatalf("expected a fail-closed WARNING explaining the ledger error, got: %+v", logger.entriesWithLevel("WARNING"))
	}
}

// No ledger wired (SetSpendLedger never called) fails OPEN: the cap is simply unavailable and
// the buy proceeds on the floor alone. This is the optional-port contract every other suite in
// this package relies on by never wiring a ledger — an explicit guard so a future change that
// made a nil ledger fail-closed (silently parking every factory buy) is caught here.
func TestBuyGood_ConcurrentCap_ProceedsWhenNoLedgerWired_FailOpen(t *testing.T) {
	executor, repo, mediator := newSpendFloorExecutor(t, &spendFloorFakeAPIClient{credits: 500000})
	// deliberately no SetSpendLedger

	ctx := common.WithPlayerToken(context.Background(), "TOKEN-W3HE")
	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(ctx, repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("with no ledger wired the cap must fail open and proceed, got error: %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("a fail-open (no-ledger) buy must proceed to a real purchase, got %+v", result)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase on the fail-open path, got %d", mediator.purchaseAttempts())
	}
}

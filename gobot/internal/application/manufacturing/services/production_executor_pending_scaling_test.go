package services

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

type fakePendingScalingReservation struct {
	amount int64
	err    error
	calls  int
}

func (f *fakePendingScalingReservation) PendingReservation(_ context.Context, _ int) (int64, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	return f.amount, nil
}

func TestRaiseForPendingScaling_WorkedExample_MaxNotAdd(t *testing.T) {
	const existingFloor = 300_000
	const pending = 508_000
	const naiveAdd = existingFloor + pending
	const treasury = 520_000

	executor := NewProductionExecutor(nil, nil, nil, nil, nil, nil)
	executor.SetPendingScalingReservation(&fakePendingScalingReservation{amount: pending})

	got := executor.raiseForPendingScaling(context.Background(), 11, existingFloor)

	if got != pending {
		t.Fatalf("want MAX(%d, %d)=%d, got %d", existingFloor, pending, pending, got)
	}
	if got == naiveAdd {
		t.Fatalf("composed floor must never equal the naive ADD (%d)", naiveAdd)
	}
	if cushion := treasury - got; cushion < 0 {
		t.Fatalf("treasury %d must clear the correctly-composed floor %d (cushion=%d)", treasury, got, cushion)
	}
	if cushion := treasury - naiveAdd; cushion >= 0 {
		t.Fatalf("test setup error: treasury %d must NOT clear the naive-ADD floor %d", treasury, naiveAdd)
	}
}

func TestRaiseForPendingScaling_ExistingFloorAboveReservation_StaysAtExistingFloor(t *testing.T) {
	const existingFloor = 600_000
	const pending = 508_000

	executor := NewProductionExecutor(nil, nil, nil, nil, nil, nil)
	executor.SetPendingScalingReservation(&fakePendingScalingReservation{amount: pending})

	got := executor.raiseForPendingScaling(context.Background(), 11, existingFloor)
	if got != existingFloor {
		t.Fatalf("a smaller pending reservation must never lower the existing floor: want %d, got %d", existingFloor, got)
	}
}

func TestRaiseForPendingScaling_ZeroAmountMeansNothingPending(t *testing.T) {
	executor := NewProductionExecutor(nil, nil, nil, nil, nil, nil)
	executor.SetPendingScalingReservation(&fakePendingScalingReservation{amount: 0})

	got := executor.raiseForPendingScaling(context.Background(), 11, 250_000)
	if got != 250_000 {
		t.Fatalf("amount=0 must leave the floor unchanged, got %d", got)
	}
}

func TestRaiseForPendingScaling_UnwiredIsByteIdentical(t *testing.T) {
	executor := NewProductionExecutor(nil, nil, nil, nil, nil, nil)

	got := executor.raiseForPendingScaling(context.Background(), 11, 250_000)
	if got != 250_000 {
		t.Fatalf("an unwired port must never change the floor, got %d", got)
	}
}

func TestRaiseForPendingScaling_ReadErrorFailsOpen(t *testing.T) {
	executor := NewProductionExecutor(nil, nil, nil, nil, nil, nil)
	fake := &fakePendingScalingReservation{err: errors.New("db unreachable")}
	executor.SetPendingScalingReservation(fake)

	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	got := executor.raiseForPendingScaling(ctx, 11, 250_000)
	if got != 250_000 {
		t.Fatalf("a read error must leave the existing floor unchanged (fail-open), got %d", got)
	}
	if fake.calls != 1 {
		t.Fatalf("expected the port to be consulted once, got %d calls", fake.calls)
	}
	if len(logger.entriesWithLevel("WARNING")) == 0 {
		t.Fatalf("a read error must be surfaced as a WARNING, not swallowed silently")
	}
}

// 358,000 is the real CapitalSplit-derived floor at this treasury (vs. the illustrative 300,000 above).
func TestBudgetedReserveFloor_PendingScalingReservation_ComposesOverTradeRaisedFloorViaMax(t *testing.T) {
	const treasury = 520_000
	const pending = 508_000
	const wantExistingFloor = 358_000
	const naiveAdd = wantExistingFloor + pending

	executor := NewProductionExecutor(nil, nil, nil, nil, nil, nil)
	executor.SetCapitalWorkSensor(&fakeCapitalWorkSensor{tradeWork: true, constructionWork: true})

	preExisting := executor.budgetedReserveFloor(context.Background(), 11, treasury)
	if preExisting != wantExistingFloor {
		t.Fatalf("test assumption wrong: expected the trade-raised floor at treasury=%d to be %d, got %d", treasury, wantExistingFloor, preExisting)
	}

	executor.SetPendingScalingReservation(&fakePendingScalingReservation{amount: pending})
	got := executor.budgetedReserveFloor(context.Background(), 11, treasury)

	if got != pending {
		t.Fatalf("want MAX(%d, %d)=%d, got %d", wantExistingFloor, pending, pending, got)
	}
	if got == naiveAdd {
		t.Fatalf("must never equal the naive-ADD floor %d", naiveAdd)
	}
	if cushion := treasury - got; cushion < 0 {
		t.Fatalf("treasury %d must clear the correctly-composed floor %d (cushion=%d)", treasury, got, cushion)
	}
	if cushion := treasury - naiveAdd; cushion >= 0 {
		t.Fatalf("treasury %d must NOT clear the naive-ADD floor %d", treasury, naiveAdd)
	}
}

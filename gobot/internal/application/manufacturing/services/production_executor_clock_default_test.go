package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-vh1s live nil-panic regression. sp-vh1s made PollForProduction and the throughput-pacing
// window clock-driven (e.clock.Now() at :984, :1141, :1173). The construction daemon builds its
// producer the direct way — NewProductionExecutor(..., nil /*clock*/, ...) at main.go:669 — whereas
// the goods-factory path defaults nil→RealClock inside NewRunFactoryCoordinatorHandler BEFORE
// building the executor. So only the construction path handed the executor a nil clock, and the
// unified gate-fill nil-panicked on EVERY construction tick at the first e.clock.Now(). The mock
// clock the pacing tests inject hid it — the real construction-path wiring was never exercised.
// These tests exercise that exact nil-clock construction; they must not panic once the constructor
// defaults the clock.

// TestConstructionPathExecutor_NilClock_DoesNotPanicOnGateFillPoll builds the executor EXACTLY as
// the construction daemon does (NewProductionExecutor with a nil clock, main.go:669) and drives the
// gate-fill poll path (PollForProduction) that panicked at production_executor.go:984. The context
// is pre-cancelled so the (timeout-less) poll loop exits deterministically right after the clock
// read — pre-fix it nil-panics at :984 before ever reaching the loop; post-fix it reaches the loop
// and returns the cancellation error.
func TestConstructionPathExecutor_NilClock_DoesNotPanicOnGateFillPoll(t *testing.T) {
	repo := &dockRaceShipRepo{
		location:      pacingTestFactoryWP,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 400,
	}
	med := &dockRaceMediator{repo: repo, dockHandler: tactics.NewDockShipHandler(repo)}
	// nil clock == the construction daemon wiring (main.go:669). This is the reproduction.
	executor := NewProductionExecutor(med, repo, nil, NewMarketLocator(nil, nil, nil, nil), nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // exit the timeout-less poll loop deterministically, just past the clock read at :984

	_, _, err := executor.PollForProduction(
		ctx, dockRaceGood, pacingTestFactoryWP, dockRaceShip, shared.MustNewPlayerID(1), nil, false, "X1-DR",
	)

	if err == nil {
		t.Fatal("expected the cancelled gate-fill poll to return an error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a context-cancellation error (proving PollForProduction ran past the clock read at :984 instead of nil-panicking), got %v", err)
	}
}

// TestNewProductionExecutorWithConfig_NilClock_YieldsUsableClock pins the defense-in-depth default at
// the constructor where the construction path bottoms out (NewProductionExecutor delegates here). A
// genuinely nil clock must default to a real system clock so the sp-vh1s throughput-pacer's clock
// reads (recentPacedOutputUnits :1141, recordPacedOutputBuy :1173) are usable rather than nil-panics.
// It drives a single gate output-buy: pre-fix it nil-panics inside the pacer; post-fix the defaulted
// clock powers a full per-lot buy.
func TestNewProductionExecutorWithConfig_NilClock_YieldsUsableClock(t *testing.T) {
	repo := &dockRaceShipRepo{
		location:      pacingTestFactoryWP,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 400,
	}
	med := &dockRaceMediator{repo: repo, dockHandler: tactics.NewDockShipHandler(repo)}
	// A genuinely nil shared.Clock interface (NOT a typed-nil *MockClock) — the construction path.
	executor := NewProductionExecutorWithConfig(
		med, repo, nil, NewMarketLocator(nil, nil, nil, nil), nil, []time.Duration{time.Millisecond}, nil,
	)

	const tradeVolume = 10
	units, _, err := executor.purchaseFabricatedOutput(
		gateModeCtx(context.Background()), dockRaceGood, pacingTestFactoryWP, dockRaceShip, shared.MustNewPlayerID(1), tradeVolume,
	)
	if err != nil {
		t.Fatalf("gate output-buy under a defaulted (nil→real) clock must not error: %v", err)
	}
	if units != tradeVolume {
		t.Fatalf("expected the defaulted clock to power a full per-lot gate buy of tv=%d, got %d", tradeVolume, units)
	}
}

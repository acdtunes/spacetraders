package services

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-vh1s live nil-panic regression. PollForProduction is clock-driven (e.clock.Now()). The
// construction daemon builds its producer the direct way — NewProductionExecutor(..., nil /*clock*/,
// ...) at main.go — whereas the goods-factory path defaults nil→RealClock inside
// NewRunFactoryCoordinatorHandler BEFORE building the executor. So only the construction path handed
// the executor a nil clock, and a gate-fill construction tick nil-panicked on the first e.clock.Now().
// This test exercises that exact nil-clock construction; it must not panic once the constructor
// defaults the clock.

// TestConstructionPathExecutor_NilClock_DoesNotPanicOnGateFillPoll builds the executor EXACTLY as the
// construction daemon does (NewProductionExecutor with a nil clock, main.go) and drives the gate-fill
// poll path (PollForProduction) that read the clock. The context is pre-cancelled so the (timeout-less)
// poll loop exits deterministically right after the clock read — previous it nil-panicked before ever
// reaching the loop; post-fix it reaches the loop and returns the cancellation error.
func TestConstructionPathExecutor_NilClock_DoesNotPanicOnGateFillPoll(t *testing.T) {
	repo := &dockRaceShipRepo{
		location:      gateFillTestFactoryWP,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 400,
	}
	med := &dockRaceMediator{repo: repo, dockHandler: tactics.NewDockShipHandler(repo)}
	// nil clock == the construction daemon wiring (main.go). This is the reproduction.
	executor := NewProductionExecutor(med, repo, nil, NewMarketLocator(nil, nil, nil, nil), nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // exit the timeout-less poll loop deterministically, just past the clock read

	_, _, _, err := executor.PollForProduction(
		ctx, dockRaceGood, gateFillTestFactoryWP, dockRaceShip, shared.MustNewPlayerID(1), nil, false, "X1-DR",
	)

	if err == nil {
		t.Fatal("expected the cancelled gate-fill poll to return an error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a context-cancellation error (proving PollForProduction ran past the clock read instead of nil-panicking), got %v", err)
	}
}

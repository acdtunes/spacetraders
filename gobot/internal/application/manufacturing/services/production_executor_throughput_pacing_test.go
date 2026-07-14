package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-vh1s Part A — THROUGHPUT-PACING (the dropped price ceiling's replacement; Admiral sign-off
// 2026-07-14). The gate buys the source factory's OUTPUT to deliver it. Buying the output FASTER
// than the factory physically produces re-depletes its supply and re-trips the ceiling in a
// stall/recover oscillation (the sp-iv65 −6.6M mechanism, real even when margin-indifferent). So a
// gate output-buy is paced to the factory's sustainable throughput: buy-rate ≤ k×tv per hour
// (analyst-validated k=2.0) with each lot ≤ tv. This is the ONLY safety limit on the (margin-blind)
// gate output buy, so it lands with this lane.

const pacingTestFactoryWP = "X1-DR-FACTORY"

// newPacingExecutor builds a ProductionExecutor over the dock-race ship/mediator fakes with a
// CONTROLLABLE clock, so the trailing-hour rate window can be exercised deterministically. The ship
// starts DOCKED at the factory with an empty 400-slot hold, so cargo space never binds the pacing.
func newPacingExecutor(t *testing.T, clock *shared.MockClock) (*ProductionExecutor, *dockRaceMediator) {
	t.Helper()
	repo := &dockRaceShipRepo{
		location:      pacingTestFactoryWP,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 400,
	}
	mediator := &dockRaceMediator{repo: repo, dockHandler: tactics.NewDockShipHandler(repo)}
	executor := NewProductionExecutorWithConfig(
		mediator, repo, nil, NewMarketLocator(nil, nil, nil, nil), clock, []time.Duration{time.Millisecond}, nil,
	)
	return executor, mediator
}

// gateModeCtx stamps the run context as a unified gate-fill node (toggle on + construction-site
// target) so the executor's pacing gate engages — the same stamp the coordinator applies per run.
func gateModeCtx(ctx context.Context) context.Context {
	ctx = WithUnifiedGateFill(ctx, true)
	return WithDeliveryTarget(ctx, ConstructionSiteTarget("X1-VB74-I55"))
}

// Core (sp-vh1s): with the default coefficient k=2.0, a gate output-buy of a tv=10 factory buys at
// most tv per lot and at most k×tv=20 units over any trailing hour. Two lots of 10 exhaust the
// window; the third is PACED OUT (0 units, harvest skipped) until the clock advances past the window,
// after which a fresh lot clears — proving both the per-lot≤tv cap and the k×tv/hr rate cap.
func TestPurchaseFabricatedOutput_GateNode_PacedToThroughput(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
	executor, _ := newPacingExecutor(t, clock)
	ctx := gateModeCtx(context.Background())

	const tradeVolume = 10
	buy := func() int {
		units, _, err := executor.purchaseFabricatedOutput(ctx, dockRaceGood, pacingTestFactoryWP, dockRaceShip, shared.MustNewPlayerID(1), tradeVolume)
		if err != nil {
			t.Fatalf("paced output buy must not error: %v", err)
		}
		return units
	}

	// Lot 1 and 2: per-lot capped at tv=10 even though the hold has 400 free slots.
	if got := buy(); got != tradeVolume {
		t.Fatalf("lot 1: expected a per-lot buy of tv=%d (cargo space must not exceed the per-lot cap), got %d", tradeVolume, got)
	}
	if got := buy(); got != tradeVolume {
		t.Fatalf("lot 2: expected a second per-lot buy of %d within the rate budget, got %d", tradeVolume, got)
	}
	// Lot 3 same hour: the k×tv=20/hr rate budget is exhausted → paced out (0 units).
	if got := buy(); got != 0 {
		t.Fatalf("lot 3: rate budget k×tv=20/hr exhausted this hour, expected 0 units (paced out), got %d — buying past throughput re-depletes the source (the stall the pacing prevents)", got)
	}
	// Advance past the trailing hour: the window clears and a fresh lot clears.
	clock.Advance(61 * time.Minute)
	if got := buy(); got != tradeVolume {
		t.Fatalf("after the trailing hour elapses the rate window must reset, expected a fresh lot of %d, got %d", tradeVolume, got)
	}
}

// A NON-gate node (a profit-factory harvest, or any run with the toggle off) is NEVER paced: the
// output buy keeps its original min(cargo space, tv) behavior with no rate cap — proving OFF is
// byte-identical and the pacing is scoped strictly to gate nodes.
func TestPurchaseFabricatedOutput_NonGateNode_NotPaced(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
	executor, _ := newPacingExecutor(t, clock)
	ctx := context.Background() // no gate stamp → a plain profit-factory harvest

	const tradeVolume = 10
	// Many consecutive lots in the same hour, all full tv — no rate cap ever engages.
	for lot := 1; lot <= 5; lot++ {
		units, _, err := executor.purchaseFabricatedOutput(ctx, dockRaceGood, pacingTestFactoryWP, dockRaceShip, shared.MustNewPlayerID(1), tradeVolume)
		if err != nil {
			t.Fatalf("lot %d: non-gate harvest must not error: %v", lot, err)
		}
		if units != tradeVolume {
			t.Fatalf("lot %d: a non-gate harvest must NOT be throughput-paced (byte-identical to today), expected %d, got %d", lot, tradeVolume, units)
		}
	}
}

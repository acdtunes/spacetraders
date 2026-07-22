package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-zrpur — the gate output-buy DRAINS to available supply. A unified gate-fill run buys the source
// factory's OUTPUT to deliver it to the jump-gate. The throughput pace guard (removed here, Admiral
// order) capped that buy at k×tv units/trailing-hour, throttling the final gate push BELOW the
// factory's already-produced buffer and starving an abundant export for zero benefit. Now the buy is
// bounded only by cargo space and what the market actually has to sell — min(cargo, available supply)
// — with the natural "you can only buy what's exported" limit doing the pacing, and the 9aoc solvency
// floor (a separate path) remaining the hard money bound.

const gateFillTestFactoryWP = "X1-DR-FACTORY"

// newGateFillExecutor builds a ProductionExecutor over the dock-race ship/mediator fakes. The ship
// starts DOCKED at the factory with an empty hold of the given capacity; the mediator fills any
// requested lot in full, so the executor's own cap is the only thing under test.
func newGateFillExecutor(t *testing.T, cargoCapacity int) (*ProductionExecutor, *dockRaceMediator) {
	t.Helper()
	repo := &dockRaceShipRepo{
		location:      gateFillTestFactoryWP,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: cargoCapacity,
	}
	mediator := &dockRaceMediator{repo: repo, dockHandler: tactics.NewDockShipHandler(repo)}
	executor := NewProductionExecutorWithConfig(
		mediator, repo, nil, NewMarketLocator(nil, nil, nil, nil),
		&shared.MockClock{CurrentTime: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)},
		[]time.Duration{time.Millisecond}, nil,
	)
	return executor, mediator
}

// gateModeCtx stamps the run context as a unified gate-fill node (toggle on + construction-site
// target) so the executor treats the output-buy as a gate fill — the same stamp the coordinator
// applies per run.
func gateModeCtx(ctx context.Context) context.Context {
	ctx = WithUnifiedGateFill(ctx, true)
	return WithDeliveryTarget(ctx, ConstructionSiteTarget("X1-VB74-I55"))
}

// A gate output-buy against an ABUNDANT export drains lot after lot with NO rate ceiling: every lot in
// the same hour buys a full trade volume, so the gate fills as fast as the factory has exported. This
// is the sp-zrpur fix — before the pace guard was removed, the third same-hour lot was throttled to 0
// ("trailing-hour throughput budget reached") once k×tv units had been bought, freezing the final push
// (gate stuck 864/1600 on an ABUNDANT F46 export). The hold has ample room, so cargo never binds.
func TestPurchaseFabricatedOutput_GateNode_DrainsToAvailableSupply_NoRateCap(t *testing.T) {
	executor, _ := newGateFillExecutor(t, 400)
	ctx := gateModeCtx(context.Background())

	const tradeVolume = 43 // the abundant F46 export volume from the bead
	buy := func(lot int) int {
		units, _, err := executor.purchaseFabricatedOutput(ctx, dockRaceGood, gateFillTestFactoryWP, dockRaceShip, shared.MustNewPlayerID(1), tradeVolume, 10)
		if err != nil {
			t.Fatalf("lot %d: gate output-buy must not error: %v", lot, err)
		}
		return units
	}

	// Four consecutive lots in the SAME hour. With the pace guard removed, none is throttled — each
	// buys a full trade volume. (Pre-removal, lot 3 returned 0: k×tv=86 with k=2.0 was exhausted after
	// two lots of 43, and every further lot in the hour was paced out and skipped.)
	for lot := 1; lot <= 4; lot++ {
		if got := buy(lot); got != tradeVolume {
			t.Fatalf("lot %d: an abundant gate export must drain a full tv=%d with no trailing-hour rate cap, got %d (a throttled lot is the sp-zrpur starvation bug)", lot, tradeVolume, got)
		}
	}
}

// The gate output-buy is bounded by cargo space when the hold is smaller than the export volume:
// min(cargo, available supply). A 20-slot hold against a tv=43 export buys 20 — the natural cargo
// bound, never a throughput rate cap.
func TestPurchaseFabricatedOutput_GateNode_CargoBoundsTheBuy(t *testing.T) {
	executor, _ := newGateFillExecutor(t, 20)
	ctx := gateModeCtx(context.Background())

	units, _, err := executor.purchaseFabricatedOutput(ctx, dockRaceGood, gateFillTestFactoryWP, dockRaceShip, shared.MustNewPlayerID(1), 43, 10)
	if err != nil {
		t.Fatalf("gate output-buy must not error: %v", err)
	}
	if units != 20 {
		t.Fatalf("a 20-slot hold against a tv=43 export must buy min(cargo, supply)=20, got %d", units)
	}
}

// A NON-gate node (a profit-factory harvest, or any run with the toggle off) drains identically:
// every lot buys a full trade volume with no rate cap. This pins that gate and non-gate output-buys
// are now the same min(cargo, tv) path — the removal did not change the resale/non-gate behavior.
func TestPurchaseFabricatedOutput_NonGateNode_DrainsToAvailableSupply(t *testing.T) {
	executor, _ := newGateFillExecutor(t, 400)
	ctx := context.Background() // no gate stamp → a plain profit-factory harvest

	const tradeVolume = 10
	for lot := 1; lot <= 5; lot++ {
		units, _, err := executor.purchaseFabricatedOutput(ctx, dockRaceGood, gateFillTestFactoryWP, dockRaceShip, shared.MustNewPlayerID(1), tradeVolume, 10)
		if err != nil {
			t.Fatalf("lot %d: non-gate harvest must not error: %v", lot, err)
		}
		if units != tradeVolume {
			t.Fatalf("lot %d: a non-gate harvest must buy a full tv=%d (unchanged), got %d", lot, tradeVolume, units)
		}
	}
}

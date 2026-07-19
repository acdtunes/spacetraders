package contract

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Depth-aware absorption exclusion (matches the trade-route circuit's evaluate()
// shape): an EXECUTED recovery shadow blocks a sink outright (actively healing),
// but PLANNED occupancy only excludes when the remaining unreserved depth can't
// fit this leg's tranche at the quoted price.

// idleArbDepthHarness wires a profitable hub->sink lane (margin 250, clearing the
// profit floor) whose SINK good carries an explicit trade volume, so a test sets
// the sink's absorptive DEPTH and the depth-aware consult math is explicit. A
// stateful absorption ledger is wired so presets drive the consult.
func idleArbDepthHarness(t *testing.T, hulls int, cfg IdleArbConfig, sinkVolume int) (*IdleArbDispatcher, *fakeIdleArbLauncher, *fakeAbsorptionLedger, absorption.LaneKey) {
	t.Helper()
	hub := idleArbWaypoint(t, "X1-HUB-E42", 0, 0)
	sink := idleArbWaypoint(t, "X1-HUB-D40", 0, 50)

	repo := &idleArbFakeShipRepo{}
	for i := 0; i < hulls; i++ {
		repo.ships = append(repo.ships, idleArbHull(t, fmt.Sprintf("TORWIND-%d", i+1), hub, testFleet))
	}
	graph := &fakeGraphProvider{waypoints: map[string]*shared.Waypoint{hub.Symbol: hub, sink.Symbol: sink}}
	markets := &idleArbFakeMarketRepo{markets: map[string]*market.Market{
		hub.Symbol:  marketAt(t, hub.Symbol, tradeGood(t, "MACHINERY", 90, 100)),
		sink.Symbol: marketAt(t, sink.Symbol, tradeGoodVol(t, "MACHINERY", 350, 360, sinkVolume)),
	}}
	clock := shared.NewRealClock()
	launcher := &fakeIdleArbLauncher{repo: repo, clock: clock}
	d := NewIdleArbDispatcher(repo, markets, graph, launcher, nil, nil, clock, shared.MustNewPlayerID(1), testFleet, cfg)
	ledger := newFakeAbsorptionLedger()
	d.SetAbsorptionLedger(ledger, false, 0)
	key := absorption.LaneKey{Waypoint: sink.Symbol, Good: "MACHINERY", Side: absorption.SideSell}
	return d, launcher, ledger, key
}

// A sink another engine has PARTIALLY reserved — but with depth left for this
// leg's tranche — is launched into. A 40-unit hull leg into a 200-deep sink with
// 100 units PLANNED has 100 units of room: depth-aware flies it.
func TestIdleArb_DepthAwareAbsorption_LaunchesIntoPartiallyRecoveredSink(t *testing.T) {
	d, launcher, ledger, key := idleArbDepthHarness(t, 2, IdleArbConfig{ReserveHulls: 1}, 200)
	ledger.preset(key, absorption.KeyOccupancy{PlannedUnits: 100}) // 100 of 200 depth taken

	launched := d.DispatchOnce(context.Background())

	if launched != 1 || len(launcher.launches) != 1 {
		t.Fatalf("depth-aware consult must launch into a partially-reserved sink with room for the tranche (binary blocked this), got %d launches", launched)
	}
	if d.skipReserved != 0 {
		t.Fatalf("a sink with enough remaining depth must NOT be a reserved skip, got skipReserved=%d", d.skipReserved)
	}
	// The launched leg still records its own absorption for the next consult.
	if ledger.recordedCount() != 1 {
		t.Fatalf("the launched leg's absorption must be recorded, got %d", ledger.recordedCount())
	}
}

// Absorption safety is NOT abandoned: when the remaining depth can't fit the
// leg's tranche, the lane is still excluded. A 40-unit leg into a 200-deep sink
// with 180 units PLANNED has only 20 units of room — depth-aware still skips it.
func TestIdleArb_AbsorptionStillExcludesWhenDepthCannotFillTranche(t *testing.T) {
	d, launcher, ledger, key := idleArbDepthHarness(t, 2, IdleArbConfig{ReserveHulls: 1}, 200)
	ledger.preset(key, absorption.KeyOccupancy{PlannedUnits: 180}) // only 20 of 200 depth left

	launched := d.DispatchOnce(context.Background())

	if launched != 0 || len(launcher.launches) != 0 {
		t.Fatalf("depth-aware consult must STILL exclude when remaining depth can't fit the leg's tranche, got %d launches", launched)
	}
	if d.skipReserved == 0 {
		t.Fatalf("a depth-exhausted sink must be attributed to the reserved skip, got skipReserved=%d", d.skipReserved)
	}
}

// A recovering EXECUTED shadow blocks outright regardless of headroom — a sink
// actively healing must not be stepped into even when nominal depth remains, exactly
// as the trade-route consult treats a shadow (Outstanding has already floored it, so
// any positive residual is still-live damage).
func TestIdleArb_DepthAware_RecoveringShadowStillBlocksEvenWithHeadroom(t *testing.T) {
	d, launcher, ledger, key := idleArbDepthHarness(t, 2, IdleArbConfig{ReserveHulls: 1}, 200)
	// Deep sink, only a small recovering residual and zero planned — plenty of
	// nominal room, but the shadow means the sink is healing.
	ledger.preset(key, absorption.KeyOccupancy{RecoveringResidual: 5})

	launched := d.DispatchOnce(context.Background())

	if launched != 0 || len(launcher.launches) != 0 {
		t.Fatalf("a recovering shadow must block outright even with depth headroom, got %d launches", launched)
	}
	if d.skipReserved == 0 {
		t.Fatalf("the shadow block must be attributed to the reserved skip, got %d", d.skipReserved)
	}
}

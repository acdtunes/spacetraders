package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The command-ship cargo baseline is a COMPARATIVE guard: it prefers dispatching a light hauler
// over double-tripping a stock frigate. These two tests pin both sides of that comparison through
// discoverShipPool's REAL pipeline, because the baseline drop happens AFTER FindIdleLightHaulers'
// last-resort admission and is therefore invisible to every pool-side test.

// coldstartFrigate is the stock era-1 command hull as a fresh COLDSTART owns it: idle, undedicated
// and BELOW the 80 baseline (40 cargo).
func coldstartFrigate(t *testing.T) *navigation.Ship {
	t.Helper()
	return newBaselineShip(t, "TORWIND-1", "COMMAND", "FRAME_FRIGATE", 40)
}

// ownedBusyHauler is a HAULER-role hull the fleet OWNS but that is not idle this tick (in transit).
// It is deliberately unavailable so the last-resort admission still offers the frigate up — leaving
// the cargo baseline as the only thing that can drop it.
func ownedBusyHauler(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	hauler := newBaselineShip(t, symbol, "HAULER", "FRAME_LIGHT_FREIGHTER", 80)
	hauler.SetNavStatus(navigation.NavStatusInTransit)
	return hauler
}

func newBaselineShip(t *testing.T, symbol, role, frame string, cargoCapacity int) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-RX27-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(400, 400)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(cargoCapacity, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), location, fuel, 400, cargoCapacity, cargo, 30,
		frame, role, nil, navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	if ship.DedicatedFleet() != "" {
		t.Fatalf("fixture %s must be UNDEDICATED", symbol)
	}
	return ship
}

func newBaselinePass(repo navigation.ShipRepository) *coordinatorPass {
	return &coordinatorPass{
		h: &RunFleetCoordinatorHandler{
			shipRepo: repo,
			clock:    &shared.MockClock{CurrentTime: time.Now()},
		},
		cmd:    &RunFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), ContainerID: "coord-1"},
		result: &RunFleetCoordinatorResponse{},
		errMon: health.NewMonitor(health.DefaultStreakThreshold),
	}
}

func containsShipSymbol(symbols []string, want string) bool {
	for _, symbol := range symbols {
		if symbol == want {
			return true
		}
	}
	return false
}

// COLDSTART: the fleet owns the frigate and nothing else, so "dispatch a light hauler instead"
// names a hull that does not exist. With nothing to prefer over it the frigate must survive the
// whole pipeline — otherwise the starved-trade contract fallback is admitted and instantly dropped
// every tick, doing no contract work in exactly the phase it exists for.
func TestDiscoverShipPool_ZeroHaulerFleetKeepsBelowBaselineFrigate(t *testing.T) {
	frigate := coldstartFrigate(t)
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{frigate}}

	pool, ok := newBaselinePass(repo).discoverShipPool(context.Background())
	if !ok {
		t.Fatal("discoverShipPool must succeed on a healthy repo")
	}
	if !containsShipSymbol(pool.available, "TORWIND-1") {
		t.Fatalf("a zero-hauler fleet has nothing to prefer over the frigate — it must stay claimable, got %v", pool.available)
	}
}

// MATURE FLEET, unchanged: the fleet owns a hauler, so the baseline's own rationale applies whether
// or not that hauler is free this tick — waiting for it beats a 40-cargo double-trip. Ownership is
// the test, NOT idleness: this hauler is in transit, which is precisely when the last-resort
// admission hands the frigate up and only the baseline can still drop it.
func TestDiscoverShipPool_OwnedBusyHaulerStillDropsBelowBaselineFrigate(t *testing.T) {
	frigate := coldstartFrigate(t)
	hauler := ownedBusyHauler(t, "TORWIND-7")
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{frigate, hauler}}

	pool, ok := newBaselinePass(repo).discoverShipPool(context.Background())
	if !ok {
		t.Fatal("discoverShipPool must succeed on a healthy repo")
	}
	if containsShipSymbol(pool.available, "TORWIND-1") {
		t.Fatalf("the fleet owns a hauler — the 80 baseline must still drop the stock frigate, got %v", pool.available)
	}
}

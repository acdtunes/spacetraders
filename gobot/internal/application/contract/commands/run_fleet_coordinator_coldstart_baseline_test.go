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
// over double-tripping a stock frigate. These tests pin every side of that comparison through
// discoverShipPool's REAL pipeline, because the baseline drop happens AFTER FindIdleLightHaulers'
// last-resort admission and is therefore invisible to every pool-side test.

// coldstartFrigate is the stock era-1 command hull as a fresh COLDSTART owns it: idle, undedicated
// and BELOW the 80 baseline (40 cargo).
func coldstartFrigate(t *testing.T) *navigation.Ship {
	t.Helper()
	return newBaselineShip(t, "TORWIND-1", "COMMAND", "FRAME_FRIGATE", 40)
}

// baselineHauler is an idle, undedicated light freighter — the alternative the baseline exists to
// prefer over the frigate. The variants below make it unavailable in each of the ways that matter.
func baselineHauler(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	return newBaselineShip(t, symbol, "HAULER", "FRAME_LIGHT_FREIGHTER", 80)
}

// ownedBusyHauler is in transit: owned, but not dispatchable this tick. Being unavailable is what
// lets the last-resort admission offer the frigate up, leaving the baseline the only thing that
// could still drop it.
func ownedBusyHauler(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	hauler := baselineHauler(t, symbol)
	hauler.SetNavStatus(navigation.NavStatusInTransit)
	return hauler
}

// claimedBusyHauler is docked but already working a container — the OTHER way the live fleet's one
// hauler is busy (TORWINDSTG4-5 was mid-contract), and a different code path from in transit.
func claimedBusyHauler(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	hauler := baselineHauler(t, symbol)
	if err := hauler.AssignToContainer("contract-worker-"+symbol, shared.NewRealClock()); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}
	return hauler
}

// pinnedHauler is walled off in ANOTHER coordinator's exclusive fleet.
func pinnedHauler(t *testing.T, symbol, fleet string) *navigation.Ship {
	t.Helper()
	hauler := baselineHauler(t, symbol)
	hauler.SetDedicatedFleet(fleet)
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

// sp-6378a CORRECTS sp-00y49's "ownership, not idleness" here. A hauler that EXISTS but is busy on
// this fleet's own work is not an alternative the coordinator can dispatch instead of the frigate
// this tick, so the comparative baseline has nothing to compare against and must not fire. Live
// (TORWINDSTG4, 2026-08-20): the one hauler was busy on another contract, and the frigate sp-5kn8v
// had just released to contract work was re-dropped by the baseline on every pass — released to a
// job it could never be given, sitting fully idle, exactly the outcome sp-5kn8v exists to prevent.
func TestDiscoverShipPool_BusyHaulerNoLongerBlocksBelowBaselineFrigate(t *testing.T) {
	frigate := coldstartFrigate(t)
	hauler := ownedBusyHauler(t, "TORWIND-7")
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{frigate, hauler}}

	pool, ok := newBaselinePass(repo).discoverShipPool(context.Background())
	if !ok {
		t.Fatal("discoverShipPool must succeed on a healthy repo")
	}
	if !containsShipSymbol(pool.available, "TORWIND-1") {
		t.Fatalf("the only hauler is busy — nothing is available to prefer over the frigate, so it must stay claimable, got %v", pool.available)
	}
}

// Same correction via the OTHER busy path: claimed by a container rather than in transit. Both must
// read as unavailable, or the fix only covers whichever kind of busy the live fleet happened to hit.
func TestDiscoverShipPool_ClaimedHaulerNoLongerBlocksBelowBaselineFrigate(t *testing.T) {
	frigate := coldstartFrigate(t)
	hauler := claimedBusyHauler(t, "TORWIND-7")
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{frigate, hauler}}

	pool, ok := newBaselinePass(repo).discoverShipPool(context.Background())
	if !ok {
		t.Fatal("discoverShipPool must succeed on a healthy repo")
	}
	if !containsShipSymbol(pool.available, "TORWIND-1") {
		t.Fatalf("the only hauler is mid-contract — the frigate must stay claimable, got %v", pool.available)
	}
}

// sp-00y49's core case, UNCHANGED: with a genuinely available hauler the coordinator has something
// better to dispatch, so the stock frigate must not be in the working set — the fleet works the
// 80-cargo hull in one trip instead of double-tripping the 40-cargo one.
func TestDiscoverShipPool_IdleHaulerStillDropsBelowBaselineFrigate(t *testing.T) {
	frigate := coldstartFrigate(t)
	hauler := baselineHauler(t, "TORWIND-7")
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{frigate, hauler}}

	pool, ok := newBaselinePass(repo).discoverShipPool(context.Background())
	if !ok {
		t.Fatal("discoverShipPool must succeed on a healthy repo")
	}
	if containsShipSymbol(pool.available, "TORWIND-1") {
		t.Fatalf("an idle hauler is available to prefer — the stock frigate must be dropped, got %v", pool.available)
	}
	if !containsShipSymbol(pool.available, "TORWIND-7") {
		t.Fatalf("the idle hauler must be the candidate, got %v", pool.available)
	}
}

// A hauler pinned to ANOTHER coordinator's fleet is not a hull this one can ever dispatch: the
// claim-filter hides it from the general pool and FindIdleShipsByFleet reads only the contract tag.
// So it is no reason to bench the frigate in ANY state, idle included — otherwise the fleet does no
// contract work at all while a trade hull tours.
func TestDiscoverShipPool_ForeignFleetHaulerNoLongerBlocksBelowBaselineFrigate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hauler func(t *testing.T) *navigation.Ship
	}{
		{"idle in the trade fleet", func(t *testing.T) *navigation.Ship { return pinnedHauler(t, "TORWIND-7", "trade") }},
		{"in transit for the trade fleet", func(t *testing.T) *navigation.Ship {
			hauler := pinnedHauler(t, "TORWIND-7", "trade")
			hauler.SetNavStatus(navigation.NavStatusInTransit)
			return hauler
		}},
		{"mid-job for the trade fleet", func(t *testing.T) *navigation.Ship {
			hauler := pinnedHauler(t, "TORWIND-7", "trade")
			if err := hauler.AssignToContainer("trade-tour-TORWIND-7", shared.NewRealClock()); err != nil {
				t.Fatalf("AssignToContainer: %v", err)
			}
			return hauler
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frigate := coldstartFrigate(t)
			repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{frigate, tc.hauler(t)}}

			pool, ok := newBaselinePass(repo).discoverShipPool(context.Background())
			if !ok {
				t.Fatal("discoverShipPool must succeed on a healthy repo")
			}
			if !containsShipSymbol(pool.available, "TORWIND-1") {
				t.Fatalf("a foreign pin is no dispatchable alternative — the frigate must stay claimable, got %v", pool.available)
			}
		})
	}
}

// EXCLUSIVE MODE is untouched here, and asserted so it stays that way: once ANY hull carries the
// contract tag the coordinator draws only from its own idle members, so a sole contract-pinned
// hauler that is busy yields an EMPTY pool no matter what the baseline decides. Seating the frigate
// in that state is a pool-admission question, deliberately not a baseline one.
func TestDiscoverShipPool_OwnFleetPinnedBusyHaulerKeepsExclusiveModeEmpty(t *testing.T) {
	hauler := pinnedHauler(t, "TORWIND-7", dedicatedFleetContract)
	if err := hauler.AssignToContainer("contract-worker-TORWIND-7", shared.NewRealClock()); err != nil {
		t.Fatalf("AssignToContainer: %v", err)
	}
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{coldstartFrigate(t), hauler}}

	pool, ok := newBaselinePass(repo).discoverShipPool(context.Background())
	if !ok {
		t.Fatal("discoverShipPool must succeed on a healthy repo")
	}
	if !pool.dedicatedFleetActive {
		t.Fatal("a contract-tagged hull must keep EXCLUSIVE MODE active")
	}
	if len(pool.available) != 0 {
		t.Fatalf("EXCLUSIVE MODE with no idle member must yield an empty pool, got %v", pool.available)
	}
}

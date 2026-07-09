package contract

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func hullFitTarget(t *testing.T) *shared.Waypoint {
	t.Helper()
	target, err := shared.NewWaypoint("X1-TW-MKT", 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	return target
}

func selectHull(t *testing.T, ships []*navigation.Ship, units int) *SelectionResult {
	t.Helper()
	result, err := SelectHullForCargo(ships, hullFitTarget(t), units)
	if err != nil {
		t.Fatalf("SelectHullForCargo: %v", err)
	}
	return result
}

// Tier 1: a light hull that fits the load must win over a heavier hull even
// when the heavy is closer - the exact waste the ladder exists to stop
// (a 225-hold heavy crossing the system for a 40-unit leg while a light
// idles). Both are regular haulers; only hold size and position differ.
func TestSelectHullForCargo_SmallestFittingHullBeatsCloserHeavy(t *testing.T) {
	heavy := newSelectorTestShipWithHull(t, "TORWIND-7", "HAULER", 10, 0, 30, 120)
	light := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 200, 0, 30, 40)

	result := selectHull(t, []*navigation.Ship{heavy, light}, 30)

	if result.Ship.ShipSymbol() != "TORWIND-3" {
		t.Fatalf("expected the smallest fitting hull TORWIND-3 (40-hold) to beat the closer heavy, got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

// Tier 1 tie-break: two fitting hulls of the same hold size are split by
// cruise travel time, keeping sp-snmb's speed-awareness inside the tier - a
// fast hull at the same distance clears the leg sooner.
func TestSelectHullForCargo_EqualHoldsTieBreakOnTravelTime(t *testing.T) {
	slow := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 100, 0, 10, 40)
	fast := newSelectorTestShipWithHull(t, "TORWIND-4", "HAULER", 100, 0, 30, 40)

	result := selectHull(t, []*navigation.Ship{slow, fast}, 30)

	if result.Ship.ShipSymbol() != "TORWIND-4" {
		t.Fatalf("expected the faster of two equal-hold hulls to win, got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

// The heavy is picked exactly when the load needs the hold: with a load too
// big for the light hull, the fitting heavy must win even though the light is
// closer.
func TestSelectHullForCargo_HeavyWinsWhenLoadNeedsTheHold(t *testing.T) {
	light := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 10, 0, 30, 40)
	heavy := newSelectorTestShipWithHull(t, "TORWIND-7", "HAULER", 200, 0, 30, 120)

	result := selectHull(t, []*navigation.Ship{light, heavy}, 100)

	if result.Ship.ShipSymbol() != "TORWIND-7" {
		t.Fatalf("expected the fitting heavy TORWIND-7 to win a 100-unit load, got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

// Tier 2 gate: the command frigate stays benched while any regular hull fits,
// even when the frigate is closer AND a tighter fit - last-resort means
// last-resort (sp-4a4e refined by l7h2 Phase 3).
func TestSelectHullForCargo_CommandFrigateBenchedWhileRegularHullFits(t *testing.T) {
	frigate := newSelectorTestShipWithHull(t, "TORWIND-1", "COMMAND", 10, 0, 36, 60)
	hauler := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 200, 0, 15, 80)

	result := selectHull(t, []*navigation.Ship{frigate, hauler}, 50)

	if result.Ship.ShipSymbol() != "TORWIND-3" {
		t.Fatalf("expected the regular hauler to bench the command frigate, got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

// Tier 2: the command frigate is drafted when it is the only hull whose hold
// fits the load - it stays an eligible candidate (sp-4a4e), stepping in
// exactly when its hold is the differentiator.
func TestSelectHullForCargo_CommandFrigateDraftedWhenOnlyHullThatFits(t *testing.T) {
	hauler := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 10, 0, 30, 8)
	frigate := newSelectorTestShipWithHull(t, "TORWIND-1", "COMMAND", 200, 0, 36, 60)

	result := selectHull(t, []*navigation.Ship{hauler, frigate}, 30)

	if result.Ship.ShipSymbol() != "TORWIND-1" {
		t.Fatalf("expected the command frigate as the only fitting hull, got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
	if !strings.Contains(result.Reason, "last resort") {
		t.Fatalf("expected a last-resort reason for the frigate pick, got %q", result.Reason)
	}
}

// Tier 3: when nothing fits in one trip, the regular hull needing the fewest
// round trips wins - and the command frigate stays benched even if its hold
// would need fewer trips still.
func TestSelectHullForCargo_PartialFitPicksFewestTripsAmongRegularHulls(t *testing.T) {
	small := newSelectorTestShipWithHull(t, "TORWIND-4", "HAULER", 10, 0, 30, 30)   // 4 trips for 100
	big := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 200, 0, 30, 40)    // 3 trips for 100
	frigate := newSelectorTestShipWithHull(t, "TORWIND-1", "COMMAND", 5, 0, 36, 60) // 2 trips, but benched

	result := selectHull(t, []*navigation.Ship{small, big, frigate}, 100)

	if result.Ship.ShipSymbol() != "TORWIND-3" {
		t.Fatalf("expected the fewest-trips regular hull TORWIND-3, got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

// Tier 4: the command frigate as sole candidate hauls the load, fit or not -
// a one-hull fleet still delivers.
func TestSelectHullForCargo_CommandFrigateAsSoleCandidate(t *testing.T) {
	frigate := newSelectorTestShipWithHull(t, "TORWIND-1", "COMMAND", 10, 0, 36, 40)

	result := selectHull(t, []*navigation.Ship{frigate}, 100)

	if result.Ship.ShipSymbol() != "TORWIND-1" {
		t.Fatalf("expected the sole-candidate frigate, got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

func TestSelectHullForCargo_NoCandidates_ReturnsError(t *testing.T) {
	if _, err := SelectHullForCargo(nil, hullFitTarget(t), 10); err == nil {
		t.Fatalf("expected an error for an empty candidate list")
	}
}

// The predicate behind the frigate gate: registration role or the
// conventional "*-1" symbol marks the command hull - both markers alone must
// trip it.
func TestIsCommandHull_ByRoleOrSymbol(t *testing.T) {
	byRole := newSelectorTestShipWithHull(t, "TORWIND-9", "COMMAND", 0, 0, 30, 40)
	bySymbol := newSelectorTestShipWithHull(t, "TORWIND-1", "HAULER", 0, 0, 30, 40)
	neither := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 0, 0, 30, 40)

	if !IsCommandHull(byRole) {
		t.Fatalf("expected role COMMAND to mark a command hull")
	}
	if !IsCommandHull(bySymbol) {
		t.Fatalf("expected the *-1 symbol to mark a command hull")
	}
	if IsCommandHull(neither) {
		t.Fatalf("expected a plain hauler not to be marked as command hull")
	}
}

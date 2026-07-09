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

// Tier 1 (sp-f66z): among hulls that FIT the load, the NEAREST wins even when
// a farther hull is smaller - the exact far-source claim the ladder used to
// make. Smallest-fit-anywhere let the coordinator claim a far small hull while
// a nearer adequate hull idled at a hub (5/8 stall-tails); nearest-adequate-
// first stops it. Both are regular haulers at the same speed, so only hold size
// and distance differ and travel time isolates proximity. (Was
// SmallestFittingHullBeatsCloserHeavy: the captain+analyst doctrine change
// inverts that expected pick.)
func TestSelectHullForCargo_NearestAdequateHullBeatsFartherSmaller(t *testing.T) {
	nearHeavy := newSelectorTestShipWithHull(t, "TORWIND-7", "HAULER", 10, 0, 30, 120)
	farLight := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 200, 0, 30, 40)

	result := selectHull(t, []*navigation.Ship{nearHeavy, farLight}, 30)

	if result.Ship.ShipSymbol() != "TORWIND-7" {
		t.Fatalf("expected the nearest adequate hull TORWIND-7 (10 away, 120-hold) to beat the farther smaller TORWIND-3 (200 away, 40-hold), got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

// Tier 1 (sp-f66z): proximity is measured by cruise travel time, which is
// speed-aware - two equal-hold hulls at the same distance are split by engine
// speed, so the faster hull that clears the leg sooner is the "nearer" one
// (sp-snmb, now the primary key rather than a within-capacity-tier tie-break).
func TestSelectHullForCargo_NearestByTravelTimeIsSpeedAware(t *testing.T) {
	slow := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 100, 0, 10, 40)
	fast := newSelectorTestShipWithHull(t, "TORWIND-4", "HAULER", 100, 0, 30, 40)

	result := selectHull(t, []*navigation.Ship{slow, fast}, 30)

	if result.Ship.ShipSymbol() != "TORWIND-4" {
		t.Fatalf("expected the faster of two equal-hold hulls to be nearest by travel time, got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

// Tier 1 tie-break (sp-f66z): when two adequate hulls are equidistant (same
// travel time), the smaller fitting hold wins - hold-size right-sizing is kept
// as the SECONDARY key, so l7h2 P3's anti-waste survives wherever proximity
// does not decide.
func TestSelectHullForCargo_EqualDistanceTieBreaksOnSmallerHold(t *testing.T) {
	bigHold := newSelectorTestShipWithHull(t, "TORWIND-7", "HAULER", 100, 0, 30, 120)
	smallHold := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 100, 0, 30, 40)

	result := selectHull(t, []*navigation.Ship{bigHold, smallHold}, 30)

	if result.Ship.ShipSymbol() != "TORWIND-3" {
		t.Fatalf("expected the smaller equidistant hold TORWIND-3 (40-hold) to win the tie-break over TORWIND-7 (120-hold), got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
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

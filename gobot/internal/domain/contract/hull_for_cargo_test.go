package contract

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	// nil, nil, nil: no ETA/ownership context; these tests exercise distance/capacity only.
	result, err := SelectHullForCargo(ships, hullFitTarget(t), units, nil, nil, nil)
	if err != nil {
		t.Fatalf("SelectHullForCargo: %v", err)
	}
	return result
}

// Tier 1: among hulls that FIT the load, the NEAREST wins even when a farther
// hull is smaller. Both are regular haulers at the same speed, so only hold
// size and distance differ and travel time isolates proximity.
func TestSelectHullForCargo_NearestAdequateHullBeatsFartherSmaller(t *testing.T) {
	nearHeavy := newSelectorTestShipWithHull(t, "TORWIND-7", "HAULER", 10, 0, 30, 120)
	farLight := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 200, 0, 30, 40)

	result := selectHull(t, []*navigation.Ship{nearHeavy, farLight}, 30)

	if result.Ship.ShipSymbol() != "TORWIND-7" {
		t.Fatalf("expected the nearest adequate hull TORWIND-7 (10 away, 120-hold) to beat the farther smaller TORWIND-3 (200 away, 40-hold), got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

// Tier 1: proximity is measured by cruise travel time, which is speed-aware -
// two equal-hold hulls at the same distance are split by engine speed, so the
// faster hull that clears the leg sooner is the "nearer" one.
func TestSelectHullForCargo_NearestByTravelTimeIsSpeedAware(t *testing.T) {
	slow := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 100, 0, 10, 40)
	fast := newSelectorTestShipWithHull(t, "TORWIND-4", "HAULER", 100, 0, 30, 40)

	result := selectHull(t, []*navigation.Ship{slow, fast}, 30)

	if result.Ship.ShipSymbol() != "TORWIND-4" {
		t.Fatalf("expected the faster of two equal-hold hulls to be nearest by travel time, got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

// Tier 1 tie-break: when two adequate hulls are equidistant (same travel
// time), the smaller fitting hold wins as the secondary key.
func TestSelectHullForCargo_EqualDistanceTieBreaksOnSmallerHold(t *testing.T) {
	bigHold := newSelectorTestShipWithHull(t, "TORWIND-7", "HAULER", 100, 0, 30, 120)
	smallHold := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 100, 0, 30, 40)

	result := selectHull(t, []*navigation.Ship{bigHold, smallHold}, 30)

	if result.Ship.ShipSymbol() != "TORWIND-3" {
		t.Fatalf("expected the smaller equidistant hold TORWIND-3 (40-hold) to win the tie-break over TORWIND-7 (120-hold), got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

// Tier 1 tiebreak: on an exact tie of distance AND capacity, the candidate
// NOT standing on its own assigned standby slot wins - prefer the displaced
// hull, leave the correctly-homed one parked.
func TestSelectHullForCargo_EqualTieBreaksOnDisplacedHullOverHomedHull(t *testing.T) {
	home := newSelectorTestShipWithHull(t, "TORWIND-7", "HAULER", 100, 0, 30, 80)
	displaced := newSelectorTestShipWithHull(t, "TORWIND-6", "HAULER", 100, 0, 30, 80)

	// Both stand at the same physical waypoint (newSelectorTestShipWithHull always
	// places its ship at symbol "X1-TW-A2"), so distance and capacity tie exactly.
	// The symbol-sorted zip gives TORWIND-6 a different slot (displaced) and
	// TORWIND-7 exactly the slot it's standing on (home).
	deliveryFleet := []string{"TORWIND-6", "TORWIND-7"}
	slots := []string{"X1-TW-OTHER", "X1-TW-A2"}

	result, err := SelectHullForCargo([]*navigation.Ship{home, displaced}, hullFitTarget(t), 30, nil, deliveryFleet, slots)
	if err != nil {
		t.Fatalf("SelectHullForCargo: %v", err)
	}

	if result.Ship.ShipSymbol() != "TORWIND-6" {
		t.Fatalf("expected the displaced hull TORWIND-6 to win the exact tie over the correctly-homed TORWIND-7, got %s (%s)",
			result.Ship.ShipSymbol(), result.Reason)
	}
}

// Precedence guard: the ownership tiebreak is consulted only on an exact
// distance+capacity tie. A nearer hull standing on its own slot must still
// beat a farther displaced hull, proving the tiebreak can never outrank
// proximity, Tier 1's primary key.
func TestSelectHullForCargo_OwnSlotTiebreakNeverOutranksADistanceDifference(t *testing.T) {
	near := newSelectorTestShipWithHull(t, "TORWIND-7", "HAULER", 10, 0, 30, 80)
	far := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 200, 0, 30, 80)

	// TORWIND-7 (near) is parked on its own slot; TORWIND-3 (far) is displaced.
	deliveryFleet := []string{"TORWIND-3", "TORWIND-7"}
	slots := []string{"X1-TW-OTHER", "X1-TW-A2"}

	result, err := SelectHullForCargo([]*navigation.Ship{near, far}, hullFitTarget(t), 30, nil, deliveryFleet, slots)
	if err != nil {
		t.Fatalf("SelectHullForCargo: %v", err)
	}

	if result.Ship.ShipSymbol() != "TORWIND-7" {
		t.Fatalf("expected the nearer hull TORWIND-7 to win despite standing on its own slot - the ownership tiebreak must never outrank a genuine distance difference, got %s (%s)",
			result.Ship.ShipSymbol(), result.Reason)
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
// last-resort.
func TestSelectHullForCargo_CommandFrigateBenchedWhileRegularHullFits(t *testing.T) {
	frigate := newSelectorTestShipWithHull(t, "TORWIND-1", "COMMAND", 10, 0, 36, 60)
	hauler := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 200, 0, 15, 80)

	result := selectHull(t, []*navigation.Ship{frigate, hauler}, 50)

	if result.Ship.ShipSymbol() != "TORWIND-3" {
		t.Fatalf("expected the regular hauler to bench the command frigate, got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
	}
}

// Tier 2: the command frigate is drafted when it is the only hull whose hold
// fits the load - it stays an eligible candidate, stepping in exactly when
// its hold is the differentiator.
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

// The command-cargo-baseline gate lives upstream, at selection time
// (ship_pool_manager.FilterCommandCargoBaseline) - SelectHullForCargo itself
// never changes, so the last-resort tier must still draft an UPGRADED frigate
// (era-2's 115-cargo hull) exactly as it drafts the smaller one above.
func TestSelectHullForCargo_UpgradedCommandFrigateDraftedWhenOnlyHullThatFits(t *testing.T) {
	hauler := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 10, 0, 30, 8)
	frigate := newSelectorTestShipWithHull(t, "TORWIND-1", "COMMAND", 200, 0, 36, 115)

	result := selectHull(t, []*navigation.Ship{hauler, frigate}, 30)

	if result.Ship.ShipSymbol() != "TORWIND-1" {
		t.Fatalf("expected the upgraded command frigate as the only fitting hull, got %s (%s)", result.Ship.ShipSymbol(), result.Reason)
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
	if _, err := SelectHullForCargo(nil, hullFitTarget(t), 10, nil, nil, nil); err == nil {
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

// newWaypointAt builds a test waypoint at the given coordinates - the
// ETA-ranking tests need distinct near/far positions independent of any
// hull's fixed test symbol.
func newWaypointAt(t *testing.T, symbol string, x, y float64) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, x, y)
	if err != nil {
		t.Fatalf("build waypoint %s: %v", symbol, err)
	}
	return wp
}

// newTestHull builds a docked, idle HAULER at the given waypoint with the
// given hold - the ETA-ranking tests vary only position, hold, and any
// supplied ETA.
func newTestHull(t *testing.T, symbol string, at *shared.Waypoint, cargoCapacity int) *navigation.Ship {
	t.Helper()
	return newSelectorTestShipWithHull(t, symbol, "HAULER", at.X, at.Y, 30, cargoCapacity)
}

// newTestHullInTransit builds an UNCLAIMED hull en route to destination by
// driving a freshly-built idle hull through the real docked -> orbit ->
// transit state machine, so its nav status and current location match what
// production navigation ever produces (StartTransit relocates the hull to
// destination).
func newTestHullInTransit(t *testing.T, symbol string, destination *shared.Waypoint, cargoCapacity int) *navigation.Ship {
	t.Helper()
	ship := newSelectorTestShipWithHull(t, symbol, "HAULER", 0, 0, 30, cargoCapacity)
	if _, err := ship.EnsureInOrbit(); err != nil {
		t.Fatalf("EnsureInOrbit: %v", err)
	}
	if err := ship.StartTransit(destination); err != nil {
		t.Fatalf("StartTransit: %v", err)
	}
	return ship
}

// Core behavior: with supplied ETAs, a hull whose TOTAL ETA is smaller wins even
// if its straight-line distance is larger (the straight-line order would invert this).
func TestSelectHullForCargo_SuppliedETAOutranksStraightLine(t *testing.T) {
	target := hullFitTarget(t)
	farWaypoint := newWaypointAt(t, "X1-TW-FAR", 500, 0)
	nearWaypoint := newWaypointAt(t, "X1-TW-NEAR", 10, 0)

	// far by distance, near by ETA (clean single hop)
	fastArrival := newTestHull(t, "TORWIND-B", farWaypoint, 80)
	// near by distance, slow by ETA (multi-hop + refuels)
	slowNear := newTestHull(t, "TORWIND-C", nearWaypoint, 80)
	etas := map[string]float64{"TORWIND-B": 120, "TORWIND-C": 600}

	res, err := SelectHullForCargo([]*navigation.Ship{slowNear, fastArrival}, target, 40, etas, nil, nil)

	require.NoError(t, err)
	assert.Equal(t, "TORWIND-B", res.Ship.ShipSymbol())
}

// Nil map preserves today's ranking exactly (fallback path).
func TestSelectHullForCargo_NilETAMapKeepsStraightLineRanking(t *testing.T) {
	target := hullFitTarget(t)
	farWaypoint := newWaypointAt(t, "X1-TW-FAR", 500, 0)
	nearWaypoint := newWaypointAt(t, "X1-TW-NEAR", 10, 0)

	near := newTestHull(t, "TORWIND-B", nearWaypoint, 80)
	far := newTestHull(t, "TORWIND-C", farWaypoint, 80)

	res, err := SelectHullForCargo([]*navigation.Ship{far, near}, target, 40, nil, nil, nil)

	require.NoError(t, err)
	assert.Equal(t, "TORWIND-B", res.Ship.ShipSymbol())
}

// Tie ladder preserved under supplied ETAs: equal ETA -> smaller hold; then the
// existing displaced-hull tiebreak (reuses its landed fixtures for the slot context).
func TestSelectHullForCargo_EqualETATieFallsToCapacityThenDisplacement(t *testing.T) {
	target := hullFitTarget(t)
	nearWaypoint := newWaypointAt(t, "X1-TW-NEAR", 10, 0)

	small := newTestHull(t, "TORWIND-B", nearWaypoint, 40)
	big := newTestHull(t, "TORWIND-C", nearWaypoint, 80)
	etas := map[string]float64{"TORWIND-B": 300, "TORWIND-C": 300}
	// Reuses TestSelectHullForCargo_EqualTieBreaksOnDisplacedHullOverHomedHull's
	// roster: neither TORWIND-B nor TORWIND-C is in it, so the ownership
	// tiebreak stays a no-op here and capacity alone decides - proving etas
	// composes with a real (non-nil) ownership context, not just nil.
	deliveryFleet := []string{"TORWIND-6", "TORWIND-7"}
	slots := []string{"X1-TW-OTHER", "X1-TW-A2"}

	res, err := SelectHullForCargo([]*navigation.Ship{big, small}, target, 30, etas, deliveryFleet, slots)

	require.NoError(t, err)
	assert.Equal(t, "TORWIND-B", res.Ship.ShipSymbol())
}

// The tie ladder's third rung - equal ETA -> capacity -> home-slot displacement - was previously
// pinned only with etas == nil (TestSelectHullForCargo_EqualTieBreaksOnDisplacedHullOverHomedHull
// above). Reuses that test's exact roster/slot fixtures, supplying an equal non-nil etas map
// instead of nil, so an equal ETA AND equal capacity still falls through to the displacement
// tiebreak rather than short-circuiting on some other key etas might have disturbed.
func TestSelectHullForCargo_EqualETAAndCapacityFallsToDisplacementTiebreak(t *testing.T) {
	home := newSelectorTestShipWithHull(t, "TORWIND-7", "HAULER", 100, 0, 30, 80)
	displaced := newSelectorTestShipWithHull(t, "TORWIND-6", "HAULER", 100, 0, 30, 80)
	etas := map[string]float64{"TORWIND-6": 300, "TORWIND-7": 300}

	// Same slot context as TestSelectHullForCargo_EqualTieBreaksOnDisplacedHullOverHomedHull: the
	// symbol-sorted zip gives TORWIND-6 a different slot (displaced) and TORWIND-7 exactly the
	// slot it's standing on (home).
	deliveryFleet := []string{"TORWIND-6", "TORWIND-7"}
	slots := []string{"X1-TW-OTHER", "X1-TW-A2"}

	result, err := SelectHullForCargo([]*navigation.Ship{home, displaced}, hullFitTarget(t), 30, etas, deliveryFleet, slots)
	if err != nil {
		t.Fatalf("SelectHullForCargo: %v", err)
	}

	if result.Ship.ShipSymbol() != "TORWIND-6" {
		t.Fatalf("expected the displaced hull TORWIND-6 to win the exact ETA+capacity tie over the correctly-homed TORWIND-7, got %s (%s)",
			result.Ship.ShipSymbol(), result.Reason)
	}
}

// RULINGS #1 invariant stated as a test: candidates in, a hull always comes out.
func TestSelectHullForCargo_SuppliedETAsNeverProduceNoSelection(t *testing.T) {
	target := hullFitTarget(t)
	farWaypoint := newWaypointAt(t, "X1-TW-FAR", 500, 0)

	only := newTestHull(t, "TORWIND-B", farWaypoint, 80)
	etas := map[string]float64{"TORWIND-B": 999999}

	res, err := SelectHullForCargo([]*navigation.Ship{only}, target, 40, etas, nil, nil)

	require.NoError(t, err)
	require.NotNil(t, res)
}

// In-transit unclaimed hulls are RANKED, not skipped, when ETAs are supplied.
func TestSelectOptimalShip_UnclaimedInTransitHullIsEligibleWithETA(t *testing.T) {
	target := hullFitTarget(t)
	farWaypoint := newWaypointAt(t, "X1-TW-FAR", 500, 0)
	nearWaypoint := newWaypointAt(t, "X1-TW-NEAR", 10, 0)

	inTransit := newTestHullInTransit(t, "TORWIND-B", nearWaypoint, 80) // unclaimed
	idleFar := newTestHull(t, "TORWIND-C", farWaypoint, 80)
	etas := map[string]float64{"TORWIND-B": 90, "TORWIND-C": 400}

	res, err := NewShipSelector().SelectOptimalShip([]*navigation.Ship{inTransit, idleFar}, target, "", 40, etas, nil, nil)

	require.NoError(t, err)
	assert.Equal(t, "TORWIND-B", res.Ship.ShipSymbol())
}

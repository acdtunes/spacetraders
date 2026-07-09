package contract

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newSelectorTestShip builds a docked, idle ship at (x,y) with the given symbol
// and role - the minimum the closest-ship rule inspects. Fixed speed=30/cargo=40
// so tests using it are only varying position.
func newSelectorTestShip(t *testing.T, symbol, role string, x, y float64) *navigation.Ship {
	t.Helper()
	return newSelectorTestShipWithHull(t, symbol, role, x, y, 30, 40)
}

// newSelectorTestShipWithHull builds a docked, idle ship at (x,y) with the given
// symbol, role, engine speed and cargo capacity - used by the hull right-sizing
// tests (sp-snmb) where speed and capacity must vary independently of position.
func newSelectorTestShipWithHull(t *testing.T, symbol, role string, x, y float64, engineSpeed, cargoCapacity int) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(cargoCapacity, 0, nil)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	wp, err := shared.NewWaypoint("X1-TW-A2", x, y)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		wp,
		fuel,
		100,
		cargoCapacity,
		cargo,
		engineSpeed,
		"FRAME_FRIGATE",
		role,
		nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	return ship
}

// Once the command ship is a candidate (sp-4a4e), the closest-ship rule must be
// able to pick it: a close COMMAND frigate beats a far hauler. This is exactly
// the selection the fallback-only pool was silently defeating - the far hauler
// (746 units) was chosen because the closer command ship never entered the pool.
func TestSelectOptimalShip_ClosestCommandShip_BeatsFartherHauler(t *testing.T) {
	target, err := shared.NewWaypoint("X1-TW-MKT", 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	hauler := newSelectorTestShip(t, "TORWIND-3", "HAULER", 700, 0)  // far
	command := newSelectorTestShip(t, "TORWIND-1", "COMMAND", 50, 0) // close

	selector := NewShipSelector()
	// Both ships share the same speed/cargo here, so unitsNeeded doesn't change
	// the outcome - this fixture is purely about the distance tie-break.
	result, err := selector.SelectOptimalShip([]*navigation.Ship{hauler, command}, target, "", 10)
	if err != nil {
		t.Fatalf("SelectOptimalShip: %v", err)
	}

	if result.Ship.ShipSymbol() != "TORWIND-1" {
		t.Fatalf("expected the closer command ship TORWIND-1 to win selection, got %s (distance %.2f)", result.Ship.ShipSymbol(), result.Distance)
	}
}

// Admiral ruling (sp-snmb): contracts can never be skipped, so cycle time must
// be cut by right-sizing the hull instead. A 2-unit delivery on a speed-36
// frigate clears faster than the same job on a speed-15 hauler even when the
// hauler is twice as close - the fast, adequately-sized hull should win a small
// delivery over raw proximity.
func TestSelectOptimalShip_FastAdequateFrigateBeatsCloserSlowHauler_ForSmallDelivery(t *testing.T) {
	target, err := shared.NewWaypoint("X1-TW-MKT", 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	// Closer but slow and oversized for a 2-unit job.
	hauler := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 50, 0, 15, 40)
	// Farther but fast; its 4-unit hold is still adequate for a 2-unit job.
	frigate := newSelectorTestShipWithHull(t, "TORWIND-1", "COMMAND", 100, 0, 36, 4)

	selector := NewShipSelector()
	result, err := selector.SelectOptimalShip([]*navigation.Ship{hauler, frigate}, target, "", 2)
	if err != nil {
		t.Fatalf("SelectOptimalShip: %v", err)
	}

	if result.Ship.ShipSymbol() != "TORWIND-1" {
		t.Fatalf("expected the fast adequate frigate TORWIND-1 to win a small delivery despite being farther, got %s (distance %.2f)", result.Ship.ShipSymbol(), result.Distance)
	}
}

// Mirror of the above: the same fast small-cargo frigate must NOT win a
// delivery that actually needs the hold - reserve big-cargo hulls for when
// the hold is needed, per the Admiral ruling. A 40-unit job forces the
// 4-cargo frigate into 10 trips, which no longer clears faster than the
// hauler doing it in one.
func TestSelectOptimalShip_BigCargoHaulerBeatsFastSmallFrigate_ForLargeDelivery(t *testing.T) {
	target, err := shared.NewWaypoint("X1-TW-MKT", 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	hauler := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 50, 0, 15, 40)
	frigate := newSelectorTestShipWithHull(t, "TORWIND-1", "COMMAND", 100, 0, 36, 4)

	selector := NewShipSelector()
	result, err := selector.SelectOptimalShip([]*navigation.Ship{hauler, frigate}, target, "", 40)
	if err != nil {
		t.Fatalf("SelectOptimalShip: %v", err)
	}

	if result.Ship.ShipSymbol() != "TORWIND-3" {
		t.Fatalf("expected the big-cargo hauler TORWIND-3 to win a delivery that needs the hold, got %s (distance %.2f)", result.Ship.ShipSymbol(), result.Distance)
	}
}

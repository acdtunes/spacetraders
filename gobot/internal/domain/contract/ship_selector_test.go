package contract

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

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

// Once the command ship is a candidate (sp-4a4e), the selection rule must be
// able to pick it - but under the l7h2 Phase 3 cargo-fit ladder it is drafted
// strictly last-resort: here its hold is the only one that fits the load, so
// it wins even from farther away. (Pre-Phase-3 this fixture asserted the
// closer command ship beat a far hauler on distance alone; the fit ladder
// benches the frigate whenever any regular hull can carry the load.)
func TestSelectOptimalShip_CommandShip_WinsWhenOnlyHullThatFits(t *testing.T) {
	target, err := shared.NewWaypoint("X1-TW-MKT", 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	hauler := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 50, 0, 30, 8)     // close, hold too small
	command := newSelectorTestShipWithHull(t, "TORWIND-1", "COMMAND", 700, 0, 30, 40) // far, fits

	selector := NewShipSelector()
	result, err := selector.SelectOptimalShip([]*navigation.Ship{hauler, command}, target, "", 10)
	if err != nil {
		t.Fatalf("SelectOptimalShip: %v", err)
	}

	if result.Ship.ShipSymbol() != "TORWIND-1" {
		t.Fatalf("expected the command ship TORWIND-1 as the only hull fitting 10 units, got %s (distance %.2f)", result.Ship.ShipSymbol(), result.Distance)
	}
}

// l7h2 Phase 3 doctrine: the command frigate is benched whenever a regular
// hull fits the load - even a frigate that is faster AND a tighter fit loses
// to a fitting hauler. Frigate-last-resort outranks smallest-fit; the frigate
// is preserved for the legs only it can carry. (Pre-Phase-3, sp-snmb's
// completion-time estimate sent the fast frigate on this 2-unit job.)
func TestSelectOptimalShip_FittingHaulerBenchesCommandFrigate_ForSmallDelivery(t *testing.T) {
	target, err := shared.NewWaypoint("X1-TW-MKT", 0, 0)
	if err != nil {
		t.Fatalf("build target waypoint: %v", err)
	}
	// Slow and oversized for a 2-unit job - but a regular hull that fits.
	hauler := newSelectorTestShipWithHull(t, "TORWIND-3", "HAULER", 50, 0, 15, 40)
	// Faster and tighter-fitting, but it is the command frigate.
	frigate := newSelectorTestShipWithHull(t, "TORWIND-1", "COMMAND", 100, 0, 36, 4)

	selector := NewShipSelector()
	result, err := selector.SelectOptimalShip([]*navigation.Ship{hauler, frigate}, target, "", 2)
	if err != nil {
		t.Fatalf("SelectOptimalShip: %v", err)
	}

	if result.Ship.ShipSymbol() != "TORWIND-3" {
		t.Fatalf("expected the fitting hauler TORWIND-3 to bench the command frigate, got %s (distance %.2f)", result.Ship.ShipSymbol(), result.Distance)
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

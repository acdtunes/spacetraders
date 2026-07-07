package contract

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newSelectorTestShip builds a docked, idle ship at (x,y) with the given symbol
// and role - the minimum the closest-ship rule inspects.
func newSelectorTestShip(t *testing.T, symbol, role string, x, y float64) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
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
		40,
		cargo,
		30,
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
	result, err := selector.SelectOptimalShip([]*navigation.Ship{hauler, command}, target, "")
	if err != nil {
		t.Fatalf("SelectOptimalShip: %v", err)
	}

	if result.Ship.ShipSymbol() != "TORWIND-1" {
		t.Fatalf("expected the closer command ship TORWIND-1 to win selection, got %s (distance %.2f)", result.Ship.ShipSymbol(), result.Distance)
	}
}

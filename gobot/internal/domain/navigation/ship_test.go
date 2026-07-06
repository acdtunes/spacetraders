package navigation_test

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func newFuelTestShip(t *testing.T, current, capacity int) *navigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(current, capacity)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	location, err := shared.NewWaypoint("X1-AU21-K82", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		"TORWIND-1",
		shared.MustNewPlayerID(1),
		location,
		fuel,
		capacity,
		40,
		cargo,
		9,
		"FRAME_HAULER",
		"HAULER",
		nil,
		navigation.NavStatusInOrbit,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// Reproduces #12: invalid API fuel data must surface a failure instead of
// silently leaving stale fuel that feeds routing/flight-mode decisions.
func TestUpdateFuelFromAPI_SurfacesErrorOnInvalidData(t *testing.T) {
	ship := newFuelTestShip(t, 80, 100)

	err := ship.UpdateFuelFromAPI(-5, 100)

	if err == nil {
		t.Fatalf("expected error for negative fuel current, got nil")
	}
	if ship.Fuel().Current != 80 || ship.Fuel().Capacity != 100 {
		t.Fatalf("expected fuel unchanged at 80/100, got %d/%d", ship.Fuel().Current, ship.Fuel().Capacity)
	}
}

// Valid API fuel data updates the ship's fuel state and returns no error.
func TestUpdateFuelFromAPI_UpdatesOnValidData(t *testing.T) {
	ship := newFuelTestShip(t, 80, 100)

	err := ship.UpdateFuelFromAPI(50, 120)

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if ship.Fuel().Current != 50 || ship.Fuel().Capacity != 120 {
		t.Fatalf("expected fuel updated to 50/120, got %d/%d", ship.Fuel().Current, ship.Fuel().Capacity)
	}
	if ship.FuelCapacity() != 120 {
		t.Fatalf("expected fuel capacity 120, got %d", ship.FuelCapacity())
	}
}

package navigation_test

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newCargoReservationTestShip builds a plain docked hull for cargo-reservation
// tests. Reservation overrides are applied by the individual tests.
func newCargoReservationTestShip(t *testing.T) *navigation.Ship {
	t.Helper()
	fuel, err := shared.NewFuel(80, 100)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	location, err := shared.NewWaypoint("X1-ZC66-BA9D", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		"TORWIND-1E", shared.MustNewPlayerID(1), location, fuel, 100, 40,
		cargo, 30, "FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// A MODULE_ symbol is reserved by DEFAULT with no per-hull state at all — the
// pure-code money guard against auto-selling ship hardware.
func TestIsDefaultReservedCargo_ModulesAndMountsReservedGoodsSellable(t *testing.T) {
	reserved := []string{"MODULE_CARGO_HOLD_III", "MODULE_JUMP_DRIVE_I", "MOUNT_MINING_LASER_II"}
	for _, good := range reserved {
		if !navigation.IsDefaultReservedCargo(good) {
			t.Errorf("%s must be reserved by default (ship hardware)", good)
		}
	}
	sellable := []string{"IRON_ORE", "FUEL", "ELECTRONICS", "SHIP_PARTS"}
	for _, good := range sellable {
		if navigation.IsDefaultReservedCargo(good) {
			t.Errorf("%s is a trade good and must NOT be reserved by default", good)
		}
	}
}

// A fresh hull (no overrides) reserves modules by default and sells trade goods —
// the executor's guard works with zero persisted state.
func TestShip_IsCargoReserved_DefaultsWithNoOverrides(t *testing.T) {
	ship := newCargoReservationTestShip(t)
	if !ship.IsCargoReserved("MODULE_CARGO_HOLD_III") {
		t.Error("a staged module must be reserved by default")
	}
	if ship.IsCargoReserved("IRON_ORE") {
		t.Error("a trade good must be sellable by default")
	}
}

// Explicit-reserve respected: a non-default good the operator protects is reserved.
func TestShip_IsCargoReserved_ExplicitReserveProtectsTradeGood(t *testing.T) {
	ship := newCargoReservationTestShip(t)
	ship.SetCargoReservation("ANTIMATTER", true)
	if !ship.IsCargoReserved("ANTIMATTER") {
		t.Error("an explicitly reserved good must be do-not-sell")
	}
	if ship.IsCargoReserved("IRON_ORE") {
		t.Error("reserving ANTIMATTER must not affect other trade goods")
	}
}

// Explicit-UNreserve allows sale: releasing a default-reserved module lets it sell
// (the deliberate-resale escape hatch), without affecting other modules.
func TestShip_IsCargoReserved_ExplicitUnreserveReleasesModule(t *testing.T) {
	ship := newCargoReservationTestShip(t)
	ship.SetCargoReservation("MODULE_CARGO_HOLD_III", false)
	if ship.IsCargoReserved("MODULE_CARGO_HOLD_III") {
		t.Error("an explicitly unreserved module must be sellable")
	}
	if !ship.IsCargoReserved("MODULE_JUMP_DRIVE_I") {
		t.Error("unreserving one module must not release every module")
	}
}

// Fail-closed: an unreadable/corrupt override set treats EVERY good as reserved —
// a read failure never converts reserved cargo into sellable manifest.
func TestShip_IsCargoReserved_CorruptStateFailsClosed(t *testing.T) {
	ship := newCargoReservationTestShip(t)
	ship.SetReservationOverrides(nil, true)
	if !ship.IsCargoReserved("MODULE_CARGO_HOLD_III") {
		t.Error("corrupt override state must fail closed for a module")
	}
	if !ship.IsCargoReserved("IRON_ORE") {
		t.Error("corrupt override state must fail closed even for a trade good (skip anything ambiguous)")
	}
}

// The override copy is defensive: mutating the returned map cannot alter the hull.
func TestShip_ReservationOverrides_ReturnsDefensiveCopy(t *testing.T) {
	ship := newCargoReservationTestShip(t)
	ship.SetCargoReservation("MODULE_CARGO_HOLD_III", false)
	got := ship.ReservationOverrides()
	got["MODULE_CARGO_HOLD_III"] = true // try to corrupt via the returned map
	if ship.IsCargoReserved("MODULE_CARGO_HOLD_III") {
		t.Error("ReservationOverrides must return a copy; external mutation must not change the hull")
	}
}

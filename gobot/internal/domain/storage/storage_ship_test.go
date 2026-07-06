package storage

import "testing"

// TestConfirmDepositBeyondCapacityWithoutReservationIsRejected reproduces the
// capacity-invariant breach: ConfirmDeposit with no prior ReserveSpace must not
// add cargo that exceeds capacity.
func TestConfirmDepositBeyondCapacityWithoutReservationIsRejected(t *testing.T) {
	ship, err := NewStorageShip("STORAGE-1", "WP-1", "OP-1", 100, nil)
	if err != nil {
		t.Fatalf("unexpected error creating ship: %v", err)
	}

	err = ship.ConfirmDeposit("IRON_ORE", 150)
	if err == nil {
		t.Fatalf("expected ConfirmDeposit beyond capacity to be rejected, got nil error")
	}

	if got := ship.GetInventory()["IRON_ORE"]; got != 0 {
		t.Fatalf("capacity invariant breached: inventory=%d, capacity=100", got)
	}
}

// TestConfirmDepositWithinCapacitySucceeds verifies the legitimate flow
// (ReserveSpace -> ConfirmDeposit) still works after the capacity guard.
func TestConfirmDepositWithinCapacitySucceeds(t *testing.T) {
	ship, err := NewStorageShip("STORAGE-1", "WP-1", "OP-1", 100, nil)
	if err != nil {
		t.Fatalf("unexpected error creating ship: %v", err)
	}

	if err := ship.ReserveSpace(50); err != nil {
		t.Fatalf("unexpected error reserving space: %v", err)
	}

	if err := ship.ConfirmDeposit("IRON_ORE", 50); err != nil {
		t.Fatalf("expected ConfirmDeposit within capacity to succeed, got: %v", err)
	}

	if got := ship.GetInventory()["IRON_ORE"]; got != 50 {
		t.Fatalf("expected inventory 50, got %d", got)
	}
}

package shared

import (
	"errors"
	"testing"
)

func TestFuelConsumeReturnsInsufficientFuelErrorWhenAmountExceedsAvailable(t *testing.T) {
	fuel, err := NewFuel(10, 100)
	if err != nil {
		t.Fatalf("NewFuel failed: %v", err)
	}

	result, err := fuel.Consume(11)

	var insufficient *InsufficientFuelError
	if !errors.As(err, &insufficient) {
		t.Fatalf("expected InsufficientFuelError, got result=%v err=%v", result, err)
	}
	if insufficient.Required != 11 || insufficient.Available != 10 {
		t.Fatalf("expected Required=11 Available=10, got Required=%d Available=%d",
			insufficient.Required, insufficient.Available)
	}
}

func TestFuelConsumeAllowsConsumingExactlyAvailableAmount(t *testing.T) {
	fuel, err := NewFuel(10, 100)
	if err != nil {
		t.Fatalf("NewFuel failed: %v", err)
	}

	result, err := fuel.Consume(10)

	if err != nil {
		t.Fatalf("expected no error consuming exact amount, got %v", err)
	}
	if result.Current != 0 {
		t.Fatalf("expected Current=0 after consuming all fuel, got %d", result.Current)
	}
}

// sp-xxhn: an authoritative source (the SpaceTraders API, or a DB row mirroring
// it) can transiently report current fuel above capacity — e.g. right after a
// frame swap that shrank the tank. Reconstruction must clamp to capacity rather
// than reject, otherwise the whole ship is dropped and the hull is sidelined.
func TestReconstructFuelClampsCurrentExceedingCapacity(t *testing.T) {
	fuel, err := ReconstructFuel(120, 100)

	if err != nil {
		t.Fatalf("expected no error reconstructing over-reported fuel, got %v", err)
	}
	if fuel.Current != 100 || fuel.Capacity != 100 {
		t.Fatalf("expected fuel clamped to 100/100, got %d/%d", fuel.Current, fuel.Capacity)
	}
}

func TestReconstructFuelLeavesValidCurrentUnchanged(t *testing.T) {
	fuel, err := ReconstructFuel(50, 100)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if fuel.Current != 50 || fuel.Capacity != 100 {
		t.Fatalf("expected fuel 50/100, got %d/%d", fuel.Current, fuel.Capacity)
	}
}

func TestReconstructFuelRejectsNegativeCurrent(t *testing.T) {
	if _, err := ReconstructFuel(-1, 100); err == nil {
		t.Fatalf("expected error for negative current fuel, got nil")
	}
}

func TestReconstructFuelRejectsNegativeCapacity(t *testing.T) {
	if _, err := ReconstructFuel(10, -1); err == nil {
		t.Fatalf("expected error for negative fuel capacity, got nil")
	}
}

// The strict invariant is preserved for genuine domain construction/mutation:
// only reconstruction from an authoritative source clamps.
func TestNewFuelStillRejectsCurrentExceedingCapacity(t *testing.T) {
	if _, err := NewFuel(120, 100); err == nil {
		t.Fatalf("expected NewFuel to reject current>capacity, got nil")
	}
}

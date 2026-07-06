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

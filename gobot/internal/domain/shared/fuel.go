package shared

import "fmt"

// Fuel represents an immutable fuel state
type Fuel struct {
	Current  int
	Capacity int
}

func NewFuel(current, capacity int) (*Fuel, error) {
	if current < 0 {
		return nil, fmt.Errorf("current fuel cannot be negative")
	}
	if capacity < 0 {
		return nil, fmt.Errorf("fuel capacity cannot be negative")
	}
	if current > capacity {
		return nil, fmt.Errorf("current fuel cannot exceed capacity")
	}

	return &Fuel{
		Current:  current,
		Capacity: capacity,
	}, nil
}

// ReconstructFuel rehydrates a Fuel value object from an authoritative external
// source (the SpaceTraders API or a DB row mirroring it), where capacity is the
// source of truth for tank size. Unlike NewFuel, it clamps current down to
// capacity instead of rejecting current>capacity: the API can transiently
// over-report fuel against a shrunk capacity (e.g. right after a frame swap),
// and rejecting the whole ship would sideline an otherwise-usable hull.
// Genuinely invalid data (negative values) still errors, so callers ingesting an
// authoritative snapshot never silently keep stale fuel.
func ReconstructFuel(current, capacity int) (*Fuel, error) {
	if current < 0 {
		return nil, fmt.Errorf("current fuel cannot be negative")
	}
	if capacity < 0 {
		return nil, fmt.Errorf("fuel capacity cannot be negative")
	}
	return &Fuel{
		Current:  min(current, capacity),
		Capacity: capacity,
	}, nil
}

func (f *Fuel) Percentage() float64 {
	if f.Capacity == 0 {
		return 0.0
	}
	return float64(f.Current) / float64(f.Capacity) * 100.0
}

func (f *Fuel) Consume(amount int) (*Fuel, error) {
	if amount < 0 {
		return nil, fmt.Errorf("fuel amount cannot be negative")
	}
	if amount > f.Current {
		return nil, NewInsufficientFuelError(amount, f.Current)
	}
	return &Fuel{
		Current:  f.Current - amount,
		Capacity: f.Capacity,
	}, nil
}

func (f *Fuel) Add(amount int) (*Fuel, error) {
	if amount < 0 {
		return nil, fmt.Errorf("add amount cannot be negative")
	}
	newCurrent := f.Current + amount
	if newCurrent > f.Capacity {
		newCurrent = f.Capacity
	}
	return &Fuel{
		Current:  newCurrent,
		Capacity: f.Capacity,
	}, nil
}

func (f *Fuel) CanTravel(required int, safetyMargin float64) bool {
	if f.Current == 0 {
		return false
	}
	requiredWithMargin := int(float64(required) * (1 + safetyMargin))
	return f.Current >= requiredWithMargin
}

func (f *Fuel) IsFull() bool {
	return f.Current == f.Capacity
}

func (f *Fuel) String() string {
	return fmt.Sprintf("Fuel(%d/%d)", f.Current, f.Capacity)
}

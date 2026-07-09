package shared

import "fmt"

// Fuel represents an immutable fuel state
type Fuel struct {
	Current  int
	Capacity int
}

// NewFuel creates a new fuel value object with validation
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
// and rejecting the whole ship would sideline an otherwise-usable hull (sp-xxhn).
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

// Percentage returns fuel as percentage of capacity
func (f *Fuel) Percentage() float64 {
	if f.Capacity == 0 {
		return 0.0
	}
	return float64(f.Current) / float64(f.Capacity) * 100.0
}

// Consume returns new Fuel with amount consumed
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

// Add returns new Fuel with amount added
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

// CanTravel checks if sufficient fuel for travel with safety margin
func (f *Fuel) CanTravel(required int, safetyMargin float64) bool {
	// Cannot travel if fuel tank is empty
	if f.Current == 0 {
		return false
	}
	requiredWithMargin := int(float64(required) * (1 + safetyMargin))
	return f.Current >= requiredWithMargin
}

// IsFull checks if fuel is at capacity
func (f *Fuel) IsFull() bool {
	return f.Current == f.Capacity
}

func (f *Fuel) String() string {
	return fmt.Sprintf("Fuel(%d/%d)", f.Current, f.Capacity)
}

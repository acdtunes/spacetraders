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
	newCurrent := f.Current - amount
	if newCurrent < 0 {
		newCurrent = 0
	}
	return &Fuel{
		Current:  newCurrent,
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

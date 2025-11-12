package shared

import "fmt"

// CargoItem represents an individual cargo item in ship's hold
type CargoItem struct {
	Symbol      string
	Name        string
	Description string
	Units       int
}

// NewCargoItem creates a new cargo item with validation
func NewCargoItem(symbol, name, description string, units int) (*CargoItem, error) {
	if units < 0 {
		return nil, fmt.Errorf("cargo units cannot be negative")
	}
	if symbol == "" {
		return nil, fmt.Errorf("cargo symbol cannot be empty")
	}

	return &CargoItem{
		Symbol:      symbol,
		Name:        name,
		Description: description,
		Units:       units,
	}, nil
}

// Cargo represents ship cargo manifest with detailed inventory
type Cargo struct {
	Capacity  int
	Units     int
	Inventory []*CargoItem
}

// NewCargo creates a new cargo manifest with validation
func NewCargo(capacity, units int, inventory []*CargoItem) (*Cargo, error) {
	if units < 0 {
		return nil, fmt.Errorf("cargo units cannot be negative")
	}
	if capacity < 0 {
		return nil, fmt.Errorf("cargo capacity cannot be negative")
	}
	if units > capacity {
		return nil, fmt.Errorf("cargo units %d exceed capacity %d", units, capacity)
	}

	// Verify inventory sum matches total units
	inventorySum := 0
	for _, item := range inventory {
		inventorySum += item.Units
	}
	if inventorySum != units {
		return nil, fmt.Errorf("inventory sum %d != total units %d", inventorySum, units)
	}

	return &Cargo{
		Capacity:  capacity,
		Units:     units,
		Inventory: inventory,
	}, nil
}

// HasItem checks if cargo contains at least minUnits of specific item
func (c *Cargo) HasItem(symbol string, minUnits int) bool {
	return c.GetItemUnits(symbol) >= minUnits
}

// GetItemUnits gets units of specific trade good in cargo (0 if not present)
func (c *Cargo) GetItemUnits(symbol string) int {
	for _, item := range c.Inventory {
		if item.Symbol == symbol {
			return item.Units
		}
	}
	return 0
}

// HasItemsOtherThan checks if cargo contains items other than specified symbol
func (c *Cargo) HasItemsOtherThan(symbol string) bool {
	for _, item := range c.Inventory {
		if item.Symbol != symbol && item.Units > 0 {
			return true
		}
	}
	return false
}

// AvailableCapacity calculates available cargo space
func (c *Cargo) AvailableCapacity() int {
	return c.Capacity - c.Units
}

// IsEmpty checks if cargo hold is empty
func (c *Cargo) IsEmpty() bool {
	return c.Units == 0
}

// IsFull checks if cargo hold is full
func (c *Cargo) IsFull() bool {
	return c.Units >= c.Capacity
}

func (c *Cargo) String() string {
	return fmt.Sprintf("Cargo(%d/%d)", c.Units, c.Capacity)
}

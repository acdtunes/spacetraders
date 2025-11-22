package navigation

import "strings"

// ShipModule represents an installed module on a ship
//
// Modules provide various capabilities to ships such as jump drives,
// mining lasers, cargo bays, etc. This value object is immutable.
//
// Invariants:
// - Symbol must be non-empty
// - Capacity and Range cannot be negative
type ShipModule struct {
	symbol   string
	capacity int
	range_   int // use range_ to avoid Go keyword
}

// NewShipModule creates a new ShipModule value object
func NewShipModule(symbol string, capacity, range_ int) *ShipModule {
	return &ShipModule{
		symbol:   symbol,
		capacity: capacity,
		range_:   range_,
	}
}

// Symbol returns the module symbol identifier (e.g., "MODULE_JUMP_DRIVE_I")
func (m *ShipModule) Symbol() string {
	return m.symbol
}

// Capacity returns the module's capacity (if applicable)
func (m *ShipModule) Capacity() int {
	return m.capacity
}

// Range returns the module's range (if applicable)
func (m *ShipModule) Range() int {
	return m.range_
}

// IsJumpDrive checks if this module is a jump drive
// Jump drive modules have symbols starting with "MODULE_JUMP_DRIVE"
func (m *ShipModule) IsJumpDrive() bool {
	return strings.HasPrefix(m.symbol, "MODULE_JUMP_DRIVE")
}

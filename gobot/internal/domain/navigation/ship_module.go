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
	symbol       string
	capacity     int
	range_       int // use range_ to avoid Go keyword
	requirements ShipRequirements
}

// NewShipModule creates a new ShipModule value object
func NewShipModule(symbol string, capacity, range_ int, requirements ShipRequirements) *ShipModule {
	return &ShipModule{
		symbol:       symbol,
		capacity:     capacity,
		range_:       range_,
		requirements: requirements,
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

// Requirements returns the module's power/crew/slot cost.
func (m *ShipModule) Requirements() ShipRequirements {
	return m.requirements
}

// IsJumpDrive checks if this module is a jump drive
// Jump drive modules have symbols starting with "MODULE_JUMP_DRIVE"
func (m *ShipModule) IsJumpDrive() bool {
	return strings.HasPrefix(m.symbol, "MODULE_JUMP_DRIVE")
}

// IsWarpDrive checks if this module is a warp drive.
// Warp drive modules have symbols starting with "MODULE_WARP_DRIVE" (e.g.
// "MODULE_WARP_DRIVE_I"). A warp drive is the physical mechanism a SHIP_EXPLORER
// uses to travel BETWEEN systems off the jump-gate network (sp-0xd0); it is
// distinct from a jump drive (MODULE_JUMP_DRIVE_*), which hops gate-to-gate.
func (m *ShipModule) IsWarpDrive() bool {
	return strings.HasPrefix(m.symbol, "MODULE_WARP_DRIVE")
}

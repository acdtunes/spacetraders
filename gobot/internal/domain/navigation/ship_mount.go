package navigation

// ShipMount represents an installed mount on a ship (mining lasers, gas
// siphons, sensor arrays, weapons, etc.). This value object is immutable and
// mirrors ShipModule's shape.
//
// Invariants:
// - Symbol must be non-empty
// - Strength cannot be negative
type ShipMount struct {
	symbol       string
	name         string
	strength     int
	deposits     []string
	requirements ShipRequirements
}

// NewShipMount creates a new ShipMount value object. deposits may be nil for
// mount types that don't restrict to specific extractable goods (e.g. a
// sensor array) - Deposits() normalizes that to an empty slice.
func NewShipMount(symbol, name string, strength int, deposits []string, requirements ShipRequirements) *ShipMount {
	return &ShipMount{
		symbol:       symbol,
		name:         name,
		strength:     strength,
		deposits:     deposits,
		requirements: requirements,
	}
}

func (m *ShipMount) Symbol() string {
	return m.symbol
}

func (m *ShipMount) Name() string {
	return m.name
}

// Strength returns the mount's strength (e.g. mining/siphon yield bonus). 0
// for mounts where strength doesn't apply.
func (m *ShipMount) Strength() int {
	return m.strength
}

// Deposits returns the extractable goods this mount is restricted to, or an
// empty slice if the mount has no deposit restriction.
func (m *ShipMount) Deposits() []string {
	if m.deposits == nil {
		return []string{}
	}
	return m.deposits
}

func (m *ShipMount) Requirements() ShipRequirements {
	return m.requirements
}

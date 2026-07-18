package shared

import (
	"fmt"
	"math"
	"strings"
)

// Waypoint represents an immutable location in space
type Waypoint struct {
	Symbol       string   `json:"symbol"`
	X            float64  `json:"x"`
	Y            float64  `json:"y"`
	SystemSymbol string   `json:"systemSymbol"`
	Type         string   `json:"type"`
	Traits       []string `json:"traits,omitempty"`
	HasFuel      bool     `json:"has_fuel"`
	Orbitals     []string `json:"orbitals,omitempty"`
}

// NewWaypoint creates a new waypoint with validation
func NewWaypoint(symbol string, x, y float64) (*Waypoint, error) {
	if symbol == "" {
		return nil, NewValidationError("symbol", "cannot be empty")
	}

	return &Waypoint{
		Symbol:       symbol,
		X:            x,
		Y:            y,
		SystemSymbol: ExtractSystemSymbol(symbol),
		Traits:       []string{},
		Orbitals:     []string{},
	}, nil
}

// DistanceTo calculates Euclidean distance to another waypoint
func (w *Waypoint) DistanceTo(other *Waypoint) float64 {
	dx := other.X - w.X
	dy := other.Y - w.Y
	return math.Sqrt(dx*dx + dy*dy)
}

// FindNearestWaypoint returns the nearest waypoint from a list and its distance
// Returns nil and 0 if targets list is empty
func FindNearestWaypoint(from *Waypoint, targets []*Waypoint) (*Waypoint, float64) {
	if len(targets) == 0 {
		return nil, 0
	}

	nearest := targets[0]
	minDistance := from.DistanceTo(targets[0])

	for _, target := range targets[1:] {
		distance := from.DistanceTo(target)
		if distance < minDistance {
			minDistance = distance
			nearest = target
		}
	}

	return nearest, minDistance
}

func (w *Waypoint) String() string {
	return fmt.Sprintf("Waypoint(%s)", w.Symbol)
}

// HasTrait checks if the waypoint has the specified trait.
//
// Traits indicate special characteristics of a waypoint:
//   - "MARKETPLACE": Has a market for buying/selling goods
//   - "SHIPYARD": Has a shipyard for purchasing ships
//   - "UNCHARTED": Not yet explored
//   - etc.
//
// This method encapsulates trait checking logic that was previously scattered
// across application layer (RouteExecutor, etc.), following Tell Don't Ask principle.
func (w *Waypoint) HasTrait(trait string) bool {
	for _, t := range w.Traits {
		if t == trait {
			return true
		}
	}
	return false
}

// IsMarketplace checks if this waypoint has a marketplace.
//
// This is a convenience method for the common trait check.
// Marketplaces allow ships to buy/sell cargo and refuel.
func (w *Waypoint) IsMarketplace() bool {
	return w.HasTrait("MARKETPLACE")
}

// IsJumpGate checks if this waypoint is a jump gate.
//
// Jump gates allow ships to travel between star systems.
// Ships must be at a jump gate waypoint to execute a jump.
func (w *Waypoint) IsJumpGate() bool {
	return w.Type == "JUMP_GATE"
}

// Waypoint trait symbols that imply on-site fuel: a MARKETPLACE or a FUEL_STATION
// sells fuel. This is the single source of truth for the has-fuel determination;
// the api adapter (waypoint converter and graph builder) reads it through the
// TraitGrantsFuel / TraitsGrantFuel predicates below rather than restating the rule.
const (
	traitMarketplace = "MARKETPLACE"
	traitFuelStation = "FUEL_STATION"
)

// TraitGrantsFuel reports whether a single waypoint trait implies on-site fuel:
// a MARKETPLACE or a FUEL_STATION sells fuel.
func TraitGrantsFuel(trait string) bool {
	return trait == traitMarketplace || trait == traitFuelStation
}

// TraitsGrantFuel reports whether any trait in the slice implies on-site fuel.
func TraitsGrantFuel(traits []string) bool {
	for _, trait := range traits {
		if TraitGrantsFuel(trait) {
			return true
		}
	}
	return false
}

// ExtractSystemSymbol extracts the system symbol from a waypoint symbol
// by finding the last hyphen and returning everything before it.
// Example: "X1-AB12-C3D4" -> "X1-AB12"
func ExtractSystemSymbol(waypointSymbol string) string {
	lastHyphen := strings.LastIndexByte(waypointSymbol, '-')
	if lastHyphen < 0 {
		return waypointSymbol
	}
	return waypointSymbol[:lastHyphen]
}

package shared

import (
	"fmt"
	"math"
)

// Waypoint represents an immutable location in space
type Waypoint struct {
	Symbol       string
	X            float64
	Y            float64
	SystemSymbol string
	Type         string
	Traits       []string
	HasFuel      bool
	Orbitals     []string
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

// IsOrbitalOf checks if this waypoint orbits another
func (w *Waypoint) IsOrbitalOf(other *Waypoint) bool {
	for _, orbital := range w.Orbitals {
		if orbital == other.Symbol {
			return true
		}
	}
	for _, orbital := range other.Orbitals {
		if orbital == w.Symbol {
			return true
		}
	}
	return false
}

func (w *Waypoint) String() string {
	return fmt.Sprintf("Waypoint(%s)", w.Symbol)
}

// ExtractSystemSymbol extracts the system symbol from a waypoint symbol
// by finding the last hyphen and returning everything before it.
// Example: "X1-AB12-C3D4" -> "X1-AB12"
func ExtractSystemSymbol(waypointSymbol string) string {
	systemSymbol := waypointSymbol
	for i := len(waypointSymbol) - 1; i >= 0; i-- {
		if waypointSymbol[i] == '-' {
			systemSymbol = waypointSymbol[:i]
			break
		}
	}
	return systemSymbol
}

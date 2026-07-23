package contractscaler

// CentralPark is one resolved central contract-sink park with its coordinates and
// demand weight — the input to the coord-dedup that collapses co-located parks
// (a planet and its moons share one coordinate) to a single homing target.
type CentralPark struct {
	Symbol string
	X, Y   float64
	Demand float64
}

// DedupeCoLocatedParks collapses co-located central parks (same coordinate — a planet
// and its moons) to a single REPRESENTATIVE, the highest-demand one (symbol-tiebroken),
// returning the representative-park→demand map C1's demand-ranked homing consumes. It
// recovers the design's spatial-spread intent (idle hulls spread across distinct
// LOCATIONS, not co-located waypoints like G49 AND G47 on one planet) without the full
// weighted-p-median: two hulls homed to the same coordinate would waste a spread slot, so
// one waypoint per location carries the location's demand. Empty input → empty map.
func DedupeCoLocatedParks(parks []CentralPark) map[string]float64 {
	rep := make(map[[2]float64]CentralPark, len(parks))
	for _, park := range parks {
		coord := [2]float64{park.X, park.Y}
		best, seen := rep[coord]
		if !seen || park.Demand > best.Demand || (park.Demand == best.Demand && park.Symbol < best.Symbol) {
			rep[coord] = park
		}
	}

	out := make(map[string]float64, len(rep))
	for _, park := range rep {
		out[park.Symbol] = park.Demand
	}
	return out
}

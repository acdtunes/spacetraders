package shared

// A waypoint's TYPE is published on its system's waypoint list whether or not
// anybody has charted it; only its TRAITS are withheld behind the UNCHARTED
// trait. Type is therefore the only evidence a charting tour holds about what a
// stop is worth before it pays the flight, which is what makes ordering by type
// possible at all.
const (
	WaypointTypeAsteroid           = "ASTEROID"
	WaypointTypeAsteroidBase       = "ASTEROID_BASE"
	WaypointTypeEngineeredAsteroid = "ENGINEERED_ASTEROID"
	WaypointTypeMoon               = "MOON"
	WaypointTypePlanet             = "PLANET"
	WaypointTypeOrbitalStation     = "ORBITAL_STATION"
	WaypointTypeFuelStation        = "FUEL_STATION"
	WaypointTypeGasGiant           = "GAS_GIANT"
	WaypointTypeJumpGate           = "JUMP_GATE"
)

// Charting tiers, lowest visited first. Each ranks a type by what charting it
// UNLOCKS, on the fleet's own charted census of which types carry a market or a
// shipyard.
const (
	// chartPriorityShipyard leads because a charted shipyard makes its system
	// BUYABLE, which funds local spares, which stage further seeds: a yard found
	// early changes what the fleet can DO next, where a market found early only
	// adds trade data. Nearly every shipyard sits on an orbital station.
	chartPriorityShipyard = 0
	// chartPriorityGate is the one tier ranked by something other than what the
	// waypoint HOLDS. Charting a gate reveals its system's GATE ADJACENCY, which
	// frontier propagation turns into new PENDING rows — the only type whose
	// charting adds SYSTEMS rather than trade data, and reach is what the expansion
	// engine runs out of first. It sits BEHIND the shipyard deliberately: yards fund
	// the spares that stage the very seeds that fly to the gates.
	chartPriorityGate = 1
	// chartPriorityMarket is the reliably market-bearing remainder. Charting one
	// early is what lets a parked scanning probe be placed on it and start producing
	// trade data WHILE the tour continues.
	chartPriorityMarket = 2
	// chartPriorityUnproven is for a type that carries a market rarely, and for any
	// type this file does not recognise. An unknown type sorts HERE rather than
	// last: not being recognised is not evidence of being worthless, and burying it
	// behind every rock in the system would be a guess in the costly direction.
	chartPriorityUnproven = 3
	// chartPriorityBarren is for a type never yet observed to hold either.
	chartPriorityBarren = 4
)

// ChartPriority orders a system's uncharted waypoints by what charting them
// unlocks, lowest visited first: shipyard-bearing, JUMP GATE, market-bearing,
// unproven, barren.
//
// IT DECIDES SEQUENCE ONLY — it orders whatever set it is given and removes
// nothing. Every uncharted waypoint is charting work whatever its type, because a
// chart pays a reward of its own wherever it lands; the tier decides only how
// soon a stop is reached, never whether it is.
//
// THE BARREN TIER IS WHERE MOST OF THE WORK IS, which is precisely why it sorts
// last: a tour that took it in catalog order would leave those flights standing
// between the fleet and every yard and market in the same system, and the parked
// scanners that trade data comes from would deploy that much later.
//
// This is a TIE-BREAK KEY, not a total order — callers sort on (priority, symbol)
// so the result is fully deterministic within a tier. Determinism is what the
// tour requires: a seed charts the head of the list and re-derives it next tick,
// so an unstable order would let it oscillate between two waypoints and never
// finish.
func ChartPriority(waypointType string) int {
	switch waypointType {
	case WaypointTypeOrbitalStation:
		return chartPriorityShipyard
	case WaypointTypeJumpGate:
		return chartPriorityGate
	case WaypointTypeMoon,
		WaypointTypePlanet,
		WaypointTypeFuelStation,
		WaypointTypeAsteroidBase,
		WaypointTypeEngineeredAsteroid:
		return chartPriorityMarket
	case WaypointTypeAsteroid:
		return chartPriorityBarren
	default:
		return chartPriorityUnproven
	}
}

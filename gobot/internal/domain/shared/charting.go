package shared

// WAYPOINT TYPE IS FREE; TRAITS ARE NOT.
//
// A system's waypoint LIST carries every waypoint's TYPE whether or not anybody
// has charted it — only TRAITS are withheld behind the UNCHARTED trait. That
// asymmetry is what this file rests on: it lets a charting tour decide what a
// waypoint is likely to be WORTH before paying the flight to find out what it
// actually HOLDS.
//
// The census below is the fleet's own charted history, counted over every
// waypoint it has ever charted. It is evidence, not intuition.
//
//	TYPE                  CHARTED  MARKETS  SHIPYARDS
//	ASTEROID                 3297        0          0
//	MOON                     2144     1521         44
//	PLANET                   1501     1252          0
//	FUEL_STATION             1129     1129          0
//	ORBITAL_STATION           965      873        523
//	GAS_GIANT                 546       72          0
//	JUMP_GATE                 373      373          0
//	ASTEROID_BASE             345      345          0
//	ENGINEERED_ASTEROID        22       22          0
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

// Charting tiers, lowest visited first.
const (
	// chartPriorityShipyard is where the compounding lives, and it is the only
	// boundary here carrying a strong argument. A charted shipyard makes its
	// system BUYABLE, which funds local spares, which stage further seeds — so a
	// yard found early changes what the fleet can DO next, where a market found
	// early only adds trade data. ORBITAL_STATION holds 523 of the 567 shipyards
	// ever seen.
	chartPriorityShipyard = 0
	// chartPriorityMarket is the market-bearing remainder. Charting these early
	// is what lets a parked scanning probe be placed on them and start producing
	// trade data WHILE the tour continues — the whole reason the sequence
	// matters. MOON also carries the only 44 shipyards not on a station.
	chartPriorityMarket = 1
	// chartPriorityUnproven is for a type that does sometimes hold a market but
	// rarely, and for any type this file does not recognise. An unknown type
	// sorts HERE rather than last: not being recognised is not evidence of being
	// worthless, and burying it behind fifty asteroids would be a guess in the
	// costly direction.
	chartPriorityUnproven = 2
	// chartPriorityBarren is for a type never once observed to hold anything.
	chartPriorityBarren = 3
)

// ChartPriority orders a system's uncharted waypoints by what charting them
// UNLOCKS. Lower is visited first.
//
// THE TOUR REMAINS EXHAUSTIVE — this decides SEQUENCE ONLY. Every waypoint in
// the system is still charted, asteroids included, and the set being ordered is
// identical to the set that was toured before. Nothing is skipped and nothing is
// hidden, so the full map still completes and uncharted_count still falls to
// zero exactly as it always did.
//
// WHY SEQUENCE IS WORTH CHANGING ANYWAY. The design already separates two roles
// — ONE probe per system flying the charting errand, and SEVERAL probes parked
// on markets scanning them. The tour used to run in arbitrary (alphabetical)
// order, so a seed could spend fifty hours on asteroids before revealing the one
// market a scanner could have been sitting on the whole time. Ordering by
// expected value deploys those scanners early and still finishes the map.
// TORWIND-18 is the live case: standing on an asteroid in X1-AJ10 with 54
// asteroids and one FUEL_STATION left, and a FUEL_STATION is ~100% a market.
//
// THE ORDERING IS ONLY POSSIBLE BECAUSE IT KEYS ON TYPE. A waypoint's SHIPYARD
// and MARKETPLACE traits are hidden until it is charted, so type is the only
// evidence available in advance — which is what makes this a cheap local change
// rather than a new source of information.
//
// This is a TIE-BREAK KEY, not a total order — callers sort by (priority,
// symbol) so the result stays fully deterministic within a tier. Determinism is
// what the tour actually requires: a seed charts the head of the list and
// re-derives it next tick, so an unstable order would let it oscillate between
// two waypoints and never finish.
//
// ASTEROID SORTS LAST on the strongest evidence in the table — 3297 charted, and
// not one market or shipyard among them — and it is also where nearly all of the
// work is, at 12772 of the 12945 waypoints still uncharted. Sorting it last is
// what stops those 12772 flights from standing between the fleet and every yard
// and market in the same system. Every one of them is still flown.
func ChartPriority(waypointType string) int {
	switch waypointType {
	case WaypointTypeOrbitalStation:
		return chartPriorityShipyard
	case WaypointTypeMoon,
		WaypointTypePlanet,
		WaypointTypeFuelStation,
		WaypointTypeJumpGate,
		WaypointTypeAsteroidBase,
		WaypointTypeEngineeredAsteroid:
		return chartPriorityMarket
	case WaypointTypeAsteroid:
		return chartPriorityBarren
	default:
		return chartPriorityUnproven
	}
}

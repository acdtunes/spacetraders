package parkedsensing

import (
	"math"
	"sort"
)

// chartcrew.go divides ONE dark system's charting work across several hulls.
//
// A charting tour is serial: a hull charts one waypoint per turn and walks to the
// next, so a system's completion time is its outstanding count times a hull's
// pace. Splitting that list between hulls is the only lever on it — the pace per
// hull is fixed by the game's flight and cooldown timers.
//
// TWO RULES, AND THEY ARE SEPARATE. The BUDGET decides how many hulls a system
// may draw, sized on how much of it is still dark; the PARTITION decides which
// waypoints each of them owns. The budget bounds what the seeder raises, the
// partition bounds what an errand already running may touch, and a budget of one
// leaves both exactly as a single-hull tour.

const (
	// defaultChartHullCap is the most hulls one dark system may draw. Every hull
	// past the first pays its own walk to the system before it charts anything,
	// so the ceiling is low: past it the walks cost more than the parallelism
	// returns.
	defaultChartHullCap = 3
	// defaultSecondChartHullAt and defaultThirdChartHullAt are the outstanding
	// counts at which a system earns its second and third hull. They sit either
	// side of the typical dark system's outstanding count, so the second hull
	// reaches the bulk of real charting work while systems close to finished are
	// left to the hull already on them.
	defaultSecondChartHullAt = 12
	defaultThirdChartHullAt  = 24
)

// chartHulls is one tick's resolved hull budget for charting tours. Zero knobs
// mean the documented defaults, which is the `tune <key> 0` revert and also the
// no-config launch.
type chartHulls struct {
	cap    int
	second int
	third  int
}

// resolveChartHulls fills the documented default in for every knob left unset.
//
// A cap of one is a legitimate operator choice and the feature's kill switch, so
// it is honoured; only a non-positive cap reverts. The thresholds are read in
// ascending order and a threshold below the one before it simply never binds
// separately — the budget is monotonic in the outstanding count either way.
func resolveChartHulls(k ExpandKnobs) chartHulls {
	h := chartHulls{cap: k.ChartHullCap, second: k.SecondChartHullAt, third: k.ThirdChartHullAt}
	if h.cap <= 0 {
		h.cap = defaultChartHullCap
	}
	if h.second <= 0 {
		h.second = defaultSecondChartHullAt
	}
	if h.third <= 0 {
		h.third = defaultThirdChartHullAt
	}
	return h
}

// budgetFor is how many hulls a system with this much outstanding work may hold.
//
// AN UNSWEPT SYSTEM REPORTS ZERO and therefore earns one hull, which is correct
// rather than incidental: nobody has looked, so there is no size to scale on, and
// the first hull's catalog sweep is what produces one.
func (h chartHulls) budgetFor(uncharted int) int {
	budget := 1
	if uncharted >= h.second {
		budget++
	}
	if uncharted >= h.third {
		budget++
	}
	if budget > h.cap {
		return h.cap
	}
	return budget
}

// seedCrew is the hulls charting one system: the one named by the system row's
// own seed columns, and any beyond it.
type seedCrew struct {
	primary string
	extras  []string
}

// seedRoster is the tick's live picture of who is charting what. It is mutated by
// every errand write so a later pass of the same tick cannot crew a system twice.
type seedRoster struct {
	crews map[string]*seedCrew
}

// newSeedRoster reads the roster off the ledger's system rows. DONE errands are
// deliberately absent: a finished errand is over, and its hull is a spare again.
func newSeedRoster(systems []ExpandSystem) *seedRoster {
	r := &seedRoster{crews: make(map[string]*seedCrew, len(systems))}
	for _, s := range systems {
		crew := &seedCrew{}
		if s.SeedShip != "" && activeSeedState(s.SeedState) {
			crew.primary = s.SeedShip
		}
		for _, extra := range s.ExtraSeeds {
			if extra.Ship != "" && activeSeedState(extra.State) {
				crew.extras = append(crew.extras, extra.Ship)
			}
		}
		if crew.primary == "" && len(crew.extras) == 0 {
			continue
		}
		r.crews[s.System] = crew
	}
	return r
}

// crew names a system's charting hulls in SYMBOL ORDER, which is what a hull's
// partition rank is read off. The order must not depend on how the ledger handed
// the rows back, or a rank swaps between ticks and two hulls trade tours.
func (r *seedRoster) crew(system string) []string {
	held, ok := r.crews[system]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(held.extras)+1)
	if held.primary != "" {
		out = append(out, held.primary)
	}
	out = append(out, held.extras...)
	sort.Strings(out)
	return out
}

func (r *seedRoster) size(system string) int {
	held, ok := r.crews[system]
	if !ok {
		return 0
	}
	if held.primary == "" {
		return len(held.extras)
	}
	return len(held.extras) + 1
}

// hulls indexes every hull on an errand anywhere, keyed on the HULL because that
// is the invariant being protected: a system may be re-targeted, but a probe
// cannot be in two places.
func (r *seedRoster) hulls() map[string]bool {
	out := make(map[string]bool, len(r.crews))
	for _, crew := range r.crews {
		if crew.primary != "" {
			out[crew.primary] = true
		}
		for _, extra := range crew.extras {
			out[extra] = true
		}
	}
	return out
}

// primaryFree reports whether a system's own seed columns are available for a new
// errand. They are the slot a new hull takes first, so a system with one hull is
// stored exactly as a single-hull tour always was.
func (r *seedRoster) primaryFree(system string) bool {
	held, ok := r.crews[system]
	return !ok || held.primary == ""
}

// join records a hull taking a place on a system's crew, and reports whether it
// took the primary slot.
func (r *seedRoster) join(system, ship string) bool {
	held, ok := r.crews[system]
	if !ok {
		held = &seedCrew{}
		r.crews[system] = held
	}
	if held.primary == "" {
		held.primary = ship
		return true
	}
	held.extras = append(held.extras, ship)
	return false
}

// leave records a hull off a system's crew, whichever slot held it.
func (r *seedRoster) leave(system, ship string) {
	held, ok := r.crews[system]
	if !ok {
		return
	}
	if held.primary == ship {
		held.primary = ""
	}
	for i, extra := range held.extras {
		if extra == ship {
			held.extras = append(held.extras[:i], held.extras[i+1:]...)
			break
		}
	}
	if held.primary == "" && len(held.extras) == 0 {
		delete(r.crews, system)
	}
}

// activeSeedState reports a seed state that still has a hull working under it.
func activeSeedState(state string) bool {
	return state == SeedStateDispatched || state == SeedStateCharting
}

// HullsOnChartingErrand names every hull the sensing ledger has out charting,
// crews included. It is the ONE definition of "this probe is already committed",
// exported so the coordinator's idle-hull passes cannot grow a second one that
// sees only a system's first seed.
func HullsOnChartingErrand(systems []ExpandSystem) map[string]bool {
	return newSeedRoster(systems).hulls()
}

// partitionOf is the share of a system's outstanding stops one hull owns, in the
// catalog's own visiting order.
//
// THE DIVISION IS BY ANGLE AROUND THE SYSTEM CENTRE, into as many equal sectors
// as the crew has hulls, and each hull takes the sector matching its rank. A
// sector is a property of the WAYPOINT ALONE — its coordinates and the crew size
// — so a stop never changes owner because its neighbours were charted. That is
// the property the whole scheme rests on: a boundary that moved as the set shrank
// would walk across the system every tick and hand two hulls the same waypoint in
// turn. Sectors can be uneven where a system's waypoints bunch, and that resolves
// itself: a hull with nothing left stands down, the crew shrinks, and the ones
// still working re-divide what remains.
//
// A ship not on the crew owns NOTHING. The roster is what grants a share, so a
// stale errand cannot fall through to the whole system by default.
//
// THE CATALOG ORDER IS PRESERVED WITHIN A SHARE, never re-sorted by geometry: a
// tour charts the head of its list, and that order is what puts a system's
// shipyard- and market-bearing waypoints ahead of its dead rock.
func partitionOf(stops []ChartStop, crew []string, ship string) []string {
	rank := -1
	for i, member := range crew {
		if member == ship {
			rank = i
			break
		}
	}
	if rank < 0 {
		return nil
	}
	out := make([]string, 0, len(stops))
	for _, stop := range stops {
		if sectorOf(stop, len(crew)) == rank {
			out = append(out, stop.Waypoint)
		}
	}
	return out
}

// sectorOf places one stop in one of k equal angular sectors around the system
// centre. Waypoint coordinates are system-relative, so the centre is the origin
// and no part of the answer depends on the rest of the set.
func sectorOf(stop ChartStop, k int) int {
	if k <= 1 {
		return 0
	}
	angle := math.Atan2(stop.Y, stop.X)
	if angle < 0 {
		angle += 2 * math.Pi
	}
	sector := int(angle / (2 * math.Pi / float64(k)))
	if sector >= k {
		// Only reachable on a float boundary at a full turn; clamping keeps the
		// sectors a total function of the plane rather than nearly one.
		return k - 1
	}
	return sector
}

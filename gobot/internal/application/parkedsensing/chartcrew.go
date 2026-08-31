package parkedsensing

import (
	"context"
	"math"
	"sort"
	"strings"
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
//
// The partition itself is SOLVED by the fleet-partitioning VRP and stored
// (chartshare.go); what lives here is the budget, the roster the partition is
// solved over, and the angular fallback that answers when the solver cannot.

// THE CREW IS SIZED ON THE MARGINAL HULL'S BREAK-EVEN, AND THE WALK IT IS PRICED
// AGAINST IS MEASURED THIS TICK. Charting a waypoint costs two seed steps, so a crew
// of c clears U outstanding in 2U/c; the c-th hull first pays two steps per gate hop
// plus the arrival sweep, 2*hops+1. It is worth adding while U/c >= hops + 0.5.
//
// hops IS THE INPUT THAT ANSWER IS MOST SENSITIVE TO AND THE ONE THAT MOVES MOST: a
// thirteen-hop walk justifies one hull on a twenty-waypoint system and a one-hop walk
// justifies nine, and the distance from what we hold to what is dark differs by that
// much between the start of an era and its end. Frozen as a constant it sizes today's
// crews on a map that no longer exists (sp-glyoe), so it is read per system per tick
// off the same gate walker claimSpares draws through.
const (
	// maxChartCrew is the widest crew the sizing can hand ONE system, and therefore the
	// largest chart_hull_cap worth advertising. It is a CEILING, not a derivation of the
	// assembly bound below: that bound rises with the outstanding count, so a system
	// whose whole catalog is dark clears it and only this holds the crew down.
	maxChartCrew = 15
	// The operator ladder's documented defaults, which are the break-even at the
	// SHORTEST walk stored adjacency can report — one gate hop. They therefore sit at
	// or below the measured answer at every rank and impose nothing until raised.
	defaultSecondChartHullAt = 3
	defaultThirdChartHullAt  = 4
	defaultChartHullTier     = 1
)

// chartWalk is the gate distance the next charting hull would fly to reach one dark
// system. UNMEASURED IS NOT ZERO: nothing we hold can reach the system, so there is
// no walk to price a second hull against and the system earns exactly one.
type chartWalk struct {
	hops     int
	measured bool
}

// chartWalks measures that distance, once per system per tick, over the tick's own
// gate walker — the memoised, store-only breadth-first search claimSpares and the
// target ordering already share, so no origin is walked twice.
//
// THE ORIGINS ARE THE SYSTEMS A SEED COULD SET OUT FROM, slotBook's own set and the
// one the distance ordering reads, so there is no second notion of where the fleet
// is. A hull reaches a target either as a spare already parked in one of them or as a
// probe bought at a yard in one of them, and both walk from there. IT IS OPTIMISTIC
// where a held system holds no yard that could sell a probe, and that optimism spends
// nothing: it can only leave a system on the target list, where the supply passes
// apply their own reach, staffing, stock and treasury tests first (RULINGS #4).
type chartWalks struct {
	reach   *gateReach
	origins []string
	memo    map[string]chartWalk
}

func newChartWalks(reach *gateReach, origins []string) *chartWalks {
	return &chartWalks{reach: reach, origins: origins, memo: map[string]chartWalk{}}
}

// forSystem is the shortest walk from anywhere we hold to this system.
func (w *chartWalks) forSystem(ctx context.Context, system string) (chartWalk, error) {
	if cached, ok := w.memo[system]; ok {
		return cached, nil
	}
	walk := chartWalk{}
	for _, origin := range w.origins {
		hops, within, err := w.reach.hops(ctx, origin, system)
		if err != nil {
			return chartWalk{}, err
		}
		if within && (!walk.measured || hops < walk.hops) {
			walk = chartWalk{hops: hops, measured: true}
		}
	}
	w.memo[system] = walk
	return walk, nil
}

// chartHulls is one tick's resolved hull budget for charting tours. Zero knobs
// mean the documented defaults, which is the `tune <key> 0` revert and also the
// no-config launch.
type chartHulls struct {
	cap    int
	second int
	third  int
	tier   int
}

// resolveChartHulls fills the documented default in for every knob left unset.
//
// A cap of one is a legitimate operator choice and the feature's kill switch, so it
// is honoured; only a non-positive cap reverts. A cap ABOVE maxChartCrew clamps down
// to it, so the tune bound's advertised range stays true through the paths that never
// see the bound — a stored value, and the boot config.
//
// THE TIER IS THE OPERATOR'S OWN STEP, not a fourth knob: hulls past the third are
// earned at the spacing between the second and the third, so retuning the pair moves
// the whole ladder. Thresholds out of order supply none and take the documented tier.
func resolveChartHulls(k ExpandKnobs) chartHulls {
	h := chartHulls{cap: k.ChartHullCap, second: k.SecondChartHullAt, third: k.ThirdChartHullAt}
	if h.cap <= 0 || h.cap > maxChartCrew {
		h.cap = maxChartCrew
	}
	if h.second <= 0 {
		h.second = defaultSecondChartHullAt
	}
	if h.third <= 0 {
		h.third = defaultThirdChartHullAt
	}
	if h.tier = h.third - h.second; h.tier <= 0 {
		h.tier = defaultChartHullTier
	}
	return h
}

// budgetFor is how many hulls a system with this much outstanding work may hold: the
// highest rank clearing all three tests below. Each of them only tightens as the rank
// rises, so the first failure ends the ladder and the budget stays monotonic in the
// outstanding count whatever the knobs say.
//
// AN UNSWEPT SYSTEM REPORTS ZERO and therefore earns one hull, which is correct
// rather than incidental: nobody has looked, so there is no size to scale on, and the
// first hull's catalog sweep is what produces one. No rank past the first clears
// paysItsWalk at zero outstanding, under any walk and any configuration.
func (h chartHulls) budgetFor(uncharted int, walk chartWalk) int {
	if !walk.measured {
		return 1
	}
	budget := 1
	for rank := 2; rank <= h.cap; rank++ {
		if !paysItsWalk(uncharted, rank, walk.hops) ||
			!arrivesToWork(uncharted, rank) ||
			uncharted < h.floorFor(rank) {
			break
		}
		budget = rank
	}
	return budget
}

// paysItsWalk is the break-even itself, in whole steps so nothing rounds: the share
// this rank takes off the tour, 2*(U/c), against the walk it adds, 2*hops+1.
func paysItsWalk(uncharted, rank, hops int) bool {
	return 2*uncharted >= rank*(2*hops+1)
}

// arrivesToWork is the ASSEMBLY BOUND, and it is what stops a short walk buying an
// arbitrarily large crew. claimSpares grants one hull per system per tick and
// advanceSeeds moves each of them one step per tick, so rank c is granted around the
// c-th tick, by which time the ranks before it have taken c*(c-1)/2 of the system's 2U
// steps; past that it is granted to a system whose work is gone. Charged against the
// WALKLESS rate no real crew reaches, so it bounds the crew rather than forecasting it.
func arrivesToWork(uncharted, rank int) bool {
	return rank*(rank-1)/2 < 2*uncharted
}

// floorFor is the OPERATOR'S BRAKE: the outstanding work a system must hold before
// this rank, on top of what the walk already earns. The second and third are named
// directly and their spacing carries every rank past them, so retuning the pair moves
// the whole ladder rather than leaving a hidden constant to disagree with it.
func (h chartHulls) floorFor(rank int) int {
	if rank <= 2 {
		return h.second
	}
	return h.third + (rank-3)*h.tier
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

// crewKey names a crew as one value, so a stored share can say which membership
// it was solved for. Symbol order, matching seedRoster.crew, so the key is a
// property of WHO is aboard and not of the order the ledger handed the rows back.
func crewKey(crew []string) string {
	ordered := append([]string(nil), crew...)
	sort.Strings(ordered)
	return strings.Join(ordered, "|")
}

// partitionBySector is the DECLARED FALLBACK, reached only when the fleet
// partitioner cannot answer (see solveShares). It divides the outstanding stops by
// ANGLE AROUND THE SYSTEM CENTRE into as many equal sectors as the crew has hulls,
// each hull taking the sector matching its rank.
//
// It stands here because charting must never stall on the routing service: a
// sector needs nothing but a waypoint's own coordinates and the crew size, so it
// is answerable with every collaborator down. It buys that with a worse partition
// — sectors are uneven wherever a system's waypoints bunch — which is why it is
// the fallback and not the rule.
func partitionBySector(stops []ChartStop, crew []string) map[string][]string {
	byShip := make(map[string][]string, len(crew))
	for _, ship := range crew {
		byShip[ship] = partitionOf(stops, crew, ship)
	}
	return byShip
}

// partitionOf is the sector share one hull owns, in the catalog's own visiting
// order — which is the tier order, so the value-bearing stops of a share stay
// ahead of its dead rock.
//
// A ship not on the crew owns NOTHING. The roster is what grants a share, so a
// stale errand cannot fall through to the whole system by default.
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

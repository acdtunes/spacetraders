package parkedsensing

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// SeedFlightUnbounded means a charting seed's reach is bounded by THE GRAPH, not by a number: the
// breadth-first search walks until the traversable component is exhausted, so a destination in
// another component is simply never found. Charting an unmapped gate is precisely the act that
// reveals systems beyond it, so a fixed ring bound re-tunes itself into staleness by succeeding.
//
// SELECTION AND THE ROUTER READ THE SAME RULE, which is what makes this safe rather than merely
// permissive: a destination past the ROUTER's reach fails silently — nextHopToward names no next
// system, the step errors, and the slot stays IN_TRANSIT naming a hull that counts against the probe
// cap and never arrives. The adapter's resolver therefore takes its bound from this same declaration
// (see adapters/parkedsensing), so selection can never outrun delivery.
const SeedFlightUnbounded = 0

// gateReach answers "how many gate hops is it from here to there?", bounded by
// the caller's own maxHops, from STORED adjacency alone.
//
// Gate connectivity is sparse, so requiring DIRECT adjacency at both ends
// exhausts the frontier almost immediately. THE BOUND IS THE WALK'S, NOT A
// PREFERENCE: a target further out is UNROUTABLE rather than merely expensive,
// because the adapter's next-hop search gives up at the same ring — every step of
// the errand fails, the hull holds probe-cap headroom, and it charts nothing.
// Reading the bound from the same declaration the walk reads keeps the two from
// drifting into handing out that stall.
//
// THE SEARCH IS FORWARD, AND THAT IS A CORRECTNESS PROPERTY: the stored graph is
// genuinely asymmetric, a gate charted from one end naming a system whose own gate
// we have not charted, so a search assuming symmetry would report routes the walk
// cannot resolve and strand a probe on each. Physical gates are two-way; our
// KNOWLEDGE of them is not, and this reads the knowledge.
//
// PURE STORE READS: Neighbours is a store read by contract, never a fetch-through
// resolver, so widening reach spends no API budget, and each origin is walked at
// most once per tick and memoised.
type gateReach struct {
	// maxHops is THIS walker's reach, per-instance rather than a package constant
	// because the engines that walk this graph ask different questions: how far a
	// CHARTING SEED may be flown (SeedFlightUnbounded), how far a surplus SCANNING
	// HULL may be drawn to fill a placement (MaxWalkRings), how far a BOUGHT hull
	// may be ferried (maxFerryHops). One shared number would mean widening the
	// seed's reach silently lengthened every placement draw too.
	maxHops int
	// gates is the gate-adjacency STORE read, narrowed so this walker can serve any
	// caller that has one without a second traversal of the same graph.
	gates GateNeighbours
	// known is the tick's neighbour map, already read by readNeighbours. It covers
	// every system in the ledger, nearly everything the search touches.
	known map[string][]string
	// fetched memoises the systems `known` does not cover. It is kept SEPARATE
	// rather than written back into the tick's map because markFrontier iterates
	// that map to decide what to record as PENDING, and quietly growing it here
	// would change which systems this tick claims to have discovered.
	fetched map[string][]string
	// hopsFrom memoises one BFS per origin: system -> hops, for the systems within
	// maxHops. The origin itself is absent, so any entry present is both reachable
	// AND at least one hop away.
	hopsFrom map[string]map[string]int
}

func newGateReach(gates GateNeighbours, neighbours map[string][]string, maxHops int) *gateReach {
	return &gateReach{
		maxHops:  maxHops,
		gates:    gates,
		known:    neighbours,
		fetched:  map[string][]string{},
		hopsFrom: map[string]map[string]int{},
	}
}

// origins lists the systems the tick may propagate from, in symbol order — the
// same set and the same order readNeighbours built.
func (r *gateReach) origins() []string { return sortedKeys(r.known) }

// adjacent reads one system's gate neighbours, from the tick's map where it can
// and the gate store where it cannot. The fallback is load-bearing rather than
// defensive: a two-hop search passes THROUGH intermediate systems, and an
// intermediate only enters the ledger when this tick's own markFrontier records it
// — after the neighbour map was built — so without it the second ring would be
// empty on exactly the ticks that first open a new neighbourhood.
func (r *gateReach) adjacent(ctx context.Context, system string) ([]string, error) {
	if known, ok := r.known[system]; ok {
		return known, nil
	}
	if cached, ok := r.fetched[system]; ok {
		return cached, nil
	}
	adjacent, err := r.gates.Neighbours(ctx, system)
	if err != nil {
		return nil, fmt.Errorf("failed to read gate neighbours of %q: %w", system, err)
	}
	r.fetched[system] = adjacent
	return adjacent, nil
}

// from returns every system reachable from origin within this walker's maxHops,
// mapped to the number of hops it takes. Breadth-first, so the recorded hop count
// is the SHORTEST one — which is what makes "prefer the nearer" mean anything.
func (r *gateReach) from(ctx context.Context, origin string) (map[string]int, error) {
	if cached, ok := r.hopsFrom[origin]; ok {
		return cached, nil
	}
	hops := map[string]int{}
	seen := map[string]bool{origin: true}
	frontier := []string{origin}

	// A NON-POSITIVE maxHops means "until the component is exhausted": the frontier
	// empties on its own when there is nowhere further to go.
	for ring := 1; (r.maxHops <= 0 || ring <= r.maxHops) && len(frontier) > 0; ring++ {
		var next []string
		for _, system := range frontier {
			adjacent, err := r.adjacent(ctx, system)
			if err != nil {
				// Propagated, never swallowed: an empty reach read permissively
				// is indistinguishable from a genuinely isolated system, and this
				// read decides whether a hull is dispatched at all.
				return nil, err
			}
			for _, neighbour := range adjacent {
				if neighbour == "" || seen[neighbour] {
					continue
				}
				seen[neighbour] = true
				hops[neighbour] = ring
				next = append(next, neighbour)
			}
		}
		sort.Strings(next) // deterministic within a ring
		frontier = next
	}
	r.hopsFrom[origin] = hops
	return hops, nil
}

// hops reports how far target is from origin, and whether it is within reach at
// all. A system that is not reachable — or IS origin — reports false.
func (r *gateReach) hops(ctx context.Context, origin, target string) (int, bool, error) {
	reachable, err := r.from(ctx, origin)
	if err != nil {
		return 0, false, err
	}
	distance, within := reachable[target]
	return distance, within, nil
}

// canReach reports whether a hull in origin could be walked to target.
func (r *gateReach) canReach(ctx context.Context, origin, target string) (bool, error) {
	_, within, err := r.hops(ctx, origin, target)
	return within, err
}

// beyondReach sorts after every reachable distance for THIS walker, so an
// unreachable target keeps its place at the back of the queue rather than jumping
// it. Derived from the walker's own bound for the same reason maxHops is.
func (r *gateReach) beyondReach() int {
	if r.maxHops <= 0 {
		// Unbounded: no real hop count can reach this, and it is far below overflow.
		return math.MaxInt32
	}
	return r.maxHops + 1
}

// orderByDistance puts the CHEAP frontier first: targets nearest the systems we
// actually hold, since a one-hop errand is one flight and a two-hop errand two,
// and the probe is held for the whole of it.
//
// IT IS A RE-SORT, NOT A REPLACEMENT: seedlessTargets' deepest-dark-first order is
// preserved WITHIN each ring, so distance only outranks it across rings, where the
// comparison is between prices rather than prizes. Distance is measured from the
// waypoints where a hull of ours is standing, the same index staffedAt reads; with
// nothing held the order is left exactly as it was.
func (r *gateReach) orderByDistance(ctx context.Context, targets []ExpandSystem, book *slotBook) ([]ExpandSystem, error) {
	held := book.heldSystems()
	if len(held) == 0 || len(targets) < 2 {
		return targets, nil
	}

	distance := make(map[string]int, len(targets))
	for _, target := range targets {
		nearest := r.beyondReach()
		for _, origin := range held {
			hops, within, err := r.hops(ctx, origin, target.System)
			if err != nil {
				return nil, err
			}
			if within && hops < nearest {
				nearest = hops
			}
		}
		distance[target.System] = nearest
	}

	ordered := append([]ExpandSystem(nil), targets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return distance[ordered[i].System] < distance[ordered[j].System]
	})
	return ordered, nil
}

// orderUnmappedFirst puts the targets whose gate adjacency we have NEVER READ at the front.
//
// IT IS THE ONLY PROPERTY THAT GROWS THE LEDGER. Charting a system whose gate we already hold rows
// for fills in its markets — real income — but the map stays the size it was. A system with no rows
// is the only kind that can name somewhere we do not hold, and markFrontier turns exactly those
// names into the PENDING rows the seed machinery works through; without this weight orderByDistance's
// hop count buries a deep unmapped-gate target behind every nearer mapped one.
//
// A WEIGHT, NOT A FILTER: it reorders and never truncates, so with no unmapped-gate target in play
// the queue is exactly what orderByDistance produced, and a filter would trade the income side away
// for the growth side. STABLE, so everything already decided survives inside each tier — distance,
// then the deepest dark, then symbol.
//
// A READ FAILURE FAILS THE TICK rather than defaulting either way: read as "mapped" it would demote
// genuine frontier territory and the fleet would quietly stop growing; read as "unmapped" it would
// promote every ordinary target at once. The tick is idempotent, so failing loudly costs one cycle.
func (m *gateMapping) orderUnmappedFirst(ctx context.Context, targets []ExpandSystem) ([]ExpandSystem, error) {
	if len(targets) < 2 {
		return targets, nil
	}
	// RESOLVED ONCE PER TARGET, UP FRONT, and never from inside the comparator: it is a pure store
	// read, but sort calls its less function O(n log n) times, which would turn a linear pass into a
	// superlinear one that grows with the frontier exactly as the frontier succeeds. THROUGH THE
	// TICK'S SHARED MEMO rather than straight at the port, because the gate-read pass asks the
	// identical question of the identical store earlier in the same tick — two independent reads could
	// observe different answers within one tick, and the gate-read pass is the one that CHANGES it.
	unmapped := make(map[string]bool, len(targets))
	for _, target := range targets {
		mapped, err := m.mapped(ctx, target.System)
		if err != nil {
			return nil, err
		}
		unmapped[target.System] = !mapped
	}

	ordered := append([]ExpandSystem(nil), targets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return unmapped[ordered[i].System] && !unmapped[ordered[j].System]
	})
	return ordered, nil
}

// originsWithin filters candidates down to the systems a hull could be flown FROM
// to arrive at target, NEAREST RING FIRST and in symbol order inside each ring.
//
// THE DIRECTION IS THE POINT, and it is why every candidate is tested with its
// OWN forward walk rather than one walk out of the target. Those agree only on a
// symmetric graph, and the gate map is not one: walking forward out of the target
// answers "where could a hull AT the target go", which for a one-way edge into the
// target is the opposite question, and a hull staged on that answer sits IN_TRANSIT
// forever on a route nextHopToward cannot resolve.
//
// CALLERS SUPPLY THEIR OWN CANDIDATE SET, which keeps the cost proportional to the
// work: seed staging passes the tick's neighbour map, the foothold path only the
// systems actually holding a surplus hull. Symbol order is the tie-break, so two
// origins the same distance out are chosen between reproducibly.
func (r *gateReach) originsWithin(ctx context.Context, candidates []string, target string) ([]string, error) {
	type candidate struct {
		system string
		hops   int
	}
	var found []candidate
	for _, origin := range candidates {
		hops, within, err := r.hops(ctx, origin, target)
		if err != nil {
			return nil, err
		}
		if within {
			found = append(found, candidate{origin, hops})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].hops < found[j].hops })

	out := make([]string, 0, len(found))
	for _, c := range found {
		out = append(out, c.system)
	}
	return out, nil
}

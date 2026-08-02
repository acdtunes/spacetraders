package parkedsensing

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// SeedFlightUnbounded means a charting seed's reach is bounded by THE GRAPH, not by a number: the
// search walks until the traversable component is exhausted.
//
// WHY THERE IS NO NUMBER. A ring bound of 9 was honestly derived — the traversable graph saturated
// there, so a bound of 10 served no additional target. It went stale in
// under a day. X1-TD22 was discovered at TEN hops, reachable only through X1-KP42 at nine, and it is
// the last system in the fleet with an unmapped jump gate — the only one whose charting can add
// systems at all. A bound tuned to today's furthest system is guaranteed to be wrong tomorrow,
// because charting an unmapped gate is precisely the act that reveals systems beyond it. The bound
// was re-tuning itself into staleness by succeeding.
//
// SO THE BOUND IS THE COMPONENT, AND IT IS SELF-LIMITING. A breadth-first search over stored
// adjacency terminates when it runs out of frontier — a destination in another component is simply
// never found, which is the same refusal the ring bound gave, reached by the graph's own shape
// instead of a guess. It cannot go stale because there is nothing to tune: the answer is "can a seed
// actually get there", which is the question that always mattered.
//
// IT STAYS CHEAP AS THE MAP GROWS because it is bounded by OUR OWN LEDGER, not the universe. The
// walk visits each known system at most once — 57 today, 300 at the owner's target — reading a map
// the tick has already built, with a memoised store fallback for the few systems it does not cover.
// That is linear in the map we hold and it does not grow with anything we have not charted.
//
// WHAT IT COSTS IS TICKS, NOT CREDITS. A gate jump burns no fuel; a crossing is two dispatch steps,
// so TD22 at ten hops is roughly twenty ticks of transit. The honest ceiling is the graph's
// eccentricity from our staffed systems — 10 today, measured — and it grows only as the frontier
// does. That is inherent in charting a distant system at all, not a cost this choice introduces.
//
// SELECTION AND THE ROUTER READ THE SAME RULE, which is the invariant that makes this safe rather
// than merely permissive. A destination past the ROUTER's reach fails silently: nextHopToward names
// no next system, the step errors, and the slot stays IN_TRANSIT still naming a hull that counts
// against the probe cap and never arrives. The adapter's resolver therefore takes its bound from
// this same declaration (see adapters/parkedsensing), so selection can never outrun delivery — and
// with both unbounded-within-the-component, "reachable" means the same thing on both sides by
// construction rather than by two numbers agreeing.
const SeedFlightUnbounded = 0

// gateReach answers "how many gate hops is it from here to there?", bounded by
// the caller's own maxHops, from STORED adjacency alone.
//
// WHY IT EXISTS. Requiring DIRECT adjacency at both gates — staging only at a
// yard in a system BORDERING the target, claiming only a spare parked in one —
// exhausts the frontier almost immediately, because gate connectivity is
// sparse: measured on the
// live fleet, 33 unseeded systems carried uncharted waypoints and exactly ONE
// was a direct neighbour of a system we occupied. Seven are within MaxWalkRings.
// No amount of money, hulls or per-tick budget buys another ring.
//
// THE BOUND IS THE WALK'S, NOT A PREFERENCE. A seed further out than
// MaxWalkRings is not merely expensive, it is UNROUTABLE: the adapter's
// next-hop search gives up at the same ring, so the errand's every step fails,
// the hull holds probe-cap headroom, and it charts nothing — strictly worse
// than never dispatching it. Reading the bound from the same declaration the
// walk reads is what keeps the two from drifting into handing out that stall.
//
// FORWARD, AND THAT IS A CORRECTNESS PROPERTY RATHER THAN A DETAIL. The search
// follows Neighbours(x) in the same direction the walk traverses it. The stored
// graph is genuinely asymmetric — measured live, 617 of 5463 edges have no
// reverse row, because a gate charted from one end names a system whose own gate
// we have not charted yet — so a search that assumed symmetry would report
// routes the walk cannot resolve, and every one of them would strand a probe.
// Physical gates are two-way; our KNOWLEDGE of them is not, and this reads the
// knowledge.
//
// PURE STORE READS, and no more of them than the tick already makes. Neighbours
// is a store read by contract, never a fetch-through resolver, so widening reach
// spends no API budget at all. Each origin is walked at most once per tick and
// memoised, so a second target in the same neighbourhood is free — which is what
// keeps the cost from growing with the frontier exactly as the frontier
// succeeds.
type gateReach struct {
	// maxHops is THIS walker's reach, per-instance rather than a package constant
	// because the engines that walk this graph ask different questions: seed
	// staging "how far may a CHARTING SEED be flown" (SeedFlightUnbounded), the
	// foothold pass "how far may a surplus SCANNING HULL be drawn to fill a
	// placement" (MaxWalkRings), the ferry "how far may a BOUGHT hull be flown to
	// its placement" (maxFerryHops). Sharing one number would mean widening the
	// seed's reach silently lengthened every placement draw too.
	maxHops int
	// gates is the gate-adjacency STORE read, narrowed so this walker can serve
	// any caller that has one — the buy queue's foothold path reaches for it
	// through BuyPorts.Gates. Narrowing is what makes reuse possible without a
	// second traversal of the same graph.
	gates GateNeighbours
	// known is the tick's neighbour map, already read by readNeighbours. It
	// covers every system in the ledger, which is nearly everything the search
	// touches.
	known map[string][]string
	// fetched memoises the systems `known` does not cover. It is kept SEPARATE
	// rather than written back into the tick's map because markFrontier iterates
	// that map to decide what to record as PENDING, and quietly growing it here
	// would change which systems this tick claims to have discovered.
	fetched map[string][]string
	// hopsFrom memoises one BFS per origin: system -> hops, for the systems
	// within maxHops. The origin itself is absent, so any entry present is
	// both reachable AND at least one hop away.
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
// and the gate store where it cannot.
//
// The fallback is load-bearing rather than defensive: a two-hop search passes
// THROUGH intermediate systems, and an intermediate only entered the ledger when
// this tick's own markFrontier recorded it — after the neighbour map was built.
// Without the fallback the second ring would be empty on exactly the ticks that
// first open a new neighbourhood.
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
	// empties on its own when there is nowhere further to go, which is the same
	// refusal a ring bound gave, reached from the graph rather than from a guess.
	for ring := 1; (r.maxHops <= 0 || ring <= r.maxHops) && len(frontier) > 0; ring++ {
		var next []string
		for _, system := range frontier {
			adjacent, err := r.adjacent(ctx, system)
			if err != nil {
				// Propagated, never swallowed. An empty reach read permissively
				// is indistinguishable from a genuinely isolated system, and
				// this is the read that decides whether a hull is dispatched at
				// all. The tick is idempotent, so failing loudly costs a cycle.
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
// unreachable target keeps its place at the back of the queue rather than
// jumping it. Derived from the walker's own bound, not a package constant, for
// the same reason maxHops is.
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
// IT IS A RE-SORT, NOT A REPLACEMENT. seedlessTargets' deepest-dark-first order
// is preserved WITHIN each ring, so the rule it encodes — resolve the biggest
// known unknown soonest — still decides between targets that cost the same to
// reach. Distance only outranks it across rings, where the comparison is
// genuinely between different prices rather than different prizes.
//
// Distance is measured from the systems a seed could actually set out from: the
// waypoints where a hull of ours is standing. That is the same index staffedAt
// reads, so "the frontier we hold" means one thing here and at the yard. With
// nothing held — a cold fleet — every target measures the same and the order is
// left exactly as it was.
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
// for fills in its markets — real income, and worth doing — but the map stays the size it was: its
// neighbours are already recorded and already chased. A system with no rows is the only kind that
// can name somewhere we do not hold, and markFrontier turns exactly those names into the PENDING
// rows the seed machinery works through.
//
// MEASURED LIVE: of 21 unseeded targets, 2 had an unmapped gate and they ranked TWENTIETH and
// TWENTY-FIRST. The mechanism was distance — orderByDistance makes hop count the primary key and both
// sat nine hops out — so the two systems that could actually extend the map sorted behind every
// nearer one and never got a seed. One of them carried the second-deepest uncharted count in the set
// and it still came last.
//
// A WEIGHT, NOT A FILTER, and that distinction is load-bearing rather than stylistic. This reorders
// and never truncates: with no unmapped-gate target in play the queue is exactly the order
// orderByDistance produced, and once the unmapped ones are covered the mapped ones are served
// unchanged. A filter would trade the income side away for the growth side; the fleet needs both.
//
// STABLE, so everything already decided survives inside each tier: distance first, then the deepest
// dark, then symbol. A deep unmapped-gate target still beats a shallow one.
//
// A READ FAILURE FAILS THE TICK rather than defaulting either way. Read as "mapped" it would demote
// genuine frontier territory to the back of the queue and the fleet would quietly stop growing; read
// as "unmapped" it would promote every ordinary target at once. The tick is idempotent and
// re-derived from scratch, so failing loudly costs one cycle.
func (m *gateMapping) orderUnmappedFirst(ctx context.Context, targets []ExpandSystem) ([]ExpandSystem, error) {
	if len(targets) < 2 {
		return targets, nil
	}
	// RESOLVED ONCE PER TARGET, UP FRONT, and never from inside the comparator. It is a pure store
	// read, but sort calls its less function O(n log n) times — asking the store there would turn a
	// linear pass into a superlinear one that grows with the frontier exactly as the frontier
	// succeeds. (A same-system short-circuit lived here briefly; it was removed because targets are
	// distinct systems by construction, so it could never fire — dead code with a plausible-sounding
	// rationale is worse than none.)
	//
	// THROUGH THE TICK'S SHARED MEMO rather than straight at the port, because the gate-read pass asks
	// the identical question of the identical store earlier in the same tick. Two consumers reading it
	// twice would not merely double a per-system store read; it would let them observe different
	// answers within one tick, and the pass that reads gates is the one that CHANGES the answer.
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

// originsWithin filters candidates down to the systems a hull could be
// flown FROM to arrive at target, NEAREST RING FIRST and in symbol order inside
// each ring.
//
// THE DIRECTION IS THE POINT, and it is why every candidate is tested with its
// OWN forward walk rather than one walk out of the target. Those two agree only
// on a symmetric graph, and the gate map is not one — measured live, 624 of 5,488
// edges (11.4%) have no reverse row. Walking forward out of the target answers
// "where could a hull AT the target go", which for a one-way edge into the target
// is the exact opposite of the question, and a hull staged on that answer is
// dispatched onto a route nextHopToward cannot resolve: it sits IN_TRANSIT
// forever, holding probe-cap headroom and doing no work.
//
// Testing each candidate forward — the same direction the placement machine will
// actually traverse — means a system is offered only if the walk it will really
// fly exists. That is the discipline sp-9fdc258d established for the seed reach,
// applied to the same graph by the same walker.
//
// CALLERS SUPPLY THEIR OWN CANDIDATE SET, which keeps the cost proportional to
// the work: seed staging passes the tick's neighbour map (every system whose
// adjacency we have measured), while the foothold path passes only the systems
// actually holding a surplus hull. Symbol order is kept as the tie-break so two
// origins the same distance out are chosen between reproducibly, tick after tick.
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

// reachesAny reports whether ANY outstanding charting target is within MaxWalkRings of a
// system we hold — the discriminator that keeps warp a fallback.
//
// It reuses the tick's existing gateReach memo and heldSystems index, so it costs no store read the
// tick was not already making, and it can never disagree with the reach the seed machinery itself
// applies: the same forward BFS, the same bound, the same origins.
func (r *gateReach) reachesAny(ctx context.Context, targets []ExpandSystem, book *slotBook) (bool, error) {
	held := book.heldSystems()
	for _, target := range targets {
		for _, origin := range held {
			within, err := r.canReach(ctx, origin, target.System)
			if err != nil {
				return false, err
			}
			if within {
				return true, nil
			}
		}
	}
	return false, nil
}

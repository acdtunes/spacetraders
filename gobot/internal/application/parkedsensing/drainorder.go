package parkedsensing

import (
	"context"
	"fmt"
	"sort"
)

// drainCandidates returns the placements to work this tick, in priority order:
// every IN_SCOPE fill sorted by COVERAGE ascending with depth as the tiebreak,
// then the seeds.
//
// COVERAGE FIRST, BECAUSE A SMALL BUDGET IS SPENT ON THE HEAD OF THIS LIST: depth
// alone lets the richest system's placements occupy the whole head of every tick,
// so a poorer system never gets a turn. Depth remains the tiebreak, so once
// coverage is even this degenerates to depth order exactly.
//
// EVERY SLOT CARRIES ITS OWN COVERAGE — the i-th outstanding placement of a system
// ranks at parked + i, so a second system on 0 outranks the first system's second
// slot. Ranking purely on probes already parked would tie a 0-probe system's whole
// backlog at rank 0 and hand it the entire tick. The index is taken walking the
// list in the order the LEDGER returned it, BEFORE any sort, because that order is
// FIFO per system and the sort below is stable — which keeps a placement from
// being overtaken by a newer one in its own system, tick after tick. The hulls a
// system already holds come from a separate narrow read; see coverageBySystem.
//
// QUEUED slots are drained alongside WANTED ones, or a claim whose purchase failed
// would be a one-way door and the placement would never be retried. A QUEUED slot
// is a candidate AND consumes a coverage index: it still needs working, and a
// claim already made is a probe already spoken for.
//
// DARK SHIPYARDS ARE A TIER ABOVE COVERAGE, NOT A TERM INSIDE IT (Admiral
// directive: all of a system's yards are manned ahead of ordinary markets). Every
// placement standing on a counter that sells a hull we buy at a price we cannot
// see sorts ahead of EVERY ordinary market, whatever either one's coverage;
// coverage-first orders within each tier, and all of a system's dark yards are
// promoted rather than only its best one (see yardqueue.go). The yardOrder built
// here is returned so the loop below can report what the ordering did.
func drainCandidates(ctx context.Context, p BuyPorts, playerID int) ([]QueuedSlot, yardOrder, error) {
	slots, err := p.Ledger.SlotsByState(ctx, playerID, SlotStateWanted, SlotStateQueued)
	if err != nil {
		return nil, yardOrder{}, fmt.Errorf("failed to list unfilled sensing slots: %w", err)
	}
	if len(slots) == 0 {
		return nil, yardOrder{}, nil
	}

	systems, err := p.Ledger.SystemsByVerdict(ctx, playerID, VerdictInScope)
	if err != nil {
		return nil, yardOrder{}, fmt.Errorf("failed to list in-scope sensing systems: %w", err)
	}
	depth, inScope := indexScreenedSystems(systems)
	fills, seeds := partitionFillsAndSeeds(slots, inScope)

	if len(fills) == 0 {
		return seeds, yardOrder{}, nil
	}
	fills = reachableFills(ctx, p, playerID, fills)
	if len(fills) == 0 {
		return seeds, yardOrder{}, nil
	}
	covered, err := coverageBySystem(ctx, p, playerID)
	if err != nil {
		return nil, yardOrder{}, err
	}

	// AFTER the reachability partition, and that order is load-bearing: a yard in
	// a system no hull can walk to is still a dark yard, so promoting it before
	// partitioning would put an impossible placement at the head of the queue.
	// Feasibility first, priority second. It is also the cheapest place to ask —
	// a tick with nothing to buy never reaches here.
	yards := readYardDemand(ctx, p, playerID)

	// Each fill's effective coverage. The per-system offset is FIFO except that
	// dark yards take the low positions — see yardFirstOffsets.
	offsets := yardFirstOffsets(fills, yards)
	ranked := make([]rankedFill, len(fills))
	for i, fill := range fills {
		ranked[i] = rankedFill{slot: fill, coverage: covered[fill.System] + offsets[i]}
	}

	// Stable, so the ledger's own waypoint ordering survives as the last
	// tiebreak — which makes the queue FIFO per system and its output
	// reproducible tick to tick.
	sort.SliceStable(ranked, func(i, j int) bool {
		return fillOutranks(ranked[i], ranked[j], yards, depth)
	})

	out := make([]QueuedSlot, 0, len(ranked)+len(seeds))
	for position, r := range ranked {
		out = append(out, r.slot)
		if !yards.wants(r.slot.Waypoint) {
			continue
		}
		yards.queued++
		if position < maxDrainAttempts {
			yards.atHead++
		}
	}
	return append(out, seeds...), yards, nil
}

func indexScreenedSystems(systems []ScreenedSystem) (map[string]int64, map[string]bool) {
	depth := make(map[string]int64, len(systems))
	inScope := make(map[string]bool, len(systems))
	for _, s := range systems {
		depth[s.System] = s.DepthCredits
		inScope[s.System] = true
	}
	return depth, inScope
}

// partitionFillsAndSeeds splits the unfilled placements into the two queues the
// drain serves in order. A MARKET/YARD placement in a system NOT judged IN_SCOPE is
// dropped by both: an unjustified placement is the one buy this queue must not make.
func partitionFillsAndSeeds(slots []QueuedSlot, inScope map[string]bool) (fills, seeds []QueuedSlot) {
	for _, slot := range slots {
		switch {
		case slot.Kind == SlotKindSpare:
			// A spare is a seed: it goes wherever the frontier needs eyes, and
			// its system carries no verdict yet, so it is never scope-filtered.
			seeds = append(seeds, slot)
		case inScope[slot.System]:
			fills = append(fills, slot)
		}
	}
	return fills, seeds
}

// fillOutranks is the queue's whole priority order: the dark-yard tier, then
// coverage, then the yard demand key, then system depth.
//
// THE TIER IS ABOVE COVERAGE, not a tiebreak inside it. A dark yard outranks every
// ordinary market in the queue, including one in a system holding no hull at all.
// A tiebreak inside coverage never fires between two different coverage values, so
// a yard in a system already holding three probes would lose to every coverage-0
// market row elsewhere.
//
// THE COST IS REAL AND ACCEPTED: placement capacity goes to yards until yards are
// exhausted, and market coverage grows only afterwards. A yard is what makes every
// future hull purchase cheap, while market coverage is already oversupplied and
// what binds trading is sink DEPTH rather than market count.
//
// IT DOES NOT RE-CONCENTRATE THE FLEET, and that is a property of the coverage
// ordering surviving INSIDE the tier rather than an assumption about how yards
// happen to be spread: a system's dark yards take its coverage indices 0,1,2,…
// (yardFirstOffsets), so its second yard ranks behind every other system's first
// and no system can take more than one place in any coverage band.
func fillOutranks(a, b rankedFill, yards yardOrder, depth map[string]int64) bool {
	ya, yb := yards.wants(a.slot.Waypoint), yards.wants(b.slot.Waypoint)
	if ya != yb {
		return ya
	}
	if a.coverage != b.coverage {
		return a.coverage < b.coverage
	}
	// The demand weighting, INSIDE the yard tier and above depth. Both keys are
	// notAYard in the market tier, so this term is dead there and ordinary
	// markets are separated by depth alone. In the yard tier it is where every
	// uncovered system's first yard meets at the same coverage, so without it a
	// dark heavy counter would be separated from a probe-only one by which system
	// happens to be richer. The key is the budget's own RankPresence position, so
	// heavy counters lead unconditionally and no second ranking is invented here.
	ka, kb := yards.key(a.slot.Waypoint), yards.key(b.slot.Waypoint)
	if ka != kb {
		return ka < kb
	}
	return depth[a.slot.System] > depth[b.slot.System]
}

// rankedFill is one fill beside the coverage it competes at — the count of hulls
// its system already holds, plus its own position among that system's outstanding
// placements.
type rankedFill struct {
	slot     QueuedSlot
	coverage int
}

// reachableFills drops the fills whose system no hull can currently walk to.
//
// Without it the coverage ordering promotes exactly the impossible entries. Fills rank
// coverage-ascending, and an unreachable system's coverage is EXACTLY ZERO — no probe has ever
// arrived there and none ever will — so an unreachable placement sorts to the very head of the
// queue, ahead of every reachable one, buying a hull that then sits in BOUGHT or IN_TRANSIT forever,
// consuming a rotation slot and a refusal every tick. An ordering that does not first partition by
// FEASIBILITY promotes the impossible.
//
// FAILS OPEN, ALWAYS. An unwired gate port or an unreadable graph means "do not filter", never
// "reject everything" — the filter exists to stop waste, not to gate spending, so a transient read
// fault must never be able to stop the fleet buying. Note the asymmetry with the money guards
// around it: those fail CLOSED because the risk is spending wrongly, while this one's failure mode
// is refusing to spend at all.
//
// NOTHING IS PERSISTED, and that is not an implementation detail (RULINGS #2). Reachability is
// TIME-VARYING — a system is unreachable only until the next graph refresh or the next gate
// completion — so a stored "unreachable" flag would outlive the staleness that produced it and
// blacklist the system permanently. The walk is re-derived every drain and thrown away.
func reachableFills(ctx context.Context, p BuyPorts, playerID int, fills []QueuedSlot) []QueuedSlot {
	graph, reachable, ok := reachableSystems(ctx, p, playerID)
	if !ok {
		return fills // unknowable this tick — filter nothing (see FAILS OPEN above)
	}
	kept := make([]QueuedSlot, 0, len(fills))
	for _, fill := range fills {
		// REJECT ONLY ON POSITIVE EVIDENCE: we have READ this system's gates, and none of them
		// connect to anywhere a hull of ours stands. A system we have never charted is UNKNOWN,
		// not impossible, and treating the two alike is what turns this filter into a
		// cold-start deadlock — a fresh era, a failed crawl or a walled graph all make almost
		// everything unreachable, and a filter that cannot tell that from a genuinely stranded
		// fleet would stop the drain buying anything at all.
		if graph.Mapped[fill.System] && !reachable[fill.System] {
			continue
		}
		kept = append(kept, fill)
	}
	return kept
}

// reachableSystems walks the fleet's own effective gate graph ONCE and returns every system a hull
// could currently reach. ok=false means the question could not be answered at all.
//
// THE SEED IS PARKED SLOTS ONLY, and the exclusion is load-bearing. coverageBySystem also tallies
// "systems the ledger says we hold", but it counts BOUGHT and IN_TRANSIT alongside PARKED — and
// seeding from those would let a hull already flying toward an unreachable system mark that system
// reachable FROM ITSELF, re-admitting exactly the slots this filter exists to reject. Only a PARKED
// slot is evidence that a hull actually STANDS somewhere.
//
// ONE READ AND ONE WALK PER DRAIN. The topology arrives as a single snapshot (PassableGraph) rather
// than one store round trip per system reached; the walk over that snapshot is then free, and
// membership is tested per fill rather than re-walked.
func reachableSystems(ctx context.Context, p BuyPorts, playerID int) (GateGraph, map[string]bool, bool) {
	if p.Gates == nil {
		return GateGraph{}, nil, false
	}
	graph, err := p.Gates.PassableGraph(ctx)
	if err != nil {
		return GateGraph{}, nil, false
	}
	parked, err := p.Ledger.SlotsByState(ctx, playerID, SlotStateParked)
	if err != nil {
		return GateGraph{}, nil, false
	}

	reachable := make(map[string]bool, len(parked))
	var frontier []string
	for _, slot := range parked {
		if slot.System == "" || reachable[slot.System] {
			continue
		}
		reachable[slot.System] = true
		frontier = append(frontier, slot.System)
	}
	if len(frontier) == 0 {
		// No probe stands anywhere, so there is no origin to walk from and nothing to conclude.
		// Reporting "nothing is reachable" here would refuse every purchase on a fleet that has
		// simply not placed its first probe yet.
		return GateGraph{}, nil, false
	}

	for len(frontier) > 0 {
		system := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		for _, next := range graph.Passable[system] {
			if next == "" || reachable[next] {
				continue
			}
			reachable[next] = true
			frontier = append(frontier, next)
		}
	}
	return graph, reachable, true
}

// coverageBySystem counts the hulls each system already has or has coming.
//
// The states are exactly the ones states.go calls hull-bearing — from BOUGHT onwards a hull exists
// and counts against the probe cap — because coverage asks the same question the cap does. Counting
// only PARKED would read a system with five probes in flight as empty and pile a sixth onto it.
//
// A SEPARATE READ RATHER THAN A WIDER CANDIDATE QUERY, and that is not stylistic. Four stages of the
// tick call SlotsByState and the STATE LIST is the only thing that tells them apart — the reconcile
// ordering tests fingerprint each stage by it — so widening the candidate query to cover these states
// would spell `allSlotStates` exactly and make the drain's read indistinguishable from the expansion
// tick's and the adoption sweep's, letting an ordering assertion match the wrong stage. The narrow
// read also keeps filled rows structurally incapable of reaching the candidate list: a filled row
// worked as a candidate would buy a second probe for a waypoint that already holds one.
//
// FAILS CLOSED, like every other read in this queue. Reading an unavailable ledger as "no coverage
// anywhere" would rank every system at zero and hand the whole budget to whichever one sorts
// deepest — the concentration this ordering exists to prevent, arriving silently and exactly when the
// ledger is unwell. Nothing has been spent at this point in the tick.
func coverageBySystem(ctx context.Context, p BuyPorts, playerID int) (map[string]int, error) {
	filled, err := p.Ledger.SlotsByState(ctx, playerID, SlotStateBought, SlotStateInTransit, SlotStateParked)
	if err != nil {
		return nil, fmt.Errorf("sensing coverage unreadable, buying nothing this tick: %w", err)
	}
	covered := make(map[string]int, len(filled))
	for _, slot := range filled {
		covered[slot.System]++
	}
	return covered, nil
}

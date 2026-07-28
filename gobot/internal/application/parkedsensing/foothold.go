package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// foothold.go breaks the buying deadlock at the edge of the map.
//
// THE DEADLOCK. The queue can only buy a probe where a hull of ours is already
// standing at the counter (see buyerAt). Expansion asks for a seed by writing a
// SPARE want at a probe yard in a system bordering the frontier — and it is
// perfectly willing to pick a yard in a system we have JUDGED but never actually
// occupied. That want can then never be funded: there is no hull there to buy
// through, and the only way to put one there is to buy one. A system reachable
// in one gate hop, with three yards that would double the fleet's buying
// network, sits unfillable forever while the queue re-reads it every tick.
//
// WHAT BREAKS IT. A hull we already own, flown across the gate to stand on that
// yard. One hull converts the whole system: the moment it parks, buyerAt answers
// for every yard in it, and the system funds its own placements — and its own
// onward seeds — out of the local counter from then on. That leverage is the
// entire justification for taking a hull off a working market, and it is why
// this path is restricted to SPARE placements, which are exactly the requests
// for a buying foothold. A MARKET want is worth one probe; a foothold is worth a
// system.
//
// WHAT IT COSTS, AND WHY THAT COST IS BOUNDED. The hull comes off a PARKED
// MARKET placement somewhere within gate reach, and that market stops being
// watched. Two guards decide which hull may be taken, and between them they mean
// the sacrifice is never coverage the fleet actually depends on:
//
//   - GOODS REDUNDANCY. Every whitelisted good the candidate observes must ALSO
//     be observed by another parked market probe in the SAME system. The system
//     therefore keeps reporting prices on exactly the goods it reported before —
//     what is lost is a second look at goods already being watched, never the
//     last look at anything. See surplusPool.
//   - MANNED POSTS. A hull named by a scout post is manning that post and is not
//     this engine's to take, whatever the sensing ledger says about it. On the
//     live fleet this is not a hypothetical: seven of the home system's thirteen
//     parked market hulls are also standing scout-post hulls, and a redundancy
//     rule on its own would have taken them.
//
// The market the hull leaves reverts to WANTED rather than being deleted, so it
// is still a placement the queue wants filled and will buy for as soon as a
// counter can fund it. The coverage is DEFERRED, not abandoned.
//
// NOTHING HERE WAITS. The two ledger writes are the whole of it: the target is
// claimed IN_TRANSIT naming the hull, the source is released, and the tick
// returns. The flying is the placement machine's job — dispatchClaim reads the
// claimed row on a later tick and advances it one RouteAcross step at a time —
// so a crossing that takes many ticks costs this tick nothing (sp-uwxwo).

// maxFootholdRetasks bounds how many surplus hulls one tick may send across a
// gate. A plain constant, deliberately not a knob, in the manner of
// MaxExpansionActions and DefaultMaxPlacementActions: it paces a burst of
// writes rather than expressing an economic preference.
//
// TWO, because two is what the constraint costs to break. Each re-task converts
// one system from unbuyable to self-funding, and the systems worth converting
// are the immediate gate neighbours holding yards — a handful, reached in a
// tick or two. A larger number would not convert them faster (the placement
// machine still flies one step per tick) and would let a single tick take
// several hulls out of one system on the strength of one redundancy reading.
//
// The bound is a backstop, not the safety property. Correctness comes from
// surplusPool re-deciding redundancy after every take, so the invariant holds
// at any cap.
const maxFootholdRetasks = 2

// footholdBroker is one tick's foothold state: the pool of releasable hulls,
// read LAZILY on first need, and the per-tick budget spent against it.
//
// LAZY BECAUSE THE COMMON TICK MUST NOT PAY FOR IT. Once the map is placed,
// every candidate finds a local counter and this path is never reached; loading
// the pool at the top of the drain would spend two ledger reads per tick to
// answer a question nobody asked. The reads happen on the first placement that
// actually cannot fund itself, and then once for the whole tick.
type footholdBroker struct {
	pool *surplusPool
	// sent counts re-tasks against maxFootholdRetasks.
	sent int
	// loaded and blocked are the two terminal states of the lazy read: loaded
	// means the pool is usable, blocked means a guard read failed and this tick
	// will attempt no foothold at all. Both are latched so a failure costs one
	// read rather than one per placement.
	loaded, blocked bool
	// reach is the tick's gate walker, shared across every placement this broker
	// serves so each source system's walk is computed once. Built lazily on first
	// use; nil between ticks, because the broker itself is per-tick (RULINGS #2).
	reach *gateReach
}

// fill tries to establish a foothold for a placement no local counter can fund,
// and reports whether a hull was sent.
//
// The eligibility test is deliberately narrow, and every clause earns its place:
//
//   - SPARE ONLY. A SPARE want is expansion's request for a buying foothold and
//     always sits on a probe yard, so filling one converts a whole system. A
//     MARKET want is worth a single probe, which never justifies taking a hull
//     off a market that is already working.
//   - WANTED ONLY. A QUEUED placement was claimed for purchase by an earlier
//     tick and may have money moving against it; re-tasking it would race that
//     intent.
//   - BOTH GUARD PORTS WIRED. See BuyPorts.Gates / MannedHulls.
func (b *footholdBroker) fill(
	ctx context.Context,
	p BuyPorts,
	playerID int,
	target QueuedSlot,
	rep *BuyReport,
) (bool, error) {
	if target.Kind != SlotKindSpare || target.State != SlotStateWanted {
		return false, nil
	}
	if p.Gates == nil || p.MannedHulls == nil {
		return false, nil
	}
	if b.sent >= maxFootholdRetasks || b.blocked {
		return false, nil
	}
	if !b.loaded {
		if !b.load(ctx, p, playerID) {
			return false, nil
		}
	}

	// ONE walker per tick, built after the pool so it is only paid for when there
	// is something to draw from. It memoises each source's walk, so a burst of
	// placements sharing a neighbourhood reads the topology once per source rather
	// than once per placement — and every read is the gate STORE, never the API.
	if b.reach == nil {
		b.reach = newGateReach(p.Gates, nil)
	}
	reach, err := sourcesWithinReach(ctx, b.reach, b.pool, target.System)
	if err != nil {
		// The topology store could not answer for this system. Blocking the
		// whole tick would be wrong — another placement's neighbourhood may read
		// perfectly well — so only this placement is passed over.
		logging.LoggerFromContext(ctx).Log("WARN", "foothold placement could not resolve its gate reach; passing it over this tick", map[string]interface{}{
			"action":   "parked_sensing_foothold_reach_failed",
			"waypoint": target.Waypoint,
			"system":   target.System,
			"error":    err.Error(),
		})
		return false, nil
	}

	sent, err := footholdFromSurplus(ctx, p, playerID, target, b.pool, reach, rep)
	if err != nil {
		return false, err
	}
	if sent {
		b.sent++
	}
	return sent, nil
}

// load reads the two facts the guards are built on and reports whether the pool
// is usable.
//
// FAIL-CLOSED AND LATCHED. Either read failing means a hull cannot be PROVEN
// releasable — an unreadable post list read permissively is an empty one, which
// says "no hull is manned" and would hand the scouting fleet's hulls away — so
// the tick does no footholds at all. It is logged rather than returned as an
// error because the drain's other work is unaffected and must go on: this path
// is an extension to the queue, not a precondition for it.
func (b *footholdBroker) load(ctx context.Context, p BuyPorts, playerID int) bool {
	parked, err := p.Ledger.SlotsByState(ctx, playerID, SlotStateParked)
	if err != nil {
		b.blocked = true
		logging.LoggerFromContext(ctx).Log("WARN", "foothold path could not read the parked placements; no foothold this tick", map[string]interface{}{
			"action": "parked_sensing_foothold_pool_unreadable",
			"error":  err.Error(),
		})
		return false
	}
	manned, err := p.MannedHulls.MannedHulls(ctx, playerID)
	if err != nil {
		b.blocked = true
		logging.LoggerFromContext(ctx).Log("WARN", "foothold path could not read the manned scout-post hulls; no foothold this tick", map[string]interface{}{
			"action": "parked_sensing_foothold_posts_unreadable",
			"error":  err.Error(),
		})
		return false
	}
	b.pool = newSurplusPool(parked, manned)
	b.loaded = true
	return true
}

// MannedHullReader reports which hulls are currently manning a scout post.
//
// A SEPARATE PORT rather than a flag on the slot row, because the fact lives in
// a different table owned by a different engine: the sensing ledger has no
// column that could carry it, and a hull can hold a sensing placement and a
// scout post at the same time. Reading it here is what keeps this path from
// treating the scouting fleet's hulls as its own reserve.
type MannedHullReader interface {
	// MannedHulls returns the set of hulls named by a post. An implementation
	// reports every post it can see; a hull absent from the set is one no post
	// claims.
	MannedHulls(ctx context.Context, playerID int) (map[string]bool, error)
}

// surplusPool is the tick's picture of which parked market hulls could be
// released, and it is MUTATED as they are taken.
//
// THE MUTATION IS THE CORRECTNESS ARGUMENT, not an optimisation. Redundancy is a
// property of a SET, so it cannot be evaluated once and then spent twice: in a
// system where A and B both watch ADVANCED_CIRCUITRY and nothing else does, a
// snapshot marks BOTH redundant — each is covered by the other — and taking both
// on the strength of that one reading loses the good entirely. Every take
// therefore removes the row from the pool, and the next take re-decides against
// what is actually left. After A goes, B is the only observer and stops being
// eligible.
//
// That is also what makes the rule self-limiting ACROSS ticks: the pool is
// rebuilt from the ledger each tick, so a system drained to its irreducible
// coverage offers nothing on the next one.
type surplusPool struct {
	// bySystem holds the PARKED MARKET rows still standing in each system.
	bySystem map[string][]QueuedSlot
	// manned names the hulls a scout post claims. Held rather than pre-filtered
	// so a manned hull still COUNTS as coverage for the redundancy test: it is
	// genuinely watching its market, it is merely not available to be taken.
	manned map[string]bool
}

// newSurplusPool groups the parked placements this tick may draw on.
//
// Only PARKED MARKET rows naming a hull are admitted, and the narrowness is
// deliberate on both counts. A row in any other state names a hull that is
// already committed — being bought, or flying to a placement — and taking it
// would fight the machine that is moving it. A SPARE row is not coverage at all,
// and reuseSpareHull already has first claim on those.
func newSurplusPool(parked []QueuedSlot, manned map[string]bool) *surplusPool {
	pool := &surplusPool{
		bySystem: make(map[string][]QueuedSlot),
		manned:   manned,
	}
	for _, row := range parked {
		if row.Kind != SlotKindMarket || row.State != SlotStateParked || row.AssignedShip == "" {
			continue
		}
		pool.bySystem[row.System] = append(pool.bySystem[row.System], row)
	}
	for system := range pool.bySystem {
		rows := pool.bySystem[system]
		// Cheapest sacrifice first, then by waypoint so a tick is reproducible.
		// Depth is the measured value of the market, so this always gives up the
		// least valuable redundant market a system holds.
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].DepthCredits != rows[j].DepthCredits {
				return rows[i].DepthCredits < rows[j].DepthCredits
			}
			return rows[i].Waypoint < rows[j].Waypoint
		})
		pool.bySystem[system] = rows
	}
	return pool
}

// systems lists the systems still holding a candidate hull, in symbol order.
//
// Symbol order rather than map order because it is the input to a reachability
// ranking whose ties are broken by symbol: an unordered candidate list would make
// two equally-distant sources swap places between ticks for no reason.
//
// It reflects the pool as it stands NOW, so a system drained by an earlier
// placement in the same tick stops being offered to the next one.
func (p *surplusPool) systems() []string {
	out := make([]string, 0, len(p.bySystem))
	for system := range p.bySystem {
		out = append(out, system)
	}
	sort.Strings(out)
	return out
}

// take removes and returns the least valuable releasable hull in system, if
// there is one.
//
// "Releasable" is the conjunction of the two guards this file exists to apply:
// the hull is claimed by no scout post, and every whitelisted good it observes
// survives its departure in another parked market of the same system.
func (p *surplusPool) take(system string) (QueuedSlot, bool) {
	rows := p.bySystem[system]
	for i, row := range rows {
		if p.manned[row.AssignedShip] {
			continue
		}
		if !coveredByOthers(row, rows) {
			continue
		}
		p.bySystem[system] = append(append([]QueuedSlot{}, rows[:i]...), rows[i+1:]...)
		return row, true
	}
	return QueuedSlot{}, false
}

// coveredByOthers reports whether every whitelisted good on candidate is also
// carried by some OTHER row in the same system.
//
// FAIL-CLOSED ON MISSING GOODS, in both directions, and neither is an accident:
//
//   - A candidate with no recorded goods is never releasable. An empty list is
//     "we do not know what this market watches", not "it watches nothing", and
//     redundancy cannot be PROVEN of an unknown. The adapter yields an empty
//     list for a row whose goods column will not decode, so a corrupt row is
//     pinned in place rather than given away.
//   - A row with no recorded goods covers nothing, so it cannot be the thing
//     that makes another row releasable.
//
// Both readings shrink the eligible set, which is the safe direction: the cost
// of being wrong here is a foothold delayed, against a market silently going
// dark.
func coveredByOthers(candidate QueuedSlot, rows []QueuedSlot) bool {
	if len(candidate.WhitelistGoods) == 0 {
		return false
	}
	for _, good := range candidate.WhitelistGoods {
		if !observedElsewhere(good, candidate.Waypoint, rows) {
			return false
		}
	}
	return true
}

// observedElsewhere reports whether some row other than the one at exclude
// carries good.
func observedElsewhere(good, exclude string, rows []QueuedSlot) bool {
	for _, row := range rows {
		if row.Waypoint == exclude {
			continue
		}
		for _, watched := range row.WhitelistGoods {
			if watched == good {
				return true
			}
		}
	}
	return false
}

// sourcesWithinReach lists the systems holding a surplus hull that could actually
// be flown TO target, nearest ring first.
//
// IT ASKS THE RIGHT QUESTION, which the walk it replaced did not. The old search
// walked FORWARD OUT OF THE TARGET and treated the result as valid sources —
// correct only if a gate edge always has a reverse, and on the live map 624 of
// 5,488 edges (11.4%) do not. For a target reachable only by a one-way edge that
// search returns precisely the systems a hull CANNOT arrive from: it would
// dispatch onto a route nextHopToward cannot resolve, and the row sits IN_TRANSIT
// naming a hull that never arrives while still counting against the probe cap.
// That is the stranding sp-9fdc258d was written to prevent, in a function it did
// not touch.
//
// THE CANDIDATE SET IS THE SURPLUS POOL, not the graph. Only a system actually
// holding a takeable hull can answer this placement, so walking from those — and
// only those — keeps the cost proportional to the pool rather than to the map,
// and shrinks as the tick spends it.
//
// BOUNDED BY MaxWalkRings through the shared walker, and it must be: that
// constant is the reach of the walk the placement machine will actually fly, and
// a claim written for a hull further out stalls silently. The bound is read from
// the same declaration the walk reads, so the two cannot drift. The target's own
// system is never returned — reach.hops reports false for origin==target, and a
// hull already there is reuseSpareHull's business, not this path's.
//
// A read failure is returned rather than swallowed. An empty reach read
// permissively would be indistinguishable from a genuinely isolated system, and
// this is the one caller that must not silently conclude "nowhere to draw from"
// when the truth is "the topology store did not answer".
func sourcesWithinReach(ctx context.Context, reach *gateReach, pool *surplusPool, target string) ([]string, error) {
	return originsWithinReach(ctx, reach, pool.systems(), target)
}

// footholdFromSurplus fills a SPARE placement no local counter can fund by
// flying a surplus hull to it from within gate reach. It reports whether one was
// sent.
//
// THE WRITE ORDER IS A MONEY GUARD, and it is the same one reuseSpareHull and
// claimSpares are built on. One hull is named by two rows for an instant, and
// which instant is chosen decides which way a crash miscounts:
//
//   - Claim the target FIRST, as here: a failure between the writes leaves both
//     rows naming the hull, so CountOwnedProbes counts it twice, the cap reads
//     the fleet as LARGER than it is, and FEWER probes are bought. Recoverable,
//     and safe.
//   - Release the source first: a failure leaves the hull named by NO row, the
//     cap reads the fleet as smaller than it is, and a replacement is bought for
//     a probe we already own — the exact direction RULINGS #4 forbids.
func footholdFromSurplus(
	ctx context.Context,
	p BuyPorts,
	playerID int,
	target QueuedSlot,
	pool *surplusPool,
	reach []string,
	rep *BuyReport,
) (bool, error) {
	for _, system := range reach {
		source, found := pool.take(system)
		if !found {
			continue
		}
		hull := source.AssignedShip

		// The ledger says PARKED; the ships table is asked whether the hull is
		// actually standing still. The two can disagree — a row is written from
		// the last confirmed reading, and another engine may have moved the hull
		// since — and re-tasking a hull that is mid-flight would have two
		// machines steering it. This is the sensing engine's reading of the same
		// "is it idle?" question the orphan dispatch asks of the fleet.
		//
		// Unreadable or absent is treated as NOT takeable: a hull we cannot
		// locate is never acted on, exactly as advanceSeed treats one. The row
		// stays out of the pool for the rest of the tick, so a hull in this state
		// is skipped rather than retried against the next target.
		pos, err := p.Ships.ShipAt(ctx, playerID, hull)
		if err != nil || !pos.Found || pos.NavStatus == navigation.NavStatusInTransit {
			continue
		}

		err = p.Ledger.TransitionSlot(ctx, playerID, target.Waypoint, target.Kind, target.State, SlotStateInTransit,
			SlotFields{AssignedShip: &hull})
		switch {
		case errors.Is(err, ErrSlotClaimed):
			// Another writer took the placement. The source is untouched — which
			// is precisely why the target is claimed first — so the hull is still
			// parked and still counted, and nothing needs undoing.
			return false, nil
		case err != nil:
			return false, fmt.Errorf("failed to re-task surplus hull %s to foothold %s: %w", hull, target.Waypoint, err)
		}

		// The market the hull leaves goes back to being a want with no hull
		// behind it. WANTED rather than deleted: the placement is still one we
		// intend to fill, and leaving it on the books is what gets a replacement
		// bought there once a counter can fund it.
		cleared := ""
		if err := p.Ledger.TransitionSlot(ctx, playerID, source.Waypoint, source.Kind, SlotStateParked, SlotStateWanted,
			SlotFields{AssignedShip: &cleared}); err != nil {
			return true, fmt.Errorf(
				"surplus hull %s re-tasked to foothold %s but its market %s was not released (hull now double-counted, cap reads high): %w",
				hull, target.Waypoint, source.Waypoint, err)
		}
		rep.Footholds++
		return true, nil
	}
	return false, nil
}

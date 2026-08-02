package parkedsensing

import (
	"context"
	"math"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// yardqueue.go makes the buy queue's ordering aware that some of its placements
// stand on a shipyard the fleet cannot price, and gives those
// placements ABSOLUTE precedence over every ordinary market.
//
// THE SECOND HALF IS AN ADMIRAL DIRECTIVE AND IT OVERRULES THE OBVIOUS DESIGN.
// Leaving coverage-first as the top-level key can only promote a yard within its
// own system's run of coverage values — and the live result was that a yard in a
// system holding three probes competed at coverage 3 and lost to every one of
// ~8,900 coverage-0 market rows elsewhere. Ninety minutes with buying restored
// bought 56 probes and put 5 on yards. So the ordering partitions
// yard-before-market ABOVE coverage; coverage-first survives inside each tier,
// which is what keeps the yard tier spread across systems. See yardFirstOffsets
// and drainCandidates' sort.
//
// THE DEFECT IT CLOSES. planSlots emits a YARD-kind slot only for a yard no
// market slot already covers, and measured live that set is EMPTY: of 2,257
// charted SHIPYARD-trait waypoints, all 2,257 also carry MARKETPLACE, and the
// ledger holds 9,664 slots for the live player with not one of kind YARD. So
// every shipyard the fleet watches is an ordinary MARKET row, and drainCandidates
// ordered those rows on coverage, then system depth, then ledger FIFO — three
// terms, none of which knows what a shipyard is. 78 of the 85 known
// heavy-freighter counters sat in 8,934 WANTED rows against 705 parked hulls with
// nothing marking any of them, which is why 116 manned yards and 117 priced yards
// matched one for one: a yard got priced only where a hull already happened to
// stand.
//
// IT DOES NOT WRITE A YARD SLOT, AND MUST NOT. One slot per waypoint is a real
// invariant (see SlotKindYard and planSlots' CONTRACT note): consumers ask "is a
// probe present at this yard?" by matching waypoint + PARKED and ignoring
// slot_kind, so a second row on one waypoint would double-count against the probe
// cap and authorise buying a probe for a waypoint we already hold. The MARKET slot
// keeps winning. What is added is that the ORDERING now knows what that row is
// standing on.
//
// WHY THE FACT IS PULLED PER TICK AND NEVER RECORDED ON THE ROW. Carrying
// yard-ness as a column on sensing_slots is the obvious shape and it would ship
// INERT. recordSlots skips any waypoint that already carries a slot, and an
// IN_SCOPE system is never re-screened — so a column written by planSlots would
// never reach one of the 8,934 rows that already exist, which are precisely the
// rows that need ordering. Worse, the fact worth ordering on is not "this is a
// shipyard" (permanent) but "this is a shipyard selling a hull we buy whose price
// we cannot see" (time-varying, and it RETRACTS the moment a hull arrives and
// prices it). A stored flag would latch that. So the demand is pulled from the
// shipyard-read budget every tick and thrown away, exactly as yardpresence.go
// pulls it — one source of truth for which yards are dark, consulted by both the
// pass that moves a hull there and the queue that funds one.

// yardDemandLimit is how many ranked presence requests the drain asks for.
//
// FAR LARGER THAN yardPresenceRequestLimit's 64, because it is a different kind of
// bound. That one caps HULL MOVES, where everything past the head is work nobody
// does this tick. This one builds a LOOKUP TABLE consulted once per candidate row,
// and a yard missing from it is a yard the queue stays blind to — the very defect
// being fixed.
//
// 4,096 exceeds the 2,257 charted shipyards on the whole map, so it truncates
// nothing today. It is a bound at all for the reason PresenceRequests states about
// its own: an absent bound read as unbounded is the reading that hurts, and this
// keeps a pathological facts map from being copied wholesale into a map on every
// tick. Truncation is SAFE regardless of where it falls, and that is a property of
// the producer rather than of this number — RankPresence orders heavy yards ahead
// of every other yard unconditionally, so a cut can only ever drop the least
// valuable tail and can never drop a heavy counter in favour of a probe one.
const yardDemandLimit = 4096

// notAYard is the sort key of a placement that is not a dark yard. It is past
// every real rank, so an ordinary market always sorts behind every yard the fleet
// cannot price and the two never tie.
const notAYard = math.MaxInt

// yardOrder is one tick's answer to "which of these placements stand on a
// shipyard the fleet cannot price, and which of those matter most".
//
// It holds NO CROSS-TICK STATE (RULINGS #2). The map is rebuilt from the budget's
// live facts on every drain and discarded with the tick, so a yard priced by any
// path simply stops appearing — there is nothing to retract because nothing was
// stored.
type yardOrder struct {
	// rank is waypoint → its position in the budget's own RankPresence ordering,
	// 0 being the best. A waypoint ABSENT from the map is not a yard that needs
	// presence, which includes every yard already priced and every yard selling
	// nothing the fleet buys.
	//
	// The position is reused rather than re-derived because RankPresence already
	// IS the demand weighting this ordering needs: heavy counters ahead of
	// everything, then read weight, then symbol. Computing a second notion of
	// which yard matters is how two engines end up disagreeing about which
	// shipyard is worth a hull.
	rank map[string]int

	// queued counts the candidate fills standing on such a yard — how many rows
	// the terms below were CONSULTED on this tick.
	queued int
	// atHead counts how many of those landed in the first maxDrainAttempts places
	// of the ordered queue: the window this tick's budget can actually reach.
	//
	// It is the pair with queued that makes a losing coordinator distinguishable
	// from an idle one. queued alone cannot: a tick with 40 dark yards in the
	// queue and none of them at the head reads exactly like a tick with none at
	// all, which is the state the fleet was found in — 78 heavy yards
	// present in the queue and reached approximately never.
	atHead int
}

// wants reports whether a waypoint is a shipyard the fleet needs presence at.
//
// IT IS THE TIER PREDICATE: true puts a placement ahead of every
// ordinary market in the queue, so what this set contains is the whole of what
// absolute precedence applies to. It is deliberately NARROWER than "stands on a
// SHIPYARD-trait waypoint" — the map comes from WantsPresence, so a yard whose
// price we already hold and a yard whose catalogue has never been read are both
// outside it. Widening it to the trait would promote counters that need nothing
// (a priced yard has already given us the listings a hull buys) and counters that
// need a free catalogue read rather than a hull. Measured live the two sets very
// nearly coincide anyway — 1,574 of 1,575 unfilled shipyard placements sit on an
// unpriced yard — because a yard gets priced only by being manned, and being
// manned is what takes its placement out of WANTED in the first place.
func (y yardOrder) wants(waypoint string) bool {
	_, ok := y.rank[waypoint]
	return ok
}

// key is the waypoint's sort position among dark yards — its presence rank, or
// notAYard for a placement that is not one.
//
// A nil map answers notAYard for everything, so an unwired or silent budget
// leaves the queue's order untouched by this term.
func (y yardOrder) key(waypoint string) int {
	if rank, ok := y.rank[waypoint]; ok {
		return rank
	}
	return notAYard
}

// YardDemandReader names the shipyards the fleet cannot price, best first.
//
// It is the read half of YardPresenceDemand and nothing else — pointedly not the
// whole interface. AdmitPresence consumes the budget's metered allowance for hull
// REPOSITIONS, and the drain performs none: it orders a queue. Handing the drain
// a port it could meter with would let a queue sort spend the allowance the
// presence pass needs to move hulls, and the two would starve each other for a
// resource only one of them uses.
type YardDemandReader interface {
	// PresenceRequests returns at most limit yards wanting presence, ranked.
	PresenceRequests(ctx context.Context, playerID int, limit int) []yardscan.PresenceRequest
}

// readYardDemand turns the budget's ranked request list into the lookup the
// ordering consults per row.
//
// FAILS OPEN, like reachableFills and unlike the money guards around it. An
// unwired port or an empty answer yields an empty map and the queue orders
// exactly as it did before — because the failure this term can suffer is
// "shipyards are not prioritised", which is the status quo ante, while a term
// that could REFUSE placements on a blind read would stop the fleet buying. The
// asymmetry is deliberate and is the same one reachableFills documents.
//
// Duplicate and empty waypoints are dropped rather than trusted. RankPresence
// already dedupes, so nothing shipped produces them, but a repeated waypoint here
// would silently overwrite a better rank with a worse one and the cost of the
// check is one map lookup per yard.
func readYardDemand(ctx context.Context, p BuyPorts, playerID int) yardOrder {
	if p.YardDemand == nil {
		return yardOrder{}
	}
	requests := p.YardDemand.PresenceRequests(ctx, playerID, yardDemandLimit)
	if len(requests) == 0 {
		return yardOrder{}
	}
	rank := make(map[string]int, len(requests))
	for position, request := range requests {
		if request.Waypoint == "" {
			continue
		}
		if _, dup := rank[request.Waypoint]; dup {
			continue
		}
		rank[request.Waypoint] = position
	}
	if len(rank) == 0 {
		return yardOrder{}
	}
	return yardOrder{rank: rank}
}

// yardFirstOffsets gives every fill its position among its OWN system's
// outstanding placements, with the dark yards taking the low positions.
//
// IT IS WHAT SPREADS THE YARD TIER ACROSS SYSTEMS. drainCandidates ranks the i-th
// outstanding placement of a system at parked + i, and the sort's top key is now
// yard-before-market, so the coverage a yard competes at only ever
// separates it from OTHER YARDS. A system's dark yards take its indices 0,1,2,…
// in RankPresence order, which means its second yard ranks behind every other
// system's first, its third behind every other system's second, and no system can
// take more than one place in any coverage band. That is the whole of the
// anti-concentration guarantee inside a tier that no longer answers to
// coverage-first from outside — measured, the 1,575 yard placements sit in 795
// systems and 517 of those hold no hull, so the coverage-0 band is 517 wide.
//
// ALL OF A SYSTEM'S YARDS ARE PROMOTED, NOT ITS BEST ONE. Every dark yard sorts
// ahead of every market here, so a system holding eight of them contributes eight
// rows to the yard tier at coverages parked+0…parked+7. There is no per-system
// cap and there must not be one: 677 systems hold two or more shipyards and the
// directive is that all of them get manned. The sort's RankPresence tiebreak
// decides which yard of a system goes FIRST; it never decides which goes at all.
//
// THE MARKETS OF A YARD SYSTEM ARE PUSHED BACK BY ITS YARD COUNT, and that is
// correct rather than incidental. A system with three dark yards has its first
// market land at offset 3, competing at parked+3 — which is the coverage that
// system will genuinely hold once the tier above it has been filled, since those
// three yards are guaranteed to be funded before any market anywhere.
//
// STABLE, so a placement that is not a yard keeps its FIFO position relative to
// its system-mates. The FIFO order is deliberate — drainCandidates documents it as
// what stops a placement being overtaken by a newer one in its own system, tick
// after tick — and the yards are the one class exempt from it. The exemption
// cannot starve anything permanently: a promoted yard leaves WANTED as soon as it
// is filled and stops competing, and the number of shipyards in a system is small
// and fixed while its markets are many.
//
// It returns offsets in the fills' own order rather than a reordered slice,
// because drainCandidates' outer sort is STABLE over ledger order and that order
// is the last tiebreak between systems. Permuting the slice to express this would
// silently change which of two equally-ranked placements from DIFFERENT systems
// goes first.
func yardFirstOffsets(fills []QueuedSlot, yards yardOrder) []int {
	offsets := make([]int, len(fills))
	if len(yards.rank) == 0 {
		// Nothing is a yard: the offsets collapse to the ledger-FIFO index this
		// queue has always used, computed the way it always was.
		outstanding := make(map[string]int, len(fills))
		for i, fill := range fills {
			offsets[i] = outstanding[fill.System]
			outstanding[fill.System]++
		}
		return offsets
	}

	bySystem := make(map[string][]int, len(fills))
	for i, fill := range fills {
		bySystem[fill.System] = append(bySystem[fill.System], i)
	}
	for _, indices := range bySystem {
		sort.SliceStable(indices, func(a, b int) bool {
			return yards.key(fills[indices[a]].Waypoint) < yards.key(fills[indices[b]].Waypoint)
		})
		for position, index := range indices {
			offsets[index] = position
		}
	}
	return offsets
}

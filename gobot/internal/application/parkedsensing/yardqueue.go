package parkedsensing

import (
	"context"
	"math"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// yardqueue.go orders the buy queue so that placements standing on a shipyard the
// fleet cannot price come before every ordinary market.
//
// YARD-BEFORE-MARKET IS THE TOP SORT KEY, and coverage orders within each tier.
// With coverage on top, a yard is only ever promoted among its own system's run of
// coverage values, so a yard in a system that already holds probes loses to every
// coverage-0 market row elsewhere. See yardFirstOffsets and drainCandidates' sort.
//
// NO YARD SLOT IS WRITTEN, AND NONE MAY BE. One slot per waypoint is a real
// invariant (see SlotKindYard and planSlots' CONTRACT note): consumers ask "is a
// probe present at this yard?" by matching waypoint + PARKED and ignoring
// slot_kind, so a second row on one waypoint would double-count against the probe
// cap and authorise buying a probe for a waypoint we already hold. Only the
// ORDERING knows what the winning MARKET row is standing on.
//
// The yard-ness is pulled per tick and never stored on the row: it retracts the
// moment a hull arrives and prices the yard, and a column would latch it.
// yardpresence.go pulls the same demand, so one source of truth says which yards
// are dark.

// yardDemandLimit is how many ranked presence requests the drain asks for. A yard
// missing from the lookup is a yard the queue stays blind to, so it is far larger
// than yardPresenceRequestLimit, which caps hull MOVES instead. It is a bound at all
// only to stop a pathological facts map being copied wholesale every tick;
// truncation is safe wherever it falls, because RankPresence orders heavy yards
// ahead of every other yard unconditionally and a cut drops only the least valuable
// tail.
const yardDemandLimit = 4096

// notAYard is the sort key of a placement that is not a dark yard. It is past
// every real rank, so an ordinary market always sorts behind every yard the fleet
// cannot price and the two never tie.
const notAYard = math.MaxInt

// yardOrder is one tick's answer to "which of these placements stand on a shipyard
// the fleet cannot price, and which of those matter most". It holds NO CROSS-TICK
// STATE (RULINGS #2): rebuilt from the budget's live facts on every drain, so a yard
// priced by any path stops appearing and there is nothing to retract.
type yardOrder struct {
	// rank is waypoint → its position in the budget's own RankPresence ordering,
	// 0 being the best. A waypoint ABSENT is not a yard that needs presence:
	// every priced yard and every yard selling nothing the fleet buys is outside
	// it. The position is reused rather than re-derived so that two engines
	// cannot end up disagreeing about which shipyard is worth a hull.
	rank map[string]int

	// queued counts the candidate fills standing on such a yard — the rows the
	// terms below were CONSULTED on this tick.
	queued int
	// atHead counts how many of those landed in the first maxDrainAttempts places
	// of the ordered queue: the window this tick's budget can reach. Paired with
	// queued it is what tells a losing coordinator from an idle one.
	atHead int
}

// wants reports whether a waypoint is a shipyard the fleet needs presence at, and is
// the TIER PREDICATE: true puts a placement ahead of every ordinary market. The map
// comes from WantsPresence, so it is deliberately narrower than the SHIPYARD trait —
// a yard whose price we already hold and a yard whose catalogue has never been read
// are both outside it, and widening it to the trait would promote counters that need
// no hull at all.
func (y yardOrder) wants(waypoint string) bool {
	_, ok := y.rank[waypoint]
	return ok
}

// key is the waypoint's sort position among dark yards — its presence rank, or
// notAYard for a placement that is not one. A nil map answers notAYard for
// everything, so an unwired or silent budget leaves the queue's order untouched.
func (y yardOrder) key(waypoint string) int {
	if rank, ok := y.rank[waypoint]; ok {
		return rank
	}
	return notAYard
}

// YardDemandReader names the shipyards the fleet cannot price, best first.
//
// It is the read half of YardPresenceDemand and pointedly not the whole interface:
// AdmitPresence meters the budget's allowance for hull REPOSITIONS and the drain
// performs none, so a port it could meter with would let a queue sort spend the
// allowance the presence pass needs to move hulls.
type YardDemandReader interface {
	// PresenceRequests returns at most limit yards wanting presence, ranked.
	PresenceRequests(ctx context.Context, playerID int, limit int) []yardscan.PresenceRequest
}

// readYardDemand turns the budget's ranked request list into the lookup the
// ordering consults per row.
//
// FAILS OPEN, like reachableFills and unlike the money guards around it: an unwired
// port or an empty answer yields an empty map and the queue orders as if this term
// did not exist, whereas a term that could REFUSE placements on a blind read would
// stop the fleet buying. Duplicate and empty waypoints are dropped rather than
// trusted — a repeat would silently overwrite a better rank with a worse one.
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
// outstanding placement of a system at parked + i, and its top sort key is
// yard-before-market, so a system's dark yards take its indices 0,1,2,… in
// RankPresence order: the second ranks behind every other system's first, and no
// system takes more than one place in any coverage band. Its markets are pushed back
// by its yard count, the coverage that system holds once the tier above it is
// filled. Every dark yard is promoted, not the best one — there is no per-system cap
// and there must not be one; the RankPresence tiebreak decides which yard of a
// system goes FIRST, never which goes at all.
//
// STABLE, so a placement that is not a yard keeps the FIFO position among its
// system-mates that stops it being overtaken by a newer one, tick after tick. Yards
// are the one class exempt, and the exemption cannot starve anything permanently: a
// promoted yard leaves WANTED as soon as it is filled, and a system's shipyards are
// few and fixed while its markets are many.
//
// Offsets come back in the fills' own order rather than as a reordered slice,
// because drainCandidates' outer sort is STABLE over ledger order and that is its
// last tiebreak between systems; permuting here would silently change which of two
// equally-ranked placements from DIFFERENT systems goes first.
func yardFirstOffsets(fills []QueuedSlot, yards yardOrder) []int {
	offsets := make([]int, len(fills))
	if len(yards.rank) == 0 {
		// Nothing is a yard: the offsets collapse to the plain ledger-FIFO index.
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

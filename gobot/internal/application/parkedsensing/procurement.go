package parkedsensing

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
)

// procurement.go makes the buy queue PRICE-AWARE across every counter it can reach,
// instead of buying at the nearest one whatever it charges.
//
// TWO MECHANISMS, FAILING IN OPPOSITE DIRECTIONS ON PURPOSE.
//
//   - The RANKING orders every counter within ferry reach by what a probe costs
//     LANDED at the placement, so the cheap frontier yard is asked first. Evidence-
//     driven, and fails OPEN: with nothing fresh to compare, the drain falls back to
//     exactly the nearest-first list it had before this file existed.
//   - The WALK-AWAY refuses any counter asking more than a multiple of the fleet's
//     own cheapest fresh ask. A money guard: it only ever SUBTRACTS a purchase, and
//     relaxes nothing it sits in front of (RULINGS #4).
//
// THE WALK-AWAY BINDS TWICE, and the second time cannot be fooled. It pre-filters the
// STORED asks so a runaway counter is not even ranked, and re-checks the LIVE quote
// at the counter (fillSlot) — because a stored price is a reading and the quote is
// the bill. A yard whose row is stale-low sails through the first check and never the
// second.
//
// COST: one bulk price read and one topology read per TICK, both local, both latched
// here. The per-placement work is arithmetic over that snapshot.

// maxProcurementCandidates bounds how many PRICE-RANKED counters one placement may
// offer, mirroring maxFerryCandidates and for its reason: every counter tried costs
// an attempt from a budget the whole tick shares, so an unbounded list lets one
// placement starve everything behind it. Three still lets fillSlot walk a list.
const maxProcurementCandidates = 3

// maxProcurementProbes bounds how many ranked counters one placement may ask "is a
// hull of ours standing here?" before conceding. The ranking spans every priced yard
// on the map, and each question is two indexed store reads — so a region whose cheap
// counters are all unstaffed would otherwise walk hundreds of them, per placement, per
// tick. Four times the candidate cap: the ranking may step past nine unstaffed
// counters, and past that the placement is better served by the nearest-first
// fallback than by more lookups.
const maxProcurementProbes = 4 * maxProcurementCandidates

// defaultProbeAskFreshness is how recently a stored price must have been read for the
// ranking to compare it, when the quartermaster's cadence is not wired through.
//
// Three of the default cadences, and the multiple is the point: the cadence is a
// FLOOR on a yard's re-read interval and never a target, so the real interval runs
// longer. Equating "fresh" with one cadence would mark almost every row stale on
// almost every tick and leave this file failing open permanently — correct-looking,
// and dead. Staleness here is cheap either way: a stale-LOW row costs one wasted
// attempt (the live quote is taken before money moves), a stale-HIGH one costs a
// counter its place in an ordering. Neither can cause an overpay.
const defaultProbeAskFreshness = 3 * time.Hour

// procurementVerdict is what the ranking has to say about ONE placement. Three
// answers, because "buy at these" and "buy at none of them" are both ANSWERS while
// "I could not look" is not, and collapsing the last into either is how a fail-open
// degrades into a silent refusal or a blind spend.
type procurementVerdict int

const (
	// procurementUnavailable: no yard within reach carries a fresh price, or the
	// snapshot could not be read. The caller falls back to the nearest-first path and
	// the cause is named once per tick.
	procurementUnavailable procurementVerdict = iota
	// procurementRanked: the returned list IS the answer, best first. It may be empty —
	// counters were priced, and none had a hull of ours standing at it.
	procurementRanked
	// procurementWalkedAway: every priced counter in reach breached the ceiling. The
	// placement is held UNCLAIMED rather than filled at a refused price.
	procurementWalkedAway
)

// procurementBroker is one tick's price snapshot and the ceiling derived from it.
// Per-TICK, never longer (RULINGS #2): a counter that got cheap between ticks is
// re-learned, and a stored "this yard is expensive" would outlive its reading. Lazy
// and latched, as ferryBroker is.
type procurementBroker struct {
	k BuyKnobs
	// fresh is the tick's comparable universe, held BOTH as a lookup ("does this yard
	// have a judged price?") and as a symbol-ordered list (Go map iteration is not
	// reproducible). One source, two shapes, built together so they cannot disagree.
	fresh map[string]ProbeAsk
	order []ProbeAsk
	// cheapest is the lowest fresh ask anywhere; ceiling is the walk-away line from it.
	// A ZERO ceiling means the guard has nothing to say and every ask passes.
	cheapest, ceiling int64
	// reach answers "how many gates from that yard's system to this placement", seeded
	// from the bulk topology snapshot so the walk costs no per-system store read.
	reach *gateReach
	// loaded and blocked are the lazy read's terminal states; warned latches the
	// fail-open notices BY CAUSE, so a tick names each once rather than once per placement.
	loaded, blocked bool
	warned          map[string]bool
}

func newProcurementBroker(k BuyKnobs) *procurementBroker {
	return &procurementBroker{k: k, warned: map[string]bool{}}
}

// The three knobs resolved to their documented defaults. A NON-POSITIVE VALUE IS THE
// REVERT, matching every other sensing knob and RULINGS #22: with no config present
// these read zero and the feature runs on its fitted values rather than shipping inert.
func (b *procurementBroker) walkAwayMult() int {
	if b.k.WalkAwayMult <= 0 {
		return domainSensing.DefaultWalkAwayMult
	}
	return b.k.WalkAwayMult
}

func (b *procurementBroker) jumpPenalty() int64 {
	if b.k.JumpPenaltyCredits <= 0 {
		return domainSensing.DefaultJumpPenaltyCredits
	}
	return b.k.JumpPenaltyCredits
}

func (b *procurementBroker) freshness() time.Duration {
	if b.k.AskFreshness <= 0 {
		return defaultProbeAskFreshness
	}
	return b.k.AskFreshness
}

// load takes the tick's price snapshot and derives the ceiling from it, reporting
// whether a ranking is possible at all.
//
// EVERY FAILURE IS LATCHED AND OPEN. An unwired port, an unreadable table, an
// unreadable topology and a fleet whose readings have all gone stale mean the same
// thing to the caller, and all leave `ceiling` at zero, which silences the walk-away
// too. Correct for this file specifically: everything it does can only subtract a
// purchase, so its absence restores the behaviour that predates it. Failing CLOSED
// would stop probe buying fleet-wide on a local read fault.
func (b *procurementBroker) load(ctx context.Context, p BuyPorts, playerID int, now time.Time) bool {
	if b.blocked {
		return false
	}
	if b.loaded {
		return true
	}
	if p.Asks == nil || p.Gates == nil {
		return b.block(ctx, "unwired", "the yard-price reader or the gate topology is not wired", nil)
	}

	asks, err := p.Asks.ProbeAsks(ctx, playerID)
	if err != nil {
		return b.block(ctx, "prices_unreadable", "the stored yard prices could not be read", err)
	}
	graph, err := p.Gates.PassableGraph(ctx)
	if err != nil {
		return b.block(ctx, "topology_unreadable", "the gate topology could not be read", err)
	}

	window := b.freshness()
	b.fresh = make(map[string]ProbeAsk, len(asks))
	for _, ask := range asks {
		// An UNPRICED row says the yard SELLS a probe, never what it charges — a
		// catalogue-only reading prices everything at zero by construction, and ranking
		// one as free would put every never-visited counter at the head of the queue.
		if ask.Yard == "" || ask.Price <= 0 || now.Sub(ask.ScannedAt) >= window {
			continue
		}
		if held, seen := b.fresh[ask.Yard]; seen && !ask.ScannedAt.After(held.ScannedAt) {
			continue // one yard, one reading: the most recent wins
		}
		b.fresh[ask.Yard] = ask
	}
	if len(b.fresh) == 0 {
		return b.block(ctx, "all_stale", fmt.Sprintf(
			"no yard carries a probe price read within the last %s", window), nil)
	}

	b.order = make([]ProbeAsk, 0, len(b.fresh))
	for _, ask := range b.fresh {
		b.order = append(b.order, ask)
		if b.cheapest == 0 || ask.Price < b.cheapest {
			b.cheapest = ask.Price
		}
	}
	sort.Slice(b.order, func(i, j int) bool { return b.order[i].Yard < b.order[j].Yard })

	b.ceiling = domainSensing.WalkAwayCeiling(b.cheapest, b.walkAwayMult())
	// maxFerryHops, not a bound of this file's own: the hull is carried by the same
	// RouteAcross walk the ferry path uses, and a counter chosen past that reach strands
	// it in IN_TRANSIT holding probe-cap headroom forever.
	b.reach = newGateReach(p.Gates, graph.Passable, maxFerryHops)
	b.loaded = true
	return true
}

// block latches a fail-open cause, names it once for the tick, and reports the
// "cannot rank" answer in one statement, so no caller can set the flag silently.
func (b *procurementBroker) block(ctx context.Context, cause, what string, err error) bool {
	b.blocked = true
	b.warn(ctx, cause, what, err)
	return false
}

func (b *procurementBroker) warn(ctx context.Context, cause, what string, err error) {
	if b.warned[cause] {
		return
	}
	b.warned[cause] = true
	fields := map[string]interface{}{
		"action": "parked_sensing_procurement_unavailable",
		"cause":  cause,
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	logging.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf(
		"sensing probe procurement is buying at the NEAREST counter rather than the cheapest: %s", what), fields)
}

// pricedOffer is one counter the ranking has judged: how far the hull must be flown
// from it, and what a probe costs landed at the placement.
type pricedOffer struct {
	ask    ProbeAsk
	hops   int
	landed int64
}

// candidates ranks every counter within ferry reach of one placement by landed cost,
// cheapest first, and resolves the buying hulls for the head of that list.
//
// THE ORDER OF THE TWO FILTERS IS LOAD-BEARING. Reach is tested first and the
// walk-away second, so `refused` counts only counters the fleet could ACTUALLY have
// bought at — which is what keeps BuyReport.WalkAwayHeld readable as "the guard bit
// here" rather than "the priced yards were all across the map".
//
// UNPRICED COUNTERS IN THE PLACEMENT'S OWN SYSTEM ARE APPENDED, NEVER DROPPED. An
// unpriced yard is a GUESS where a fresh reading is EVIDENCE — the ranking
// ProbeYardIsCandidate already applies — so it sorts behind every judged counter and
// is still offered, keeping the discovery path this queue has always had. It cannot
// smuggle a refused counter back in: a yard the walk-away judged is skipped by name,
// and the live-quote check binds on whatever an unpriced one turns out to charge.
func (b *procurementBroker) candidates(
	ctx context.Context,
	t *drainTick,
	slot QueuedSlot,
	inSystem []QueuedSlot,
	now time.Time,
) ([]purchaseCandidate, procurementVerdict, error) {
	if !b.load(ctx, t.p, t.playerID, now) {
		return nil, procurementUnavailable, nil
	}

	offers, refused := b.offersFor(ctx, slot)
	if len(offers)+refused == 0 {
		// Nothing priced is in reach of THIS placement. Not a walk-away — the guard was
		// never consulted — so the caller falls back, which can still find an unpriced
		// counter or a cross-gate one.
		b.warn(ctx, "none_in_reach", fmt.Sprintf(
			"no priced counter is within %d gates of %s", maxFerryHops, slot.Waypoint), nil)
		return nil, procurementUnavailable, nil
	}

	buys := make([]purchaseCandidate, 0, maxProcurementCandidates)
	probed, truncated := 0, false
	for _, offer := range offers {
		if len(buys) >= maxProcurementCandidates {
			break
		}
		if probed >= maxProcurementProbes {
			truncated = true
			break
		}
		probed++
		buyer, found, err := buyerAt(ctx, t.p, t.playerID, offer.ask.Yard, inSystem)
		if err != nil {
			return nil, procurementUnavailable, err
		}
		if !found {
			continue
		}
		candidate := newPurchaseCandidate(slot.System, offer.ask.Yard, buyer)
		candidate.ferryHops = offer.hops
		buys = append(buys, candidate)
	}

	local, err := t.candidatesInSystem(ctx, slot, inSystem, now)
	if err != nil {
		return nil, procurementUnavailable, err
	}
	for _, candidate := range local {
		if _, judged := b.fresh[candidate.yard]; judged {
			continue // already ranked above, or already refused by the walk-away
		}
		buys = append(buys, candidate)
	}

	if len(buys) == 0 {
		// `truncated` disqualifies the hold: a walk-away must mean "every counter the
		// fleet could have bought at was too dear", and a list we stopped walking has
		// not established that.
		if refused > 0 && !truncated {
			return nil, procurementWalkedAway, nil
		}
		// Counters were priced and none can transact — no hull of ours stands at any of
		// them. NOT a refusal, so the placement falls through to the nearest-first path,
		// whose cross-gate search can still reach an UNPRICED remote counter this ranking
		// never considered. Without this the ranking would narrow the candidate set it was
		// only ever meant to reorder.
		return nil, procurementUnavailable, nil
	}
	return buys, procurementRanked, nil
}

// offersFor prices every fresh-read counter that can deliver to this placement, best
// first, and reports how many the ceiling refused.
//
// A YARD WHOSE DISTANCE CANNOT BE RESOLVED IS DROPPED, not charged a default: an
// unresolvable distance is exactly the case where buying strands the hull. Dropping
// is the fail-closed direction and costs nothing — the nearest-first fallback is
// still behind this placement.
func (b *procurementBroker) offersFor(ctx context.Context, slot QueuedSlot) ([]pricedOffer, int) {
	gateFee, penalty := domainSensing.DefaultGateFeeCredits, b.jumpPenalty()
	offers := make([]pricedOffer, 0, len(b.order))
	refused := 0
	for _, ask := range b.order {
		hops := 0
		if ask.System != slot.System {
			distance, within, err := b.reach.hops(ctx, ask.System, slot.System)
			if err != nil || !within {
				continue
			}
			hops = distance
		}
		if b.overCeiling(ask.Price) {
			refused++
			continue
		}
		offers = append(offers, pricedOffer{
			ask:    ask,
			hops:   hops,
			landed: domainSensing.LandedYardCost(ask.Price, hops, gateFee, penalty),
		})
	}
	// Landed cost decides; hops break a tie so a hull that need not fly does not; the
	// yard symbol makes the order TOTAL — sort.Slice is not stable and equal prices are
	// common in a frontier band, so without it the head would vary between two runs
	// over the same rows.
	sort.Slice(offers, func(i, j int) bool {
		if offers[i].landed != offers[j].landed {
			return offers[i].landed < offers[j].landed
		}
		if offers[i].hops != offers[j].hops {
			return offers[i].hops < offers[j].hops
		}
		return offers[i].ask.Yard < offers[j].ask.Yard
	})
	return offers, refused
}

// overCeiling is the walk-away, asked of a stored ask when the ranking is built and
// of the LIVE quote when the counter names its price. A zero ceiling passes
// everything — see domainSensing.WalkAwayCeiling for why that is the only safe
// direction for an absent reference.
func (b *procurementBroker) overCeiling(ask int64) bool {
	return b.ceiling > 0 && ask > b.ceiling
}

// walkAwayReason states the refusal in the numbers an operator would otherwise have
// to reconstruct, verbatim into BuyReport.Refusals.
func (b *procurementBroker) walkAwayReason(quote int64) string {
	return fmt.Sprintf(
		"asked %d, above the walk-away ceiling %d (%dx the fleet's cheapest fresh ask, %d)",
		quote, b.ceiling, b.walkAwayMult(), b.cheapest)
}

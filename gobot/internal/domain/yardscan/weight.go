// Package yardscan holds the one thing the fleet's shipyard-read budget needs
// that its market-read sibling could not supply: what a SHIPYARD is worth
// looking at.
//
// The budget arithmetic itself is not reimplemented here. Interval, MaxStaleness,
// the anti-starvation escape, the Earning/Discretionary classes and the admission
// decision all come from internal/domain/marketscan, which is generic over "a
// cached row with a value weight and a fixed allowance" and is imported as-is.
// What differs between a market and a shipyard is only the VALUE TERM, and this
// package is that term.
//
// WHY THE MARKET'S VALUE TERM DOES NOT TRANSFER. A market's worth is
// its bid-ask spread: a continuous, self-revealing quantity that every scan
// re-measures, so marketscan can weight a market on what it last quoted. A
// shipyard has no spread. It has a CATALOGUE (which hull types it sells, which
// survives a presence-less GET) and PRICES (which appear only when a hull of ours
// is standing at the counter — see adapters/parkedsensing/yard_ports.go). Ranking
// yards by anything derived from price would therefore rank them by whether we
// happened to have a hull there, which is the opposite of useful: the yards worth
// looking at are precisely the ones we have NOT priced.
//
// So a yard is weighted by DEMAND — by what the fleet is trying to buy — and the
// single most valuable read in the fleet is a yard we already know sells a hull we
// want but whose price we have never seen.
//
// THAT ORDERING IS NOT THEORETICAL. It is the incident this package was written
// for. With shipyard reads paced only by a blunt cadence floor
// (quartermaster_cadence_secs raised 3600 -> 86400 during an incident), 84 yards
// were known to sell SHIP_HEAVY_FREIGHTER and exactly 4 of them had a price. The
// other 80 had sat unpriced for 21-24 hours while the fleet bought 24 heavies at
// up to 2,288,156 against a visible cheapest of 1,918,293. The cadence floor was
// not merely too slow — it could not PRIORITISE, so it starved exactly the yards
// the buy loop was about to spend millions at. A budget that allocates by demand
// cannot reproduce that failure: those 80 yards are the top of its rotation by
// construction.
package yardscan

import "math"

// Baseline is the weight of a yard the fleet has no reason to look at: one it has
// scanned, whose catalogue holds nothing we are shopping for. It is the floor of
// the weight domain, matching marketscan's own baseline so both budgets express
// intervals on the same scale.
const Baseline = 1.0

// Facts is what the fleet knows about one shipyard at the moment it is considered
// for a read. Every field is derived from the persisted shipyard inventory and the
// budget's own recent traffic — none of it costs an API request to establish,
// which is what lets the weight be evaluated on every admission.
type Facts struct {
	// Unknown means no scan of this yard has ever landed: we do not know what it
	// sells, let alone what it charges. It outranks a known-but-dull yard because
	// an unopened catalogue MIGHT hold the hull we need, and it is outranked by a
	// known-wanted-but-unpriced yard because a confirmed lead beats a speculative
	// one. Unknown makes the other fields meaningless and is read first.
	Unknown bool

	// SellsWanted means this yard's known catalogue includes at least one hull
	// type the fleet is currently shopping for.
	SellsWanted bool

	// Priced means we hold a non-zero purchase price here for at least one wanted
	// type. Its absence alongside SellsWanted is the top-value case: a confirmed
	// seller of a hull we want, at an unknown price, which is the one thing a
	// buy loop cannot reason about and the one thing only a read can fix.
	Priced bool

	// Targeted means a money guard priced a hull at this yard recently — the
	// fleet is not merely shopping, it is buying HERE. It is the strongest signal
	// available and the only one that is about a specific counter rather than
	// about a hull type.
	//
	// Note what this does and does not do. The guard read itself is Earning-class
	// and was never deniable, so Targeted does not exist to admit it. It exists so
	// the ROTATION keeps a yard we are actively buying from fresh between guard
	// reads, instead of letting it decay to the baseline the moment the buy loop
	// looks away.
	Targeted bool
}

// Weight is a yard's share of the fixed shipyard-read allowance, in
// [Baseline, clampR].
//
// The four tiers, in the order they are tested and for the reason each outranks
// the next:
//
//  1. Targeted — we are buying at this counter now. Nothing is worth more.
//  2. SellsWanted && !Priced — a confirmed seller of a hull we want, at an
//     unknown price. This is the tier the 80 starved yards live in, and it sits
//     at the clamp deliberately: an unpriced lead is worth as much as an active
//     one, because it is how an active one gets found cheaper.
//  3. Unknown, or SellsWanted && Priced — the middle. An unopened catalogue and a
//     priced lead that may have drifted are worth the same: both are speculative,
//     one about what a yard sells and one about what it now charges.
//  4. Everything else — a scanned yard selling nothing we want.
//
// The middle tier is the GEOMETRIC mean of the baseline and the clamp, not the
// arithmetic one. Weights divide a fixed allowance, so they compose
// multiplicatively: sqrt(clampR) sits exactly halfway between baseline and clamp
// in the ratio that actually governs the rotation, and — unlike (1+clampR)/2 — it
// keeps that midpoint honest at every clamp instead of drifting toward the top as
// the clamp is raised.
//
// A clampR below the baseline flattens every tier to the baseline, which is how
// demand weighting is switched off: admission then turns purely on dueness and
// token availability, exactly as marketscan's own clamp of 1 does.
func Weight(f Facts, clampR int) float64 {
	clamp := float64(clampR)
	if clamp < Baseline {
		return Baseline
	}
	switch {
	case f.Targeted:
		return clamp
	case f.Unknown:
		return math.Sqrt(clamp)
	case f.SellsWanted && !f.Priced:
		return clamp
	case f.SellsWanted:
		return math.Sqrt(clamp)
	default:
		return Baseline
	}
}

// PriorWeight is the weight of a yard nothing is yet known about — Weight of the
// Unknown fact, named so callers computing a map-wide total do not have to
// construct a Facts to ask for it.
//
// It is what every yard is worth at boot, before any catalogue has been read, and
// therefore what the budget's total weight is built from on a cold start. Pitching
// it above the baseline rather than at it matters for the same reason marketscan's
// optimistic prior does: a yard nobody has opened is looked at sooner than one
// opened and found dull, so discovery is funded out of the ordinary rotation
// instead of needing an allowance of its own.
func PriorWeight(clampR int) float64 {
	return Weight(Facts{Unknown: true}, clampR)
}

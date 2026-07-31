package yardscan

import "sort"

// presence.go answers the question a shipyard-read budget cannot answer by
// reading harder: WHICH YARDS NEED A HULL STANDING AT THEM (sp-fox5u).
//
// THE MECHANIC, and why no amount of scanning substitutes for it.
// `GET /systems/{s}/waypoints/{w}/shipyard` answers with two things, and only
// one of them is free. `shipTypes` — what the counter sells — comes back with no
// hull anywhere near the waypoint, and the free catalogue pass
// (application/parkedsensing/yardcatalog.go) already harvests it. `ships` — the
// listings carrying purchasePrice — appears ONLY when a hull of ours is present.
// Measured live: of 85 known heavy-selling yards, the 4 that were priced were
// exactly the 4 with one of our hulls parked on them, and the 81 unpriced ones
// had zero. The correlation is total because it is not a correlation at all — it
// is the API's rule.
//
// So a budget that allocates reads by demand (weight.go) does its job perfectly
// and still learns nothing new about an unoccupied counter: it spends a request
// to be told the same catalogue again. sp-mb0er's rotation was measured sweeping
// 229 rows in ten minutes and could not move the priced count off 4. That is not
// a scan-rate problem, and raising the rate makes it worse rather than better.
//
// WHAT THIS PACKAGE ADDS. The budget's top weight tier — SellsWanted && !Priced,
// a confirmed seller of a hull we want at a price we have never seen — IS the
// set of yards that need presence. This file turns that tier into a ranked list
// of PRESENCE REQUESTS: "send a hull here, and the next ordinary scan reads the
// price for free." It decides only WHICH yards and IN WHAT ORDER. Whether a hull
// can be spared, and which one, is deliberately not decided here — that is a
// fact about the sensing fleet, and it lives with the sensing fleet
// (application/parkedsensing/yardpresence.go).

// PresenceRequest is one yard the fleet should put a hull on, with the two facts
// a dispatcher needs to rank and route it.
type PresenceRequest struct {
	// Waypoint is the shipyard that needs a hull standing on it.
	Waypoint string
	// System is the system it sits in. Carried rather than re-derived because
	// the dispatcher's whole search — which hulls are already near enough to be
	// spared — is keyed by system, and re-parsing the symbol at every comparison
	// would put string surgery in the middle of a ranking loop.
	System string
	// Heavy marks a yard whose catalogue holds one of the hull classes the
	// fleet's acquisition path exists to buy.
	//
	// It is a SEPARATE FIELD rather than something read off Weight, and the
	// separation is the point. Weight ranks a yard for a READ, where a confirmed
	// unpriced seller of any wanted type sits at the clamp regardless of which
	// type it is — one request, one row, and the cheap ones are as worth
	// refreshing as the dear ones. Presence ranks a yard for a HULL MOVE, which
	// is a far scarcer resource, and there the difference between a counter
	// selling a 2,000,000-credit freighter and one selling a probe is the whole
	// decision. Folding heavy into Weight would have distorted the read
	// rotation to express a fact only the mover needs.
	Heavy bool
	// Weight is the yard's read weight, carried through so presence inherits the
	// budget's own ordering inside a heavy class instead of inventing a second,
	// divergent notion of which yard matters.
	Weight float64
}

// WantsPresence reports whether a yard's known facts justify sending a hull to
// stand on it.
//
// It is exactly the budget's top weight tier and deliberately no wider:
//
//   - SellsWanted, so the move is aimed at a counter we have CONFIRMED sells
//     something the fleet buys. Speculation is not worth a hull.
//   - !Priced, because presence buys exactly one thing — the listings array —
//     and a yard we already hold a price for has already given it to us. Sending
//     a hull there would spend the fleet's scarcest discovery resource to learn
//     nothing.
//   - !Unknown, and this clause is the one that is easy to get wrong. An
//     unopened catalogue MIGHT hold a heavy, and Weight ranks it above a dull
//     yard for precisely that reason — but the thing it needs is a CATALOGUE
//     READ, which costs one request and no hull at all, and the free presence-less
//     pass already sweeps it. Admitting Unknown here would fly hulls at yards
//     that turn out to sell nothing we want, spending the expensive remedy on a
//     problem the cheap one solves. A yard graduates into this set the moment
//     its catalogue lands and shows a wanted type.
//
// The ordering between the two passes is therefore free discovery first, hull
// moves only for confirmed leads.
func WantsPresence(f Facts) bool {
	return !f.Unknown && f.SellsWanted && !f.Priced
}

// RankPresence orders presence requests best first: heavy yards ahead of the
// rest, then by read weight, then by symbol.
//
// HEAVY OUTRANKS EVERYTHING, because the two classes are not comparable in
// credits. The incident behind sp-mb0er had the fleet buying 24 heavies at up to
// 2,288,156 each against a visible cheapest of 1,918,293 — one unpriced heavy
// counter is worth more than every probe yard on the map put together, and a
// ranking that let a burst of cheap yards crowd it out would reproduce exactly
// the starvation the demand weighting was written to end.
//
// WEIGHT BREAKS TIES INSIDE A CLASS, so presence inherits the read budget's
// ordering rather than inventing a second one: a yard a money guard is actively
// pricing at (Targeted) is already ranked above a merely-wanted one there, and
// that judgement carries through here for free.
//
// THE SYMBOL TIE-BREAK IS LOAD-BEARING, not tidiness. Equal-ranked yards are the
// common case — every non-heavy unpriced seller sits at the same clamp — and
// sort.Slice is not stable, so without a total order the head of the queue would
// differ between two runs over the same rows. That would make the bounded pick
// depend on the order the store happened to return, which is untestable and
// would let one tick's dispatch differ from the next for no reason.
//
// The input is not mutated: a caller holding the budget's own slice must not
// find it reordered under it.
func RankPresence(requests []PresenceRequest) []PresenceRequest {
	out := make([]PresenceRequest, 0, len(requests))
	seen := make(map[string]struct{}, len(requests))
	for _, r := range requests {
		if r.Waypoint == "" {
			continue
		}
		// A waypoint listed twice would claim two of the dispatcher's bounded
		// slots for one counter. No producer emits duplicates today, but nothing
		// in the type forbids it and the cost of one slipping through is a wasted
		// hull move.
		if _, dup := seen[r.Waypoint]; dup {
			continue
		}
		seen[r.Waypoint] = struct{}{}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Heavy != out[j].Heavy {
			return out[i].Heavy
		}
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Waypoint < out[j].Waypoint
	})
	return out
}

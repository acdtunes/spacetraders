package trading

import (
	"sort"
	"time"
)

// tradeTypeExport is the GoodListing.TradeType value for a market that PRODUCES and
// sells a good (an exporter). Its Bid is a low sellback price, so it can never be a
// sell destination. Matches domain/market.TradeTypeExport without importing that
// package into this pure-domain ranking code.
const tradeTypeExport = "EXPORT"

// GoodListing is one cached market's trade data for a single good. Bid and Ask
// are named from the MARKET's side; the columns they come from are named from
// OURS, so the mapping is a rename and NOT a swap — getting it backwards is the
// "inverted-margin trap" that overstates every spread ~2x (market-doctrine):
//
//   - Bid = what a ship RECEIVES per unit when SELLING this good TO the market
//     = the market_data.sell_price column = TradeGood.SellPrice().
//   - Ask = what a ship PAYS per unit when BUYING this good FROM the market
//     = the market_data.purchase_price column = TradeGood.PurchasePrice().
//
// Ask therefore exceeds Bid at any one market. A listing quoting the reverse is
// impossible data (see IsCrossed) and is refused, not ranked.
//
// A profitable lane BUYS at a source market's Ask and SELLS at a destination
// market's Bid, so profit per unit = destination.Bid − source.Ask. Buy at
// exporters (low Ask), sell at importers (high Bid).
//
// This doc named the two columns the other way round until sp-en5h7. It was
// consistent with the data only because the data itself was transposed.
type GoodListing struct {
	Good      string
	Waypoint  string
	TradeType string // EXPORT, IMPORT, or EXCHANGE
	Bid       int    // sell_price column: what we RECEIVE selling TO it
	Ask       int    // purchase_price column: what we PAY buying FROM it
	Supply    string
	Activity  string
	Volume    int
	// ObservedAt is when the source market snapshot these prices came from was
	// last refreshed into the cache (market.Market.LastUpdated). The ranker uses
	// it to reject lanes priced from observations older than maxListingAge, so a
	// lane whose cached spread has since moved cannot win selection and execute at
	// stale prices. Zero value means "unknown age" — treated as fresh so
	// callers that don't populate it (older tests) rank unchanged.
	ObservedAt time.Time
}

// IsCrossed reports whether this listing quotes an ask BELOW its bid — buy a unit
// and sell it straight back to the same market at a profit. No real market does
// that, so a crossed listing is bad data and never a bargain.
//
// It is the tradeability precondition RankSpreads applies before any pair is
// considered, and it exists because of sp-en5h7: every market_data row written
// before that fix holds its two prices transposed, so reading one with the
// CORRECTED mapping yields exactly this shape. Two such rows at different
// waypoints would otherwise rank as a lane worth roughly twice the true spread —
// real money spent chasing a spread that is not there.
//
// Refusing them makes the correction safe in either deploy order: legacy rows
// produce no lane at all rather than a phantom one. The predicate can only ever
// REMOVE lanes, never admit one that was previously refused (RULINGS #4).
//
// Equality is allowed: a zero-rake EXCHANGE quote is unusual but not impossible,
// and it yields no positive spread anyway. The threshold matches the sensing
// model's own inverted-quote guard (parkedsensing.RelativeSpread) so the codebase
// states "impossible quote" exactly once.
func (l GoodListing) IsCrossed() bool {
	return l.Ask < l.Bid
}

// ArbitrageLane is the best buy-here / sell-there circuit for a single good
// within one system, computed purely from cached listings.
type ArbitrageLane struct {
	Good           string
	SourceWaypoint string // buy here, paying SourceAsk per unit
	DestWaypoint   string // sell here, receiving DestBid per unit
	SourceAsk      int    // source market's SELL price (what we pay)
	DestBid        int    // destination market's BUY price (what we receive)
	SourceSupply   string
	DestActivity   string
	SpreadPerUnit  int // DestBid − SourceAsk (always > 0 for a returned lane)
	VolumeCap      int // min(source.Volume, dest.Volume) — market-absorption bound
	CappedSpread   int // SpreadPerUnit × VolumeCap — the ranking key
}

// ClearsFloor reports whether the lane's per-unit spread clears the bid-floor
// discipline (MinBidMargin) that the circuit executor enforces per visit via
// MarginAlive. It is the single tradeability predicate shared by lane SELECTION
// (which must never pick a lane the executor would immediately refuse) and the
// `market spreads` scan display (which flags which ranked lanes are actually
// flyable). Equivalent to MarginAlive(l.DestBid, l.SourceAsk) because
// SpreadPerUnit == DestBid − SourceAsk for every ranked lane.
func (l ArbitrageLane) ClearsFloor() bool {
	return l.SpreadPerUnit >= MinBidMargin
}

// ClearsFloorAfterGates reports whether ONE revenue-earning trip down this lane is work once every
// gate crossing it pays for is charged against it. The trip grosses SpreadPerUnit × VolumeCap, pays
// one perGateFee PER CROSSING, and what remains must still clear MinBidMargin over every unit it
// moved.
//
// The CALLER states how many crossings, because that depends on what the trip is: a hull dedicated
// to a lane re-crosses to re-buy, and the fee is charged per ORIGIN gate, so its round trip is
// billed for the return leg at the same rate as the outbound one.
//
// Charging the crossing against the FLOOR rather than against break-even is what makes this a
// census guard and not an accounting note. A trip that merely out-earns its gates leaves a realised
// per-unit margin the executor refuses on sight, so counting it demands a hull for work that will
// not be flown. MinBidMargin is not lowered here; it is applied to a net quantity.
//
// A sink that absorbs nothing is refused outright: zero units earn zero at any spread, and the
// inequality alone would read that as clearing a zero floor. Impossible inputs (negative hops or
// fee) fail closed rather than letting two negatives multiply into a phantom credit inside a money
// guard, and the arithmetic widens to int64 before multiplying so a deep lane cannot overflow into
// a negative gross. gateCrossings is 0 within one system, where this reduces exactly to ClearsFloor.
func (l ArbitrageLane) ClearsFloorAfterGates(gateCrossings int, perGateFee int64) bool {
	if gateCrossings < 0 || perGateFee < 0 || l.VolumeCap <= 0 {
		return false
	}
	net := int64(l.SpreadPerUnit)*int64(l.VolumeCap) - int64(gateCrossings)*perGateFee
	return net >= int64(MinBidMargin)*int64(l.VolumeCap)
}

// FirstDisciplinedLane returns the highest-ranked lane whose per-unit spread clears
// the bid-floor discipline (ClearsFloor), or ok=false when no lane does. The input
// must be RankSpreads-ordered, so the walk yields the DEEPEST volume-capped lane the
// executor will actually fly — MarginAlive holds on its first observation.
//
// This reconciles scan ranking with execution discipline: the scan ranks
// by volume-capped spread and deliberately keeps sub-floor lanes visible (it is an
// observation tool), but the top capped-spread lane can have a per-unit spread below
// the floor. Selecting that lane makes the executor refuse it on the first visit and
// fly zero. Selecting the first lane that clears the floor guarantees a selected lane
// flies ≥1 visit instead of a silent zero-visit run.
func FirstDisciplinedLane(lanes []ArbitrageLane) (ArbitrageLane, bool) {
	for _, l := range lanes {
		if l.ClearsFloor() {
			return l, true
		}
	}
	return ArbitrageLane{}, false
}

// RankSpreads ranks the single best arbitrage lane per good across a system's
// cached market listings, using CORRECTED market-perspective column semantics:
// profit/unit = destination Bid (BUY column) − source Ask (SELL column).
//
// Lanes are ranked by volume-capped spread (per-unit spread × min tradable
// volume), not by raw per-unit spread: a fat spread on a thin market is worth
// less than a modest spread on a deep one (market-doctrine — size to absorption,
// not to the biggest hold).
//
// For each good it selects the ordered (source, dest) market pair, source
// waypoint ≠ dest waypoint, that maximises the volume-capped spread; goods with
// no positive-spread pair are omitted. Output is ordered by CappedSpread desc,
// then SpreadPerUnit desc, then good symbol asc for stable ranking.
func RankSpreads(listings []GoodListing) []ArbitrageLane {
	byGood := tradeableByGood(listings)

	lanes := make([]ArbitrageLane, 0, len(byGood))
	for good, markets := range byGood {
		if lane, ok := bestLaneForGood(good, markets); ok {
			lanes = append(lanes, lane)
		}
	}

	sortLanes(lanes)
	return lanes
}

// EnumerateLanes returns EVERY flyable (source, dest) pair of every good in the listings — a
// CENSUS of the profitable work on offer, where RankSpreads is a SELECTION over the same pairs.
//
// The two answer different questions and are not interchangeable. "Which lane should this hull
// fly" wants one lane per good; "how much profitable work exists" wants all of them, and counting
// with the selector silently collapses each good's whole lane set to its single best member.
//
// Callers pooling listings from several systems get cross-system lanes here, which RankSpreads
// would also collapse. Whether a hull can REACH the far end is the caller's constraint to apply:
// this is a pure price census and knows nothing about routes.
func EnumerateLanes(listings []GoodListing) []ArbitrageLane {
	var lanes []ArbitrageLane
	WalkLanes(listings, func(l ArbitrageLane) { lanes = append(lanes, l) })

	sortLanes(lanes)
	return lanes
}

// WalkLanes visits the same lanes EnumerateLanes returns without ever holding them all — the shape
// a caller that only counts or folds its survivors wants, since the pair count is QUADRATIC in the
// listings and pooling several systems' markets is what makes that bite.
//
// Goods are visited one at a time and CONTIGUOUSLY, so per-good bookkeeping can be scoped to the
// good in hand. Order is otherwise unspecified; a caller needing a ranking wants EnumerateLanes.
func WalkLanes(listings []GoodListing, visit func(ArbitrageLane)) {
	for good, markets := range tradeableByGood(listings) {
		walkLaneCandidates(good, markets, visit)
	}
}

// tradeableByGood groups listings by good, dropping the crossed quotes that are impossible data
// rather than bargains (IsCrossed).
func tradeableByGood(listings []GoodListing) map[string][]GoodListing {
	byGood := make(map[string][]GoodListing)
	for _, l := range listings {
		if l.IsCrossed() {
			continue
		}
		byGood[l.Good] = append(byGood[l.Good], l)
	}
	return byGood
}

// sortLanes orders lanes by volume-capped spread desc, then per-unit spread desc, then good asc;
// the waypoint tie-break makes a census of several lanes per good deterministic despite the map
// iteration that produced it.
func sortLanes(lanes []ArbitrageLane) {
	sort.SliceStable(lanes, func(i, j int) bool {
		if lanes[i].CappedSpread != lanes[j].CappedSpread {
			return lanes[i].CappedSpread > lanes[j].CappedSpread
		}
		if lanes[i].SpreadPerUnit != lanes[j].SpreadPerUnit {
			return lanes[i].SpreadPerUnit > lanes[j].SpreadPerUnit
		}
		if lanes[i].Good != lanes[j].Good {
			return lanes[i].Good < lanes[j].Good
		}
		if lanes[i].SourceWaypoint != lanes[j].SourceWaypoint {
			return lanes[i].SourceWaypoint < lanes[j].SourceWaypoint
		}
		return lanes[i].DestWaypoint < lanes[j].DestWaypoint
	})
}

// RankSpreadsForHold ranks lanes exactly as RankSpreads does, then re-orders them
// by a hold-fit-weighted score so a big hull is not sent to crush a lane far
// shallower than its hold.
//
// RankSpreads alone ranks by raw CappedSpread (SpreadPerUnit × VolumeCap), which
// rewards a thin, deep-spread lane exactly as if any hull could fill it — but a
// lane's VolumeCap is a market-absorption bound, not a hold-sized one. A hull far
// bigger than VolumeCap will not clear a single tranche at that depth before
// moving the price; a lane whose cap is a large fraction of the hold is the one
// the hull can actually absorb. shipCapacity <= 0 (no ship context) falls back to
// plain RankSpreads ordering unchanged.
//
// This is a standalone, single-system convenience path (and the direct subject of
// this package's hold-fit tests below). The production multi-system coordinator
// does NOT compose this function — it calls HoldFitWeight directly inside its own
// unified ranking pass (application-layer rankLanesByCircuitRate), because that
// pass must ALSO apply a cross-system gate penalty, and two independent
// from-scratch re-rankings cannot be chained (each recomputes its score purely
// from a lane's own fields, so whichever runs last silently discards the other's
// reordering). Keep this wrapper's behavior correct in isolation, but do not
// assume it is the code path actually exercised in production lane selection.
func RankSpreadsForHold(listings []GoodListing, shipCapacity int) []ArbitrageLane {
	ranked := RankSpreads(listings)
	if shipCapacity <= 0 {
		return ranked
	}
	return reorderByHoldFit(ranked, shipCapacity)
}

// HoldFitWeight scores how much of a lane's volume cap the hull can actually
// absorb, as a fraction of the hold: min(volumeCap, shipCapacity) / shipCapacity.
// A lane whose cap meets or exceeds the hold saturates to 1.0 (full hold-fit); a
// lane whose cap is a small sliver of the hold scores close to 0.
//
// Exported so the application-layer coordinator can fold hold-fit directly into
// its own unified ranking pass (rankLanesByCircuitRate) alongside the
// cross-system gate penalty, rather than chaining two separate from-scratch
// re-rankings that would silently cancel each other out. Callers must guard
// shipCapacity > 0 themselves; this function does not special-case shipCapacity
// <= 0 (reorderByHoldFit's caller, RankSpreadsForHold, guarantees it here).
func HoldFitWeight(volumeCap, shipCapacity int) float64 {
	effective := volumeCap
	if effective > shipCapacity {
		effective = shipCapacity
	}
	if effective < 0 {
		effective = 0
	}
	return float64(effective) / float64(shipCapacity)
}

// reorderByHoldFit re-ranks lanes by CappedSpread × HoldFitWeight, descending,
// tie-broken by the lane's real SpreadPerUnit desc then Good asc — the same
// ranking-only-adjustment contract rankLanesByCircuitRate follows elsewhere: it
// reorders the slice but never mutates a lane's real, unpenalized economics
// (SpreadPerUnit, VolumeCap, CappedSpread all pass through untouched).
func reorderByHoldFit(lanes []ArbitrageLane, shipCapacity int) []ArbitrageLane {
	type scoredLane struct {
		lane  ArbitrageLane
		score float64
	}

	scored := make([]scoredLane, len(lanes))
	for i, l := range lanes {
		scored[i] = scoredLane{
			lane:  l,
			score: float64(l.CappedSpread) * HoldFitWeight(l.VolumeCap, shipCapacity),
		}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if scored[i].lane.SpreadPerUnit != scored[j].lane.SpreadPerUnit {
			return scored[i].lane.SpreadPerUnit > scored[j].lane.SpreadPerUnit
		}
		return scored[i].lane.Good < scored[j].lane.Good
	})

	result := make([]ArbitrageLane, len(scored))
	for i, s := range scored {
		result[i] = s.lane
	}
	return result
}

// bestLaneForGood picks the ordered (source, dest) market pair, source waypoint ≠
// dest waypoint, that maximises the volume-capped spread for a single good. It
// returns ok=false when the good trades in fewer than two distinct markets or no
// pair yields a positive per-unit spread (destBid − sourceAsk > 0).
//
// Volume — which interacts non-monotonically with per-unit spread — decides the
// winner, rather than assuming cheapest-ask × richest-bid is always deepest.
func bestLaneForGood(good string, markets []GoodListing) (ArbitrageLane, bool) {
	var best ArbitrageLane
	found := false

	walkLaneCandidates(good, markets, func(candidate ArbitrageLane) {
		if !found || betterLane(candidate, best) {
			best = candidate
			found = true
		}
	})

	return best, found
}

// walkLaneCandidates visits every ordered (source, dest) pair for one good that a hull could fly:
// distinct waypoints, an eligible sink, and a positive per-unit spread. It is the single statement
// of what makes a pair tradeable, shared by lane selection and the lane census so the two cannot
// come to disagree about which pairs exist. A visitor rather than a slice because the pair count is
// quadratic and a census over pooled markets must not hold it.
func walkLaneCandidates(good string, markets []GoodListing, visit func(ArbitrageLane)) {
	for si := range markets {
		source := markets[si]
		for di := range markets {
			dest := markets[di]
			if source.Waypoint == dest.Waypoint {
				continue
			}

			// Never sell into an EXPORT market's bid: an exporter's Bid is a low sellback
			// price, not a real import sink. A valid sink is IMPORT or EXCHANGE. An
			// unknown/empty trade type is left eligible (fail-open on missing data,
			// matching the manufacturing sell_market_distributor reference filter).
			if dest.TradeType == tradeTypeExport {
				continue
			}

			spreadPerUnit := dest.Bid - source.Ask
			if spreadPerUnit <= 0 {
				continue
			}

			volumeCap := source.Volume
			if dest.Volume < volumeCap {
				volumeCap = dest.Volume
			}

			visit(ArbitrageLane{
				Good:           good,
				SourceWaypoint: source.Waypoint,
				DestWaypoint:   dest.Waypoint,
				SourceAsk:      source.Ask,
				DestBid:        dest.Bid,
				SourceSupply:   source.Supply,
				DestActivity:   dest.Activity,
				SpreadPerUnit:  spreadPerUnit,
				VolumeCap:      volumeCap,
				CappedSpread:   spreadPerUnit * volumeCap,
			})
		}
	}
}

// betterLane reports whether a should outrank b for the same good: deeper
// volume-capped spread wins, then higher per-unit spread as a tie-break.
func betterLane(a, b ArbitrageLane) bool {
	if a.CappedSpread != b.CappedSpread {
		return a.CappedSpread > b.CappedSpread
	}
	return a.SpreadPerUnit > b.SpreadPerUnit
}

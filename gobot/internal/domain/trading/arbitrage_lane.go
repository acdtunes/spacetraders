package trading

import (
	"sort"
	"time"
)

// tradeTypeExport is the GoodListing.TradeType value for a market that PRODUCES and
// sells a good (an exporter). Its Bid is a low sellback price, so it can never be a
// sell destination (sp-9mkf Bug 3). Matches domain/market.TradeTypeExport without
// importing that package into this pure-domain ranking code.
const tradeTypeExport = "EXPORT"

// GoodListing is one cached market's trade data for a single good, expressed in
// SpaceTraders' MARKET-perspective column semantics — the "inverted-margin trap"
// that overstates every spread ~2x when read backwards (market-doctrine):
//
//   - Bid = the market's BUY price (the BUY / PurchasePrice column) = what a ship
//     RECEIVES per unit when SELLING this good TO the market.
//   - Ask = the market's SELL price (the SELL / SellPrice column) = what a ship
//     PAYS per unit when BUYING this good FROM the market.
//
// A profitable lane BUYS at a source market's Ask and SELLS at a destination
// market's Bid, so profit per unit = destination.Bid − source.Ask. Buy at
// exporters (low Ask), sell at importers (high Bid).
type GoodListing struct {
	Good      string
	Waypoint  string
	TradeType string // EXPORT, IMPORT, or EXCHANGE
	Bid       int    // market BUY price (PurchasePrice): received selling TO it
	Ask       int    // market SELL price (SellPrice): paid buying FROM it
	Supply    string
	Activity  string
	Volume    int
	// ObservedAt is when the source market snapshot these prices came from was
	// last refreshed into the cache (market.Market.LastUpdated). The ranker uses
	// it to reject lanes priced from observations older than maxListingAge, so a
	// lane whose cached spread has since moved cannot win selection and execute at
	// stale prices (sp-xwa1). Zero value means "unknown age" — treated as fresh so
	// callers that don't populate it (older tests) rank unchanged.
	ObservedAt time.Time
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

// FirstDisciplinedLane returns the highest-ranked lane whose per-unit spread clears
// the bid-floor discipline (ClearsFloor), or ok=false when no lane does. The input
// must be RankSpreads-ordered, so the walk yields the DEEPEST volume-capped lane the
// executor will actually fly — MarginAlive holds on its first observation.
//
// This reconciles scan ranking with execution discipline (sp-sh6w): the scan ranks
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
	byGood := make(map[string][]GoodListing)
	for _, l := range listings {
		byGood[l.Good] = append(byGood[l.Good], l)
	}

	lanes := make([]ArbitrageLane, 0, len(byGood))
	for good, markets := range byGood {
		if lane, ok := bestLaneForGood(good, markets); ok {
			lanes = append(lanes, lane)
		}
	}

	sort.SliceStable(lanes, func(i, j int) bool {
		if lanes[i].CappedSpread != lanes[j].CappedSpread {
			return lanes[i].CappedSpread > lanes[j].CappedSpread
		}
		if lanes[i].SpreadPerUnit != lanes[j].SpreadPerUnit {
			return lanes[i].SpreadPerUnit > lanes[j].SpreadPerUnit
		}
		return lanes[i].Good < lanes[j].Good
	})

	return lanes
}

// RankSpreadsForHold ranks lanes exactly as RankSpreads does, then re-orders them
// by a hold-fit-weighted score so a big hull is not sent to crush a lane far
// shallower than its hold (sp-pnx0 — a 225-cargo heavy on a vol-20 lane bought
// loads that drove the source price up and crushed the destination sink, earning
// like a light ship while risking the treasury like a heavy one).
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
// unified ranking pass (application-layer rankLanesWithGatePenalty), because that
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
// its own unified ranking pass (rankLanesWithGatePenalty) alongside the
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
// ranking-only-adjustment contract rankLanesWithGatePenalty follows elsewhere: it
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
// Markets per good are few, so the O(n²) pair scan is trivial and lets volume —
// which interacts non-monotonically with per-unit spread — decide the winner
// rather than assuming cheapest-ask × richest-bid is always deepest.
func bestLaneForGood(good string, markets []GoodListing) (ArbitrageLane, bool) {
	var best ArbitrageLane
	found := false

	for si := range markets {
		source := markets[si]
		for di := range markets {
			dest := markets[di]
			if source.Waypoint == dest.Waypoint {
				continue
			}

			// sp-9mkf (Bug 3): never sell into an EXPORT market's bid. An exporter's Bid
			// is a low sellback price, not a real import sink (LAB_INSTRUMENTS dumped into
			// the exporter C37 at 2,347/u). A valid sink is IMPORT or EXCHANGE. An
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
			cappedSpread := spreadPerUnit * volumeCap

			candidate := ArbitrageLane{
				Good:           good,
				SourceWaypoint: source.Waypoint,
				DestWaypoint:   dest.Waypoint,
				SourceAsk:      source.Ask,
				DestBid:        dest.Bid,
				SourceSupply:   source.Supply,
				DestActivity:   dest.Activity,
				SpreadPerUnit:  spreadPerUnit,
				VolumeCap:      volumeCap,
				CappedSpread:   cappedSpread,
			}

			if !found || betterLane(candidate, best) {
				best = candidate
				found = true
			}
		}
	}

	return best, found
}

// betterLane reports whether a should outrank b for the same good: deeper
// volume-capped spread wins, then higher per-unit spread as a tie-break.
func betterLane(a, b ArbitrageLane) bool {
	if a.CappedSpread != b.CappedSpread {
		return a.CappedSpread > b.CappedSpread
	}
	return a.SpreadPerUnit > b.SpreadPerUnit
}

package trading

import "sort"

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

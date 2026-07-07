package trading

import "testing"

// spreadFixture is a hand-computed, four-good system used to pin RankSpreads'
// arithmetic and ordering. The numbers are chosen so that:
//
//   - FIREARMS has the deepest volume-capped spread and must rank first even
//     though GADGETS has a HIGHER per-unit spread (proves volume-capping, not raw
//     per-unit spread, drives the ranking).
//   - MICROPROCESSORS appears in only one market → no pair → omitted.
//   - WATER has no positive-spread pair → omitted.
//
// Column semantics (market-doctrine): Bid = BUY column = what we RECEIVE selling
// TO the market; Ask = SELL column = what we PAY buying FROM the market. A lane
// buys at a source's Ask (an exporter) and sells at a dest's Bid (an importer).
func spreadFixture() []GoodListing {
	return []GoodListing{
		// FIREARMS: buy at E41 (export, Ask 300), sell at J56 (import, Bid 900).
		// spread/unit = 900 − 300 = 600; cap = min(60,20) = 20; capped = 12000.
		{Good: "FIREARMS", Waypoint: "X1-SYS-E41", TradeType: "EXPORT", Bid: 250, Ask: 300, Supply: "MODERATE", Activity: "STRONG", Volume: 60},
		{Good: "FIREARMS", Waypoint: "X1-SYS-J56", TradeType: "IMPORT", Bid: 900, Ask: 950, Supply: "SCARCE", Activity: "GROWING", Volume: 20},

		// GADGETS: buy at A1 (export, Ask 100), sell at B2 (import, Bid 1100).
		// spread/unit = 1000 (HIGHER than FIREARMS) but cap = min(2,40) = 2, so
		// capped = 2000 — must rank BELOW FIREARMS.
		{Good: "GADGETS", Waypoint: "X1-SYS-A1", TradeType: "EXPORT", Bid: 80, Ask: 100, Supply: "HIGH", Activity: "WEAK", Volume: 2},
		{Good: "GADGETS", Waypoint: "X1-SYS-B2", TradeType: "IMPORT", Bid: 1100, Ask: 1150, Supply: "SCARCE", Activity: "GROWING", Volume: 40},

		// MICROPROCESSORS: single market → no source/dest pair → omitted.
		{Good: "MICROPROCESSORS", Waypoint: "X1-SYS-C3", TradeType: "EXPORT", Bid: 950, Ask: 1000, Supply: "MODERATE", Activity: "STRONG", Volume: 10},

		// WATER: two markets but every cross bid ≤ ask → no positive spread → omitted.
		{Good: "WATER", Waypoint: "X1-SYS-D4", TradeType: "EXCHANGE", Bid: 50, Ask: 60, Supply: "ABUNDANT", Activity: "STRONG", Volume: 100},
		{Good: "WATER", Waypoint: "X1-SYS-E5", TradeType: "EXCHANGE", Bid: 55, Ask: 65, Supply: "ABUNDANT", Activity: "STRONG", Volume: 100},
	}
}

func TestRankSpreads_MatchesHandComputedRankingAndVolumeCap(t *testing.T) {
	lanes := RankSpreads(spreadFixture())

	if len(lanes) != 2 {
		t.Fatalf("expected 2 profitable lanes (FIREARMS, GADGETS); MICROPROCESSORS and WATER must be omitted, got %d: %+v", len(lanes), lanes)
	}

	// FIREARMS ranks first on volume-capped spread despite GADGETS' higher
	// per-unit spread — this is the whole point of capping by min volume.
	top := lanes[0]
	if top.Good != "FIREARMS" {
		t.Fatalf("expected FIREARMS to rank first (capped 12000 > GADGETS 2000), got %q", top.Good)
	}
	if top.SourceWaypoint != "X1-SYS-E41" || top.DestWaypoint != "X1-SYS-J56" {
		t.Fatalf("FIREARMS lane must buy at exporter E41 and sell at importer J56, got source=%q dest=%q", top.SourceWaypoint, top.DestWaypoint)
	}
	if top.SourceAsk != 300 || top.DestBid != 900 {
		t.Fatalf("FIREARMS must pay source Ask 300 and receive dest Bid 900, got ask=%d bid=%d", top.SourceAsk, top.DestBid)
	}
	if top.SpreadPerUnit != 600 {
		t.Fatalf("FIREARMS spread/unit must be destBid(900)−sourceAsk(300)=600, got %d", top.SpreadPerUnit)
	}
	if top.VolumeCap != 20 {
		t.Fatalf("FIREARMS volume cap must be min(60,20)=20, got %d", top.VolumeCap)
	}
	if top.CappedSpread != 12000 {
		t.Fatalf("FIREARMS capped spread must be 600*20=12000, got %d", top.CappedSpread)
	}

	second := lanes[1]
	if second.Good != "GADGETS" {
		t.Fatalf("expected GADGETS to rank second, got %q", second.Good)
	}
	if second.SpreadPerUnit != 1000 {
		t.Fatalf("GADGETS spread/unit must be 1100−100=1000, got %d", second.SpreadPerUnit)
	}
	if second.VolumeCap != 2 {
		t.Fatalf("GADGETS volume cap must be min(2,40)=2, got %d", second.VolumeCap)
	}
	if second.CappedSpread != 2000 {
		t.Fatalf("GADGETS capped spread must be 1000*2=2000, got %d", second.CappedSpread)
	}
}

// TestRankSpreads_InvertedColumnGuard is the inverted-margin-trap sentinel. With
// the CORRECT column semantics, FIREARMS' spread/unit is destBid(900) −
// sourceAsk(300) = 600. An implementation that reads the columns backwards —
// treating the SELL column (Ask) as revenue and the BUY column (Bid) as cost —
// would instead compute destAsk(950) − sourceBid(250) = 700 (~2x-family error)
// and could even reverse the buy/sell direction. This test fails loudly for any
// such inversion.
func TestRankSpreads_InvertedColumnGuard(t *testing.T) {
	lanes := RankSpreads([]GoodListing{
		{Good: "FIREARMS", Waypoint: "X1-SYS-E41", TradeType: "EXPORT", Bid: 250, Ask: 300, Volume: 60},
		{Good: "FIREARMS", Waypoint: "X1-SYS-J56", TradeType: "IMPORT", Bid: 900, Ask: 950, Volume: 20},
	})

	if len(lanes) != 1 {
		t.Fatalf("expected exactly 1 FIREARMS lane, got %d", len(lanes))
	}
	lane := lanes[0]

	const invertedSpread = 700 // destAsk(950) − sourceBid(250): the wrong reading
	if lane.SpreadPerUnit == invertedSpread {
		t.Fatalf("spread/unit is %d — columns are inverted (using Ask as revenue / Bid as cost); must be destBid−sourceAsk", lane.SpreadPerUnit)
	}
	if lane.SpreadPerUnit != 600 {
		t.Fatalf("spread/unit must be destBid(900)−sourceAsk(300)=600, got %d", lane.SpreadPerUnit)
	}
	// Direction must be buy-at-exporter (E41) → sell-at-importer (J56), never reversed.
	if lane.SourceWaypoint != "X1-SYS-E41" || lane.DestWaypoint != "X1-SYS-J56" {
		t.Fatalf("inverted direction: must buy at exporter E41 and sell at importer J56, got source=%q dest=%q", lane.SourceWaypoint, lane.DestWaypoint)
	}
}

package trading

import "testing"

// A CROSSED quote — one waypoint asking LESS than it bids — is impossible market
// data, and ranking it is how sp-en5h7's corrupted rows turn into spend.
//
// Every market_data row written before the sp-en5h7 fix holds its two prices
// transposed. Read with the CORRECTED mapping (Ask←purchase_price,
// Bid←sell_price) such a row reports an ask BELOW its bid, which reads as free
// money: buy at the low ask, sell at the high bid. bestLaneForGood only refuses
// a pair sharing a waypoint, so two legacy rows at DIFFERENT waypoints produce a
// lane whose spread is roughly the true spread doubled — the "inverted-margin
// trap" the package doc already warns about, arriving through the data instead
// of through the wiring.
//
// The guard exists so the correction is safe in EITHER deploy order: a legacy row
// that reaches the ranker is refused, never traded. It can only ever REMOVE
// lanes (RULINGS #4 — money guards fail closed and may only get stricter).

// MACHINERY as it is actually stored today at a previous market: the API quoted
// purchasePrice 6334 / sellPrice 3123, and the transposed writer persisted
// purchase_price=3123, sell_price=6334. The corrected reader therefore hands the
// ranker Ask=3123 with Bid=6334.
func legacyMachineryListing(waypoint string) GoodListing {
	return GoodListing{
		Good: "MACHINERY", Waypoint: waypoint, TradeType: "IMPORT",
		Ask: 3123, Bid: 6334,
		Supply: "MODERATE", Activity: "STRONG", Volume: 20,
	}
}

func TestRankSpreads_RefusesCrossedQuotes(t *testing.T) {
	cases := []struct {
		name     string
		listings []GoodListing
	}{
		{
			// The phantom this guard exists for. Spread would be 6334−3123 = 3211
			// per unit, clearing MinBidMargin (1000) more than three times over, on
			// a lane that does not exist. Without the guard the planner buys.
			name: "legacy transposed rows at two waypoints",
			listings: []GoodListing{
				legacyMachineryListing("X1-DA89-DC6A"),
				legacyMachineryListing("X1-DA89-B7"),
			},
		},
		{
			// Same refusal for a quote the API itself might one day emit crossed:
			// the guard is a statement about impossible data, not about legacy rows.
			name: "genuinely crossed quote from a live source",
			listings: []GoodListing{
				{Good: "FUEL", Waypoint: "X1-AA1-M1", TradeType: "EXCHANGE", Ask: 68, Bid: 72, Volume: 100},
				{Good: "FUEL", Waypoint: "X1-AA1-M2", TradeType: "IMPORT", Ask: 60, Bid: 9000, Volume: 100},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if lanes := RankSpreads(tc.listings); len(lanes) != 0 {
				t.Fatalf("ranked %d lane(s) from crossed quotes, want 0 — first lane %s %s→%s spread/unit %d",
					len(lanes), lanes[0].Good, lanes[0].SourceWaypoint, lanes[0].DestWaypoint, lanes[0].SpreadPerUnit)
			}
		})
	}
}

// The guard must refuse ONLY the impossible. Corrected rows for the very same
// good still rank, so a guard that simply dropped every MACHINERY listing (or
// every listing at all) fails here — this is what stops the fix from silently
// switching the planner off.
func TestRankSpreads_StillRanksCorrectlyOrientedQuotes(t *testing.T) {
	// The same two markets with their prices stored the RIGHT way round: each
	// waypoint charges more than it pays, and the cross-market lane is real.
	lanes := RankSpreads([]GoodListing{
		{Good: "MACHINERY", Waypoint: "X1-DA89-DC6A", TradeType: "EXPORT", Ask: 3123, Bid: 3000, Volume: 20},
		{Good: "MACHINERY", Waypoint: "X1-DA89-B7", TradeType: "IMPORT", Ask: 6500, Bid: 6334, Volume: 20},
	})

	if len(lanes) != 1 {
		t.Fatalf("ranked %d lane(s) from correctly-oriented quotes, want 1", len(lanes))
	}
	got := lanes[0]
	if got.SourceWaypoint != "X1-DA89-DC6A" || got.DestWaypoint != "X1-DA89-B7" {
		t.Fatalf("lane %s→%s, want X1-DA89-DC6A→X1-DA89-B7 (buy at the exporter's ask, sell into the importer's bid)",
			got.SourceWaypoint, got.DestWaypoint)
	}
	// 6334 (dest bid, what we receive) − 3123 (source ask, what we pay) = 3211.
	if got.SpreadPerUnit != 3211 {
		t.Fatalf("SpreadPerUnit = %d, want 3211 (destBid 6334 − sourceAsk 3123)", got.SpreadPerUnit)
	}
	if !got.ClearsFloor() {
		t.Fatalf("a 3211/unit spread must clear MinBidMargin (%d)", MinBidMargin)
	}
}

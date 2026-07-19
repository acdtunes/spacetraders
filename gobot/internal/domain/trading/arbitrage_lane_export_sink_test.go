package trading

import "testing"

// A destination whose TradeType is EXPORT is an exporter — its Bid is
// a low sellback price, never a real import sink. The ranker must NOT select it as a
// lane destination even when destBid − sourceAsk is positive.
func TestRankSpreads_ExcludesExportMarketAsSink(t *testing.T) {
	// Two EXPORT markets for LAB_INSTRUMENTS: a cheap exporter (source) and C37 (whose
	// sellback bid gives a POSITIVE spread over the cheap ask). Without the sink filter
	// the ranker would select D15A→C37 (spread 2347−1500=847). With it, C37 is refused
	// as a sink and — with no importer present — LAB_INSTRUMENTS yields no lane at all.
	listings := []GoodListing{
		{Good: "LAB_INSTRUMENTS", Waypoint: "X1-DP51-D15A", TradeType: "EXPORT", Bid: 1200, Ask: 1500, Supply: "ABUNDANT", Activity: "WEAK", Volume: 40},
		{Good: "LAB_INSTRUMENTS", Waypoint: "X1-GQ92-C37", TradeType: "EXPORT", Bid: 2347, Ask: 2649, Supply: "MODERATE", Activity: "STRONG", Volume: 60},
	}

	lanes := RankSpreads(listings)

	for _, l := range lanes {
		if l.Good == "LAB_INSTRUMENTS" {
			t.Fatalf("LAB_INSTRUMENTS must yield NO lane — its only positive-spread dest (C37) is an EXPORTER, never a sink; got %+v", l)
		}
	}
}

// The guard is scoped strictly to the SINK: an EXPORT market remains a valid SOURCE,
// and an IMPORT/EXCHANGE destination is unaffected — a normal exporter→importer lane
// still ranks unchanged (guard-adding only).
func TestRankSpreads_ExportSourceToImportSink_StillRanks(t *testing.T) {
	listings := []GoodListing{
		{Good: "FIREARMS", Waypoint: "X1-SYS-E41", TradeType: "EXPORT", Bid: 250, Ask: 300, Supply: "MODERATE", Activity: "STRONG", Volume: 60},
		{Good: "FIREARMS", Waypoint: "X1-SYS-J56", TradeType: "IMPORT", Bid: 900, Ask: 950, Supply: "SCARCE", Activity: "GROWING", Volume: 20},
	}

	lanes := RankSpreads(listings)

	if len(lanes) != 1 || lanes[0].DestWaypoint != "X1-SYS-J56" {
		t.Fatalf("a normal exporter→importer lane must still rank (sink J56 is IMPORT), got %+v", lanes)
	}
}

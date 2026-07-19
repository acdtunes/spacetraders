package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The tour snapshot must zero the sink-side Bid for an EXPORT good so the solver —
// which admits any positive-bid market as a sell destination — cannot select an
// exporter as a sink. The Ask is preserved so the export market stays a valid BUY
// source; IMPORT/EXCHANGE bids are untouched.
func TestBuildTourSnapshot_ZeroesExportBid_PreservesImportAndExchangeBid(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-5 * time.Minute)

	repo := &snapFakeMarketRepo{
		order: map[string][]string{"X1-GQ92": {"X1-GQ92-C37", "X1-GQ92-A1", "X1-GQ92-XCH"}},
		markets: map[string]*market.Market{
			// EXPORT: C37 lists LAB_INSTRUMENTS with a low sellback bid 2347 (the trap).
			"X1-GQ92-C37": mustMarket(t, "X1-GQ92-C37", fresh,
				mustGood(t, "LAB_INSTRUMENTS", 2347, 2649, 60, "MODERATE", "STRONG", market.TradeTypeExport)),
			// IMPORT: a real lab sink, bid 6155.
			"X1-GQ92-A1": mustMarket(t, "X1-GQ92-A1", fresh,
				mustGood(t, "LAB_INSTRUMENTS", 6155, 6300, 20, "SCARCE", "STRONG", market.TradeTypeImport)),
			// EXCHANGE: bid preserved (a market you can sell into).
			"X1-GQ92-XCH": mustMarket(t, "X1-GQ92-XCH", fresh,
				mustGood(t, "FUEL", 90, 100, 100, "ABUNDANT", "STRONG", market.TradeTypeExchange)),
		},
	}
	wps := &snapFakeWaypointRepo{byS: map[string][]*shared.Waypoint{}}

	snapshot, _, err := BuildTourSnapshot(context.Background(), repo, wps, []string{"X1-GQ92"}, 1, now, time.Hour)
	if err != nil {
		t.Fatalf("BuildTourSnapshot: %v", err)
	}

	var exportRow, importRow, exchangeRow *routing.TourGoodSnapshot
	for i := range snapshot {
		switch snapshot[i].Waypoint {
		case "X1-GQ92-C37":
			exportRow = &snapshot[i]
		case "X1-GQ92-A1":
			importRow = &snapshot[i]
		case "X1-GQ92-XCH":
			exchangeRow = &snapshot[i]
		}
	}
	if exportRow == nil || importRow == nil || exchangeRow == nil {
		t.Fatalf("expected all three market rows in the snapshot, got %+v", snapshot)
	}
	if exportRow.Bid != 0 {
		t.Fatalf("EXPORT market C37 must have a zeroed sink Bid (never a sell dest), got %d", exportRow.Bid)
	}
	if exportRow.Ask != 2649 {
		t.Fatalf("EXPORT market C37 must keep its Ask 2649 (valid buy source), got %d", exportRow.Ask)
	}
	if importRow.Bid != 6155 {
		t.Fatalf("IMPORT market A1 must keep its sink Bid 6155, got %d", importRow.Bid)
	}
	if exchangeRow.Bid != 90 {
		t.Fatalf("EXCHANGE market must keep its Bid 90, got %d", exchangeRow.Bid)
	}
}

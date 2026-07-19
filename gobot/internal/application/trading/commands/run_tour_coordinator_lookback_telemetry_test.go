package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// A look-back manifest buy (buyLookbackItem) must record a tour_leg_telemetry row for
// its FULL bought units and the volume-weighted realized price, exactly like
// executeBuy's leg buys — so telemetry buy legs reconcile 1:1 (rows AND units) with
// PURCHASE_CARGO transactions. A buy that writes the ledger transaction but skips the
// telemetry row silently drops cost from any downstream $/hr netting that computes
// sells-minus-buys off telemetry alone.

// buyTelemetryLegs returns just the recorded BUY legs (a small helper so the assertions
// read cleanly). Safe to read tel.rows directly: buyLookbackItem is synchronous and no
// goroutine writes concurrently in these tests (mirrors tourFakeTelemetry.ListByPlayer,
// which also reads rows without the lock).
func buyTelemetryLegs(tel *tourFakeTelemetry) []trading.TourLegTelemetry {
	var buys []trading.TourLegTelemetry
	for _, r := range tel.rows {
		if r.IsBuy {
			buys = append(buys, r)
		}
	}
	return buys
}

// A single look-back item buy must persist ONE telemetry buy leg carrying the FULL
// units bought and the volume-weighted realized unit price. RED before the fix
// (buyLookbackItem records nothing); GREEN after. Mirrors the ceiling-abort test's
// direct drive of buyLookbackItem, but with a live ask under the ceiling so the buy
// completes.
func TestTour_Lookback_RecordsTelemetryForBuy(t *testing.T) {
	fx := lookbackFixture() // PARTS at X1-HU21-A: ask 100, trade_volume 40
	tel := &tourFakeTelemetry{}
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, tel)
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-LB-TEL", PlayerID: 1, ContainerID: "ctr-lb-tel", MinMargin: 1}
	resp := &RunTourCoordinatorResponse{}
	// Cached prices: ask 100, sink bid 300 → ceiling = min(100*1.15=115, 300-1=299) = 115.
	// Live ask 100 ≤ 115 → the buy completes, buying min(Units 20, space 100, trade_volume
	// 40) = 20 units at 100/u = 2,000 total.
	item := lookbackItem{Good: "PARTS", SourceWaypoint: "X1-HU21-A", Units: 20, SourceAsk: 100, DestBid: 300}

	got := h.buyLookbackItem(context.Background(), cmd, resp, map[string]int{}, item, 10_000_000, 10_000_000, 0)

	if got != 20 {
		t.Fatalf("expected 20 units bought, got %d", got)
	}
	if resp.TotalSpent != 2000 {
		t.Fatalf("expected 2000 spent (20 units × 100), got %d", resp.TotalSpent)
	}

	buys := buyTelemetryLegs(tel)
	if len(buys) != 1 {
		t.Fatalf("look-back buy must persist exactly one telemetry buy leg, got %d rows: %+v", len(buys), tel.rows)
	}
	row := buys[0]
	if row.Good != "PARTS" {
		t.Fatalf("telemetry buy good = %q, want PARTS", row.Good)
	}
	if row.RealizedUnits != 20 {
		t.Fatalf("telemetry must record the FULL bought units (20), got %d — a dropped/undercounted buy is the sp-rd21 inflation bug", row.RealizedUnits)
	}
	if row.RealizedUnitPrice != 100 { // VWAP = TotalCost 2000 / 20 units
		t.Fatalf("telemetry must record the volume-weighted realized price (100), got %d", row.RealizedUnitPrice)
	}
	if row.Waypoint != "X1-HU21-A" {
		t.Fatalf("telemetry waypoint = %q, want the source waypoint X1-HU21-A", row.Waypoint)
	}
	if row.TourID != "ctr-lb-tel" || row.PlayerID != 1 {
		t.Fatalf("telemetry scoping = tour %q player %d, want ctr-lb-tel / 1", row.TourID, row.PlayerID)
	}
}

// Reconciliation at the manifest level: loading a look-back manifest before a jump must
// leave telemetry buy legs that reconcile with what was actually bought (units and
// cost), so the windowed telemetry-netting rate does not over-count profit by omitting
// the look-back buy leg. Drives loadLookbackManifest end-to-end (build → buy → record).
func TestTour_Lookback_ManifestBuysReconcileWithTelemetry(t *testing.T) {
	fx := lookbackFixture() // HU21 EXPORTS PARTS (ask 100, vol 40); UQ16 IMPORTS PARTS (bid 300, vol 40)
	tel := &tourFakeTelemetry{}
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, tel)
	// No API client wired → the working-capital reserve guard is off, so the manifest buy
	// completes (the reserve-floor path is covered by TestTour_Lookback_ReserveFloorSkipsBuy).
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "TOUR-LB-RECON", PlayerID: 1, ContainerID: "ctr-lb-recon"}
	resp := &RunTourCoordinatorResponse{}

	loaded := h.loadLookbackManifest(context.Background(), cmd, resp, map[string]int{}, "X1-HU21", "X1-UQ16", 10_000_000, 0)

	if loaded <= 0 {
		t.Fatalf("expected the PARTS manifest to load, got %d units", loaded)
	}

	buys := buyTelemetryLegs(tel)
	if len(buys) == 0 {
		t.Fatalf("the look-back manifest bought %d units but recorded ZERO telemetry buy legs — the sp-rd21 drop", loaded)
	}
	buyUnits, buyGross := 0, 0
	for _, r := range buys {
		buyUnits += r.RealizedUnits
		buyGross += r.RealizedUnits * r.RealizedUnitPrice
	}
	if buyUnits != loaded {
		t.Fatalf("telemetry buy units (%d) must reconcile with the units actually loaded (%d)", buyUnits, loaded)
	}
	// Cost booked on the response must equal the telemetry buy gross (rows × VWAP).
	if int64(buyGross) != resp.TotalSpent {
		t.Fatalf("telemetry buy gross (%d) must reconcile with realized spend (%d)", buyGross, resp.TotalSpent)
	}
}

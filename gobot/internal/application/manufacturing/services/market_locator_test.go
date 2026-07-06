package services

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

func newScannedMarket(t *testing.T, waypointSymbol, good, supply, activity string) *market.Market {
	t.Helper()
	tradeGood, err := market.NewTradeGood(good, &supply, &activity, 100, 90, 40, market.TradeTypeExport)
	if err != nil {
		t.Fatalf("NewTradeGood(%s): %v", good, err)
	}
	m, err := market.NewMarket(waypointSymbol, []market.TradeGood{*tradeGood}, time.Now())
	if err != nil {
		t.Fatalf("NewMarket(%s): %v", waypointSymbol, err)
	}
	return m
}

// Reproduces the bug: GetMarketData returns (nil, nil) for a waypoint that has
// a marketplace but was never scanned. FindBestExportMarket only checked err,
// then called marketData.FindGood on the nil market and panicked.
func TestFindBestExportMarket_SkipsUnscannedWaypoints(t *testing.T) {
	scanned := newScannedMarket(t, "X1-TEST-B2", "FAB_MATS", "ABUNDANT", "STRONG")
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-TEST-A1", "X1-TEST-B2"}, // A1 never scanned
		markets:         map[string]*market.Market{"X1-TEST-B2": scanned},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindBestExportMarket(context.Background(), "FAB_MATS", "X1-TEST", 1)
	if err != nil {
		t.Fatalf("FindBestExportMarket: %v", err)
	}
	if result.WaypointSymbol != "X1-TEST-B2" {
		t.Errorf("expected scanned market X1-TEST-B2, got %s", result.WaypointSymbol)
	}
}

// When every waypoint is unscanned, the locator must return an error, not panic.
func TestFindBestExportMarket_AllWaypointsUnscanned_ReturnsError(t *testing.T) {
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{"X1-TEST-A1"},
		markets:         map[string]*market.Market{},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindBestExportMarket(context.Background(), "FAB_MATS", "X1-TEST", 1)
	if err == nil {
		t.Fatalf("expected error when no scanned market exports the good, got result %+v", result)
	}
}

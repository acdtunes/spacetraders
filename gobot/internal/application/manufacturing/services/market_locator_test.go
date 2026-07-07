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

// newTradeTypeMarket builds a single-good market with an explicit trade type so
// import/exchange sources (not just exports) can be exercised.
func newTradeTypeMarket(t *testing.T, waypointSymbol, good, supply, activity string, tradeType market.TradeType, sellPrice int) *market.Market {
	t.Helper()
	tradeGood, err := market.NewTradeGood(good, &supply, &activity, sellPrice+10, sellPrice, 40, tradeType)
	if err != nil {
		t.Fatalf("NewTradeGood(%s): %v", good, err)
	}
	m, err := market.NewMarket(waypointSymbol, []market.TradeGood{*tradeGood}, time.Now())
	if err != nil {
		t.Fatalf("NewMarket(%s): %v", waypointSymbol, err)
	}
	return m
}

// Field case (sp-r900): no EXPORT market clears the MODERATE+ floor for
// ADVANCED_CIRCUITRY (only a LIMITED exporter), but an IMPORT market holds
// ABUNDANT accumulated stock. FindConstructionSource must fall back to the
// import market as a buy source rather than reporting "no source".
func TestFindConstructionSource_FallsBackToAbundantImportMarket(t *testing.T) {
	const limitedExport = "X1-KA42-D40"
	const abundantImport = "X1-KA42-A4"

	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{limitedExport, abundantImport},
		markets: map[string]*market.Market{
			limitedExport:  newTradeTypeMarket(t, limitedExport, "ADVANCED_CIRCUITRY", "LIMITED", "RESTRICTED", market.TradeTypeExport, 5757),
			abundantImport: newTradeTypeMarket(t, abundantImport, "ADVANCED_CIRCUITRY", "ABUNDANT", "STRONG", market.TradeTypeImport, 6694),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindConstructionSource(context.Background(), "ADVANCED_CIRCUITRY", "X1-KA42", 1)
	if err != nil {
		t.Fatalf("FindConstructionSource: %v", err)
	}
	if result == nil {
		t.Fatal("expected import-market fallback source, got nil (material would stall indefinitely)")
	}
	if result.WaypointSymbol != abundantImport {
		t.Errorf("expected import fallback source %s, got %s", abundantImport, result.WaypointSymbol)
	}
}

// When a qualifying EXPORT market exists, it must be preferred over an import
// market even if the import holds more stock (exports are the cheaper source).
func TestFindConstructionSource_PrefersExportOverImport(t *testing.T) {
	const moderateExport = "X1-KA42-D45"
	const abundantImport = "X1-KA42-A4"

	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{moderateExport, abundantImport},
		markets: map[string]*market.Market{
			moderateExport: newTradeTypeMarket(t, moderateExport, "ADVANCED_CIRCUITRY", "MODERATE", "RESTRICTED", market.TradeTypeExport, 5757),
			abundantImport: newTradeTypeMarket(t, abundantImport, "ADVANCED_CIRCUITRY", "ABUNDANT", "STRONG", market.TradeTypeImport, 6694),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindConstructionSource(context.Background(), "ADVANCED_CIRCUITRY", "X1-KA42", 1)
	if err != nil {
		t.Fatalf("FindConstructionSource: %v", err)
	}
	if result == nil || result.WaypointSymbol != moderateExport {
		t.Fatalf("expected export market %s preferred, got %+v", moderateExport, result)
	}
}

// When no market qualifies (only a LIMITED exporter, no accumulated import
// stock), FindConstructionSource returns (nil, nil) so the caller defers the
// material rather than failing the pipeline.
func TestFindConstructionSource_NoQualifyingMarket_ReturnsNil(t *testing.T) {
	const limitedExport = "X1-KA42-D40"

	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{limitedExport},
		markets: map[string]*market.Market{
			limitedExport: newTradeTypeMarket(t, limitedExport, "ADVANCED_CIRCUITRY", "LIMITED", "RESTRICTED", market.TradeTypeExport, 5757),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindConstructionSource(context.Background(), "ADVANCED_CIRCUITRY", "X1-KA42", 1)
	if err != nil {
		t.Fatalf("FindConstructionSource: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil source (defer), got %+v", result)
	}
}

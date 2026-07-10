package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-9mkf (Bug 1): FindExportMarket must never source a good from an IMPORT market.
// A FOOD factory that IMPORTS fertilizers still lists a fertilizers sell_price, so the
// old trade-type-blind cheapest-ask query returned the factory itself as the "source";
// the feed was then bought and delivered (sold back) at that same waypoint — a
// guaranteed round-trip loss (−75k at FF5F). Only EXPORT/EXCHANGE markets can source.
func TestFindExportMarket_RefusesImportOnlyMarketAsSource(t *testing.T) {
	const foodFactory = "X1-UQ16-FF5F" // consumes (imports) FERTILIZERS
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{foodFactory},
		markets: map[string]*market.Market{
			foodFactory: newTradeTypeMarket(t, foodFactory, "FERTILIZERS", "MODERATE", "STRONG", market.TradeTypeImport, 470),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindExportMarket(context.Background(), "FERTILIZERS", "X1-UQ16", 1)
	if err == nil {
		t.Fatalf("FindExportMarket must refuse an IMPORT-only market as a source (round-trip loss), got %+v", result)
	}
}

// With a real EXPORT market present, FindExportMarket returns the exporter and ignores
// a co-located IMPORT listing for the same good — EVEN when the import listing's ask is
// cheaper. This is the crux of the fix: it picks the cheapest EXPORT/EXCHANGE, never
// merely the cheapest row.
func TestFindExportMarket_PrefersExporterOverCheaperImport(t *testing.T) {
	const exporter = "X1-UQ16-EX1A"
	const factory = "X1-UQ16-FF5F"
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{factory, exporter},
		markets: map[string]*market.Market{
			factory:  newTradeTypeMarket(t, factory, "FERTILIZERS", "MODERATE", "STRONG", market.TradeTypeImport, 300), // cheaper ask, but IMPORT
			exporter: newTradeTypeMarket(t, exporter, "FERTILIZERS", "ABUNDANT", "WEAK", market.TradeTypeExport, 470),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindExportMarket(context.Background(), "FERTILIZERS", "X1-UQ16", 1)
	if err != nil {
		t.Fatalf("FindExportMarket: %v", err)
	}
	if result.WaypointSymbol != exporter {
		t.Fatalf("must source from the EXPORT market %s, never the cheaper IMPORT factory %s, got %s", exporter, factory, result.WaypointSymbol)
	}
}

// An EXCHANGE market remains a valid buy source (only IMPORT is excluded).
func TestFindExportMarket_AllowsExchangeSource(t *testing.T) {
	const exchange = "X1-UQ16-XCH1"
	repo := &plannerStubMarketRepo{
		marketWaypoints: []string{exchange},
		markets: map[string]*market.Market{
			exchange: newTradeTypeMarket(t, exchange, "FERTILIZERS", "ABUNDANT", "STRONG", market.TradeTypeExchange, 420),
		},
	}
	locator := NewMarketLocator(repo, nil, nil, nil)

	result, err := locator.FindExportMarket(context.Background(), "FERTILIZERS", "X1-UQ16", 1)
	if err != nil {
		t.Fatalf("FindExportMarket must allow an EXCHANGE source: %v", err)
	}
	if result.WaypointSymbol != exchange {
		t.Fatalf("expected EXCHANGE source %s, got %s", exchange, result.WaypointSymbol)
	}
}

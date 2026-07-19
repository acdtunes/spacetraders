package ship

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// fakePriceHistoryRepo captures every RecordPriceChange call so a test can
// assert on exactly what was persisted, without a real database. Only
// RecordPriceChange is exercised by recordPriceChanges; the rest of
// market.MarketPriceHistoryRepository is implemented to satisfy the
// interface.
type fakePriceHistoryRepo struct {
	recorded []*market.MarketPriceHistory
}

func (f *fakePriceHistoryRepo) RecordPriceChange(_ context.Context, history *market.MarketPriceHistory) error {
	f.recorded = append(f.recorded, history)
	return nil
}

func (f *fakePriceHistoryRepo) GetPriceHistory(context.Context, string, string, time.Time, int) ([]*market.MarketPriceHistory, error) {
	return nil, nil
}

func (f *fakePriceHistoryRepo) GetVolatilityMetrics(context.Context, string, int) (*market.VolatilityMetrics, error) {
	return nil, nil
}

func (f *fakePriceHistoryRepo) FindMostVolatileGoods(context.Context, int, int) ([]*market.GoodVolatility, error) {
	return nil, nil
}

func (f *fakePriceHistoryRepo) GetMarketStability(context.Context, string, string, int) (*market.MarketStability, error) {
	return nil, nil
}

// recordPriceChanges is the sole write path that turns a freshly
// scanned TradeGood's Supply()/Activity() into a persisted
// MarketPriceHistory row - it's exactly the code path tier-at-capture-time
// depends on. This proves the observed tier lands in the row unchanged (not
// just that the plumbing compiles), for both the "good is new" and "good's
// price changed" trigger conditions in pricesChanged.
func TestRecordPriceChanges_ThreadsObservedTierIntoHistoryRow(t *testing.T) {
	supply := "LIMITED"
	activity := "WEAK"
	oldGood, err := market.NewTradeGood("MEDICINE", nil, nil, 1000, 1050, 20, market.TradeTypeExport)
	if err != nil {
		t.Fatalf("NewTradeGood(old): %v", err)
	}
	newGood, err := market.NewTradeGood("MEDICINE", &supply, &activity, 900, 950, 20, market.TradeTypeExport)
	if err != nil {
		t.Fatalf("NewTradeGood(new): %v", err)
	}

	existingMarket, err := market.NewMarket("X1-NK36-D39", []market.TradeGood{*oldGood}, time.Now())
	if err != nil {
		t.Fatalf("NewMarket: %v", err)
	}

	repo := &fakePriceHistoryRepo{}
	scanner := &MarketScanner{priceHistoryRepo: repo}

	scanner.recordPriceChanges(context.Background(), existingMarket, "X1-NK36-D39",
		[]market.TradeGood{*newGood}, 1, noopLogger{})

	if len(repo.recorded) != 1 {
		t.Fatalf("recorded %d price history rows, want 1", len(repo.recorded))
	}
	got := repo.recorded[0]
	if s := got.Supply(); s == nil || *s != "LIMITED" {
		t.Fatalf("Supply() = %v, want LIMITED", s)
	}
	if a := got.Activity(); a == nil || *a != "WEAK" {
		t.Fatalf("Activity() = %v, want WEAK", a)
	}
}

// TestRecordPriceChanges_NewGoodCapturesTierToo covers the "good didn't
// exist in the previous scan" branch of recordPriceChanges (as opposed to
// the "existing good's price changed" branch above) - both are independent
// paths into the same history-recording call.
func TestRecordPriceChanges_NewGoodCapturesTierToo(t *testing.T) {
	supply := "ABUNDANT"
	activity := "STRONG"
	newGood, err := market.NewTradeGood("FUEL", &supply, &activity, 90, 95, 1000, market.TradeTypeExport)
	if err != nil {
		t.Fatalf("NewTradeGood: %v", err)
	}

	existingMarket, err := market.NewMarket("X1-NK36-D39", []market.TradeGood{}, time.Now())
	if err != nil {
		t.Fatalf("NewMarket: %v", err)
	}

	repo := &fakePriceHistoryRepo{}
	scanner := &MarketScanner{priceHistoryRepo: repo}

	scanner.recordPriceChanges(context.Background(), existingMarket, "X1-NK36-D39",
		[]market.TradeGood{*newGood}, 1, noopLogger{})

	if len(repo.recorded) != 1 {
		t.Fatalf("recorded %d price history rows, want 1", len(repo.recorded))
	}
	got := repo.recorded[0]
	if s := got.Supply(); s == nil || *s != "ABUNDANT" {
		t.Fatalf("Supply() = %v, want ABUNDANT", s)
	}
	if a := got.Activity(); a == nil || *a != "STRONG" {
		t.Fatalf("Activity() = %v, want STRONG", a)
	}
}

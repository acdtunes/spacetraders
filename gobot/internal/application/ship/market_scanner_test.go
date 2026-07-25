package ship

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
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

// adversarialMarketRepo is the cached-market read behind the recent-scan gate.
// It is deliberately ADVERSARIAL: when readErr is set it STILL returns a
// perfectly fresh market, so a gate that ignores the error reuses a cache it was
// never able to read and skips a scan it owed.
type adversarialMarketRepo struct {
	scoutingQuery.MarketRepository
	waypoint    string
	lastUpdated time.Time
	readErr     error
}

func (r *adversarialMarketRepo) GetMarketData(context.Context, string, int) (*market.Market, error) {
	supply, activity := "MODERATE", "WEAK"
	g, err := market.NewTradeGood("IRON_ORE", &supply, &activity, 100, 200, 1000, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	m, err := market.NewMarket(r.waypoint, []market.TradeGood{*g}, r.lastUpdated)
	if err != nil {
		return nil, err
	}
	return m, r.readErr
}

func (r *adversarialMarketRepo) UpsertMarketData(context.Context, uint, string, []market.TradeGood, time.Time) error {
	return nil
}

// countingScanAPI records the live GetMarket calls, which is the whole
// observable of the gate: a skipped scan is a GetMarket that never fired.
type countingScanAPI struct {
	domainPorts.APIClient
	gets int
}

func (c *countingScanAPI) GetMarket(_ context.Context, _, waypointSymbol, _ string) (*domainPorts.MarketData, error) {
	c.gets++
	return &domainPorts.MarketData{Symbol: waypointSymbol}, nil
}

// The gate skips a scan on the strength of the CACHED row, so an unreadable
// cache is not evidence of freshness. A read error must fall through to a scan
// rather than silently reuse whatever the failed read happened to return.
func TestScanAndSaveMarketFresh_CacheReadError_ScansAnyway(t *testing.T) {
	const waypoint = "X1-DEDUP-MKT"
	cases := []struct {
		name     string
		readErr  error
		wantGets int
	}{
		{"readable fresh cache is reused", nil, 0},
		{"unreadable cache falls through to a scan", errors.New("db down"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &countingScanAPI{}
			scanner := NewMarketScanner(api,
				&adversarialMarketRepo{waypoint: waypoint, lastUpdated: time.Now(), readErr: tc.readErr},
				nil, nil)

			ctx := common.WithPlayerToken(context.Background(), "test-token")
			_, err := scanner.ScanAndSaveMarketFresh(ctx, 1, waypoint, 75*time.Second)

			require.NoError(t, err)
			require.Equal(t, tc.wantGets, api.gets)
		})
	}
}

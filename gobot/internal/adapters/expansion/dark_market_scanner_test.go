package expansion

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeMarketBacklogSource doubles the two persistence reads the scan-only backlog composes: the
// charted market systems (current era, fuel-excluded) and the systems the player has ANY market
// data for. It lets the subtraction logic be pinned with no DB.
type fakeMarketBacklogSource struct {
	counts     map[string]int
	scanned    map[string]float64
	countsErr  error
	scannedErr error
}

func (f *fakeMarketBacklogSource) ChartedMarketSystemCounts(_ context.Context) (map[string]int, error) {
	return f.counts, f.countsErr
}

func (f *fakeMarketBacklogSource) MaxAgeSecondsBySystem(_ context.Context, _ int) (map[string]float64, error) {
	return f.scanned, f.scannedErr
}

// The "enumerate the candidate class" proof: the dark backlog is EXACTLY the charted market systems
// (current era, fuel-excluded) MINUS the systems that already carry any player market_data. A
// scanned charted-market system is excluded; a scanned system that is not a charted market is
// irrelevant; the surviving systems keep their market count for scan-only ranking.
func TestDarkMarketScanner_ReturnsChartedMarketSystemsWithNoScanData(t *testing.T) {
	src := &fakeMarketBacklogSource{
		counts: map[string]int{
			"X1-DARK-A":  3,  // charted market, never scanned → DARK
			"X1-DARK-B":  14, // charted market, never scanned → DARK
			"X1-SCANNED": 5,  // charted market, HAS scan data → excluded
		},
		scanned: map[string]float64{
			"X1-SCANNED": 120.0,
			"X1-OTHER":   60.0, // scanned but not a charted market system → irrelevant
		},
	}
	scanner := NewDarkMarketScanner(src, 3600) // X1-SCANNED (age 120s) is well within the stale threshold → fresh

	got, err := scanner.ChartedUnscannedMarketSystems(context.Background(), 3)
	require.NoError(t, err)

	bySystem := map[string]int{}
	for _, candidate := range got {
		bySystem[candidate.SystemSymbol] = candidate.MarketCount
	}
	require.Len(t, got, 2, "charted market systems with no (or stale) player market_data are dark")
	require.Equal(t, 3, bySystem["X1-DARK-A"], "market count is carried through for scan ranking")
	require.Equal(t, 14, bySystem["X1-DARK-B"])
	require.NotContains(t, bySystem, "X1-SCANNED", "a charted market system with FRESH player market_data is not dark")
}

// COVERAGE GAP: the dark-market backlog must be "charted + has marketplaces +
// no OR STALE market_data", not just NEVER-scanned. A charted market whose price data has gone stale
// re-enters the backlog even though it carries market_data — so it is NOT in the old zero-only
// internal queue but IS in the honest backlog (and discovery_share=0 will re-declare a scan for it).
// This is test (a): the broadened backlog includes a system the old queue excluded. Removing the
// staleness branch drops X1-STALE and fails this.
func TestDarkMarketScanner_IncludesStaleMarkets_NotJustNeverScanned(t *testing.T) {
	src := &fakeMarketBacklogSource{
		counts: map[string]int{
			"X1-NEVER": 17, // charted market, prices never scanned (the live handoff gap) → dark
			"X1-STALE": 4,  // charted market, market_data 2h old → STALE, re-enters the backlog
			"X1-FRESH": 9,  // charted market, market_data 10m old → fresh, excluded
		},
		scanned: map[string]float64{
			"X1-STALE": 7200.0, // 2h — beyond the 1h stale threshold
			"X1-FRESH": 600.0,  // 10m — within threshold
		},
	}
	scanner := NewDarkMarketScanner(src, 3600) // stale after 1h

	got, err := scanner.ChartedUnscannedMarketSystems(context.Background(), 3)
	require.NoError(t, err)

	bySystem := map[string]int{}
	for _, candidate := range got {
		bySystem[candidate.SystemSymbol] = candidate.MarketCount
	}
	require.Len(t, got, 2, "the broadened backlog is charted-with-marketplaces with NO OR STALE market_data")
	require.Contains(t, bySystem, "X1-NEVER", "a never-price-scanned charted market is dark")
	require.Contains(t, bySystem, "X1-STALE", "a STALE charted market is in the honest backlog though NOT in the old zero-only queue")
	require.NotContains(t, bySystem, "X1-FRESH", "a freshly-scanned market is not dark")

	// Staleness disabled (threshold <= 0) → only NEVER-scanned systems are dark.
	zeroOnly := NewDarkMarketScanner(src, 0)
	got, err = zeroOnly.ChartedUnscannedMarketSystems(context.Background(), 3)
	require.NoError(t, err)
	require.Len(t, got, 1, "with staleness disabled only the never-scanned system is dark — the OLD internal queue")
	require.Equal(t, "X1-NEVER", got[0].SystemSymbol)
}

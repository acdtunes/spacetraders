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
	scanner := NewDarkMarketScanner(src)

	got, err := scanner.ChartedUnscannedMarketSystems(context.Background(), 3)
	require.NoError(t, err)

	bySystem := map[string]int{}
	for _, candidate := range got {
		bySystem[candidate.SystemSymbol] = candidate.MarketCount
	}
	require.Len(t, got, 2, "only charted market systems with ZERO player market_data are dark")
	require.Equal(t, 3, bySystem["X1-DARK-A"], "market count is carried through for scan-only ranking")
	require.Equal(t, 14, bySystem["X1-DARK-B"])
	require.NotContains(t, bySystem, "X1-SCANNED", "a charted market system with any player market_data is not dark")
}

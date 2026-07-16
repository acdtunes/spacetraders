package expansion

import (
	"context"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
)

// marketBacklogSource is the narrow persistence slice the scan-only backlog reads: the CHARTED
// market systems (current era, fuel-excluded — ChartedMarketSystemCounts) and the systems the
// player has ANY market_data for (MaxAgeSecondsBySystem, whose keys are exactly the already-scanned
// systems). Both are satisfied by *persistence.MarketRepositoryGORM, so the charted and scanned
// views share one era-scoped, fuel-excluded notion of a market and cannot drift.
type marketBacklogSource interface {
	ChartedMarketSystemCounts(ctx context.Context) (map[string]int, error)
	MaxAgeSecondsBySystem(ctx context.Context, playerID int) (map[string]float64, error)
}

// DarkMarketScanner backs the frontier coordinator's scan-only backlog port
// (commands.DarkMarketScanner, sp-jide). It answers "which charted MARKET systems has the player
// never scanned" by SUBTRACTING the scanned set from the charted-market set — the FULL discovered
// "dark" backlog. It is deliberately UNBOUNDED by gate hops: unlike the expansion-frontier BFS
// (which surfaces only the ~frontier subset a probe can reach in a few jumps), this is every
// charted market system with zero player market_data, the complete set the operator drains when
// expansion is paused. Era scoping lives in ChartedMarketSystemCounts (current-era waypoints only),
// so a dead-universe system can never enter the backlog.
type DarkMarketScanner struct {
	source marketBacklogSource
}

// NewDarkMarketScanner wires the scan-only backlog over the market persistence reads.
func NewDarkMarketScanner(source marketBacklogSource) *DarkMarketScanner {
	return &DarkMarketScanner{source: source}
}

// ChartedUnscannedMarketSystems returns every charted market system the player has no market_data
// for, each annotated with its marketplace-waypoint count (the scan-only ranking key). A read
// failure on either source surfaces as an error so the coordinator idles this cycle rather than
// declaring against a partial view.
func (s *DarkMarketScanner) ChartedUnscannedMarketSystems(ctx context.Context, playerID int) ([]expansionCmd.ScanCandidate, error) {
	counts, err := s.source.ChartedMarketSystemCounts(ctx)
	if err != nil {
		return nil, err
	}
	scanned, err := s.source.MaxAgeSecondsBySystem(ctx, playerID)
	if err != nil {
		return nil, err
	}

	dark := make([]expansionCmd.ScanCandidate, 0, len(counts))
	for system, count := range counts {
		if _, isScanned := scanned[system]; isScanned {
			continue // the player already has market_data here — not dark
		}
		dark = append(dark, expansionCmd.ScanCandidate{SystemSymbol: system, MarketCount: count})
	}
	return dark, nil
}

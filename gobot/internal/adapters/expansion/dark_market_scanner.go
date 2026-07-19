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
// (commands.DarkMarketScanner). It answers "which charted MARKET systems has the player
// never scanned" by SUBTRACTING the scanned set from the charted-market set — the FULL discovered
// "dark" backlog. It is deliberately UNBOUNDED by gate hops: unlike the expansion-frontier BFS
// (which surfaces only the ~frontier subset a probe can reach in a few jumps), this is every
// charted market system with zero player market_data, the complete set the operator drains when
// expansion is paused. Era scoping lives in ChartedMarketSystemCounts (current-era waypoints only),
// so a dead-universe system can never enter the backlog.
// DefaultStaleMarketSeconds is the coverage-gap staleness threshold: a charted market whose
// oldest player market_data is older than this is treated as effectively dark and re-enters the
// backlog. Set well ABOVE the freshness sizer's ~1h SLA (4h) so it catches only markets the sizer has
// ABANDONED — not its normal refresh cadence — while still surfacing genuinely stale price data the
// optimizer can no longer trust. The dominant dark set is still the NEVER-scanned systems (age
// infinite); staleness is the secondary "or stale" clause.
const DefaultStaleMarketSeconds = 4 * 60 * 60

type DarkMarketScanner struct {
	source            marketBacklogSource
	staleAfterSeconds float64
}

// NewDarkMarketScanner wires the dark-market backlog over the market persistence reads. staleAfterSeconds
// is the "or stale" threshold (see DefaultStaleMarketSeconds); a non-positive value disables staleness so
// only NEVER-scanned charted markets are dark.
func NewDarkMarketScanner(source marketBacklogSource, staleAfterSeconds float64) *DarkMarketScanner {
	return &DarkMarketScanner{source: source, staleAfterSeconds: staleAfterSeconds}
}

// ChartedUnscannedMarketSystems returns every charted market system with NO or STALE player
// market_data (coverage-gap broadening), each annotated with its marketplace-waypoint count
// (the scan ranking key). This is the WHOLE charted frontier's dark/stale set — the honest "dark-market
// backlog", broader than the old never-scanned-only queue: a system whose markets were charted but
// whose prices were never scanned (the live charting→price-scan handoff gap) is dark, and a system
// whose market_data has gone stale re-enters. A read failure on either source surfaces as an error so
// the coordinator idles this cycle rather than declaring against a partial view.
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
		age, isScanned := scanned[system]
		if isScanned && !s.isStale(age) {
			continue // the player has FRESH market_data here — not dark
		}
		// Never scanned (isScanned == false → prices never recorded, the live gap) OR stale.
		dark = append(dark, expansionCmd.ScanCandidate{SystemSymbol: system, MarketCount: count})
	}
	return dark, nil
}

// isStale reports whether a scanned system's oldest market age exceeds the staleness threshold. A
// non-positive threshold disables staleness, so only never-scanned systems are dark.
func (s *DarkMarketScanner) isStale(ageSeconds float64) bool {
	return s.staleAfterSeconds > 0 && ageSeconds > s.staleAfterSeconds
}

package expansion

import (
	"context"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
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

// scoutCoverageSource reads the live standing scout posts so the dark classification can reconcile
// market staleness against each system's OWN freshness SLA instead of the fixed DefaultStaleMarketSeconds
// bar (sp-gucu). A manned standing post scanned within its SLA is being scanned as designed — healthy,
// not dark — so its FreshnessTarget×grace replaces the fixed bar for that system. Satisfied live by
// *persistence.GormScoutPostRepository. Optional: a nil source leaves the fixed bar governing every
// system (byte-identical to pre-sp-gucu).
type scoutCoverageSource interface {
	ListActive(ctx context.Context, playerID int) ([]*domainScouting.ScoutPost, error)
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
// backlog. It is the FIXED FALLBACK bar for systems with no manned standing post; a system that IS a
// manned standing post is judged against its own per-system SLA instead (sp-gucu). Set well ABOVE the
// freshness sizer's ~1h SLA (4h) so it catches only markets the sizer has ABANDONED — not its normal
// refresh cadence — while still surfacing genuinely stale price data the optimizer can no longer trust.
// The dominant dark set is still the NEVER-scanned systems (age infinite); staleness is the secondary
// "or stale" clause.
const DefaultStaleMarketSeconds = 4 * 60 * 60

// defaultSLAGrace cushions the per-system freshness SLA (sp-gucu) so a market age hovering right at its
// target does not flap in and out of the dark census cycle to cycle. Applied ONLY to manned standing
// posts; the fixed DefaultStaleMarketSeconds bar is already well clear of the sizer's cadence and needs none.
const defaultSLAGrace = 1.25

type DarkMarketScanner struct {
	source            marketBacklogSource
	staleAfterSeconds float64
	coverage          scoutCoverageSource // sp-gucu: manned-standing-post SLA source; nil → fixed bar for all systems
	slaGrace          float64             // sp-gucu: SLA multiplier before a manned post is flagged (edge-flap cushion)
}

// NewDarkMarketScanner wires the dark-market backlog over the market persistence reads. staleAfterSeconds
// is the "or stale" threshold (see DefaultStaleMarketSeconds); a non-positive value disables staleness so
// only NEVER-scanned charted markets are dark.
func NewDarkMarketScanner(source marketBacklogSource, staleAfterSeconds float64) *DarkMarketScanner {
	return &DarkMarketScanner{source: source, staleAfterSeconds: staleAfterSeconds}
}

// SetScoutCoverageSource wires the standing-post SLA reader (sp-gucu). With it set, a charted market
// that is a MANNED STANDING post is classified against its own FreshnessTarget×grace rather than the
// fixed DefaultStaleMarketSeconds bar — so a post scanned slower than 4h but WITHIN its 4–10h SLA is no
// longer mislabeled dark (the false "nothing is draining" alarm). Leaving it unset keeps the fixed bar
// for every system (byte-identical to pre-sp-gucu).
func (s *DarkMarketScanner) SetScoutCoverageSource(c scoutCoverageSource) {
	s.coverage = c
	s.slaGrace = defaultSLAGrace
}

// ChartedUnscannedMarketSystems returns every charted market system with NO or STALE player
// market_data (coverage-gap broadening), each annotated with its marketplace-waypoint count
// (the scan ranking key). This is the WHOLE charted frontier's dark/stale set — the honest "dark-market
// backlog", broader than the old never-scanned-only queue: a system whose markets were charted but
// whose prices were never scanned (the live charting→price-scan handoff gap) is dark, and a system
// whose market_data has gone stale re-enters. Staleness is per-system (sp-gucu): a manned standing post
// is judged against its own freshness SLA, so it is dark only when stale BEYOND that SLA — not at a
// fixed 4h it is being scanned as designed to exceed. A read failure on either market source surfaces
// as an error so the coordinator idles this cycle rather than declaring against a partial view.
func (s *DarkMarketScanner) ChartedUnscannedMarketSystems(ctx context.Context, playerID int) ([]expansionCmd.ScanCandidate, error) {
	counts, err := s.source.ChartedMarketSystemCounts(ctx)
	if err != nil {
		return nil, err
	}
	scanned, err := s.source.MaxAgeSecondsBySystem(ctx, playerID)
	if err != nil {
		return nil, err
	}

	// sp-gucu: per-system staleness bar from MANNED STANDING scout posts. A system a probe is actively
	// touring is healthy within its OWN freshness SLA (4–10h by design), not at the fixed 4h bar — so its
	// SLA replaces the fixed bar. A nil/failed coverage read yields a nil map → every system keeps the
	// fixed bar (fail-safe: never HIDES a dark system, only forgoes the SLA widening).
	slaBySystem := s.mannedStandingSLASeconds(ctx, playerID)

	dark := make([]expansionCmd.ScanCandidate, 0, len(counts))
	for system, count := range counts {
		age, isScanned := scanned[system]
		if isScanned && !s.isStaleForSystem(system, age, slaBySystem) {
			continue // scanned and within THIS system's freshness bar — not dark
		}
		// Never scanned (isScanned == false → prices never recorded, the live gap) OR stale past its bar.
		dark = append(dark, expansionCmd.ScanCandidate{SystemSymbol: system, MarketCount: count})
	}
	return dark, nil
}

// isStaleForSystem reports whether a scanned system's oldest market age is past its EFFECTIVE staleness
// bar (sp-gucu): a manned standing post's own FreshnessTarget×grace when present in slaBySystem, else the
// fixed DefaultStaleMarketSeconds. This is the reconciliation — a manned post scanned within its own SLA
// is not dark even when older than the fixed 4h bar; every other system keeps the fixed bar.
func (s *DarkMarketScanner) isStaleForSystem(system string, ageSeconds float64, slaBySystem map[string]float64) bool {
	if slaSeconds, ok := slaBySystem[system]; ok {
		return ageSeconds > slaSeconds // manned standing post → its own (grace-scaled) SLA
	}
	return s.isStale(ageSeconds) // no manned standing post → fixed fallback bar
}

// mannedStandingSLASeconds maps system → FreshnessTarget×grace (seconds) for every MANNED STANDING
// scout post (sp-gucu): the systems a probe is actively touring, whose own SLA — not the fixed bar —
// decides dark. Unmanned posts (uncovered → fixed bar → dark once past 4h), sweep-once posts, and posts
// with no configured SLA are omitted. A nil source or a read error returns nil so the fixed bar governs
// (fail-safe: a coverage gap never hides a dark system, it only forgoes the SLA widening).
func (s *DarkMarketScanner) mannedStandingSLASeconds(ctx context.Context, playerID int) map[string]float64 {
	if s.coverage == nil {
		return nil
	}
	posts, err := s.coverage.ListActive(ctx, playerID)
	if err != nil {
		return nil
	}
	grace := s.slaGrace
	if grace < 1 {
		grace = defaultSLAGrace
	}
	sla := make(map[string]float64, len(posts))
	for _, p := range posts {
		if p == nil || p.Kind != domainScouting.PostKindStanding || p.MannedCount() == 0 {
			continue // only a manned standing post is "being scanned to an SLA"
		}
		target := p.FreshnessTarget.Seconds()
		if target <= 0 {
			continue // no configured SLA → let the fixed bar decide
		}
		sla[p.SystemSymbol] = target * grace
	}
	return sla
}

// isStale reports whether a scanned system's oldest market age exceeds the fixed staleness threshold. A
// non-positive threshold disables staleness, so only never-scanned systems are dark. Used as the fallback
// bar for systems with no manned standing post (sp-gucu).
func (s *DarkMarketScanner) isStale(ageSeconds float64) bool {
	return s.staleAfterSeconds > 0 && ageSeconds > s.staleAfterSeconds
}

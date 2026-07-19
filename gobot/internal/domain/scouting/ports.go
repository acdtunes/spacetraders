package scouting

import "context"

// ScoutPostRepository is the persistence port for the desired-state posts table.
// All reads are scoped to the open era so a universe reset never leaves the
// coordinator manning dead-era posts (mirrors the era-scoping of
// waypoints/gate-edges). The daemon is the only
// writer (RULINGS #3): the reconciler persists assignment changes and the
// captain's CLI edits both funnel through this port, never a config file.
type ScoutPostRepository interface {
	// ListActive returns every post owned by playerID in the open era. Returns
	// an empty slice (not an error) when there is no open era or no posts.
	ListActive(ctx context.Context, playerID int) ([]*ScoutPost, error)

	// Upsert writes the full desired state of post, keyed by (PlayerID,
	// SystemSymbol), stamping it with the open era. On insert it sets post.ID.
	// The caller owns every field — Upsert never merges — so an assignment the
	// caller did not intend to change must already be populated on post.
	Upsert(ctx context.Context, post *ScoutPost) error

	// Remove deletes the post for (playerID, systemSymbol) in the open era. It is
	// not an error to remove a post that does not exist.
	Remove(ctx context.Context, playerID int, systemSymbol string) error
}

// SystemFreshnessSnapshot is one market-bearing system's live freshness census:
// the three inputs the auto-sizer needs to size a standing post to the SLA,
// all derived from persisted scan telemetry so the coordinator holds none of it itself.
type SystemFreshnessSnapshot struct {
	SystemSymbol string
	// MarketCount is how many marketplace waypoints the system holds — the sizing
	// numerator (required_probes = ceil(markets × cycle / sla)).
	MarketCount int
	// OldestAgeSeconds is the worst-case market staleness — MAX(now - last_scan) across
	// the system's markets. It is the CLOSED-LOOP ground truth: when it breaches the SLA
	// the sizer raises demand beyond the static model regardless of what the model says.
	OldestAgeSeconds float64
	// MeasuredCycleSeconds is the empirically measured per-market scan interval (the
	// median gap between consecutive market scans in the system). 0 ⇒ not enough scan
	// telemetry yet; the coordinator seeds a default until it fills in.
	MeasuredCycleSeconds float64
	// CycleSamples is how many consecutive-interval samples back MeasuredCycleSeconds —
	// the coordinator trusts the measurement only once it clears a minimum sample floor,
	// otherwise it falls back to the fleet-wide median or the seed.
	CycleSamples int
	// Markets is the per-market age+value-weight breakdown backing the PERCENTILE
	// target: the sizer computes the (value-weighted) P90 age from these rather than reacting
	// to the tail-dominated OldestAgeSeconds (the max). EMPTY ⇒ the sizer falls back to
	// OldestAgeSeconds (max-age), so a census that predates the breakdown, and aggregate-only
	// test fixtures, keep working unchanged.
	Markets []MarketFreshnessSample
}

// MarketFreshnessSample is one market's (waypoint's) contribution to a system's freshness
// percentile: its current staleness (AgeSeconds) and its VALUE WEIGHT — the
// census adapter sets the weight to Σ(trade_volume × mid-price) across the market's goods,
// so a high-throughput arb market pulls the percentile up while a low-traffic peripheral
// straggler stays in the tolerated tail. Weight is ignored when value-weighting is off (the
// percentile is then a plain count-based nearest-rank), and an all-zero-weight system
// degrades to uniform so a missing weight source never divides by zero.
type MarketFreshnessSample struct {
	AgeSeconds float64
	Weight     float64
}

// SystemFreshnessReader supplies the per-system freshness census the market-freshness
// auto-sizer reconciles against. One call per tick returns every market-bearing
// system for the player. Satisfied by the GORM market repository, which derives all three
// fields from the market_data scan timestamps in a single pass.
type SystemFreshnessReader interface {
	SystemsFreshness(ctx context.Context, playerID int) ([]SystemFreshnessSnapshot, error)
}

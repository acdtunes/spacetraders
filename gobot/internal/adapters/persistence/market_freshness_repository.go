package persistence

import (
	"context"
	"fmt"
	"sort"
	"time"

	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MaxAgeSecondsBySystem returns, for every system with at least one cached
// market row for playerID, the current worst-case staleness in seconds —
// MAX(now - last_updated) across that system's markets, i.e. the age of the
// single OLDEST row (backs the scout_freshness_actual_seconds gauge). One
// query per sweep covers every system in a single pass rather
// than one query per POSTED system; the coordinator's sweep looks up just
// the systems it has POSTED coverage for in the returned map. System is
// derived from each row's waypoint_symbol via shared.ExtractSystemSymbol so
// this reuses the same waypoint-to-system parsing rule the rest of the
// codebase shares, instead of a dialect-specific SQL substring/group-by.
func (r *MarketRepositoryGORM) MaxAgeSecondsBySystem(
	ctx context.Context,
	playerID int,
) (map[string]float64, error) {
	var rows []struct {
		WaypointSymbol string
		LastUpdated    time.Time
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, last_updated").
		Where("player_id = ?", playerID).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to compute market freshness: %w", err)
	}

	oldest := make(map[string]time.Time)
	for _, row := range rows {
		system := shared.ExtractSystemSymbol(row.WaypointSymbol)
		if existing, ok := oldest[system]; !ok || row.LastUpdated.Before(existing) {
			oldest[system] = row.LastUpdated
		}
	}

	now := time.Now()
	ages := make(map[string]float64, len(oldest))
	for system, ts := range oldest {
		ages[system] = now.Sub(ts).Seconds()
	}

	return ages, nil
}

// SystemsFreshness returns the per-system freshness census the market-freshness
// auto-sizer reconciles against: for every system with cached market rows,
// its market count, worst-case market age, and the EMPIRICALLY MEASURED per-market scan
// cycle. All three come from the market_data scan timestamps in a single pass, so the
// coordinator holds no telemetry of its own.
//
// market_data has one row per (waypoint, good), so the per-good rows are first collapsed
// to one scan time per WAYPOINT (a market) — the latest, defensively, though a market's
// goods share one scan. The per-market cycle is then the MEDIAN gap between consecutive
// market scans in the system (MedianScanIntervalSeconds): with a single probe cycling the
// system this is exactly the market-to-market travel+scan interval; with N probes the
// interleaved scans compress it toward interval/N, which the closed-loop age feedback then
// corrects. Attributing scans to the specific probe that made them (for a pure single-probe
// cycle even under multi-probe manning) needs a scanner id on the scan row and is deferred.
//
// Each market also carries a VALUE WEIGHT = Σ(trade_volume × mid-price) over its goods
// (mid-price = (purchase+sell)/2, side-neutral), the per-market throughput proxy the sizer's
// value-weighted percentile uses so a high-value stale arb market pulls the P90 up while a
// low-traffic peripheral straggler stays in the tolerated tail. The percentile itself is computed
// IN CODE (WeightedPercentileAgeSeconds), not via SQL percentile_cont, so it is dialect-agnostic
// (the test harness is SQLite) and honors the live target_percentile / value_weighted knobs.
func (r *MarketRepositoryGORM) SystemsFreshness(
	ctx context.Context,
	playerID int,
) ([]domainScouting.SystemFreshnessSnapshot, error) {
	var rows []marketFreshnessRow

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, last_updated, trade_volume, purchase_price, sell_price, activity").
		Where("player_id = ?", playerID).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read system freshness: %w", err)
	}

	marketsBySystem := make(map[string][]marketRollup)
	for waypoint, market := range collapseToMarkets(rows) {
		system := shared.ExtractSystemSymbol(waypoint)
		marketsBySystem[system] = append(marketsBySystem[system], *market)
	}

	now := time.Now()
	out := make([]domainScouting.SystemFreshnessSnapshot, 0, len(marketsBySystem))
	for system, markets := range marketsBySystem {
		out = append(out, systemFreshnessSnapshot(system, markets, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SystemSymbol < out[j].SystemSymbol })
	return out, nil
}

type marketFreshnessRow struct {
	WaypointSymbol string
	LastUpdated    time.Time
	TradeVolume    int
	PurchasePrice  int
	SellPrice      int
	Activity       *string
}

type marketRollup struct {
	waypoint      string
	latest        time.Time
	weight        float64
	activity      string
	topGoodWeight float64
}

func (m marketRollup) freshnessSample(now time.Time) domainScouting.MarketFreshnessSample {
	return domainScouting.MarketFreshnessSample{
		AgeSeconds: now.Sub(m.latest).Seconds(),
		Weight:     m.weight,
		Activity:   m.activity,
		Waypoint:   m.waypoint,
	}
}

// Latest scan time is taken defensively: a market's goods normally share one scan. Activity
// comes from the good dominating the Σ(trade_volume × mid-price) weight.
func collapseToMarkets(rows []marketFreshnessRow) map[string]*marketRollup {
	perWaypoint := make(map[string]*marketRollup, len(rows))
	for _, row := range rows {
		market := perWaypoint[row.WaypointSymbol]
		if market == nil {
			market = &marketRollup{waypoint: row.WaypointSymbol, latest: row.LastUpdated}
			perWaypoint[row.WaypointSymbol] = market
		}
		if row.LastUpdated.After(market.latest) {
			market.latest = row.LastUpdated
		}
		midPrice := float64(row.PurchasePrice+row.SellPrice) / 2
		goodWeight := float64(row.TradeVolume) * midPrice
		market.weight += goodWeight
		if goodWeight >= market.topGoodWeight {
			market.topGoodWeight = goodWeight
			market.activity = derefString(row.Activity)
		}
	}
	return perWaypoint
}

func systemFreshnessSnapshot(system string, markets []marketRollup, now time.Time) domainScouting.SystemFreshnessSnapshot {
	oldest := markets[0].latest
	scanTimes := make([]time.Time, 0, len(markets))
	samples := make([]domainScouting.MarketFreshnessSample, 0, len(markets))
	for _, market := range markets {
		if market.latest.Before(oldest) {
			oldest = market.latest
		}
		scanTimes = append(scanTimes, market.latest)
		samples = append(samples, market.freshnessSample(now))
	}
	cycleSeconds, sampleCount := domainScouting.MedianScanIntervalSeconds(scanTimes)
	return domainScouting.SystemFreshnessSnapshot{
		SystemSymbol:         system,
		MarketCount:          len(markets),
		OldestAgeSeconds:     now.Sub(oldest).Seconds(),
		MeasuredCycleSeconds: cycleSeconds,
		CycleSamples:         sampleCount,
		Markets:              samples,
	}
}

// MarketDepthRows returns one row per (waypoint, good) for playerID from
// market_data: the system (derived from the waypoint symbol via
// shared.ExtractSystemSymbol, the codebase's one waypoint-to-system rule), the
// trade volume, and the side-neutral integer mid-price
// (purchase_price + sell_price) / 2. No filtering happens here — the sensing
// domain applies the goods whitelist and depth floor — but the read is strictly
// player-scoped: the table is multi-player and a competitor's rows would poison
// the census.
func (r *MarketRepositoryGORM) MarketDepthRows(ctx context.Context, playerID int) ([]domainScouting.MarketDepthRow, error) {
	var rows []struct {
		WaypointSymbol string
		GoodSymbol     string
		TradeVolume    int
		PurchasePrice  int
		SellPrice      int
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol, trade_volume, purchase_price, sell_price").
		Where("player_id = ?", playerID).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read market depth rows: %w", err)
	}

	out := make([]domainScouting.MarketDepthRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, domainScouting.MarketDepthRow{
			System:      shared.ExtractSystemSymbol(row.WaypointSymbol),
			Waypoint:    row.WaypointSymbol,
			Good:        row.GoodSymbol,
			TradeVolume: row.TradeVolume,
			MidPrice:    (row.PurchasePrice + row.SellPrice) / 2,
		})
	}
	return out, nil
}

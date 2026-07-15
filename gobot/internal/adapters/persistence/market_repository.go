package persistence

import (
	"context"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const marketDataTable = "market_data"

// MarketRepositoryGORM implements market persistence using GORM
type MarketRepositoryGORM struct {
	db *gorm.DB
}

// NewMarketRepository creates a new GORM-based market repository
func NewMarketRepository(db *gorm.DB) *MarketRepositoryGORM {
	return &MarketRepositoryGORM{db: db}
}

// UpsertMarketData inserts or updates market data for a waypoint
// Database schema: market_data table has one row per (waypoint, good) combination
// Primary key is (waypoint_symbol, good_symbol)
func (r *MarketRepositoryGORM) UpsertMarketData(
	ctx context.Context,
	playerID uint,
	waypointSymbol string,
	goods []market.TradeGood,
	timestamp time.Time,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete all existing trade goods for this waypoint
		// We'll re-insert them with updated data
		if err := tx.Where("player_id = ? AND waypoint_symbol = ?", playerID, waypointSymbol).
			Delete(&MarketData{}).Error; err != nil {
			return fmt.Errorf("failed to delete old market data: %w", err)
		}

		// Insert all trade goods for this waypoint
		if len(goods) > 0 {
			marketDataRecords := make([]MarketData, len(goods))
			for i, good := range goods {
				supply := good.Supply()
				activity := good.Activity()
				var tradeType *string
				if good.TradeType() != "" {
					tt := string(good.TradeType())
					tradeType = &tt
				}
				marketDataRecords[i] = MarketData{
					WaypointSymbol: waypointSymbol,
					GoodSymbol:     good.Symbol(),
					Supply:         supply,
					Activity:       activity,
					PurchasePrice:  good.PurchasePrice(),
					SellPrice:      good.SellPrice(),
					TradeVolume:    good.TradeVolume(),
					TradeType:      tradeType,
					LastUpdated:    timestamp,
					PlayerID:       int(playerID),
				}
			}

			if err := tx.Create(&marketDataRecords).Error; err != nil {
				return fmt.Errorf("failed to insert market data: %w", err)
			}
		}

		return nil
	})
}

// GetMarketData retrieves market data for a specific waypoint
// Database schema: multiple rows in market_data, one per (waypoint, good)
func (r *MarketRepositoryGORM) GetMarketData(
	ctx context.Context,
	waypointSymbol string,
	playerID int,
) (*market.Market, error) {
	// Query all goods for this waypoint
	var marketDataRecords []MarketData
	err := r.db.WithContext(ctx).
		Where("player_id = ? AND waypoint_symbol = ?", playerID, waypointSymbol).
		Find(&marketDataRecords).Error

	if err != nil {
		return nil, fmt.Errorf("failed to get market data: %w", err)
	}

	if len(marketDataRecords) == 0 {
		return nil, nil
	}

	goods, timestamp, err := recordsToGoods(marketDataRecords)
	if err != nil {
		return nil, err
	}

	return market.NewMarket(waypointSymbol, goods, timestamp)
}

func recordsToGoods(records []MarketData) ([]market.TradeGood, time.Time, error) {
	goods := make([]market.TradeGood, len(records))
	var timestamp time.Time
	for i, record := range records {
		var tradeType market.TradeType
		if record.TradeType != nil {
			tradeType = market.TradeType(*record.TradeType)
		}
		good, err := market.NewTradeGood(
			record.GoodSymbol,
			record.Supply,
			record.Activity,
			record.PurchasePrice,
			record.SellPrice,
			record.TradeVolume,
			tradeType,
		)
		if err != nil {
			return nil, timestamp, fmt.Errorf("invalid trade good in database: %w", err)
		}
		goods[i] = *good
		timestamp = record.LastUpdated
	}

	return goods, timestamp, nil
}

// ListMarketsInSystem retrieves all markets in a system, optionally filtered by age
// Database schema: multiple rows per waypoint, need to group by waypoint_symbol
func (r *MarketRepositoryGORM) ListMarketsInSystem(
	ctx context.Context,
	playerID uint,
	systemSymbol string,
	maxAgeMinutes int,
) ([]market.Market, error) {
	query := r.db.WithContext(ctx).
		Where("player_id = ? AND waypoint_symbol LIKE ?", playerID, systemSymbol+"-%")

	if maxAgeMinutes > 0 {
		cutoff := time.Now().Add(-time.Duration(maxAgeMinutes) * time.Minute)
		query = query.Where("last_updated >= ?", cutoff)
	}

	var marketDataList []MarketData
	if err := query.Find(&marketDataList).Error; err != nil {
		return nil, fmt.Errorf("failed to list markets: %w", err)
	}

	// Group records by waypoint
	waypointGoods := make(map[string][]MarketData)
	for _, record := range marketDataList {
		waypointGoods[record.WaypointSymbol] = append(waypointGoods[record.WaypointSymbol], record)
	}

	// Convert each waypoint's goods to a Market
	markets := make([]market.Market, 0, len(waypointGoods))
	for waypointSymbol, records := range waypointGoods {
		goods, timestamp, err := recordsToGoods(records)
		if err != nil {
			return nil, err
		}

		m, err := market.NewMarket(waypointSymbol, goods, timestamp)
		if err != nil {
			return nil, err
		}
		markets = append(markets, *m)
	}

	return markets, nil
}

// FindCheapestMarketSelling finds the market with the lowest sell price for a specific good in a system.
// Note: This returns any market with the good - the caller must check supply level at execution time.
// For manufacturing, the COLLECT task checks supply is HIGH/ABUNDANT before buying.
func (r *MarketRepositoryGORM) FindCheapestMarketSelling(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*market.CheapestMarketResult, error) {
	var result struct {
		WaypointSymbol string
		TradeSymbol    string
		SellPrice      int
		Supply         *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol as trade_symbol, sell_price, supply").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Order("sell_price ASC").
		Limit(1).
		Scan(&result).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find cheapest market: %w", err)
	}

	if result.WaypointSymbol == "" {
		return nil, nil
	}

	supply := derefString(result.Supply)

	return &market.CheapestMarketResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.TradeSymbol,
		SellPrice:      result.SellPrice,
		Supply:         supply,
	}, nil
}

// FindCheapestMarketsSellingAllSystems returns up to limit markets selling the
// good across ALL systems with scanned data, cheapest first. Scouts only scan
// systems the fleet can fly, so "has market data" doubles as the reachability
// filter. Used by the trade engine's demand miner (its local marketAskFinder
// port) to price cross-system SOURCE asks — NOT by contract sourcing, which is
// HOME-system only (RULINGS #14). Deliberately NOT on the MarketRepository
// interface so existing fakes keep compiling.
func (r *MarketRepositoryGORM) FindCheapestMarketsSellingAllSystems(
	ctx context.Context,
	goodSymbol string,
	playerID int,
	limit int,
) ([]market.CheapestMarketResult, error) {
	var rows []struct {
		WaypointSymbol string
		TradeSymbol    string
		SellPrice      int
		Supply         *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol as trade_symbol, sell_price, supply").
		Where("player_id = ?", playerID).
		Where("good_symbol = ?", goodSymbol).
		Order("sell_price ASC").
		Limit(limit).
		Scan(&rows).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find cheapest markets across systems: %w", err)
	}

	results := make([]market.CheapestMarketResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, market.CheapestMarketResult{
			WaypointSymbol: row.WaypointSymbol,
			TradeSymbol:    row.TradeSymbol,
			SellPrice:      row.SellPrice,
			Supply:         derefString(row.Supply),
		})
	}

	return results, nil
}

// FindCheapestMarketSellingWithSupply finds the cheapest market with a specific supply level.
// This enables supply-priority selection for raw materials: ABUNDANT > HIGH > MODERATE.
// Returns nil if no market exists with the specified supply level.
func (r *MarketRepositoryGORM) FindCheapestMarketSellingWithSupply(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
	supplyLevel string,
) (*market.CheapestMarketResult, error) {
	var result struct {
		WaypointSymbol string
		TradeSymbol    string
		SellPrice      int
		Supply         *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol as trade_symbol, sell_price, supply").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Where("supply = ?", supplyLevel).
		Order("sell_price ASC").
		Limit(1).
		Scan(&result).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find cheapest market with supply %s: %w", supplyLevel, err)
	}

	if result.WaypointSymbol == "" {
		return nil, nil // No market with this supply level
	}

	supply := derefString(result.Supply)

	return &market.CheapestMarketResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.TradeSymbol,
		SellPrice:      result.SellPrice,
		Supply:         supply,
	}, nil
}

// FindBestMarketBuying finds the market with the highest purchase price for a specific good in a system
// This returns the best market to sell to (where we get paid the most)
func (r *MarketRepositoryGORM) FindBestMarketBuying(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*market.BestMarketBuyingResult, error) {
	var result struct {
		WaypointSymbol string
		TradeSymbol    string
		PurchasePrice  int
		Supply         *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol as trade_symbol, purchase_price, supply").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Order("purchase_price DESC").
		Limit(1).
		Scan(&result).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find best market buying: %w", err)
	}

	// If no result found, return nil (not an error)
	if result.WaypointSymbol == "" {
		return nil, nil
	}

	supply := derefString(result.Supply)

	return &market.BestMarketBuyingResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.TradeSymbol,
		PurchasePrice:  result.PurchasePrice,
		Supply:         supply,
	}, nil
}

// BestSinksAcrossSystems returns, for each requested good, the single highest-bid
// sell destination ACROSS ALL SYSTEMS for the player (sp-mtvg). EXPORT markets are
// excluded so the result mirrors the tour snapshot's sink eligibility — an EXPORT bid
// is a low sellback the solver zeroes (sp-9mkf), never a real sell destination. Rows
// older than maxAge (relative to now) are excluded so a moved price never reports a
// phantom sink. A good with no fresh non-EXPORT sink is simply absent from the map.
//
// It backs the tour coordinator's out-of-horizon lane diagnostic: a returned sink whose
// SystemSymbol falls outside the 1-gate-hop tour graph is a profitable lane the planner
// structurally cannot see. Read-only and best-effort by contract (the caller treats any
// error as "no diagnostic this cycle"); it never participates in trade selection.
func (r *MarketRepositoryGORM) BestSinksAcrossSystems(
	ctx context.Context,
	goods []string,
	playerID int,
	maxAge time.Duration,
	now time.Time,
) (map[string]market.GlobalSinkResult, error) {
	out := map[string]market.GlobalSinkResult{}
	if len(goods) == 0 {
		return out, nil
	}
	var rows []struct {
		GoodSymbol     string
		WaypointSymbol string
		PurchasePrice  int
	}
	// DISTINCT ON (good_symbol) ... ORDER BY good_symbol, purchase_price DESC keeps the
	// single best (highest-bid) sink row per good in one indexed pass (Postgres).
	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("DISTINCT ON (good_symbol) good_symbol, waypoint_symbol, purchase_price").
		Where("player_id = ?", playerID).
		Where("good_symbol IN ?", goods).
		Where("last_updated >= ?", now.Add(-maxAge)).
		Where("(trade_type IS NULL OR trade_type <> ?)", string(market.TradeTypeExport)).
		Order("good_symbol, purchase_price DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to find best sinks across systems: %w", err)
	}
	for _, row := range rows {
		if row.PurchasePrice <= 0 {
			continue
		}
		out[row.GoodSymbol] = market.GlobalSinkResult{
			WaypointSymbol: row.WaypointSymbol,
			SystemSymbol:   shared.ExtractSystemSymbol(row.WaypointSymbol),
			Bid:            row.PurchasePrice,
		}
	}
	return out, nil
}

// FindAllMarketsInSystem returns all distinct market waypoint symbols in a system
// This is used for fleet rebalancing to discover all available markets
// Excludes FUEL_STATION waypoints (filters by type, not by trade good count)
func (r *MarketRepositoryGORM) FindAllMarketsInSystem(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) ([]string, error) {
	var waypoints []string

	// Query waypoints table for marketplaces excluding fuel stations
	// Same filtering logic as scout operation (assign_scouting_fleet.go:216-219).
	// Era-scoped (eraScopePredicate) exactly like GormWaypointRepository so a
	// dead-era waypoint row (sp-vapw) can never surface as a live market: this
	// query hits the waypoints table directly instead of going through the
	// era-scoped repository, so it must apply the predicate itself.
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	err := r.db.WithContext(ctx).
		Table("waypoints").
		Select("waypoint_symbol").
		Where("system_symbol = ?", systemSymbol).
		Where("type != ?", "FUEL_STATION").
		Where("traits LIKE ?", "%MARKETPLACE%").
		Where(predicate, args...).
		Pluck("waypoint_symbol", &waypoints).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find markets in system: %w", err)
	}

	return waypoints, nil
}

// MaxAgeSecondsBySystem returns, for every system with at least one cached
// market row for playerID, the current worst-case staleness in seconds —
// MAX(now - last_updated) across that system's markets, i.e. the age of the
// single OLDEST row (sp-dp92 P7: backs the scout_freshness_actual_seconds
// gauge). One query per sweep covers every system in a single pass rather
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
// auto-sizer reconciles against (sp-orgp): for every system with cached market rows,
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
func (r *MarketRepositoryGORM) SystemsFreshness(
	ctx context.Context,
	playerID int,
) ([]domainScouting.SystemFreshnessSnapshot, error) {
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
		return nil, fmt.Errorf("failed to read system freshness: %w", err)
	}

	// Collapse per-(waypoint,good) rows to one latest scan time per waypoint (market).
	perWaypoint := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		if existing, ok := perWaypoint[row.WaypointSymbol]; !ok || row.LastUpdated.After(existing) {
			perWaypoint[row.WaypointSymbol] = row.LastUpdated
		}
	}

	// Group markets by system.
	scanTimesBySystem := make(map[string][]time.Time)
	for waypoint, ts := range perWaypoint {
		system := shared.ExtractSystemSymbol(waypoint)
		scanTimesBySystem[system] = append(scanTimesBySystem[system], ts)
	}

	now := time.Now()
	out := make([]domainScouting.SystemFreshnessSnapshot, 0, len(scanTimesBySystem))
	for system, times := range scanTimesBySystem {
		oldest := times[0]
		for _, ts := range times {
			if ts.Before(oldest) {
				oldest = ts
			}
		}
		cycleSeconds, samples := domainScouting.MedianScanIntervalSeconds(times)
		out = append(out, domainScouting.SystemFreshnessSnapshot{
			SystemSymbol:         system,
			MarketCount:          len(times),
			OldestAgeSeconds:     now.Sub(oldest).Seconds(),
			MeasuredCycleSeconds: cycleSeconds,
			CycleSamples:         samples,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SystemSymbol < out[j].SystemSymbol })
	return out, nil
}

// openEraID mirrors GormWaypointRepository.openEraID: the open era is the highest
// era_id with no closed_at. nil (no open era yet) scopes the read to NULL era_id
// rows, matching the pre-close transition window. FindAllMarketsInSystem needs its
// own resolver because it queries the waypoints table directly rather than through
// GormWaypointRepository.
func (r *MarketRepositoryGORM) openEraID(ctx context.Context) *int {
	var era EraModel
	if err := r.db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error; err != nil {
		return nil
	}
	id := era.EraID
	return &id
}

// FindBestMarketForBuying finds the best market to buy a good from, scoring by trade type, supply, and activity.
// Preference order for trade type (best to worst): EXPORT > EXCHANGE > IMPORT > NULL
// Preference order for supply (best to worst): ABUNDANT > HIGH > MODERATE > LIMITED > SCARCE
// Preference order for activity (best to worst): RESTRICTED > WEAK > GROWING > STRONG
// Lower score = better market
func (r *MarketRepositoryGORM) FindBestMarketForBuying(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*market.BestBuyingMarketResult, error) {
	// Find all markets selling this good in the system
	var results []struct {
		WaypointSymbol string
		GoodSymbol     string
		SellPrice      int
		Supply         *string
		Activity       *string
		TradeType      *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol, sell_price, supply, activity, trade_type").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Scan(&results).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find markets selling %s: %w", goodSymbol, err)
	}

	if len(results) == 0 {
		return nil, nil // Not available in any market
	}

	// Score each market and find the best one
	var bestResult *market.BestBuyingMarketResult
	bestScore := 100000 // Start with a high score

	for _, r := range results {
		supply := derefString(r.Supply)
		activity := derefString(r.Activity)
		tradeType := derefString(r.TradeType)

		// Calculate score (lower is better)
		score := scoreMarketForBuying(tradeType, supply, activity)

		if bestResult == nil || score < bestScore {
			bestScore = score
			bestResult = &market.BestBuyingMarketResult{
				WaypointSymbol: r.WaypointSymbol,
				TradeSymbol:    r.GoodSymbol,
				SellPrice:      r.SellPrice,
				Supply:         supply,
				Activity:       activity,
				TradeType:      market.TradeType(tradeType),
				Score:          score,
			}
		}
	}

	return bestResult, nil
}

// scoreMarketForBuying calculates a score for a market when buying (lower = better)
// Trade Type: EXPORT(0) > EXCHANGE(1) > IMPORT(2) > NULL(3) (weight: 1000)
// Supply: ABUNDANT(0) > HIGH(1) > MODERATE(2) > LIMITED(3) > SCARCE(4) (weight: 10)
// Activity: WEAK(0) > GROWING(1) > STRONG(2) > RESTRICTED(3) (weight: 1, follows BuyerActivityScore)
//
// EXPORT markets are factories that PRODUCE the good - best prices!
// EXCHANGE markets trade goods - moderate prices
// IMPORT markets CONSUME goods - worst prices for buying
//
// Final score = trade_type_score * 1000 + supply_score * 10 + activity_score
func scoreMarketForBuying(tradeType, supply, activity string) int {
	// Trade type is most important: EXPORT markets produce goods = cheap prices
	tradeTypeScore := 3 // Unknown/NULL = worst
	switch tradeType {
	case "EXPORT":
		tradeTypeScore = 0 // Best - factory produces this good
	case "EXCHANGE":
		tradeTypeScore = 1 // OK - trading post
	case "IMPORT":
		tradeTypeScore = 2 // Worst - consumer market (expensive)
	}

	supplyScore := 5 - manufacturing.SupplyLevel(supply).Order()

	activityScore := 4 - market.ActivityLevel(activity).BuyerActivityScore()

	// Trade type weighted 1000x, supply weighted 10x, activity weighted 1x
	// This ensures EXPORT markets ALWAYS preferred over EXCHANGE over IMPORT
	return tradeTypeScore*1000 + supplyScore*10 + activityScore
}

// MarketGoodListing represents one cached market's trade data for a single good,
// including data age so callers can judge staleness before acting on it (L58:
// a stale-availability premise flipped an entire plan).
type MarketGoodListing struct {
	WaypointSymbol string
	TradeType      string
	PurchasePrice  int
	SellPrice      int
	Supply         string
	Activity       string
	TradeVolume    int
	LastUpdated    time.Time
}

// FindMarketsTradingGood returns every cached market known to trade goodSymbol,
// optionally scoped to systemSymbol. Read-only over MarketData; callers sort by
// side (buy/sell) themselves, this finder never hides staleness.
func (r *MarketRepositoryGORM) FindMarketsTradingGood(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) ([]MarketGoodListing, error) {
	query := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, trade_type, purchase_price, sell_price, supply, activity, trade_volume, last_updated").
		Where("player_id = ?", playerID).
		Where("good_symbol = ?", goodSymbol)

	if systemSymbol != "" {
		query = query.Where("waypoint_symbol LIKE ?", systemSymbol+"-%")
	}

	var rows []struct {
		WaypointSymbol string
		TradeType      *string
		PurchasePrice  int
		SellPrice      int
		Supply         *string
		Activity       *string
		TradeVolume    int
		LastUpdated    time.Time
	}

	if err := query.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to find markets trading %s: %w", goodSymbol, err)
	}

	listings := make([]MarketGoodListing, len(rows))
	for i, row := range rows {
		listings[i] = MarketGoodListing{
			WaypointSymbol: row.WaypointSymbol,
			TradeType:      derefString(row.TradeType),
			PurchasePrice:  row.PurchasePrice,
			SellPrice:      row.SellPrice,
			Supply:         derefString(row.Supply),
			Activity:       derefString(row.Activity),
			TradeVolume:    row.TradeVolume,
			LastUpdated:    row.LastUpdated,
		}
	}

	return listings, nil
}

// SystemMarketGoodListing is one cached (market, good) row within a system,
// carrying the good symbol so callers can rank cross-market spreads without a
// per-good round trip. Prices keep the market's perspective (see MarketGoodListing).
type SystemMarketGoodListing struct {
	WaypointSymbol string
	GoodSymbol     string
	TradeType      string
	PurchasePrice  int // market BUY price: what a ship RECEIVES selling TO this market
	SellPrice      int // market SELL price: what a ship PAYS buying FROM this market
	Supply         string
	Activity       string
	TradeVolume    int
	LastUpdated    time.Time
}

// FindAllGoodListingsInSystem returns every cached (market, good) row for a
// system in one read, so the arbitrage scanner can compute cross-market spreads
// from cache without a query per good. Read-only over MarketData; staleness is
// carried per row (LastUpdated) and never hidden by this finder.
func (r *MarketRepositoryGORM) FindAllGoodListingsInSystem(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) ([]SystemMarketGoodListing, error) {
	var rows []struct {
		WaypointSymbol string
		GoodSymbol     string
		TradeType      *string
		PurchasePrice  int
		SellPrice      int
		Supply         *string
		Activity       *string
		TradeVolume    int
		LastUpdated    time.Time
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol, trade_type, purchase_price, sell_price, supply, activity, trade_volume, last_updated").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to find good listings in system %s: %w", systemSymbol, err)
	}

	listings := make([]SystemMarketGoodListing, len(rows))
	for i, row := range rows {
		listings[i] = SystemMarketGoodListing{
			WaypointSymbol: row.WaypointSymbol,
			GoodSymbol:     row.GoodSymbol,
			TradeType:      derefString(row.TradeType),
			PurchasePrice:  row.PurchasePrice,
			SellPrice:      row.SellPrice,
			Supply:         derefString(row.Supply),
			Activity:       derefString(row.Activity),
			TradeVolume:    row.TradeVolume,
			LastUpdated:    row.LastUpdated,
		}
	}

	return listings, nil
}

// FindFactoryForGood finds a market that EXPORTS a specific good (i.e., a factory that produces it).
// Only returns markets where trade_type = 'EXPORT', meaning the market produces this good.
// Returns nil if no factory exists for this good in the system.
func (r *MarketRepositoryGORM) FindFactoryForGood(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*market.FactoryResult, error) {
	var result struct {
		WaypointSymbol string
		GoodSymbol     string
		SellPrice      int
		Supply         *string
		Activity       *string
	}

	// Only select markets where trade_type = 'EXPORT' (factories that produce this good)
	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol, sell_price, supply, activity").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Where("trade_type = ?", "EXPORT").
		Order("sell_price ASC"). // Prefer cheapest factory
		Limit(1).
		Scan(&result).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find factory for %s: %w", goodSymbol, err)
	}

	// If no result found, return nil (no factory exists)
	if result.WaypointSymbol == "" {
		return nil, nil
	}

	supply := derefString(result.Supply)
	activity := derefString(result.Activity)

	return &market.FactoryResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.GoodSymbol,
		SellPrice:      result.SellPrice,
		Supply:         supply,
		Activity:       activity,
	}, nil
}

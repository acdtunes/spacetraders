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

// FindCheapestMarketSelling finds the market with the lowest ASK for a specific good in a
// system — the cheapest place for US to BUY it. The ask is market_data.purchase_price (what
// we PAY, the larger of the two prices); sell_price is the bid the market pays us (sp-en5h7).
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
		PurchasePrice  int
		Supply         *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol as trade_symbol, purchase_price, supply").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Order("purchase_price ASC").
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
		Ask:            result.PurchasePrice,
		Supply:         supply,
	}, nil
}

// FindCheapestMarketsSellingAllSystems returns up to limit markets selling the
// good across ALL systems with scanned data, cheapest ASK first (purchase_price —
// what we PAY, the larger of the two prices; sp-en5h7). Scouts only scan
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
		PurchasePrice  int
		Supply         *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol as trade_symbol, purchase_price, supply").
		Where("player_id = ?", playerID).
		Where("good_symbol = ?", goodSymbol).
		Order("purchase_price ASC").
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
			Ask:            row.PurchasePrice,
			Supply:         derefString(row.Supply),
		})
	}

	return results, nil
}

// FindCheapestMarketSellingWithSupply finds the lowest-ASK market with a specific supply level
// (the ask is purchase_price — what we PAY, the larger price; sp-en5h7).
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
		PurchasePrice  int
		Supply         *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol as trade_symbol, purchase_price, supply").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Where("supply = ?", supplyLevel).
		Order("purchase_price ASC").
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
		Ask:            result.PurchasePrice,
		Supply:         supply,
	}, nil
}

// FindBestMarketBuying finds the market with the highest BID for a specific good in a system —
// the best market to SELL to (where we get paid the most). The bid is market_data.sell_price
// (what the market PAYS us, the smaller of the two prices); purchase_price is the ask we would
// pay to buy (sp-en5h7).
func (r *MarketRepositoryGORM) FindBestMarketBuying(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*market.BestMarketBuyingResult, error) {
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
		Order("sell_price DESC").
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
		Bid:            result.SellPrice,
		Supply:         supply,
	}, nil
}

// crossSystemGood is one good's best cross-system market row — the shared result shape both
// global scanners return before mapping it to their typed sink/source result.
type crossSystemGood struct {
	GoodSymbol     string
	WaypointSymbol string
	Price          int
	TradeVolume    int
}

// bestAcrossSystems is the ONE parameterized cross-system scan behind BOTH
// BestSinksAcrossSystems (best bid) and BestSourcesAcrossSystems (cheapest ask): DISTINCT ON
// (good_symbol) the best priceColumn per good across ALL SYSTEMS, with the counterpart trade
// type excluded (a sink is never an EXPORT, a source is never an IMPORT), and stale/zero-price
// rows dropped. sp-mepj discovery shares this one code path rather than a copy-pasted
// symmetric twin — the reuse invariant covers discovery too. priceColumn/priceDir/
// excludeTradeType are fixed internal column names, never caller input (no injection surface).
func (r *MarketRepositoryGORM) bestAcrossSystems(
	ctx context.Context,
	goods []string,
	playerID int,
	maxAge time.Duration,
	now time.Time,
	priceColumn, priceDir, excludeTradeType string,
) ([]crossSystemGood, error) {
	if len(goods) == 0 {
		return nil, nil
	}
	var rows []struct {
		GoodSymbol     string
		WaypointSymbol string
		Price          int
		TradeVolume    int
	}
	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select(fmt.Sprintf("DISTINCT ON (good_symbol) good_symbol, waypoint_symbol, %s AS price, trade_volume", priceColumn)).
		Where("player_id = ?", playerID).
		Where("good_symbol IN ?", goods).
		Where("last_updated >= ?", now.Add(-maxAge)).
		Where(priceColumn+" > 0").
		Where("(trade_type IS NULL OR trade_type <> ?)", excludeTradeType).
		Order(fmt.Sprintf("good_symbol, %s %s", priceColumn, priceDir)).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]crossSystemGood, 0, len(rows))
	for _, row := range rows {
		if row.Price <= 0 {
			continue
		}
		out = append(out, crossSystemGood{
			GoodSymbol:     row.GoodSymbol,
			WaypointSymbol: row.WaypointSymbol,
			Price:          row.Price,
			TradeVolume:    row.TradeVolume,
		})
	}
	return out, nil
}

// BestSinksAcrossSystems returns, for each requested good, the single highest-bid sell
// destination ACROSS ALL SYSTEMS for the player. EXPORT markets are excluded so the result
// mirrors the tour snapshot's sink eligibility — an EXPORT bid is a low sellback the solver
// zeroes, never a real sell destination. A good with no fresh non-EXPORT sink is simply
// absent from the map. It backs the tour coordinator's out-of-horizon lane diagnostic AND the
// long-haul engine's discovery. Read-only and best-effort by contract.
func (r *MarketRepositoryGORM) BestSinksAcrossSystems(
	ctx context.Context,
	goods []string,
	playerID int,
	maxAge time.Duration,
	now time.Time,
) (map[string]market.GlobalSinkResult, error) {
	// The BID is sell_price — what the market PAYS us (sp-en5h7); highest first.
	rows, err := r.bestAcrossSystems(ctx, goods, playerID, maxAge, now, "sell_price", "DESC", string(market.TradeTypeExport))
	if err != nil {
		return nil, fmt.Errorf("failed to find best sinks across systems: %w", err)
	}
	out := make(map[string]market.GlobalSinkResult, len(rows))
	for _, row := range rows {
		out[row.GoodSymbol] = market.GlobalSinkResult{
			WaypointSymbol: row.WaypointSymbol,
			SystemSymbol:   shared.ExtractSystemSymbol(row.WaypointSymbol),
			Bid:            row.Price,
			TradeVolume:    row.TradeVolume,
		}
	}
	return out, nil
}

// BestSourcesAcrossSystems returns, for each requested good, the CHEAPEST buy source ACROSS
// ALL SYSTEMS for the player — the source-side mirror of BestSinksAcrossSystems, sharing the
// ONE bestAcrossSystems scan (sp-mepj §2). IMPORT markets are excluded (an importer only BUYS
// a good, never sells one to us), symmetric to the sink scan's EXPORT exclusion. A good with
// no fresh sellable source is simply absent from the map. Paired per-good with the sink scan
// it yields (good, source, sink, ask, bid, depths) spanning any number of gate hops — the
// multi-hop lanes the 1-gate-hop tour/arb horizon structurally cannot see.
func (r *MarketRepositoryGORM) BestSourcesAcrossSystems(
	ctx context.Context,
	goods []string,
	playerID int,
	maxAge time.Duration,
	now time.Time,
) (map[string]market.GlobalSourceResult, error) {
	// The ASK is purchase_price — what WE PAY to buy (sp-en5h7); cheapest first.
	rows, err := r.bestAcrossSystems(ctx, goods, playerID, maxAge, now, "purchase_price", "ASC", string(market.TradeTypeImport))
	if err != nil {
		return nil, fmt.Errorf("failed to find best sources across systems: %w", err)
	}
	out := make(map[string]market.GlobalSourceResult, len(rows))
	for _, row := range rows {
		out[row.GoodSymbol] = market.GlobalSourceResult{
			WaypointSymbol: row.WaypointSymbol,
			SystemSymbol:   shared.ExtractSystemSymbol(row.WaypointSymbol),
			Ask:            row.Price,
			TradeVolume:    row.TradeVolume,
		}
	}
	return out, nil
}

// DistinctTradedGoods returns every good with a fresh market observation for the player —
// the long-haul engine's discovery universe (the goods to scan for out-of-horizon lanes,
// sp-mepj §2). Rows older than maxAge are excluded so a dead good never widens the scan. A
// plain SELECT DISTINCT (not the DISTINCT ON the best-price scans need), ordered for a stable
// scan set.
func (r *MarketRepositoryGORM) DistinctTradedGoods(ctx context.Context, playerID int, maxAge time.Duration, now time.Time) ([]string, error) {
	var rows []struct{ GoodSymbol string }
	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("DISTINCT good_symbol").
		Where("player_id = ?", playerID).
		Where("last_updated >= ?", now.Add(-maxAge)).
		Where("good_symbol <> ''").
		Order("good_symbol").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list distinct traded goods: %w", err)
	}
	goods := make([]string, 0, len(rows))
	for _, row := range rows {
		goods = append(goods, row.GoodSymbol)
	}
	return goods, nil
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
	// dead-era waypoint row can never surface as a live market: this
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

// ChartedMarketSystemCounts returns every CHARTED market system (current era) mapped to its
// count of real marketplace waypoints — the scan-only backlog's charted side. It applies
// the IDENTICAL filter as FindAllMarketsInSystem (a non-FUEL_STATION waypoint bearing the
// MARKETPLACE trait, era-scoped so a dead-era row can never surface as a live market), only global
// and grouped by system instead of scoped to one, so it enumerates the FULL discovered market set
// in a single query. The scan-only adapter subtracts the player's already-scanned systems
// (MaxAgeSecondsBySystem keys) from this to get the "dark" backlog, so the two views share the same
// era-scoped, fuel-excluded notion of a market and cannot drift.
func (r *MarketRepositoryGORM) ChartedMarketSystemCounts(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		SystemSymbol string
		Cnt          int
	}

	predicate, args := eraScopePredicate(r.openEraID(ctx))
	err := r.db.WithContext(ctx).
		Table("waypoints").
		Select("system_symbol, count(*) as cnt").
		Where("type != ?", "FUEL_STATION").
		Where("traits LIKE ?", "%MARKETPLACE%").
		Where(predicate, args...).
		Group("system_symbol").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to count charted market systems: %w", err)
	}

	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.SystemSymbol] = row.Cnt
	}
	return counts, nil
}

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
	var rows []struct {
		WaypointSymbol string
		LastUpdated    time.Time
		TradeVolume    int
		PurchasePrice  int
		SellPrice      int
		Activity       *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, last_updated, trade_volume, purchase_price, sell_price, activity").
		Where("player_id = ?", playerID).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read system freshness: %w", err)
	}

	// Collapse per-(waypoint,good) rows to one market: the latest scan time (defensively — a
	// market's goods share one scan), the summed Σ(trade_volume × mid-price) value weight, and the
	// activity of the DOMINANT (highest trade_volume × mid-price) good — the throughput proxy the
	// value weight already uses, so a market's freshness activity tracks the good that
	// actually drives its throughput rather than an arbitrary or low-volume one.
	type marketRollup struct {
		waypoint      string
		latest        time.Time
		weight        float64
		activity      string
		topGoodWeight float64
	}
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

	// Group markets by system.
	marketsBySystem := make(map[string][]marketRollup)
	for waypoint, market := range perWaypoint {
		system := shared.ExtractSystemSymbol(waypoint)
		marketsBySystem[system] = append(marketsBySystem[system], *market)
	}

	now := time.Now()
	out := make([]domainScouting.SystemFreshnessSnapshot, 0, len(marketsBySystem))
	for system, markets := range marketsBySystem {
		oldest := markets[0].latest
		scanTimes := make([]time.Time, 0, len(markets))
		samples := make([]domainScouting.MarketFreshnessSample, 0, len(markets))
		for _, market := range markets {
			if market.latest.Before(oldest) {
				oldest = market.latest
			}
			scanTimes = append(scanTimes, market.latest)
			samples = append(samples, domainScouting.MarketFreshnessSample{
				AgeSeconds: now.Sub(market.latest).Seconds(),
				Weight:     market.weight,
				Activity:   market.activity,
				Waypoint:   market.waypoint, // sp-wuksw: sink identity the demand re-weighting keys on
			})
		}
		cycleSeconds, sampleCount := domainScouting.MedianScanIntervalSeconds(scanTimes)
		out = append(out, domainScouting.SystemFreshnessSnapshot{
			SystemSymbol:         system,
			MarketCount:          len(markets),
			OldestAgeSeconds:     now.Sub(oldest).Seconds(),
			MeasuredCycleSeconds: cycleSeconds,
			CycleSamples:         sampleCount,
			Markets:              samples,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SystemSymbol < out[j].SystemSymbol })
	return out, nil
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
// Because this is the BUY side, the price it reports is the ASK — market_data.purchase_price,
// what WE PAY (the larger of the two prices; sp-en5h7).
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
		PurchasePrice  int
		Supply         *string
		Activity       *string
		TradeType      *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol, purchase_price, supply, activity, trade_type").
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
				Ask:            r.PurchasePrice,
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
// including data age so callers can judge staleness before acting on it.
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
// per-good round trip. Prices are carried straight through from market_data
// (see MarketGoodListing) under the sp-en5h7 convention: purchase_price is the
// ASK (the larger), sell_price the BID (the smaller).
type SystemMarketGoodListing struct {
	WaypointSymbol string
	GoodSymbol     string
	TradeType      string
	PurchasePrice  int // the ASK: what a ship PAYS buying FROM this market (the larger)
	SellPrice      int // the BID: what a ship RECEIVES selling TO this market (the smaller)
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
// We BUY from a factory, so the price it reports is the ASK — market_data.purchase_price, what
// WE PAY (the larger of the two prices; sp-en5h7).
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
		PurchasePrice  int
		Supply         *string
		Activity       *string
	}

	// Only select markets where trade_type = 'EXPORT' (factories that produce this good)
	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol, purchase_price, supply, activity").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Where("trade_type = ?", "EXPORT").
		Order("purchase_price ASC"). // Prefer cheapest factory (lowest ask)
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
		Ask:            result.PurchasePrice,
		Supply:         supply,
		Activity:       activity,
	}, nil
}

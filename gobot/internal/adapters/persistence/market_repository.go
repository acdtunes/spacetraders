package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

const marketDataTable = "market_data"

// MarketRepositoryGORM implements market persistence using GORM
type MarketRepositoryGORM struct {
	db *gorm.DB
}

var _ market.MarketRepository = (*MarketRepositoryGORM)(nil)

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
		if err := tx.Where("player_id = ? AND waypoint_symbol = ?", playerID, waypointSymbol).
			Delete(&MarketData{}).Error; err != nil {
			return fmt.Errorf("failed to delete old market data: %w", err)
		}

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

	waypointGoods := make(map[string][]MarketData)
	for _, record := range marketDataList {
		waypointGoods[record.WaypointSymbol] = append(waypointGoods[record.WaypointSymbol], record)
	}

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

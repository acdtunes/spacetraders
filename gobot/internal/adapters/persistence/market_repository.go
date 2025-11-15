package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

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
				marketDataRecords[i] = MarketData{
					WaypointSymbol: waypointSymbol,
					GoodSymbol:     good.Symbol(),
					Supply:         supply,
					Activity:       activity,
					PurchasePrice:  good.PurchasePrice(),
					SellPrice:      good.SellPrice(),
					TradeVolume:    good.TradeVolume(),
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
	playerID uint,
	waypointSymbol string,
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

	// Convert to domain objects
	goods := make([]market.TradeGood, len(marketDataRecords))
	var timestamp time.Time
	for i, record := range marketDataRecords {
		good, err := market.NewTradeGood(
			record.GoodSymbol,
			record.Supply,
			record.Activity,
			record.PurchasePrice,
			record.SellPrice,
			record.TradeVolume,
		)
		if err != nil {
			return nil, fmt.Errorf("invalid trade good in database: %w", err)
		}
		goods[i] = *good
		timestamp = record.LastUpdated
	}

	return market.NewMarket(waypointSymbol, goods, timestamp)
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
		goods := make([]market.TradeGood, len(records))
		var timestamp time.Time
		for i, record := range records {
			good, err := market.NewTradeGood(
				record.GoodSymbol,
				record.Supply,
				record.Activity,
				record.PurchasePrice,
				record.SellPrice,
				record.TradeVolume,
			)
			if err != nil {
				return nil, fmt.Errorf("invalid trade good in database: %w", err)
			}
			goods[i] = *good
			timestamp = record.LastUpdated
		}

		m, err := market.NewMarket(waypointSymbol, goods, timestamp)
		if err != nil {
			return nil, err
		}
		markets = append(markets, *m)
	}

	return markets, nil
}

// FindCheapestMarketSelling finds the market with the lowest sell price for a specific good in a system
func (r *MarketRepositoryGORM) FindCheapestMarketSelling(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*trading.CheapestMarketResult, error) {
	// Query to find the cheapest market selling the specified good
	// Query market_data table directly (no join needed - all data is in this table)
	// Filter by system, good symbol, and order by sell_price ascending
	var result struct {
		WaypointSymbol string
		TradeSymbol    string
		SellPrice      int
		Supply         *string
	}

	err := r.db.WithContext(ctx).
		Table("market_data").
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

	// If no result found, return nil (not an error)
	if result.WaypointSymbol == "" {
		return nil, nil
	}

	supply := ""
	if result.Supply != nil {
		supply = *result.Supply
	}

	return &trading.CheapestMarketResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.TradeSymbol,
		SellPrice:      result.SellPrice,
		Supply:         supply,
	}, nil
}


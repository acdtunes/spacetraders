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
func (r *MarketRepositoryGORM) UpsertMarketData(
	ctx context.Context,
	playerID uint,
	waypointSymbol string,
	goods []market.TradeGood,
	timestamp time.Time,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Find or create MarketData
		var marketData MarketData
		err := tx.Where("player_id = ? AND waypoint_symbol = ?", playerID, waypointSymbol).
			First(&marketData).Error

		if err == gorm.ErrRecordNotFound {
			// Create new record
			marketData = MarketData{
				PlayerID:       playerID,
				WaypointSymbol: waypointSymbol,
				LastUpdated:    timestamp,
				CreatedAt:      time.Now(),
			}
			if err := tx.Create(&marketData).Error; err != nil {
				return fmt.Errorf("failed to create market data: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("failed to query market data: %w", err)
		} else {
			// Update timestamp
			marketData.LastUpdated = timestamp
			if err := tx.Save(&marketData).Error; err != nil {
				return fmt.Errorf("failed to update market data: %w", err)
			}
		}

		// Delete existing trade goods
		if err := tx.Where("market_data_id = ?", marketData.ID).Delete(&TradeGoodData{}).Error; err != nil {
			return fmt.Errorf("failed to delete old trade goods: %w", err)
		}

		// Insert new trade goods
		if len(goods) > 0 {
			tradeGoodsData := make([]TradeGoodData, len(goods))
			for i, good := range goods {
				tradeGoodsData[i] = TradeGoodData{
					MarketDataID:  marketData.ID,
					Symbol:        good.Symbol(),
					Supply:        good.Supply(),
					Activity:      good.Activity(),
					PurchasePrice: good.PurchasePrice(),
					SellPrice:     good.SellPrice(),
					TradeVolume:   good.TradeVolume(),
				}
			}

			if err := tx.Create(&tradeGoodsData).Error; err != nil {
				return fmt.Errorf("failed to insert trade goods: %w", err)
			}
		}

		return nil
	})
}

// GetMarketData retrieves market data for a specific waypoint
func (r *MarketRepositoryGORM) GetMarketData(
	ctx context.Context,
	playerID uint,
	waypointSymbol string,
) (*market.Market, error) {
	var marketData MarketData
	err := r.db.WithContext(ctx).
		Preload("TradeGoods").
		Where("player_id = ? AND waypoint_symbol = ?", playerID, waypointSymbol).
		First(&marketData).Error

	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get market data: %w", err)
	}

	return r.toDomain(&marketData)
}

// ListMarketsInSystem retrieves all markets in a system, optionally filtered by age
func (r *MarketRepositoryGORM) ListMarketsInSystem(
	ctx context.Context,
	playerID uint,
	systemSymbol string,
	maxAgeMinutes int,
) ([]market.Market, error) {
	query := r.db.WithContext(ctx).
		Preload("TradeGoods").
		Where("player_id = ? AND waypoint_symbol LIKE ?", playerID, systemSymbol+"-%")

	if maxAgeMinutes > 0 {
		cutoff := time.Now().Add(-time.Duration(maxAgeMinutes) * time.Minute)
		query = query.Where("last_updated >= ?", cutoff)
	}

	var marketDataList []MarketData
	if err := query.Find(&marketDataList).Error; err != nil {
		return nil, fmt.Errorf("failed to list markets: %w", err)
	}

	markets := make([]market.Market, 0, len(marketDataList))
	for _, md := range marketDataList {
		m, err := r.toDomain(&md)
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
	// Join market_data and trade_goods tables, filter by system and good symbol
	// Order by sell_price ascending and limit to 1
	var result struct {
		WaypointSymbol string
		TradeSymbol    string
		SellPrice      int
		Supply         *string
	}

	err := r.db.WithContext(ctx).
		Table("market_data").
		Select("market_data.waypoint_symbol, trade_goods.symbol as trade_symbol, trade_goods.sell_price, trade_goods.supply").
		Joins("INNER JOIN trade_goods ON trade_goods.market_data_id = market_data.id").
		Where("market_data.player_id = ?", playerID).
		Where("market_data.waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("trade_goods.symbol = ?", goodSymbol).
		Order("trade_goods.sell_price ASC").
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

// toDomain converts database models to domain Market entity
func (r *MarketRepositoryGORM) toDomain(md *MarketData) (*market.Market, error) {
	goods := make([]market.TradeGood, len(md.TradeGoods))
	for i, tg := range md.TradeGoods {
		good, err := market.NewTradeGood(
			tg.Symbol,
			tg.Supply,
			tg.Activity,
			tg.PurchasePrice,
			tg.SellPrice,
			tg.TradeVolume,
		)
		if err != nil {
			return nil, fmt.Errorf("invalid trade good in database: %w", err)
		}
		goods[i] = *good
	}

	return market.NewMarket(md.WaypointSymbol, goods, md.LastUpdated)
}

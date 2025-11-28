package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
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

	// Convert to domain objects
	goods := make([]market.TradeGood, len(marketDataRecords))
	var timestamp time.Time
	for i, record := range marketDataRecords {
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

	if result.WaypointSymbol == "" {
		return nil, nil
	}

	supply := ""
	if result.Supply != nil {
		supply = *result.Supply
	}

	return &market.CheapestMarketResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.TradeSymbol,
		SellPrice:      result.SellPrice,
		Supply:         supply,
	}, nil
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
		Table("market_data").
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

	supply := ""
	if result.Supply != nil {
		supply = *result.Supply
	}

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
		Table("market_data").
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

	supply := ""
	if result.Supply != nil {
		supply = *result.Supply
	}

	return &market.BestMarketBuyingResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.TradeSymbol,
		PurchasePrice:  result.PurchasePrice,
		Supply:         supply,
	}, nil
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
	// Same filtering logic as scout operation (assign_scouting_fleet.go:216-219)
	err := r.db.WithContext(ctx).
		Table("waypoints").
		Select("waypoint_symbol").
		Where("system_symbol = ?", systemSymbol).
		Where("type != ?", "FUEL_STATION").
		Where("traits LIKE ?", "%MARKETPLACE%").
		Pluck("waypoint_symbol", &waypoints).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find markets in system: %w", err)
	}

	return waypoints, nil
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
		Table("market_data").
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
		supply := ""
		if r.Supply != nil {
			supply = *r.Supply
		}
		activity := ""
		if r.Activity != nil {
			activity = *r.Activity
		}
		tradeType := ""
		if r.TradeType != nil {
			tradeType = *r.TradeType
		}

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
// Activity: RESTRICTED(0) > WEAK(1) > GROWING(2) > STRONG(3) (weight: 1)
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

	supplyScore := 5 // Unknown = worst
	switch supply {
	case "ABUNDANT":
		supplyScore = 0
	case "HIGH":
		supplyScore = 1
	case "MODERATE":
		supplyScore = 2
	case "LIMITED":
		supplyScore = 3
	case "SCARCE":
		supplyScore = 4
	}

	activityScore := 4 // Unknown = worst
	switch activity {
	case "RESTRICTED":
		activityScore = 0 // Best - stable prices
	case "WEAK":
		activityScore = 1
	case "GROWING":
		activityScore = 2
	case "STRONG":
		activityScore = 3
	}

	// Trade type weighted 1000x, supply weighted 10x, activity weighted 1x
	// This ensures EXPORT markets ALWAYS preferred over EXCHANGE over IMPORT
	return tradeTypeScore*1000 + supplyScore*10 + activityScore
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
		Table("market_data").
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

	supply := ""
	if result.Supply != nil {
		supply = *result.Supply
	}
	activity := ""
	if result.Activity != nil {
		activity = *result.Activity
	}

	return &market.FactoryResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.GoodSymbol,
		SellPrice:      result.SellPrice,
		Supply:         supply,
		Activity:       activity,
	}, nil
}

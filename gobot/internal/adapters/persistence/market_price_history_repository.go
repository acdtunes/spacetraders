package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"gorm.io/gorm"
)

// GormMarketPriceHistoryRepository implements MarketPriceHistoryRepository using GORM
type GormMarketPriceHistoryRepository struct {
	db *gorm.DB
}

// NewGormMarketPriceHistoryRepository creates a new GORM market price history repository
func NewGormMarketPriceHistoryRepository(db *gorm.DB) *GormMarketPriceHistoryRepository {
	return &GormMarketPriceHistoryRepository{db: db}
}

// RecordPriceChange persists a new price history entry
func (r *GormMarketPriceHistoryRepository) RecordPriceChange(
	ctx context.Context,
	history *market.MarketPriceHistory,
) error {
	model := &MarketPriceHistoryModel{
		WaypointSymbol: history.WaypointSymbol(),
		GoodSymbol:     history.GoodSymbol(),
		PlayerID:       history.PlayerID().Value(),
		PurchasePrice:  history.PurchasePrice(),
		SellPrice:      history.SellPrice(),
		Supply:         history.Supply(),
		Activity:       history.Activity(),
		TradeVolume:    history.TradeVolume(),
		RecordedAt:     history.RecordedAt(),
	}

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return fmt.Errorf("failed to record price change: %w", result.Error)
	}

	return nil
}

// GetPriceHistory retrieves price history for a specific market/good pair
// Returns entries ordered by recorded_at DESC (newest first)
func (r *GormMarketPriceHistoryRepository) GetPriceHistory(
	ctx context.Context,
	waypointSymbol string,
	goodSymbol string,
	since time.Time,
	limit int,
) ([]*market.MarketPriceHistory, error) {
	var models []MarketPriceHistoryModel
	query := r.db.WithContext(ctx).
		Where("waypoint_symbol = ? AND good_symbol = ?", waypointSymbol, goodSymbol)

	if !since.IsZero() {
		query = query.Where("recorded_at >= ?", since)
	}

	query = query.Order("recorded_at DESC")

	if limit > 0 {
		query = query.Limit(limit)
	}

	result := query.Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get price history: %w", result.Error)
	}

	histories := make([]*market.MarketPriceHistory, 0, len(models))
	for _, model := range models {
		history, err := r.modelToHistory(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert model to history: %w", err)
		}
		histories = append(histories, history)
	}

	return histories, nil
}

// GetVolatilityMetrics calculates price volatility statistics for a good
// Returns mean price, std deviation, max price change %, and change frequency
func (r *GormMarketPriceHistoryRepository) GetVolatilityMetrics(
	ctx context.Context,
	goodSymbol string,
	windowHours int,
) (*market.VolatilityMetrics, error) {
	since := time.Now().Add(-time.Duration(windowHours) * time.Hour)

	// Query to calculate statistics using window functions
	type result struct {
		MeanPrice      float64
		StdDev         float64
		MaxPriceChange float64
		SampleSize     int
		WindowStart    time.Time
		WindowEnd      time.Time
	}

	var res result
	err := r.db.WithContext(ctx).Raw(`
		WITH price_changes AS (
			SELECT
				(purchase_price + sell_price) / 2.0 as avg_price,
				recorded_at,
				LAG((purchase_price + sell_price) / 2.0) OVER (ORDER BY recorded_at) as prev_price
			FROM market_price_history
			WHERE good_symbol = ? AND recorded_at >= ?
			ORDER BY recorded_at
		),
		price_change_pcts AS (
			SELECT
				avg_price,
				recorded_at,
				CASE
					WHEN prev_price > 0 AND prev_price IS NOT NULL
					THEN ABS(((avg_price - prev_price) / prev_price) * 100.0)
					ELSE 0
				END as change_pct
			FROM price_changes
		)
		SELECT
			AVG(avg_price) as mean_price,
			COALESCE(STDDEV_SAMP(avg_price), 0) as std_dev,
			COALESCE(MAX(change_pct), 0) as max_price_change,
			COUNT(*) as sample_size,
			MIN(recorded_at) as window_start,
			MAX(recorded_at) as window_end
		FROM price_change_pcts
	`, goodSymbol, since).Scan(&res).Error

	if err != nil {
		return nil, fmt.Errorf("failed to calculate volatility metrics: %w", err)
	}

	// If no data, return empty metrics
	if res.SampleSize == 0 {
		return &market.VolatilityMetrics{
			GoodSymbol:      goodSymbol,
			MeanPrice:       0,
			StdDeviation:    0,
			MaxPriceChange:  0,
			ChangeFrequency: 0,
			SampleSize:      0,
		}, nil
	}

	// Calculate change frequency (changes per hour)
	changeFrequency := 0.0
	if !res.WindowStart.IsZero() && !res.WindowEnd.IsZero() {
		hoursDiff := res.WindowEnd.Sub(res.WindowStart).Hours()
		if hoursDiff > 0 {
			// Sample size - 1 because first record has no previous price
			changeFrequency = float64(res.SampleSize-1) / hoursDiff
		}
	}

	return &market.VolatilityMetrics{
		GoodSymbol:      goodSymbol,
		MeanPrice:       res.MeanPrice,
		StdDeviation:    res.StdDev,
		MaxPriceChange:  res.MaxPriceChange,
		ChangeFrequency: changeFrequency,
		SampleSize:      res.SampleSize,
	}, nil
}

// FindMostVolatileGoods identifies goods with highest price drift
// Returns top N goods sorted by volatility score (descending)
func (r *GormMarketPriceHistoryRepository) FindMostVolatileGoods(
	ctx context.Context,
	limit int,
	windowHours int,
) ([]*market.GoodVolatility, error) {
	since := time.Now().Add(-time.Duration(windowHours) * time.Hour)

	type result struct {
		GoodSymbol      string
		VolatilityScore float64
		ChangeCount     int
	}

	var results []result
	err := r.db.WithContext(ctx).Raw(`
		WITH price_changes AS (
			SELECT
				good_symbol,
				(purchase_price + sell_price) / 2.0 as avg_price,
				recorded_at,
				LAG((purchase_price + sell_price) / 2.0) OVER (PARTITION BY good_symbol ORDER BY recorded_at) as prev_price
			FROM market_price_history
			WHERE recorded_at >= ?
		),
		good_volatility AS (
			SELECT
				good_symbol,
				COUNT(*) as change_count,
				COALESCE(STDDEV_SAMP(avg_price), 0) as std_dev,
				COALESCE(AVG(
					CASE
						WHEN prev_price > 0 AND prev_price IS NOT NULL
						THEN ABS(((avg_price - prev_price) / prev_price) * 100.0)
						ELSE 0
					END
				), 0) as avg_change_pct
			FROM price_changes
			GROUP BY good_symbol
		)
		SELECT
			good_symbol,
			(std_dev + (avg_change_pct * 10)) as volatility_score,
			change_count
		FROM good_volatility
		WHERE change_count > 1
		ORDER BY volatility_score DESC
		LIMIT ?
	`, since, limit).Scan(&results).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find most volatile goods: %w", err)
	}

	volatileGoods := make([]*market.GoodVolatility, len(results))
	for i, res := range results {
		volatileGoods[i] = &market.GoodVolatility{
			GoodSymbol:      res.GoodSymbol,
			VolatilityScore: res.VolatilityScore,
			ChangeCount:     res.ChangeCount,
		}
	}

	return volatileGoods, nil
}

// GetMarketStability calculates how stable a specific market is for a good
// Returns stability score (0-100, higher = more stable)
func (r *GormMarketPriceHistoryRepository) GetMarketStability(
	ctx context.Context,
	waypointSymbol string,
	goodSymbol string,
	windowHours int,
) (*market.MarketStability, error) {
	since := time.Now().Add(-time.Duration(windowHours) * time.Hour)

	type result struct {
		MinPrice      int
		MaxPrice      int
		AvgChangeSize float64
		ChangeCount   int
	}

	var res result
	err := r.db.WithContext(ctx).Raw(`
		WITH price_data AS (
			SELECT
				(purchase_price + sell_price) / 2.0 as avg_price,
				recorded_at,
				LAG((purchase_price + sell_price) / 2.0) OVER (ORDER BY recorded_at) as prev_price
			FROM market_price_history
			WHERE waypoint_symbol = ? AND good_symbol = ? AND recorded_at >= ?
			ORDER BY recorded_at
		),
		price_stats AS (
			SELECT
				MIN(avg_price) as min_price,
				MAX(avg_price) as max_price,
				COUNT(*) as change_count,
				AVG(
					CASE
						WHEN prev_price > 0 AND prev_price IS NOT NULL
						THEN ABS(((avg_price - prev_price) / prev_price) * 100.0)
						ELSE 0
					END
				) as avg_change_size
			FROM price_data
		)
		SELECT
			COALESCE(min_price::int, 0) as min_price,
			COALESCE(max_price::int, 0) as max_price,
			COALESCE(avg_change_size, 0) as avg_change_size,
			change_count
		FROM price_stats
	`, waypointSymbol, goodSymbol, since).Scan(&res).Error

	if err != nil {
		return nil, fmt.Errorf("failed to calculate market stability: %w", err)
	}

	// If no data, return nil stability metrics
	if res.ChangeCount == 0 {
		return nil, fmt.Errorf("no price history data for market %s and good %s", waypointSymbol, goodSymbol)
	}

	// Calculate price range
	priceRange := res.MaxPrice - res.MinPrice

	// Calculate stability score (0-100, higher = more stable)
	// Lower average change size and smaller price range = higher stability
	stabilityScore := 100.0
	if res.AvgChangeSize > 0 {
		stabilityScore -= res.AvgChangeSize // Penalize by average change %
	}
	if priceRange > 0 && res.MinPrice > 0 {
		rangePercent := float64(priceRange) / float64(res.MinPrice) * 100.0
		stabilityScore -= rangePercent / 2.0 // Penalize by half the range %
	}

	// Ensure score stays in bounds
	if stabilityScore < 0 {
		stabilityScore = 0
	}
	if stabilityScore > 100 {
		stabilityScore = 100
	}

	return &market.MarketStability{
		WaypointSymbol: waypointSymbol,
		GoodSymbol:     goodSymbol,
		StabilityScore: stabilityScore,
		PriceRange:     priceRange,
		AvgChangeSize:  res.AvgChangeSize,
	}, nil
}

// modelToHistory converts a GORM model to a domain entity
func (r *GormMarketPriceHistoryRepository) modelToHistory(model *MarketPriceHistoryModel) (*market.MarketPriceHistory, error) {
	playerID, err := shared.NewPlayerID(model.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID: %w", err)
	}

	history, err := market.NewMarketPriceHistoryWithID(
		model.ID,
		model.WaypointSymbol,
		model.GoodSymbol,
		playerID,
		model.PurchasePrice,
		model.SellPrice,
		model.Supply,
		model.Activity,
		model.TradeVolume,
		model.RecordedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create history entity: %w", err)
	}

	return history, nil
}

package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"gorm.io/gorm"
)

// GormPurchaseHistoryRepository implements PurchaseHistoryRepository using GORM
type GormPurchaseHistoryRepository struct {
	db *gorm.DB
}

// NewGormPurchaseHistoryRepository creates a new GORM purchase history repository
func NewGormPurchaseHistoryRepository(db *gorm.DB) *GormPurchaseHistoryRepository {
	return &GormPurchaseHistoryRepository{db: db}
}

// Add saves a new purchase history record
func (r *GormPurchaseHistoryRepository) Add(ctx context.Context, history *contract.PurchaseHistory) error {
	model := r.entityToModel(history)

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return fmt.Errorf("failed to add purchase history: %w", result.Error)
	}

	return nil
}

// FindRecentMarkets retrieves the most frequently used markets within the time window
// Returns waypoint symbols ordered by frequency (most used first), limited to the specified count
func (r *GormPurchaseHistoryRepository) FindRecentMarkets(
	ctx context.Context,
	playerID int,
	systemSymbol string,
	limit int,
	sinceDays int,
) ([]string, error) {
	// Calculate the cutoff time
	cutoffTime := time.Now().UTC().Add(-time.Duration(sinceDays) * 24 * time.Hour)

	// Query for distinct waypoints ordered by frequency
	var results []struct {
		WaypointSymbol string
		Count          int64
	}

	err := r.db.WithContext(ctx).
		Model(&ContractPurchaseHistoryModel{}).
		Select("waypoint_symbol, COUNT(*) as count").
		Where("player_id = ? AND system_symbol = ? AND purchased_at >= ?", playerID, systemSymbol, cutoffTime).
		Group("waypoint_symbol").
		Order("count DESC").
		Limit(limit).
		Scan(&results).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find recent markets: %w", err)
	}

	// Extract waypoint symbols
	waypoints := make([]string, 0, len(results))
	for _, result := range results {
		waypoints = append(waypoints, result.WaypointSymbol)
	}

	return waypoints, nil
}

// entityToModel converts domain entity to database model
func (r *GormPurchaseHistoryRepository) entityToModel(history *contract.PurchaseHistory) *ContractPurchaseHistoryModel {
	return &ContractPurchaseHistoryModel{
		PlayerID:       history.PlayerID(),
		SystemSymbol:   history.SystemSymbol(),
		WaypointSymbol: history.WaypointSymbol(),
		TradeGood:      history.TradeGood(),
		PurchasedAt:    history.PurchasedAt(),
		ContractID:     history.ContractID(),
	}
}

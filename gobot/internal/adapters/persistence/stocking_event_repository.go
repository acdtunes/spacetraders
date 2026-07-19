package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// StockingEventRepositoryGORM is the GORM-backed implementation of the domain
// storage.StockingRecorder port — the stock-IN mirror of
// WithdrawalEventRepositoryGORM. It maps the domain event DTO to the WarehouseStockingModel
// row and back, so the stocker coordinator and any stock-IN report never touch GORM types
// directly.
type StockingEventRepositoryGORM struct {
	db *gorm.DB
}

// compile-time assertion that the GORM repo satisfies the domain port.
var _ storage.StockingRecorder = (*StockingEventRepositoryGORM)(nil)

// NewStockingEventRepository creates a GORM-backed stocking-event repository.
func NewStockingEventRepository(db *gorm.DB) *StockingEventRepositoryGORM {
	return &StockingEventRepositoryGORM{db: db}
}

// Record persists one stocker→warehouse deposit event.
func (r *StockingEventRepositoryGORM) Record(ctx context.Context, event storage.StockingEvent) error {
	row := &WarehouseStockingModel{
		Good:              event.Good,
		Units:             event.Units,
		WarehouseWaypoint: event.WarehouseWaypoint,
		SourceWaypoint:    event.SourceWaypoint,
		ShipSymbol:        event.Ship,
		PlayerID:          event.PlayerID,
		DepositedAt:       event.DepositedAt,
	}
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("record warehouse stocking event: %w", err)
	}
	return nil
}

// ListByPlayer returns playerID's deposit events whose deposited_at is at or after since,
// ordered by insertion (id ASC) so deposits read back in the order they happened. A zero
// since returns the full history.
func (r *StockingEventRepositoryGORM) ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]storage.StockingEvent, error) {
	var rows []WarehouseStockingModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND deposited_at >= ?", playerID, since).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list warehouse stocking events for player %d: %w", playerID, err)
	}

	out := make([]storage.StockingEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, storage.StockingEvent{
			Good:              row.Good,
			Units:             row.Units,
			WarehouseWaypoint: row.WarehouseWaypoint,
			SourceWaypoint:    row.SourceWaypoint,
			Ship:              row.ShipSymbol,
			PlayerID:          row.PlayerID,
			DepositedAt:       row.DepositedAt,
		})
	}
	return out, nil
}

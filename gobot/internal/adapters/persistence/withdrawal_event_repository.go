package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// WithdrawalEventRepositoryGORM is the GORM-backed implementation of the domain
// storage.WithdrawalRecorder port (sp-kqxe). It maps the domain event DTO to the
// WarehouseWithdrawalModel row and back, so the contract delivery executor and any
// warehouse-ROI report never touch GORM types directly.
type WithdrawalEventRepositoryGORM struct {
	db *gorm.DB
}

// compile-time assertion that the GORM repo satisfies the domain port.
var _ storage.WithdrawalRecorder = (*WithdrawalEventRepositoryGORM)(nil)

// NewWithdrawalEventRepository creates a GORM-backed withdrawal-event repository.
func NewWithdrawalEventRepository(db *gorm.DB) *WithdrawalEventRepositoryGORM {
	return &WithdrawalEventRepositoryGORM{db: db}
}

// Record persists one warehouse→hauler withdrawal event.
func (r *WithdrawalEventRepositoryGORM) Record(ctx context.Context, event storage.WithdrawalEvent) error {
	row := &WarehouseWithdrawalModel{
		Good:        event.Good,
		Units:       event.Units,
		Waypoint:    event.Waypoint,
		ShipSymbol:  event.Ship,
		ContractID:  event.ContractID,
		PlayerID:    event.PlayerID,
		WithdrawnAt: event.WithdrawnAt,
	}
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("record warehouse withdrawal event: %w", err)
	}
	return nil
}

// ListByPlayer returns playerID's withdrawal events whose withdrawn_at is at or
// after since, ordered by insertion (id ASC) so draws read back in the order they
// happened. A zero since returns the full history.
func (r *WithdrawalEventRepositoryGORM) ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]storage.WithdrawalEvent, error) {
	var rows []WarehouseWithdrawalModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND withdrawn_at >= ?", playerID, since).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list warehouse withdrawal events for player %d: %w", playerID, err)
	}

	out := make([]storage.WithdrawalEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, storage.WithdrawalEvent{
			Good:        row.Good,
			Units:       row.Units,
			Waypoint:    row.Waypoint,
			Ship:        row.ShipSymbol,
			ContractID:  row.ContractID,
			PlayerID:    row.PlayerID,
			WithdrawnAt: row.WithdrawnAt,
		})
	}
	return out, nil
}

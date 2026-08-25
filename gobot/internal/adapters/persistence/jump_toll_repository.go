package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// GormJumpTollRepository is the GORM-backed implementation of trading.JumpTollRepository:
// one row per measured gate hop, read back as the window the per-hop toll is estimated over.
//
// THE AGGREGATE IS DELIBERATELY NOT IN SQL. Its shape is a decay-weighted average of
// per-bucket medians, and pushing that down would need percentile_cont plus a date_trunc,
// which is Postgres-specific — while the estimator's whole point is that the STATISTIC is
// arguable and will be retuned. Keeping it in the domain keeps it unit-testable against a
// slice and portable across the SQLite the tests run on. The read is bounded (a limit, and a
// window) and cached by its caller, so the rows never accumulate into a load problem.
type GormJumpTollRepository struct {
	db *gorm.DB
}

// NewGormJumpTollRepository creates a GORM-backed jump toll sample repository.
func NewGormJumpTollRepository(db *gorm.DB) *GormJumpTollRepository {
	return &GormJumpTollRepository{db: db}
}

var _ trading.JumpTollRepository = (*GormJumpTollRepository)(nil)

// RecordJumpToll persists one measured hop.
func (r *GormJumpTollRepository) RecordJumpToll(
	ctx context.Context, playerID int, shipSymbol, fromSystem, toSystem string, sample trading.JumpTollSample,
) error {
	if r == nil || r.db == nil {
		return nil
	}
	row := &JumpTollSampleModel{
		ShipSymbol:      shipSymbol,
		FromSystem:      fromSystem,
		ToSystem:        toSystem,
		WaitSeconds:     sample.WaitSeconds,
		CooldownSeconds: sample.CooldownSeconds,
		PlayerID:        playerID,
		RecordedAt:      sample.RecordedAt,
	}
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("record jump toll sample for %s: %w", shipSymbol, err)
	}
	return nil
}

// RecentJumpTolls returns playerID's samples recorded at or after since, NEWEST FIRST.
//
// The ordering pairs with the limit: truncation drops the OLDEST rows, which are the ones the
// estimator's decay would have discounted anyway. A non-positive limit reads the whole window.
func (r *GormJumpTollRepository) RecentJumpTolls(
	ctx context.Context, playerID int, since time.Time, limit int,
) ([]trading.JumpTollSample, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	query := r.db.WithContext(ctx).Model(&JumpTollSampleModel{}).
		Where("player_id = ? AND recorded_at >= ?", playerID, since).
		Order("recorded_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}

	var rows []JumpTollSampleModel
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list jump toll samples for player %d: %w", playerID, err)
	}

	out := make([]trading.JumpTollSample, 0, len(rows))
	for _, row := range rows {
		out = append(out, trading.JumpTollSample{
			WaitSeconds:     row.WaitSeconds,
			CooldownSeconds: row.CooldownSeconds,
			RecordedAt:      row.RecordedAt,
		})
	}
	return out, nil
}

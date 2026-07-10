package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// TourTelemetryRepositoryGORM is the GORM-backed implementation of
// trading.TourTelemetryRepository (sp-1ek0 P1b). It maps the domain DTO to the
// TourLegTelemetryModel row and back, so the tour_run executor and the graduation
// report never touch GORM types directly.
type TourTelemetryRepositoryGORM struct {
	db *gorm.DB
}

// NewTourTelemetryRepository creates a GORM-backed tour telemetry repository.
func NewTourTelemetryRepository(db *gorm.DB) *TourTelemetryRepositoryGORM {
	return &TourTelemetryRepositoryGORM{db: db}
}

// RecordLeg persists one planned-vs-realized trade record.
func (r *TourTelemetryRepositoryGORM) RecordLeg(ctx context.Context, leg trading.TourLegTelemetry) error {
	row := &TourLegTelemetryModel{
		TourID:            leg.TourID,
		ShipSymbol:        leg.ShipSymbol,
		LegIndex:          leg.LegIndex,
		Waypoint:          leg.Waypoint,
		Good:              leg.Good,
		IsBuy:             leg.IsBuy,
		PlannedUnits:      leg.PlannedUnits,
		RealizedUnits:     leg.RealizedUnits,
		PlannedUnitPrice:  leg.PlannedUnitPrice,
		RealizedUnitPrice: leg.RealizedUnitPrice,
		PlannedAt:         leg.PlannedAt,
		RealizedAt:        leg.RealizedAt,
		PlayerID:          leg.PlayerID,
	}
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("record tour leg telemetry: %w", err)
	}
	return nil
}

// ListByPlayer returns playerID's telemetry rows whose planned_at is at or after
// since, ordered by insertion (id ASC) so a tour's legs read back in execution order.
func (r *TourTelemetryRepositoryGORM) ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error) {
	var rows []TourLegTelemetryModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND planned_at >= ?", playerID, since).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tour leg telemetry for player %d: %w", playerID, err)
	}

	out := make([]trading.TourLegTelemetry, 0, len(rows))
	for _, row := range rows {
		out = append(out, trading.TourLegTelemetry{
			TourID:            row.TourID,
			ShipSymbol:        row.ShipSymbol,
			LegIndex:          row.LegIndex,
			Waypoint:          row.Waypoint,
			Good:              row.Good,
			IsBuy:             row.IsBuy,
			PlannedUnits:      row.PlannedUnits,
			RealizedUnits:     row.RealizedUnits,
			PlannedUnitPrice:  row.PlannedUnitPrice,
			RealizedUnitPrice: row.RealizedUnitPrice,
			PlannedAt:         row.PlannedAt,
			RealizedAt:        row.RealizedAt,
			PlayerID:          row.PlayerID,
		})
	}
	return out, nil
}

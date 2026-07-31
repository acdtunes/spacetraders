package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// TourTelemetryRepositoryGORM is the GORM-backed implementation of
// trading.TourTelemetryRepository. It maps the domain DTO to the
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
//
// An unset Engine is filled from the LegIndex class rather than stored empty: the column's
// whole purpose is that EVERY leg with realized cargo is attributable (sp-fzt09), and a
// blank row would reintroduce exactly the unattributable population it was added to remove.
// Callers are still expected to declare their engine — recordLeg takes it as a required
// parameter — so this is a floor, not the normal path.
func (r *TourTelemetryRepositoryGORM) RecordLeg(ctx context.Context, leg trading.TourLegTelemetry) error {
	engine := leg.Engine
	if engine == "" {
		engine = trading.EngineForLegIndex(leg.LegIndex)
	}
	row := &TourLegTelemetryModel{
		TourID:            leg.TourID,
		ShipSymbol:        leg.ShipSymbol,
		Engine:            string(engine),
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
//
// planned_at holds when EXECUTION of the leg started, not when the plan was made (see
// trading.TourLegTelemetry.PlannedAt for why it must stay that way), so this window admits
// legs by the moment the hull acted. A tour's legs are stamped incrementally as it runs, so
// a tour straddling either boundary is the normal case rather than an edge — which is what
// tour_rate.go's trade matching exists to handle.
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
			Engine:            trading.LegEngine(row.Engine),
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

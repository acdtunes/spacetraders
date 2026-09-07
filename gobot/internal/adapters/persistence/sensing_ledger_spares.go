package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

// SpareHulls returns the player's era-scoped reserve probes — hulls we own that
// hold no placement (see SensingSpareHullModel), hull-ordered for reproducibility.
func (r *SensingLedgerRepository) SpareHulls(ctx context.Context, playerID int) ([]SensingSpareHullModel, error) {
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var models []SensingSpareHullModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Where(predicate, args...).
		Order("ship_symbol").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list sensing reserve probes: %w", err)
	}
	return models, nil
}

// UpsertSpareHull records one probe as a reserve where it stands. Keyed on the
// HULL, so re-recording a probe that moved UPDATES its waypoint rather than adding
// a row — which is what makes it idempotent under a per-tick pass.
func (r *SensingLedgerRepository) UpsertSpareHull(ctx context.Context, playerID int, shipSymbol, waypoint, system string) error {
	model := SensingSpareHullModel{
		PlayerID:       playerID,
		ShipSymbol:     shipSymbol,
		WaypointSymbol: waypoint,
		SystemSymbol:   system,
		EraID:          r.openEraID(ctx),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "player_id"}, {Name: "ship_symbol"}},
			DoUpdates: clause.AssignmentColumns([]string{"waypoint_symbol", "system_symbol", "era_id", "updated_at"}),
		}).
		Create(&model).Error; err != nil {
		return fmt.Errorf("failed to record reserve probe %q at %q: %w", shipSymbol, waypoint, err)
	}
	return nil
}

// DeleteSpareHull takes one probe out of the reserve pool, once a placement or an
// errand names it instead.
//
// ADDRESSED BY HULL AND BY NOTHING ELSE, the money guard this table exists for
// (RULINGS #4): "the SPARE row at this waypoint" names a SET once several reserves
// stand at one yard. A missing row is NOT an error, and the delete is era-AGNOSTIC.
func (r *SensingLedgerRepository) DeleteSpareHull(ctx context.Context, playerID int, shipSymbol string) error {
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND ship_symbol = ?", playerID, shipSymbol).
		Delete(&SensingSpareHullModel{}).Error; err != nil {
		return fmt.Errorf("failed to release reserve probe %q: %w", shipSymbol, err)
	}
	return nil
}

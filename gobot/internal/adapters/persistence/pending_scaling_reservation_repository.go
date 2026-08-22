package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// defaultPendingScalingStaleWindow bounds how long a published reservation stays "active" once
// bootstrap stops refreshing it. Bootstrap re-publishes every tick it remains capital-blocked at
// the 45s cadence (defaultBootstrapTickSeconds); this window is 2x that — long enough that one
// merely-slow tick never false-negatives, short enough that a stopped container's last-published
// row self-clears within a couple of ticks.
const defaultPendingScalingStaleWindow = 90 * time.Second

// PendingScalingReservationRepository is the durable, player-scoped, single-row "pending
// fleet-scaling purchase is capital-blocked" signal. One concrete adapter implements both sides —
// Publish (the PendingScalingReservationPublisher port bootstrap calls) and PendingReservation
// (the PendingScalingReservation port construction's spend guard calls) — each satisfied
// structurally, with neither application package imported here.
type PendingScalingReservationRepository struct {
	db          *gorm.DB
	staleWindow time.Duration
}

// NewPendingScalingReservationRepository builds the repository with the default staleness window.
func NewPendingScalingReservationRepository(db *gorm.DB) *PendingScalingReservationRepository {
	return &PendingScalingReservationRepository{db: db, staleWindow: defaultPendingScalingStaleWindow}
}

// Publish UPSERTs the player's single reservation row, refreshing UpdatedAt. No Delete/Expire
// method: PendingReservation's staleness check retires a reservation once Publish stops being called.
func (r *PendingScalingReservationRepository) Publish(ctx context.Context, playerID int, targetAmount int64) error {
	row := &PendingScalingReservationModel{
		PlayerID:     playerID,
		TargetAmount: targetAmount,
		UpdatedAt:    time.Now(),
	}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "player_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"target_amount", "updated_at"}),
		}).
		Create(row).Error; err != nil {
		return fmt.Errorf("publish pending scaling reservation for player %d: %w", playerID, err)
	}
	return nil
}

// PendingReservation reads the player's active reservation target, or 0 when there is none or the
// row has gone STALE (UpdatedAt older than staleWindow) — freshness derived at READ time.
func (r *PendingScalingReservationRepository) PendingReservation(ctx context.Context, playerID int) (int64, error) {
	var row PendingScalingReservationModel
	cutoff := time.Now().Add(-r.staleWindow)
	err := r.db.WithContext(ctx).
		Where("player_id = ? AND updated_at >= ?", playerID, cutoff).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil // absent or stale — both read as "nothing pending right now"
	}
	if err != nil {
		return 0, fmt.Errorf("read pending scaling reservation for player %d: %w", playerID, err)
	}
	return row.TargetAmount, nil
}

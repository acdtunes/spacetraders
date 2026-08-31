package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/application/hullrepair"
)

// UnreadableHullLedger persists the open unreadable-hull episodes the automatic repair
// works through.
type UnreadableHullLedger struct {
	db *gorm.DB
}

// NewUnreadableHullLedger builds the ledger over the daemon's database.
func NewUnreadableHullLedger(db *gorm.DB) *UnreadableHullLedger {
	return &UnreadableHullLedger{db: db}
}

// Observe opens an episode, or refreshes an open one. An existing episode keeps its
// attempt count and its backoff: re-observing a hull every fleet read must not reset the
// bound that stops the repair looping.
func (l *UnreadableHullLedger) Observe(ctx context.Context, playerID int, symbol string, at time.Time) error {
	if l.db == nil {
		return errors.New("no database wired for the unreadable-hull ledger")
	}
	res := l.db.WithContext(ctx).Model(&UnreadableHullModel{}).
		Where("player_id = ? AND ship_symbol = ?", playerID, symbol).
		Update("last_seen_at", at)
	if res.Error != nil {
		return fmt.Errorf("refresh the unreadable-hull episode for %s: %w", symbol, res.Error)
	}
	if res.RowsAffected > 0 {
		return nil
	}
	row := UnreadableHullModel{
		PlayerID:      playerID,
		ShipSymbol:    symbol,
		FirstSeenAt:   at,
		LastSeenAt:    at,
		NextAttemptAt: at,
	}
	if err := l.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("open an unreadable-hull episode for %s: %w", symbol, err)
	}
	return nil
}

// Due lists the open, non-escalated episodes whose backoff has expired.
func (l *UnreadableHullLedger) Due(ctx context.Context, playerID int, at time.Time) ([]hullrepair.Record, error) {
	if l.db == nil {
		return nil, errors.New("no database wired for the unreadable-hull ledger")
	}
	var rows []UnreadableHullModel
	if err := l.db.WithContext(ctx).
		Where("player_id = ? AND escalated_at IS NULL AND next_attempt_at <= ?", playerID, at).
		Order("first_seen_at").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list the due unreadable-hull episodes: %w", err)
	}
	records := make([]hullrepair.Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, unreadableHullRecord(row))
	}
	return records, nil
}

// Save persists an episode's attempt count, backoff, outcome and escalation.
func (l *UnreadableHullLedger) Save(ctx context.Context, rec hullrepair.Record) error {
	if l.db == nil {
		return errors.New("no database wired for the unreadable-hull ledger")
	}
	res := l.db.WithContext(ctx).Model(&UnreadableHullModel{}).
		Where("player_id = ? AND ship_symbol = ?", rec.PlayerID, rec.ShipSymbol).
		Updates(map[string]interface{}{
			"attempts":        rec.Attempts,
			"next_attempt_at": rec.NextAttemptAt,
			"escalated_at":    rec.EscalatedAt,
			"last_outcome":    rec.LastOutcome,
			"last_reason":     rec.LastReason,
		})
	if res.Error != nil {
		return fmt.Errorf("record the repair attempt for %s: %w", rec.ShipSymbol, res.Error)
	}
	if res.RowsAffected == 0 {
		row := unreadableHullModel(rec)
		if err := l.db.WithContext(ctx).Create(&row).Error; err != nil {
			return fmt.Errorf("record the repair attempt for %s: %w", rec.ShipSymbol, err)
		}
	}
	return nil
}

// Clear closes an episode.
func (l *UnreadableHullLedger) Clear(ctx context.Context, playerID int, symbol string) error {
	if l.db == nil {
		return errors.New("no database wired for the unreadable-hull ledger")
	}
	if err := l.db.WithContext(ctx).
		Where("player_id = ? AND ship_symbol = ?", playerID, symbol).
		Delete(&UnreadableHullModel{}).Error; err != nil {
		return fmt.Errorf("close the unreadable-hull episode for %s: %w", symbol, err)
	}
	return nil
}

// Find returns one open episode.
func (l *UnreadableHullLedger) Find(ctx context.Context, playerID int, symbol string) (hullrepair.Record, bool, error) {
	if l.db == nil {
		return hullrepair.Record{}, false, errors.New("no database wired for the unreadable-hull ledger")
	}
	var row UnreadableHullModel
	err := l.db.WithContext(ctx).
		Where("player_id = ? AND ship_symbol = ?", playerID, symbol).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return hullrepair.Record{}, false, nil
	}
	if err != nil {
		return hullrepair.Record{}, false, fmt.Errorf("read the unreadable-hull episode for %s: %w", symbol, err)
	}
	return unreadableHullRecord(row), true, nil
}

// ListOpen returns every open episode for a player, escalated ones included, so an
// operator can see what the repair gave up on.
func (l *UnreadableHullLedger) ListOpen(ctx context.Context, playerID int) ([]hullrepair.Record, error) {
	if l.db == nil {
		return nil, errors.New("no database wired for the unreadable-hull ledger")
	}
	var rows []UnreadableHullModel
	if err := l.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Order("first_seen_at").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list the unreadable-hull episodes: %w", err)
	}
	records := make([]hullrepair.Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, unreadableHullRecord(row))
	}
	return records, nil
}

func unreadableHullRecord(row UnreadableHullModel) hullrepair.Record {
	return hullrepair.Record{
		PlayerID:      row.PlayerID,
		ShipSymbol:    row.ShipSymbol,
		FirstSeenAt:   row.FirstSeenAt,
		Attempts:      row.Attempts,
		NextAttemptAt: row.NextAttemptAt,
		EscalatedAt:   row.EscalatedAt,
		LastOutcome:   row.LastOutcome,
		LastReason:    row.LastReason,
	}
}

func unreadableHullModel(rec hullrepair.Record) UnreadableHullModel {
	return UnreadableHullModel{
		PlayerID:      rec.PlayerID,
		ShipSymbol:    rec.ShipSymbol,
		FirstSeenAt:   rec.FirstSeenAt,
		LastSeenAt:    rec.FirstSeenAt,
		Attempts:      rec.Attempts,
		NextAttemptAt: rec.NextAttemptAt,
		EscalatedAt:   rec.EscalatedAt,
		LastOutcome:   rec.LastOutcome,
		LastReason:    rec.LastReason,
	}
}

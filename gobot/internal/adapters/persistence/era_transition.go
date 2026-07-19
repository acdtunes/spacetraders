package persistence

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// TransitionReport captures the outcome of an era transition (close prior + open new).
type TransitionReport struct {
	// ClosedEra is the prior era that was closed, or nil if none was open.
	ClosedEra *EraModel
	// ClosedCredits is the best-effort final_credits stamped on the closed era.
	ClosedCredits int64
	// NewPlayerID is the id of the freshly-created player row for the new era.
	NewPlayerID int
	// NewEra is the newly-opened era.
	NewEra *EraModel
}

// TransitionEra atomically closes the currently-open era (if any) — stamping
// closed_at + best-effort final_credits on it — and opens a new era for a freshly
// created player, all in one transaction.
//
// Unlike CloseEra it NEVER truncates the player-partitioned market_data /
// system_graphs caches: migration 032 keys history by player_id and the agent
// symbol is intentionally non-unique across eras, so prior-era history coexists
// with the new era via a distinct player_id. Truncating it would destroy the
// prior era's charts. It also leaves the prior player's token intact — the
// container drain, not a token blank, retires the old era — so this repository
// call is a pure era/player row flip.
//
// This is the era-flip half of `universe transition`.
func (r *EraRepository) TransitionEra(ctx context.Context, newPlayer *PlayerModel, newEra *EraModel) (*TransitionReport, error) {
	open, err := r.FindOpenEra(ctx)
	if err != nil {
		return nil, err
	}

	report := &TransitionReport{}
	now := time.Now().UTC()

	// Compute the closing era's final credits with the same anchor semantics as
	// CloseEra (read-only, outside the write transaction).
	if open != nil {
		credits, cerr := r.anchoredCredits(ctx, open.PlayerID)
		if cerr != nil {
			return nil, cerr
		}
		report.ClosedCredits = credits
	}

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if open != nil {
			if err := tx.Model(&EraModel{}).Where("era_id = ?", open.EraID).
				Updates(map[string]any{"closed_at": now, "final_credits": report.ClosedCredits}).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(newPlayer).Error; err != nil {
			return err
		}
		newEra.PlayerID = newPlayer.ID
		return tx.Create(newEra).Error
	})
	if err != nil {
		return nil, err
	}

	if open != nil {
		open.ClosedAt = &now
		open.FinalCredits = &report.ClosedCredits
		report.ClosedEra = open
	}
	report.NewPlayerID = newPlayer.ID
	report.NewEra = newEra
	return report, nil
}

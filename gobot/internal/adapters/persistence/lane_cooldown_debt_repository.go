package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LaneCooldownDebtRepositoryGORM is the durable half of the trade engine's full-lane compression
// ledger: the sink domain/trading.LaneCooldownLedger writes each accrual through to, and the source
// the daemon reloads it from at boot (RULINGS #2).
//
// It stores only what the ledger already holds — the lane key, the dimensionless debt fraction and
// the instant it was stamped — so a reload is exact and needs no price, no market depth and no
// reconstruction. That is the whole reason this table exists for the full-lane keys and not for the
// source-drain keys: those are rebuilt from the purchase rows, which record them durably already,
// while no transaction row anywhere carries a lane's (source, dest) pair.
type LaneCooldownDebtRepositoryGORM struct {
	db       *gorm.DB
	playerID int
}

// NewLaneCooldownDebtRepositoryGORM builds the lane-debt store for one player. The ledger key
// carries no player dimension, so the scope is baked in here rather than passed per call — the same
// per-player view over a shared table the depot and operation repositories take.
func NewLaneCooldownDebtRepositoryGORM(db *gorm.DB, playerID int) *LaneCooldownDebtRepositoryGORM {
	return &LaneCooldownDebtRepositoryGORM{db: db, playerID: playerID}
}

// Save upserts one lane's debt, keyed on (player, source, dest, good). The ledger stores a single
// decayed scalar per lane rather than a list of trades, so each accrual REPLACES the row rather
// than adding to it — the row is the lane's current debt, not a log of drains.
//
// It refuses a key with no destination. Those are the construction gate feed's source-drain keys,
// which are reconstructed from the purchase rows; a second durable copy of them here is one free to
// drift from the rows it duplicates. The ledger already filters them out before calling, so this is
// the second arm of that guard, not the only one.
func (r *LaneCooldownDebtRepositoryGORM) Save(ctx context.Context, key trading.LaneKey, debt float64, at time.Time) error {
	if key.Source == "" || key.Dest == "" || key.Good == "" {
		return nil
	}
	model := &LaneCooldownDebtModel{
		PlayerID:  r.playerID,
		Source:    key.Source,
		Dest:      key.Dest,
		Good:      key.Good,
		Debt:      debt,
		AccruedAt: at,
	}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "player_id"}, {Name: "source_waypoint"}, {Name: "dest_waypoint"}, {Name: "good_symbol"},
			},
			DoUpdates: clause.AssignmentColumns([]string{"debt", "accrued_at"}),
		}).
		Create(model).Error; err != nil {
		return fmt.Errorf("save lane cooldown debt %s->%s %s: %w", key.Source, key.Dest, key.Good, err)
	}
	return nil
}

// LaneDebt is one persisted lane's debt as of the instant it was accrued.
type LaneDebt struct {
	Key       trading.LaneKey
	Debt      float64
	AccruedAt time.Time
}

// LoadSince returns this player's lane debts stamped at or after `since` — the boot reload.
//
// The window is the caller's, and it matters: debt decays as exp(-dt/tau), so past a few tau a row
// restores a fraction of a percent of what it accrued while still occupying a key the double-count
// guard would then refuse. Rows older than the window are left in place rather than deleted; they
// are harmless, and a delete here would make a read path a writer.
func (r *LaneCooldownDebtRepositoryGORM) LoadSince(ctx context.Context, since time.Time) ([]LaneDebt, error) {
	var models []LaneCooldownDebtModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND accrued_at >= ? AND dest_waypoint <> ''", r.playerID, since).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("load lane cooldown debts: %w", err)
	}
	debts := make([]LaneDebt, 0, len(models))
	for _, m := range models {
		if m.Debt <= 0 {
			continue
		}
		debts = append(debts, LaneDebt{
			Key:       trading.LaneKey{Source: m.Source, Dest: m.Dest, Good: m.Good},
			Debt:      m.Debt,
			AccruedAt: m.AccruedAt,
		})
	}
	return debts, nil
}

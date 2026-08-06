package persistence

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

type EraRepository struct {
	db *gorm.DB
}

func NewEraRepository(db *gorm.DB) *EraRepository {
	return &EraRepository{db: db}
}

type CloseReport struct {
	Era           *EraModel
	AlreadyClosed bool
	FinalCredits  int64
	// FinalCreditsKnown reports whether FinalCredits is a READING or a placeholder (sp-2ms9x).
	//
	// It exists because a zero cannot speak for itself: an empty ledger and a genuinely bankrupt
	// agent both produce 0, and before this the caller had no way to tell them apart. A log line
	// alone was not enough — nothing a caller can branch on, and nothing a test can assert, so the
	// distinction was real in the code and invisible everywhere else.
	FinalCreditsKnown   bool
	WaypointsBackfilled int64
}

type ScrubReport struct {
	Era     *EraModel
	Deleted map[string]int64
	Total   int64
}

func (r *EraRepository) CreatePlayerWithEra(ctx context.Context, player *PlayerModel, era *EraModel) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(player).Error; err != nil {
			return err
		}
		era.PlayerID = player.ID
		return tx.Create(era).Error
	})
}

func (r *EraRepository) FindOpenEra(ctx context.Context) (*EraModel, error) {
	var era EraModel
	err := r.db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &era, nil
}

// IsContractGraduated reports whether the player's CURRENT era is contract-graduated (sp-difa.1) —
// the durable manual signal that the operator has retired contracts as the funding floor. Scoped by
// player_id to the player's era row (one-per-player; the most recent era_id if several linger), so a
// FRESH era reads its column default (false = UN-graduated). No era row yet ⇒ false (fail-OPEN: an
// unprovisioned/unknown player is treated as UN-graduated, so contracts run — a missing row must never
// silently suppress the funding floor). It is the shared read both the capacity reconciler and the
// bootstrap observer consult to decide whether to run the contract-delivery op.
func (r *EraRepository) IsContractGraduated(ctx context.Context, playerID int) (bool, error) {
	var eras []EraModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Order("era_id DESC").Limit(1).
		Find(&eras).Error; err != nil {
		return false, fmt.Errorf("failed to read contract-graduation for player %d: %w", playerID, err)
	}
	if len(eras) == 0 {
		return false, nil
	}
	return eras[0].ContractsGraduated, nil
}

// SetContractGraduated sets (graduate) or clears (ungraduate) the player's contract-graduation flag on
// its era row(s) (sp-difa.1) — the durable per-player era-scoped manual decision. Returns the number of
// era rows updated so the caller can distinguish a real change from "no era row for this player".
func (r *EraRepository) SetContractGraduated(ctx context.Context, playerID int, graduated bool) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&EraModel{}).
		Where("player_id = ?", playerID).
		Update("contracts_graduated", graduated)
	if res.Error != nil {
		return 0, fmt.Errorf("failed to set contract-graduation=%v for player %d: %w", graduated, playerID, res.Error)
	}
	return res.RowsAffected, nil
}

func (r *EraRepository) FindByName(ctx context.Context, name string) (*EraModel, error) {
	var era EraModel
	err := r.db.WithContext(ctx).Where("name = ?", name).First(&era).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("era not found: %s", name)
	}
	if err != nil {
		return nil, err
	}
	return &era, nil
}

func (r *EraRepository) CloseEra(ctx context.Context, name string) (*CloseReport, error) {
	era, err := r.FindByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if era.ClosedAt != nil {
		return &CloseReport{Era: era, AlreadyClosed: true}, nil
	}

	credits, known, err := r.anchoredCredits(ctx, era.PlayerID)
	if err != nil {
		return nil, err
	}
	if !known {
		// The figure recorded below is NOT a reading. Saying so here is the whole of sp-2ms9x for
		// this call site: previously a zero went into final_credits indistinguishable from a real one.
		log.Printf("WARNING: closing era %d with NO recorded transactions for player %d — final_credits is being stored as 0 because the balance is UNKNOWN, not because the agent was broke",
			era.EraID, era.PlayerID)
	}

	report := &CloseReport{Era: era, FinalCredits: credits, FinalCreditsKnown: known}
	now := time.Now().UTC()

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&EraModel{}).Where("era_id = ?", era.EraID).
			Updates(map[string]any{"closed_at": now, "final_credits": credits}).Error; err != nil {
			return err
		}
		if err := tx.Model(&PlayerModel{}).Where("id = ?", era.PlayerID).
			Update("token", "").Error; err != nil {
			return err
		}
		if err := truncateCaches(tx); err != nil {
			return err
		}
		res := tx.Model(&WaypointModel{}).Where("era_id IS NULL").Update("era_id", era.EraID)
		if res.Error != nil {
			return res.Error
		}
		report.WaypointsBackfilled = res.RowsAffected
		return nil
	})
	if err != nil {
		return nil, err
	}

	era.ClosedAt = &now
	era.FinalCredits = &credits
	return report, nil
}

func (r *EraRepository) ScrubEra(ctx context.Context, name string) (*ScrubReport, error) {
	era, err := r.FindByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if era.ClosedAt == nil {
		return nil, fmt.Errorf("refused: era %q is still open; close it before scrubbing", name)
	}

	report := &ScrubReport{Era: era, Deleted: map[string]int64{}}
	wipe := []struct {
		table string
		model any
	}{
		{"container_logs", &ContainerLogModel{}},
		{"containers", &ContainerModel{}},
		{"ships", &ShipModel{}},
		{"manufacturing_factory_states", &ManufacturingFactoryStateModel{}},
		{"gas_operations", &GasOperationModel{}},
		{"storage_operations", &StorageOperationModel{}},
	}

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, w := range wipe {
			res := tx.Where("player_id = ?", era.PlayerID).Delete(w.model)
			if res.Error != nil {
				return res.Error
			}
			report.Deleted[w.table] = res.RowsAffected
			report.Total += res.RowsAffected
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

// anchoredCredits reports the era's credit figure and whether it could be READ AT ALL.
//
// THE BOOLEAN IS THE POINT (sp-2ms9x). This used Find into a SINGLE STRUCT, which does not return
// ErrRecordNotFound: an empty result left the struct zero-valued with a nil error, so an unreadable
// balance became the value 0 and both callers wrote it to the database as the era's final_credits —
// a fabricated figure that later accounting reads as fact.
//
// It reports rather than refuses. An earlier attempt returned an error on the empty case, which
// broke era TRANSITION: a universe flip whose closing era has no recorded transactions is real and
// reachable (TestTransition_MintPathPersistsANonNullEraFaction), and blocking the flip is worse than
// recording an unknown. So the emptiness is surfaced to the caller, which decides — and both callers
// now say so in their logs instead of passing a silent zero along.
func (r *EraRepository) anchoredCredits(ctx context.Context, playerID int) (int64, bool, error) {
	var anchor TransactionModel
	err := r.db.WithContext(ctx).
		Where("player_id = ? AND transaction_type LIKE ?", playerID, "CONTRACT_%").
		Order("timestamp DESC, created_at DESC, id DESC").
		Limit(1).
		Find(&anchor).Error
	if err != nil {
		return 0, false, err
	}
	if anchor.ID != "" {
		var delta struct{ Sum int64 }
		err = r.db.WithContext(ctx).Model(&TransactionModel{}).
			Select("COALESCE(SUM(amount), 0) AS sum").
			Where("player_id = ? AND timestamp > ?", playerID, anchor.Timestamp).
			Scan(&delta).Error
		if err != nil {
			return 0, false, err
		}
		return int64(anchor.BalanceAfter) + delta.Sum, true, nil
	}

	// EMPTY IS NOT ZERO (sp-2ms9x). This used Find into a SINGLE STRUCT, which does not return
	// ErrRecordNotFound: an empty result left the struct zero-valued with a nil error, so an
	// unreadable balance became the value 0 — and both callers write that figure to the database as
	// the era's final_credits. A fabricated zero there is a permanent, wrong historical record, and
	// era close is exactly when a ledger may be empty.
	//
	// It fails rather than inventing a number. An era whose ledger holds nothing has final credits
	// that are genuinely UNKNOWN, and refusing to close on a figure we cannot read is the honest
	// answer — the alternative is a durable lie that later accounting reads as fact. Closing an era
	// with zero recorded transactions is anomalous in itself and worth surfacing.
	var latest TransactionModel
	err = r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Order("timestamp DESC, created_at DESC, id DESC").
		First(&latest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil // empty ledger: UNKNOWN, not zero — the caller must say so
	}
	if err != nil {
		return 0, false, err
	}
	return int64(latest.BalanceAfter), true, nil
}

func truncateCaches(tx *gorm.DB) error {
	if tx.Dialector.Name() == "postgres" {
		return tx.Exec("TRUNCATE market_data, system_graphs RESTART IDENTITY").Error
	}
	if err := tx.Exec("DELETE FROM market_data").Error; err != nil {
		return err
	}
	return tx.Exec("DELETE FROM system_graphs").Error
}

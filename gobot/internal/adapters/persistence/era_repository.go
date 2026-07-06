package persistence

import (
	"context"
	"errors"
	"fmt"
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
	Era                 *EraModel
	AlreadyClosed       bool
	FinalCredits        int64
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

	credits, err := r.anchoredCredits(ctx, era.PlayerID)
	if err != nil {
		return nil, err
	}

	report := &CloseReport{Era: era, FinalCredits: credits}
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

func (r *EraRepository) anchoredCredits(ctx context.Context, playerID int) (int64, error) {
	var anchor TransactionModel
	err := r.db.WithContext(ctx).
		Where("player_id = ? AND transaction_type LIKE ?", playerID, "CONTRACT_%").
		Order("timestamp DESC, created_at DESC, id DESC").
		Limit(1).
		Find(&anchor).Error
	if err != nil {
		return 0, err
	}
	if anchor.ID != "" {
		var delta struct{ Sum int64 }
		err = r.db.WithContext(ctx).Model(&TransactionModel{}).
			Select("COALESCE(SUM(amount), 0) AS sum").
			Where("player_id = ? AND timestamp > ?", playerID, anchor.Timestamp).
			Scan(&delta).Error
		if err != nil {
			return 0, err
		}
		return int64(anchor.BalanceAfter) + delta.Sum, nil
	}

	var latest TransactionModel
	err = r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Order("timestamp DESC, created_at DESC, id DESC").
		Limit(1).
		Find(&latest).Error
	if err != nil {
		return 0, err
	}
	return int64(latest.BalanceAfter), nil
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

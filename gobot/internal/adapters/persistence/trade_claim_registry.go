package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// TradeClaimRegistryGORM is the trade_claims adapter. Rows are stamped with the open era
// and reads are scoped to it, so a dead era's claims never penalise a live ranking.
type TradeClaimRegistryGORM struct {
	db *gorm.DB
}

var _ mvt.ClaimRegistry = (*TradeClaimRegistryGORM)(nil)

func NewTradeClaimRegistry(db *gorm.DB) *TradeClaimRegistryGORM {
	return &TradeClaimRegistryGORM{db: db}
}

func (r *TradeClaimRegistryGORM) openEraID(ctx context.Context) *int {
	var era EraModel
	if err := r.db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error; err != nil {
		return nil
	}
	id := era.EraID
	return &id
}

func (r *TradeClaimRegistryGORM) eraScope(ctx context.Context, q *gorm.DB) *gorm.DB {
	if era := r.openEraID(ctx); era != nil {
		return q.Where("era_id = ?", *era)
	}
	return q.Where("era_id IS NULL")
}

func (r *TradeClaimRegistryGORM) Upsert(ctx context.Context, playerID int, hull, system string, at time.Time) error {
	if r.db == nil {
		return errors.New("no database wired for the trade claim registry")
	}
	row := TradeClaimModel{PlayerID: playerID, Hull: hull, System: system, ClaimedAt: at, ArrivedAt: nil, EraID: r.openEraID(ctx)}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "player_id"}, {Name: "hull"}},
		DoUpdates: clause.AssignmentColumns([]string{"system", "claimed_at", "arrived_at", "era_id"}),
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("upsert trade claim %s→%s: %w", hull, system, err)
	}
	return nil
}

func (r *TradeClaimRegistryGORM) MarkArrived(ctx context.Context, playerID int, hull string, at time.Time) error {
	if err := r.db.WithContext(ctx).Model(&TradeClaimModel{}).
		Where("player_id = ? AND hull = ?", playerID, hull).
		Update("arrived_at", at).Error; err != nil {
		return fmt.Errorf("mark trade claim arrived %s: %w", hull, err)
	}
	return nil
}

func (r *TradeClaimRegistryGORM) Release(ctx context.Context, playerID int, hull string) error {
	if err := r.db.WithContext(ctx).Where("player_id = ? AND hull = ?", playerID, hull).Delete(&TradeClaimModel{}).Error; err != nil {
		return fmt.Errorf("release trade claim %s: %w", hull, err)
	}
	return nil
}

func (r *TradeClaimRegistryGORM) Get(ctx context.Context, playerID int, hull string) (mvt.Claim, bool, error) {
	var row TradeClaimModel
	err := r.eraScope(ctx, r.db.WithContext(ctx).Where("player_id = ? AND hull = ?", playerID, hull)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return mvt.Claim{}, false, nil
	}
	if err != nil {
		return mvt.Claim{}, false, fmt.Errorf("read trade claim %s: %w", hull, err)
	}
	return mvt.Claim{Hull: row.Hull, System: row.System, ClaimedAt: row.ClaimedAt, ArrivedAt: row.ArrivedAt}, true, nil
}

func (r *TradeClaimRegistryGORM) InTransit(ctx context.Context, playerID int) (map[string]int, error) {
	var rows []struct {
		System string
		N      int
	}
	q := r.eraScope(ctx, r.db.WithContext(ctx).Model(&TradeClaimModel{}).
		Select("system, COUNT(*) AS n").
		Where("player_id = ? AND arrived_at IS NULL", playerID)).
		Group("system")
	if err := q.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("count in-transit trade claims: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		out[row.System] = row.N
	}
	return out, nil
}

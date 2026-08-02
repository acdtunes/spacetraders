package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type HistoryRepository struct {
	db *gorm.DB
}

func NewHistoryRepository(db *gorm.DB) *HistoryRepository {
	return &HistoryRepository{db: db}
}

func (r *HistoryRepository) eraPlayerIDs(ctx context.Context, eraID *int) ([]int, map[int]int, error) {
	var eras []EraModel
	q := r.db.WithContext(ctx)
	if eraID != nil {
		q = q.Where("era_id = ?", *eraID)
	}
	if err := q.Find(&eras).Error; err != nil {
		return nil, nil, fmt.Errorf("failed to load eras: %w", err)
	}
	ids := make([]int, 0, len(eras))
	playerToEra := make(map[int]int, len(eras))
	for _, e := range eras {
		ids = append(ids, e.PlayerID)
		playerToEra[e.PlayerID] = e.EraID
	}
	return ids, playerToEra, nil
}

// CurrentEraID resolves the era a player belongs to — the CURRENT universe when playerID is the
// running agent. It is how a RUNTIME demand mine confines a delivery-system scope to the current
// universe: SpaceTraders regenerates the universe on each weekly reset and REUSES
// system symbols, so a system symbol alone does NOT identify a universe — a nil (all-eras) scope
// joined to a system filter aggregates every past universe that reused that symbol. Returns nil
// when the player has no era row (fail-open: the caller then scopes to all eras, the prior
// behavior). Each era row carries a single player_id, so a player maps to exactly one era.
func (r *HistoryRepository) CurrentEraID(ctx context.Context, playerID int) (*int, error) {
	var eras []EraModel
	if err := r.db.WithContext(ctx).Where("player_id = ?", playerID).Limit(1).Find(&eras).Error; err != nil {
		return nil, fmt.Errorf("failed to resolve current era for player %d: %w", playerID, err)
	}
	if len(eras) == 0 {
		return nil, nil
	}
	return &eras[0].EraID, nil
}

func (r *HistoryRepository) eraNames(ctx context.Context) (map[int]string, error) {
	var eras []EraModel
	if err := r.db.WithContext(ctx).Find(&eras).Error; err != nil {
		return nil, fmt.Errorf("failed to load eras: %w", err)
	}
	names := make(map[int]string, len(eras))
	for _, e := range eras {
		names[e.EraID] = e.Name
	}
	return names, nil
}

func (r *HistoryRepository) ListEras(ctx context.Context) ([]EraOverview, error) {
	var eras []EraModel
	if err := r.db.WithContext(ctx).Order("era_id ASC").Find(&eras).Error; err != nil {
		return nil, fmt.Errorf("failed to list eras: %w", err)
	}

	out := make([]EraOverview, 0, len(eras))
	for _, e := range eras {
		o := EraOverview{
			EraID:       e.EraID,
			Name:        e.Name,
			AgentSymbol: e.AgentSymbol,
		}
		if e.Faction != nil {
			o.Faction = *e.Faction
		}
		if e.UniverseResetDate != nil {
			o.UniverseResetDate = e.UniverseResetDate.Format("2006-01-02")
		}
		if e.RegisteredAt != nil {
			o.RegisteredAt = e.RegisteredAt.Format(time.RFC3339)
		}
		if e.ClosedAt != nil {
			o.ClosedAt = e.ClosedAt.Format(time.RFC3339)
		}
		if e.FinalCredits != nil {
			o.FinalCredits = *e.FinalCredits
		}
		if e.RegisteredAt != nil {
			end := time.Now()
			if e.ClosedAt != nil {
				end = *e.ClosedAt
			}
			o.DurationDays = end.Sub(*e.RegisteredAt).Hours() / 24
		}
		out = append(out, o)
	}
	return out, nil
}

func (r *HistoryRepository) LatestClosedEraID(ctx context.Context) (*int, error) {
	var era EraModel
	err := r.db.WithContext(ctx).
		Where("closed_at IS NOT NULL").
		Order("era_id DESC").
		First(&era).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find latest closed era: %w", err)
	}
	return &era.EraID, nil
}

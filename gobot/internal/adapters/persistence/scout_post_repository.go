package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"gorm.io/gorm"
)

// GormScoutPostRepository persists the desired-state scout posts table (sp-cxpq)
// with GORM. Reads are strictly scoped to the open era: a post from a prior,
// now-closed era is invisible, so the coordinator never tries to man dead-era
// posts after a universe reset (the sp-njpu class of cross-era zombie). Writes
// stamp the open era's id, and a re-add in a new era reuses the (player, system)
// row rather than colliding on the unique index.
type GormScoutPostRepository struct {
	db *gorm.DB
}

// NewGormScoutPostRepository creates a new GORM scout post repository.
func NewGormScoutPostRepository(db *gorm.DB) *GormScoutPostRepository {
	return &GormScoutPostRepository{db: db}
}

// openEraID returns the current open era's id, or nil when every era is closed.
func (r *GormScoutPostRepository) openEraID(ctx context.Context) *int {
	var era EraModel
	err := r.db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error
	if err != nil {
		return nil
	}
	id := era.EraID
	return &id
}

// ListActive returns every post owned by playerID in the open era.
func (r *GormScoutPostRepository) ListActive(ctx context.Context, playerID int) ([]*domainScouting.ScoutPost, error) {
	openEra := r.openEraID(ctx)
	if openEra == nil {
		// No open era → nothing is live. Return empty, not an error: the
		// coordinator polls this every tick and a between-eras gap is normal.
		return nil, nil
	}

	var models []ScoutPostModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND era_id = ?", playerID, *openEra).
		Order("system_symbol ASC").
		Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list scout posts: %w", result.Error)
	}

	posts := make([]*domainScouting.ScoutPost, len(models))
	for i := range models {
		posts[i] = modelToScoutPost(&models[i])
	}
	return posts, nil
}

// Upsert writes the full desired state of post keyed by (PlayerID, SystemSymbol),
// stamping the open era. It never merges — the caller owns every field.
func (r *GormScoutPostRepository) Upsert(ctx context.Context, post *domainScouting.ScoutPost) error {
	openEra := r.openEraID(ctx)
	if openEra == nil {
		return fmt.Errorf("cannot upsert scout post: no open era")
	}

	model := scoutPostToModel(post)
	model.EraID = openEra

	var existing ScoutPostModel
	err := r.db.WithContext(ctx).
		Where("player_id = ? AND system_symbol = ?", post.PlayerID, post.SystemSymbol).
		First(&existing).Error
	switch {
	case err == nil:
		// Reuse the existing row (and its created_at + id), restamping era so a
		// re-add in a new era revives a dead-era row instead of colliding.
		model.ID = existing.ID
		model.CreatedAt = existing.CreatedAt
		if saveErr := r.db.WithContext(ctx).Save(model).Error; saveErr != nil {
			return fmt.Errorf("failed to update scout post: %w", saveErr)
		}
		post.ID = existing.ID
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		if model.CreatedAt.IsZero() {
			model.CreatedAt = time.Now()
		}
		if createErr := r.db.WithContext(ctx).Create(model).Error; createErr != nil {
			return fmt.Errorf("failed to create scout post: %w", createErr)
		}
		post.ID = model.ID
		return nil
	default:
		return fmt.Errorf("failed to look up scout post: %w", err)
	}
}

// Remove deletes the post for (playerID, systemSymbol). Not finding a row to
// delete is not an error.
func (r *GormScoutPostRepository) Remove(ctx context.Context, playerID int, systemSymbol string) error {
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND system_symbol = ?", playerID, systemSymbol).
		Delete(&ScoutPostModel{})
	if result.Error != nil {
		return fmt.Errorf("failed to remove scout post: %w", result.Error)
	}
	return nil
}

func scoutPostToModel(p *domainScouting.ScoutPost) *ScoutPostModel {
	return &ScoutPostModel{
		ID:                     p.ID,
		PlayerID:               p.PlayerID,
		SystemSymbol:           p.SystemSymbol,
		FreshnessTargetSeconds: int(p.FreshnessTarget.Seconds()),
		Kind:                   string(p.Kind),
		AssignedHull:           stringToPtr(p.AssignedHull),
		TourContainerID:        stringToPtr(p.TourContainerID),
		RepositionContainerID:  stringToPtr(p.RepositionContainerID),
		CreatedAt:              p.CreatedAt,
	}
}

func modelToScoutPost(m *ScoutPostModel) *domainScouting.ScoutPost {
	return &domainScouting.ScoutPost{
		ID:                    m.ID,
		PlayerID:              m.PlayerID,
		SystemSymbol:          m.SystemSymbol,
		FreshnessTarget:       time.Duration(m.FreshnessTargetSeconds) * time.Second,
		Kind:                  domainScouting.PostKind(m.Kind),
		AssignedHull:          derefString(m.AssignedHull),
		TourContainerID:       derefString(m.TourContainerID),
		RepositionContainerID: derefString(m.RepositionContainerID),
		CreatedAt:             m.CreatedAt,
	}
}

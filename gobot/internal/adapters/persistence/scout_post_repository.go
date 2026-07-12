package persistence

import (
	"context"
	"encoding/json"
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
	hulls := p.Hulls
	if hulls < 1 {
		hulls = 1
	}
	return &ScoutPostModel{
		ID:                     p.ID,
		PlayerID:               p.PlayerID,
		SystemSymbol:           p.SystemSymbol,
		FreshnessTargetSeconds: int(p.FreshnessTarget.Seconds()),
		Kind:                   string(p.Kind),
		AssignedHull:           stringToPtr(p.AssignedHull),
		TourContainerID:        stringToPtr(p.TourContainerID),
		RepositionContainerID:  stringToPtr(p.RepositionContainerID),
		Hulls:                  hulls,
		PrimaryPartition:       marshalPartition(p.PrimaryPartition),
		ExtraSlots:             marshalExtraSlots(p.ExtraSlots),
		RespawnAttempts:        p.RespawnAttempts,
		RespawnParkedUntil:     timeToPtr(p.RespawnParkedUntil),
		CreatedAt:              p.CreatedAt,
	}
}

func modelToScoutPost(m *ScoutPostModel) *domainScouting.ScoutPost {
	hulls := m.Hulls
	if hulls < 1 {
		hulls = 1 // a legacy row (column added by AutoMigrate) reads as single-hull.
	}
	return &domainScouting.ScoutPost{
		ID:                    m.ID,
		PlayerID:              m.PlayerID,
		SystemSymbol:          m.SystemSymbol,
		FreshnessTarget:       time.Duration(m.FreshnessTargetSeconds) * time.Second,
		Kind:                  domainScouting.PostKind(m.Kind),
		AssignedHull:          derefString(m.AssignedHull),
		TourContainerID:       derefString(m.TourContainerID),
		RepositionContainerID: derefString(m.RepositionContainerID),
		Hulls:                 hulls,
		PrimaryPartition:      unmarshalPartition(m.PrimaryPartition),
		ExtraSlots:            unmarshalExtraSlots(m.ExtraSlots),
		RespawnAttempts:       m.RespawnAttempts,
		RespawnParkedUntil:    derefTime(m.RespawnParkedUntil),
		CreatedAt:             m.CreatedAt,
	}
}

// marshalPartition JSON-encodes a slot's market list, returning nil for an empty
// partition so a single-hull row leaves primary_partition NULL (byte-identical to
// the pre-enry layout) (sp-enry).
func marshalPartition(markets []string) *string {
	if len(markets) == 0 {
		return nil
	}
	b, err := json.Marshal(markets)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

// unmarshalPartition decodes a slot's market list; a NULL/empty/garbled column
// reads as no partition (the tour-all-markets default).
func unmarshalPartition(raw *string) []string {
	if raw == nil || *raw == "" {
		return nil
	}
	var markets []string
	if err := json.Unmarshal([]byte(*raw), &markets); err != nil {
		return nil
	}
	return markets
}

// extraSlotDTO is the persisted shape of a non-primary manning slot (sp-enry): the
// same fields as the primary (scalar columns), carried in the extra_slots JSON array.
type extraSlotDTO struct {
	AssignedHull          string   `json:"assigned_hull,omitempty"`
	TourContainerID       string   `json:"tour_container_id,omitempty"`
	RepositionContainerID string   `json:"reposition_container_id,omitempty"`
	Partition             []string `json:"partition,omitempty"`
}

// marshalExtraSlots JSON-encodes slots 1..N-1, returning nil for a single-hull post
// so extra_slots stays NULL (byte-identical to the pre-enry layout) (sp-enry).
func marshalExtraSlots(slots []domainScouting.ScoutPostSlot) *string {
	if len(slots) == 0 {
		return nil
	}
	dtos := make([]extraSlotDTO, len(slots))
	for i, s := range slots {
		dtos[i] = extraSlotDTO{
			AssignedHull:          s.AssignedHull,
			TourContainerID:       s.TourContainerID,
			RepositionContainerID: s.RepositionContainerID,
			Partition:             s.Partition,
		}
	}
	b, err := json.Marshal(dtos)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

// unmarshalExtraSlots decodes slots 1..N-1; a NULL/empty/garbled column reads as no
// extra slots (a single-hull post).
func unmarshalExtraSlots(raw *string) []domainScouting.ScoutPostSlot {
	if raw == nil || *raw == "" {
		return nil
	}
	var dtos []extraSlotDTO
	if err := json.Unmarshal([]byte(*raw), &dtos); err != nil {
		return nil
	}
	slots := make([]domainScouting.ScoutPostSlot, len(dtos))
	for i, d := range dtos {
		slots[i] = domainScouting.ScoutPostSlot{
			AssignedHull:          d.AssignedHull,
			TourContainerID:       d.TourContainerID,
			RepositionContainerID: d.RepositionContainerID,
			Partition:             d.Partition,
		}
	}
	return slots
}

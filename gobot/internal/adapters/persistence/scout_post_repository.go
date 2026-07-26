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

// GormScoutPostRepository persists the desired-state scout posts table
// with GORM. Reads are strictly scoped to the open era: a post from a prior,
// now-closed era is invisible, so the coordinator never tries to man dead-era
// posts after a universe reset (the cross-era zombie class). Writes
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

// UpdateHulls updates ONLY the hull budget of the (playerID, systemSymbol) post in the
// open era: the market-freshness auto-sizer's narrow, manning-preserving resize
// seam. Unlike Upsert (which writes the whole row and would clobber any assignment the
// scout reconciler concurrently wrote), this touches a single column, so resizing a live
// post can never lose its manning. Updating a post that does not exist is a no-op, not an
// error — the caller declares a missing post through Upsert instead.
func (r *GormScoutPostRepository) UpdateHulls(ctx context.Context, playerID int, systemSymbol string, hulls int) error {
	openEra := r.openEraID(ctx)
	if openEra == nil {
		return fmt.Errorf("cannot update scout post hulls: no open era")
	}
	result := r.db.WithContext(ctx).
		Model(&ScoutPostModel{}).
		Where("player_id = ? AND system_symbol = ? AND era_id = ?", playerID, systemSymbol, *openEra).
		Update("hulls", hulls)
	if result.Error != nil {
		return fmt.Errorf("failed to update scout post hulls: %w", result.Error)
	}
	return nil
}

// UpdateSensingState updates ONLY the sensing-owned columns — hulls, dormant, and
// hot_waypoints — of the (playerID, systemSymbol) post in the open era: the probe-sensing
// coordinator's narrow live-post delta seam, mirroring UpdateHulls. A full-row Upsert here
// would clobber the manning/partition/respawn columns the scout reconciler concurrently
// writes and the min_hulls floor bootstrap stamps behind a once-latch (a revert it would
// never repair). Updating a post that does not exist is a no-op, not an error — the caller
// declares a missing post through Upsert instead.
func (r *GormScoutPostRepository) UpdateSensingState(ctx context.Context, playerID int, systemSymbol string, hulls int, dormant bool, hotWaypoints []string) error {
	openEra := r.openEraID(ctx)
	if openEra == nil {
		return fmt.Errorf("cannot update scout post sensing state: no open era")
	}
	result := r.db.WithContext(ctx).
		Model(&ScoutPostModel{}).
		Where("player_id = ? AND system_symbol = ? AND era_id = ?", playerID, systemSymbol, *openEra).
		Updates(map[string]interface{}{
			"hulls":         hulls,
			"dormant":       dormant,
			"hot_waypoints": marshalPartition(hotWaypoints),
		})
	if result.Error != nil {
		return fmt.Errorf("failed to update scout post sensing state: %w", result.Error)
	}
	return nil
}

// UpdateMinHulls updates ONLY the manning-floor column of the (playerID, systemSymbol)
// post in the open era: the narrow seam bootstrap uses to stamp the home post's permanent
// probe_target floor (sp-2ci9y) without disturbing the freshsizer's Hulls resize or the
// reconciler's manning — a single-column write, so it cannot clobber either. min_hulls is a
// DISJOINT column from hulls: bootstrap is the sole writer of the floor, the freshsizer the
// sole writer of the budget, so the two never oscillate. Updating a post that does not exist
// is a no-op (the caller declares a missing post first).
func (r *GormScoutPostRepository) UpdateMinHulls(ctx context.Context, playerID int, systemSymbol string, minHulls int) error {
	openEra := r.openEraID(ctx)
	if openEra == nil {
		return fmt.Errorf("cannot update scout post min hulls: no open era")
	}
	result := r.db.WithContext(ctx).
		Model(&ScoutPostModel{}).
		Where("player_id = ? AND system_symbol = ? AND era_id = ?", playerID, systemSymbol, *openEra).
		Update("min_hulls", minHulls)
	if result.Error != nil {
		return fmt.Errorf("failed to update scout post min hulls: %w", result.Error)
	}
	return nil
}

// IsDormant reports whether the (playerID, system) post in the open era is
// dormant — the scout tour's park-in-place read (scouting.DormancyReader). A
// missing post, like a between-eras gap, reads as NOT dormant: only an
// explicit rotation bit may park a probe. Errors are surfaced so the consumer
// can fail toward scanning.
func (r *GormScoutPostRepository) IsDormant(ctx context.Context, playerID int, system string) (bool, error) {
	openEra := r.openEraID(ctx)
	if openEra == nil {
		return false, nil
	}
	var model ScoutPostModel
	err := r.db.WithContext(ctx).
		Where("player_id = ? AND system_symbol = ? AND era_id = ?", playerID, system, *openEra).
		First(&model).Error
	switch {
	case err == nil:
		return model.Dormant, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("failed to read scout post dormancy: %w", err)
	}
}

// HotWaypoints returns the (playerID, system) STANDING post's stage-2 circuit
// in the open era — the scout tour's restriction read (scouting.DormancyReader).
// A missing post, a between-eras gap, and a sweep-once post all read empty:
// only a standing post may narrow a circuit, and a sweep's one pass IS the
// first scan, so it must see everything. Errors are surfaced so the consumer
// can fail toward the full circuit.
func (r *GormScoutPostRepository) HotWaypoints(ctx context.Context, playerID int, system string) ([]string, error) {
	openEra := r.openEraID(ctx)
	if openEra == nil {
		return nil, nil
	}
	var model ScoutPostModel
	err := r.db.WithContext(ctx).
		Where("player_id = ? AND system_symbol = ? AND era_id = ?", playerID, system, *openEra).
		First(&model).Error
	switch {
	case err == nil:
		if model.Kind != string(domainScouting.PostKindStanding) {
			return nil, nil
		}
		return unmarshalPartition(model.HotWaypoints), nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("failed to read scout post hot waypoints: %w", err)
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
		MinHulls:               p.MinHulls,
		Dormant:                p.Dormant,
		HotWaypoints:           marshalPartition(p.HotWaypoints),
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
		MinHulls:              m.MinHulls,
		Dormant:               m.Dormant,
		HotWaypoints:          unmarshalPartition(m.HotWaypoints),
		PrimaryPartition:      unmarshalPartition(m.PrimaryPartition),
		ExtraSlots:            unmarshalExtraSlots(m.ExtraSlots),
		RespawnAttempts:       m.RespawnAttempts,
		RespawnParkedUntil:    derefTime(m.RespawnParkedUntil),
		CreatedAt:             m.CreatedAt,
	}
}

// marshalPartition JSON-encodes a slot's market list, returning nil for an empty
// partition so a single-hull row leaves primary_partition NULL.
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

// extraSlotDTO is the persisted shape of a non-primary manning slot: the
// same fields as the primary (scalar columns), carried in the extra_slots JSON array.
type extraSlotDTO struct {
	AssignedHull          string   `json:"assigned_hull,omitempty"`
	TourContainerID       string   `json:"tour_container_id,omitempty"`
	RepositionContainerID string   `json:"reposition_container_id,omitempty"`
	Partition             []string `json:"partition,omitempty"`
}

// marshalExtraSlots JSON-encodes slots 1..N-1, returning nil for a single-hull post
// so extra_slots stays NULL.
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

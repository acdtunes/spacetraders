package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GormSystemGraphRepository implements SystemGraphRepository using GORM
type GormSystemGraphRepository struct {
	db *gorm.DB
}

// NewGormSystemGraphRepository creates a new GORM-based system graph repository
func NewGormSystemGraphRepository(db *gorm.DB) system.SystemGraphRepository {
	return &GormSystemGraphRepository{
		db: db,
	}
}

// Get retrieves a system's cached graph, scoped to the open era. A closed era's graph reads as
// a MISS and rebuilds: its waypoints no longer exist, and routing on them 4201s forever.
func (r *GormSystemGraphRepository) Get(ctx context.Context, systemSymbol string) (*system.NavigationGraph, error) {
	var model SystemGraphModel

	predicate, args := eraScopePredicate(r.openEraID(ctx))
	err := r.db.WithContext(ctx).
		Where("system_symbol = ?", systemSymbol).
		Where(predicate, args...).
		First(&model).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil // Cache miss
		}
		return nil, fmt.Errorf("failed to get system graph: %w", err)
	}

	var graph system.NavigationGraph
	if err := json.Unmarshal([]byte(model.GraphData), &graph); err != nil {
		return nil, fmt.Errorf("failed to unmarshal graph data: %w", err)
	}

	return &graph, nil
}

// Add persists a graph for a system (upsert)
func (r *GormSystemGraphRepository) Add(ctx context.Context, systemSymbol string, graph *system.NavigationGraph) error {
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		return fmt.Errorf("failed to marshal graph: %w", err)
	}

	now := time.Now()
	model := SystemGraphModel{
		SystemSymbol: systemSymbol,
		GraphData:    string(graphJSON),
		EraID:        r.openEraID(ctx),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	// era_id is rewritten on conflict, so the row a previous era left behind is re-stamped by
	// the first write of this one rather than lingering as a permanent miss.
	err = r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "system_symbol"}},
			DoUpdates: clause.AssignmentColumns([]string{"graph_data", "updated_at", "era_id"}),
		}).
		Create(&model).Error

	if err != nil {
		return fmt.Errorf("failed to add system graph: %w", err)
	}

	return nil
}

// openEraID mirrors GormWaypointRepository.openEraID; nil scopes to NULL era_id rows.
func (r *GormSystemGraphRepository) openEraID(ctx context.Context) *int {
	var era EraModel
	if err := r.db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error; err != nil {
		return nil
	}
	id := era.EraID
	return &id
}

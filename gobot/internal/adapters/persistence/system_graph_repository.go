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
	db        *gorm.DB
	converter system.IWaypointConverter
}

// NewGormSystemGraphRepository creates a new GORM-based system graph repository
func NewGormSystemGraphRepository(db *gorm.DB, converter system.IWaypointConverter) system.SystemGraphRepository {
	return &GormSystemGraphRepository{
		db:        db,
		converter: converter,
	}
}

// Get retrieves a graph for a system from cache
func (r *GormSystemGraphRepository) Get(ctx context.Context, systemSymbol string) (*system.NavigationGraph, error) {
	var model SystemGraphModel

	err := r.db.WithContext(ctx).
		Where("system_symbol = ?", systemSymbol).
		First(&model).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil // Cache miss
		}
		return nil, fmt.Errorf("failed to get system graph: %w", err)
	}

	// Parse JSON to legacy format first
	var legacyGraph map[string]interface{}
	if err := json.Unmarshal([]byte(model.GraphData), &legacyGraph); err != nil {
		return nil, fmt.Errorf("failed to unmarshal graph data: %w", err)
	}

	// Convert from legacy format to NavigationGraph
	graph, err := system.FromLegacyFormat(legacyGraph, r.converter)
	if err != nil {
		return nil, fmt.Errorf("failed to convert legacy graph format: %w", err)
	}

	return graph, nil
}

// Add persists a graph for a system (upsert)
func (r *GormSystemGraphRepository) Add(ctx context.Context, systemSymbol string, graph *system.NavigationGraph) error {
	// Convert to legacy format for JSON serialization
	legacyGraph := graph.ToLegacyFormat()

	// Marshal to JSON
	graphJSON, err := json.Marshal(legacyGraph)
	if err != nil {
		return fmt.Errorf("failed to marshal graph: %w", err)
	}

	now := time.Now()
	model := SystemGraphModel{
		SystemSymbol: systemSymbol,
		GraphData:    string(graphJSON),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	// Upsert: Insert or update if exists
	err = r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "system_symbol"}},
			DoUpdates: clause.AssignmentColumns([]string{"graph_data", "updated_at"}),
		}).
		Create(&model).Error

	if err != nil {
		return fmt.Errorf("failed to add system graph: %w", err)
	}

	return nil
}

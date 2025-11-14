package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// GormWaypointRepository implements WaypointRepository using GORM
type GormWaypointRepository struct {
	db *gorm.DB
}

// NewGormWaypointRepository creates a new GORM waypoint repository
func NewGormWaypointRepository(db *gorm.DB) *GormWaypointRepository {
	return &GormWaypointRepository{db: db}
}

// FindBySymbol retrieves a waypoint by symbol
func (r *GormWaypointRepository) FindBySymbol(ctx context.Context, symbol, systemSymbol string) (*shared.Waypoint, error) {
	var model WaypointModel
	result := r.db.WithContext(ctx).Where("waypoint_symbol = ? AND system_symbol = ?", symbol, systemSymbol).First(&model)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("waypoint not found: %s", symbol)
		}
		return nil, fmt.Errorf("failed to find waypoint: %w", result.Error)
	}

	return r.modelToWaypoint(&model)
}

// ListBySystem retrieves all waypoints in a system
func (r *GormWaypointRepository) ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error) {
	var models []WaypointModel
	result := r.db.WithContext(ctx).Where("system_symbol = ?", systemSymbol).Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list waypoints: %w", result.Error)
	}

	waypoints := make([]*shared.Waypoint, 0, len(models))
	for _, model := range models {
		waypoint, err := r.modelToWaypoint(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert waypoint %s: %w", model.WaypointSymbol, err)
		}
		waypoints = append(waypoints, waypoint)
	}

	return waypoints, nil
}

// ListBySystemWithTrait retrieves waypoints in a system filtered by a specific trait
func (r *GormWaypointRepository) ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error) {
	var models []WaypointModel
	// Use LIKE with JSON array pattern to find trait in JSON array string
	// Handles both ["TRAIT"] and ["OTHER","TRAIT"] patterns
	pattern := fmt.Sprintf("%%\"%s\"%%", trait)
	result := r.db.WithContext(ctx).
		Where("system_symbol = ? AND traits LIKE ?", systemSymbol, pattern).
		Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list waypoints by trait: %w", result.Error)
	}

	waypoints := make([]*shared.Waypoint, 0, len(models))
	for _, model := range models {
		waypoint, err := r.modelToWaypoint(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert waypoint %s: %w", model.WaypointSymbol, err)
		}
		waypoints = append(waypoints, waypoint)
	}

	return waypoints, nil
}

// ListBySystemWithType retrieves waypoints in a system filtered by waypoint type
func (r *GormWaypointRepository) ListBySystemWithType(ctx context.Context, systemSymbol, waypointType string) ([]*shared.Waypoint, error) {
	var models []WaypointModel
	result := r.db.WithContext(ctx).
		Where("system_symbol = ? AND type = ?", systemSymbol, waypointType).
		Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list waypoints by type: %w", result.Error)
	}

	waypoints := make([]*shared.Waypoint, 0, len(models))
	for _, model := range models {
		waypoint, err := r.modelToWaypoint(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert waypoint %s: %w", model.WaypointSymbol, err)
		}
		waypoints = append(waypoints, waypoint)
	}

	return waypoints, nil
}

// ListBySystemWithFuel retrieves waypoints in a system that have fuel stations
func (r *GormWaypointRepository) ListBySystemWithFuel(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error) {
	var models []WaypointModel
	result := r.db.WithContext(ctx).
		Where("system_symbol = ? AND has_fuel = 1", systemSymbol).
		Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list waypoints with fuel: %w", result.Error)
	}

	waypoints := make([]*shared.Waypoint, 0, len(models))
	for _, model := range models {
		waypoint, err := r.modelToWaypoint(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert waypoint %s: %w", model.WaypointSymbol, err)
		}
		waypoints = append(waypoints, waypoint)
	}

	return waypoints, nil
}

// Save persists a waypoint
func (r *GormWaypointRepository) Save(ctx context.Context, waypoint *shared.Waypoint) error {
	model, err := r.waypointToModel(waypoint)
	if err != nil {
		return fmt.Errorf("failed to convert waypoint to model: %w", err)
	}

	// Upsert: create or update
	result := r.db.WithContext(ctx).Save(model)
	if result.Error != nil {
		return fmt.Errorf("failed to save waypoint: %w", result.Error)
	}

	return nil
}

// modelToWaypoint converts database model to domain entity
func (r *GormWaypointRepository) modelToWaypoint(model *WaypointModel) (*shared.Waypoint, error) {
	waypoint, err := shared.NewWaypoint(model.WaypointSymbol, model.X, model.Y)
	if err != nil {
		return nil, err
	}

	waypoint.SystemSymbol = model.SystemSymbol
	waypoint.Type = model.Type
	waypoint.HasFuel = model.HasFuel == 1

	// Parse traits JSON array
	if model.Traits != "" {
		var traits []string
		if err := json.Unmarshal([]byte(model.Traits), &traits); err != nil {
			// If parsing fails, leave empty
			traits = []string{}
		}
		waypoint.Traits = traits
	}

	// Parse orbitals JSON array
	if model.Orbitals != "" {
		var orbitals []string
		if err := json.Unmarshal([]byte(model.Orbitals), &orbitals); err != nil {
			// If parsing fails, leave empty
			orbitals = []string{}
		}
		waypoint.Orbitals = orbitals
	}

	return waypoint, nil
}

// waypointToModel converts domain entity to database model
func (r *GormWaypointRepository) waypointToModel(waypoint *shared.Waypoint) (*WaypointModel, error) {
	hasFuel := 0
	if waypoint.HasFuel {
		hasFuel = 1
	}

	var traitsJSON string
	if len(waypoint.Traits) > 0 {
		bytes, err := json.Marshal(waypoint.Traits)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal traits: %w", err)
		}
		traitsJSON = string(bytes)
	}

	var orbitalsJSON string
	if len(waypoint.Orbitals) > 0 {
		bytes, err := json.Marshal(waypoint.Orbitals)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal orbitals: %w", err)
		}
		orbitalsJSON = string(bytes)
	}

	return &WaypointModel{
		WaypointSymbol: waypoint.Symbol,
		SystemSymbol:   waypoint.SystemSymbol,
		Type:           waypoint.Type,
		X:              waypoint.X,
		Y:              waypoint.Y,
		Traits:         traitsJSON,
		HasFuel:        hasFuel,
		Orbitals:       orbitalsJSON,
	}, nil
}

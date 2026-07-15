package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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

// FindBySymbol retrieves a waypoint by symbol with 1-day TTL validation
func (r *GormWaypointRepository) FindBySymbol(ctx context.Context, symbol, systemSymbol string) (*shared.Waypoint, error) {
	var model WaypointModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	result := r.db.WithContext(ctx).
		Where("waypoint_symbol = ? AND system_symbol = ?", symbol, systemSymbol).
		Where(predicate, args...).
		First(&model)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("waypoint not found: %s", symbol)
		}
		return nil, fmt.Errorf("failed to find waypoint: %w", result.Error)
	}

	// Check TTL (1 day) - if expired or no timestamp, treat as cache miss
	if model.SyncedAt != "" {
		syncedAt, err := time.Parse(time.RFC3339, model.SyncedAt)
		if err == nil && time.Since(syncedAt) < 24*time.Hour {
			return r.modelToWaypoint(&model)
		}
	}

	return nil, fmt.Errorf("waypoint cache expired: %s", symbol)
}

// HasWaypointTrait reports whether the waypoint bears the given trait, reading it
// as the IMMUTABLE physical fact it is: era-AGNOSTIC and TTL-AGNOSTIC. A waypoint's
// traits (SHIPYARD, MARKETPLACE, ...) and type are invariant across universe eras
// and never go stale — so, unlike FindBySymbol whose era-scope and 24h TTL are
// correct only for VOLATILE price/nav data, the cached row for the symbol is
// authoritative no matter which era stamped it or how long ago it was synced.
// This is the dedicated immutable-trait path (sp-42ow): FindBySymbol's gates were
// silently filtering out ~97 of 108 real SHIPYARD waypoints (prior-era and/or
// >24h stale), so the scout's shipyard scan no-op'd at virtually every yard.
//
// waypoint_symbol is the table's sole primary key, so at most one row exists per
// symbol; the era_id DESC order is defensive (prefer the newest-era row) and, since
// traits are immutable, the pick is behaviorally irrelevant either way. A missing
// row reads as (false, nil) — not an error — meaning the waypoint is simply not
// cached yet, so the caller retries once the cache is warm. A cheap local read: no
// API budget is spent probing the trait.
func (r *GormWaypointRepository) HasWaypointTrait(ctx context.Context, waypointSymbol, trait string) (bool, error) {
	var model WaypointModel
	result := r.db.WithContext(ctx).
		Where("waypoint_symbol = ?", waypointSymbol).
		Order("era_id DESC").
		First(&model)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, fmt.Errorf("failed to read waypoint trait for %s: %w", waypointSymbol, result.Error)
	}

	waypoint, err := r.modelToWaypoint(&model)
	if err != nil {
		return false, fmt.Errorf("failed to decode waypoint %s: %w", waypointSymbol, err)
	}
	return waypoint.HasTrait(trait), nil
}

// ListBySystem retrieves all waypoints in a system scoped to the open era.
// Rows carrying the open era's era_id and rows with a NULL era_id (pre-close
// transition, not yet backfilled) are considered live; closed-era rows are inert.
func (r *GormWaypointRepository) ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error) {
	var models []WaypointModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	result := r.db.WithContext(ctx).
		Where("system_symbol = ?", systemSymbol).
		Where(predicate, args...).
		Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list waypoints: %w", result.Error)
	}

	return r.modelsToWaypoints(models)
}

// ListBySystemForEra retrieves waypoints in a system for one explicit era,
// keeping closed-era history reachable after live reads have scoped it away.
func (r *GormWaypointRepository) ListBySystemForEra(ctx context.Context, systemSymbol string, eraID int) ([]*shared.Waypoint, error) {
	var models []WaypointModel
	result := r.db.WithContext(ctx).
		Where("system_symbol = ? AND era_id = ?", systemSymbol, eraID).
		Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list waypoints for era: %w", result.Error)
	}

	return r.modelsToWaypoints(models)
}

func (r *GormWaypointRepository) openEraID(ctx context.Context) *int {
	var era EraModel
	err := r.db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error
	if err != nil {
		return nil
	}
	id := era.EraID
	return &id
}

func eraScopePredicate(openEraID *int) (string, []any) {
	if openEraID == nil {
		return "era_id IS NULL", nil
	}
	return "(era_id = ? OR era_id IS NULL)", []any{*openEraID}
}

// ListWithTrait retrieves EVERY cached waypoint bearing the given trait across ALL
// systems, read as the IMMUTABLE physical fact it is: era-AGNOSTIC and TTL-agnostic,
// exactly like HasWaypointTrait. This is the sp-rhju backfill's charted-shipyard
// enumerator: an era-SCOPED read here would repeat the precise sp-42ow bug that
// filtered out ~97 of 108 real SHIPYARD waypoints (prior-era and/or stale rows), so
// the sweep would only ever see ~10% of the shipyards and the blind spot it exists to
// close would stay open. A physical SHIPYARD trait never changes across eras, so a
// prior-era row is still authoritative proof the system holds a shipyard; downstream
// the enumerator intersects this set with the CURRENT gate-reachable frontier, which
// filters any dead-universe symbol a probe could not actually be relayed to. A cheap
// local read — no API budget is spent.
func (r *GormWaypointRepository) ListWithTrait(ctx context.Context, trait string) ([]*shared.Waypoint, error) {
	pattern := fmt.Sprintf("%%\"%s\"%%", trait)
	var models []WaypointModel
	result := r.db.WithContext(ctx).
		Where("traits LIKE ?", pattern).
		Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list waypoints with trait %s: %w", trait, result.Error)
	}
	return r.modelsToWaypoints(models)
}

// ListBySystemWithTrait retrieves waypoints in a system filtered by a specific trait
func (r *GormWaypointRepository) ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error) {
	var models []WaypointModel
	// Use LIKE with JSON array pattern to find trait in JSON array string
	// Handles both ["TRAIT"] and ["OTHER","TRAIT"] patterns
	pattern := fmt.Sprintf("%%\"%s\"%%", trait)
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	result := r.db.WithContext(ctx).
		Where("system_symbol = ? AND traits LIKE ?", systemSymbol, pattern).
		Where(predicate, args...).
		Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list waypoints by trait: %w", result.Error)
	}

	return r.modelsToWaypoints(models)
}

// Add persists a waypoint
func (r *GormWaypointRepository) Add(ctx context.Context, waypoint *shared.Waypoint) error {
	model, err := r.waypointToModel(waypoint)
	if err != nil {
		return fmt.Errorf("failed to convert waypoint to model: %w", err)
	}

	model.EraID = r.openEraID(ctx)

	// Upsert: create or update
	result := r.db.WithContext(ctx).Save(model)
	if result.Error != nil {
		return fmt.Errorf("failed to add waypoint: %w", result.Error)
	}

	return nil
}

func (r *GormWaypointRepository) modelsToWaypoints(models []WaypointModel) ([]*shared.Waypoint, error) {
	waypoints := make([]*shared.Waypoint, 0, len(models))
	for i := range models {
		waypoint, err := r.modelToWaypoint(&models[i])
		if err != nil {
			return nil, fmt.Errorf("failed to convert waypoint %s: %w", models[i].WaypointSymbol, err)
		}
		waypoints = append(waypoints, waypoint)
	}

	return waypoints, nil
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
		SyncedAt:       time.Now().Format(time.RFC3339),
	}, nil
}

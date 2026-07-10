package persistence

import (
	"context"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// gateEdgeFreshWindow bounds how long a stored gate edge is trusted before a
// lookup treats it as stale and triggers a live re-fetch. Mirrors
// WaypointModel's own 24h TTL: the jump-gate topology is effectively static
// within an era (a gate's connection set does not churn hour-to-hour), so a day
// is a comfortable freshness bound that keeps the graph self-healing across a
// long-running daemon without hammering the API on every routing lookup.
const gateEdgeFreshWindow = 24 * time.Hour

// GormGateEdgeRepository implements system.GateEdgeRepository over GORM. It is
// the persisted gate-graph adjacency store (sp-7gr2). Every read is era-scoped
// exactly like GormWaypointRepository (openEraID + eraScopePredicate) so
// dead-era rows (sp-vapw) never leak into live routing; a system's edge set is
// REPLACED atomically on each sync so a since-severed connection cannot linger.
type GormGateEdgeRepository struct {
	db *gorm.DB
}

// NewGormGateEdgeRepository creates a new GORM-backed gate edge repository.
func NewGormGateEdgeRepository(db *gorm.DB) *GormGateEdgeRepository {
	return &GormGateEdgeRepository{db: db}
}

// Edges returns systemSymbol's stored neighbor edges, era-scoped. ok is false on
// a genuine miss (no rows) OR when the newest stored row is older than
// gateEdgeFreshWindow — both are lazy-refresh signals the service resolves by
// fetching the live gate. A NULL synced_at is treated as stale (unknown age →
// refresh), the inverse of the waypoint cache's "unknown age is fresh" choice:
// here a routing decision rides on the data, so the safe default is to re-fetch.
func (r *GormGateEdgeRepository) Edges(ctx context.Context, systemSymbol string) ([]system.GateEdge, bool, error) {
	var models []GateEdgeModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	if err := r.db.WithContext(ctx).
		Where("system_symbol = ?", systemSymbol).
		Where(predicate, args...).
		Find(&models).Error; err != nil {
		return nil, false, fmt.Errorf("failed to list gate edges for %s: %w", systemSymbol, err)
	}

	if len(models) == 0 {
		return nil, false, nil
	}
	if r.anyStale(models) {
		return nil, false, nil
	}

	edges := make([]system.GateEdge, 0, len(models))
	for _, m := range models {
		edges = append(edges, system.GateEdge{
			ConnectedSystem: m.ConnectedSystem,
			GateWaypoint:    m.GateWaypoint,
		})
	}
	return edges, true, nil
}

// GateWaypointOf returns systemSymbol's own jump-gate waypoint if any era-scoped
// edge records it as a connection (i.e. a neighbor's row (neighbor→systemSymbol)
// carries systemSymbol's gate as its GateWaypoint). This reverse lookup lets an
// uncharted system be fetched live without its system graph.
func (r *GormGateEdgeRepository) GateWaypointOf(ctx context.Context, systemSymbol string) (string, bool, error) {
	var model GateEdgeModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	err := r.db.WithContext(ctx).
		Where("connected_system = ?", systemSymbol).
		Where(predicate, args...).
		First(&model).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to resolve gate waypoint for %s: %w", systemSymbol, err)
	}
	return model.GateWaypoint, true, nil
}

// Replace atomically swaps systemSymbol's stored edge set for edges. It deletes
// every existing row for the system (across ALL eras, so a re-sync also purges a
// dead-era row for that system) then inserts the fresh set stamped with the open
// era and the current sync time. Delete-then-insert (not per-row upsert) gives
// correct "the adjacency is now exactly this" semantics: a connection dropped
// upstream disappears here too.
func (r *GormGateEdgeRepository) Replace(ctx context.Context, systemSymbol string, edges []system.GateEdge) error {
	eraID := r.openEraID(ctx)
	syncedAt := time.Now().Format(time.RFC3339)

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("system_symbol = ?", systemSymbol).Delete(&GateEdgeModel{}).Error; err != nil {
			return fmt.Errorf("failed to clear gate edges for %s: %w", systemSymbol, err)
		}
		if len(edges) == 0 {
			return nil
		}
		rows := make([]GateEdgeModel, 0, len(edges))
		for _, e := range edges {
			rows = append(rows, GateEdgeModel{
				SystemSymbol:    systemSymbol,
				ConnectedSystem: e.ConnectedSystem,
				GateWaypoint:    e.GateWaypoint,
				EraID:           eraID,
				SyncedAt:        syncedAt,
			})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("failed to insert gate edges for %s: %w", systemSymbol, err)
		}
		return nil
	})
}

// Adjacency returns every stored system's neighbor systems, era-scoped, sorted
// for a stable `system gates` overview. Pure read; the service layer does any
// live fetch-through for a specific system.
func (r *GormGateEdgeRepository) Adjacency(ctx context.Context) (map[string][]string, error) {
	var models []GateEdgeModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	if err := r.db.WithContext(ctx).
		Where(predicate, args...).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list gate adjacency: %w", err)
	}

	adjacency := make(map[string][]string)
	for _, m := range models {
		adjacency[m.SystemSymbol] = append(adjacency[m.SystemSymbol], m.ConnectedSystem)
	}
	for system := range adjacency {
		sort.Strings(adjacency[system])
	}
	return adjacency, nil
}

// anyStale reports whether any row's synced_at is missing/unparseable or older
// than gateEdgeFreshWindow. A system's edges are written in one Replace() with a
// single timestamp, so any stale row means the whole set is stale.
func (r *GormGateEdgeRepository) anyStale(models []GateEdgeModel) bool {
	for _, m := range models {
		if m.SyncedAt == "" {
			return true
		}
		syncedAt, err := time.Parse(time.RFC3339, m.SyncedAt)
		if err != nil || time.Since(syncedAt) >= gateEdgeFreshWindow {
			return true
		}
	}
	return false
}

// openEraID mirrors GormWaypointRepository.openEraID: the open era is the highest
// era_id with no closed_at. nil (no open era yet) scopes reads/writes to NULL
// era_id rows, matching the pre-close transition window.
func (r *GormGateEdgeRepository) openEraID(ctx context.Context) *int {
	var era EraModel
	if err := r.db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error; err != nil {
		return nil
	}
	id := era.EraID
	return &id
}

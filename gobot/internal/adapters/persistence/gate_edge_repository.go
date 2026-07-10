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

// gateEdgeUnderConstructionFreshWindow is the SHORTER freshness bound for an edge
// whose neighbor gate is still under construction (sp-8qhu). A healthy edge is
// effectively static within an era (24h), but a build COMPLETES on its own clock:
// pinning an under-construction edge to a 2h window means the daemon re-probes it
// and notices the completion same-era, instead of holding a "still building"
// verdict stale for a full day and refusing an now-valid route.
const gateEdgeUnderConstructionFreshWindow = 2 * time.Hour

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
			ConnectedSystem:   m.ConnectedSystem,
			GateWaypoint:      m.GateWaypoint,
			UnderConstruction: m.UnderConstruction,
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
				SystemSymbol:      systemSymbol,
				ConnectedSystem:   e.ConnectedSystem,
				GateWaypoint:      e.GateWaypoint,
				EraID:             eraID,
				SyncedAt:          syncedAt,
				UnderConstruction: e.UnderConstruction,
			})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("failed to insert gate edges for %s: %w", systemSymbol, err)
		}
		return nil
	})
}

// Adjacency returns every stored system's neighbor edges, era-scoped, sorted by
// neighbor symbol for a stable `system gates` overview. Edges carry
// UnderConstruction so the verb can flag unbuilt gates. Pure read; the service
// layer does any live fetch-through for a specific system.
func (r *GormGateEdgeRepository) Adjacency(ctx context.Context) (map[string][]system.GateEdge, error) {
	var models []GateEdgeModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	if err := r.db.WithContext(ctx).
		Where(predicate, args...).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list gate adjacency: %w", err)
	}

	adjacency := make(map[string][]system.GateEdge)
	for _, m := range models {
		adjacency[m.SystemSymbol] = append(adjacency[m.SystemSymbol], system.GateEdge{
			ConnectedSystem:   m.ConnectedSystem,
			GateWaypoint:      m.GateWaypoint,
			UnderConstruction: m.UnderConstruction,
			// Adjacency is a raw dump — flag a stale row so the verb marks it as
			// unverified (its UnderConstruction value is re-probed on next route).
			Stale: rowStale(m),
		})
	}
	for sys := range adjacency {
		sort.Slice(adjacency[sys], func(i, j int) bool {
			return adjacency[sys][i].ConnectedSystem < adjacency[sys][j].ConnectedSystem
		})
	}
	return adjacency, nil
}

// anyStale reports whether any row is stale. A system's edges are written in one
// Replace() with a single timestamp, so any stale row means the whole set is stale
// (the lazy-refresh signal that forces a full re-fetch + re-probe before routing
// trusts the set).
func (r *GormGateEdgeRepository) anyStale(models []GateEdgeModel) bool {
	for _, m := range models {
		if rowStale(m) {
			return true
		}
	}
	return false
}

// rowStale reports whether a single edge row's cache is stale: its synced_at is
// missing/unparseable, or older than its freshness window. The window is per-row —
// an under-construction edge uses the SHORTER window (sp-8qhu) so a build
// completion is re-probed same-era, while a healthy edge keeps the 24h window. An
// EMPTY synced_at is always stale: this is what the deploy-time cache invalidation
// (AutoMigrate clearing synced_at on the column's introduction) relies on to force
// a re-probe of pre-tracking rows before they are ever trusted for routing.
func rowStale(m GateEdgeModel) bool {
	if m.SyncedAt == "" {
		return true
	}
	syncedAt, err := time.Parse(time.RFC3339, m.SyncedAt)
	if err != nil {
		return true
	}
	return time.Since(syncedAt) >= freshWindowFor(m)
}

// freshWindowFor is the freshness bound for one edge: the shorter
// under-construction window when the neighbor gate is still building, the standard
// window otherwise.
func freshWindowFor(m GateEdgeModel) time.Duration {
	if m.UnderConstruction {
		return gateEdgeUnderConstructionFreshWindow
	}
	return gateEdgeFreshWindow
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

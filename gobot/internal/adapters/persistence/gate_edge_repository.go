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

// unreadableMarker is the sentinel connected_system of a negative-result BACKOFF
// marker row (sp-ikx1): a row that records an UNREADABLE system's backoff state
// (UnreadableSince/AttemptCount) rather than a real edge. A real edge's connected
// system is always non-empty (ExtractSystemSymbol never yields ""), so "" cleanly
// separates the two: edge reads exclude markers, backoff reads select only them.
const unreadableMarker = ""

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
		// Exclude the negative-result backoff marker (sp-ikx1) — it is not an edge. A
		// system that has ONLY a marker row therefore reads as a genuine MISS, which is
		// what routes the caller into the backoff check instead of trusting an empty
		// "connects nowhere" set.
		Where("connected_system <> ?", unreadableMarker).
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
// upstream disappears here too. The all-rows delete also clears any negative-result
// backoff MARKER for the system (sp-ikx1): a gate that becomes readable again is
// self-healed off the backoff clock, no explicit reset needed.
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

// UnreadableState returns systemSymbol's persisted negative-result backoff (sp-ikx1),
// era-scoped: the consecutive-failed-probe count and the last-probe timestamp off the
// marker row (connected_system = ""). ok=false when no marker exists for the open era
// (never failed, cleared by a successful Replace, or left behind by a closed era — an
// era close resets the backoff exactly like the rest of the gate cache). A marker whose
// timestamp is missing/unparseable also reads as ok=false, so a corrupt row degrades to
// "re-probe now", never a permanent skip.
func (r *GormGateEdgeRepository) UnreadableState(ctx context.Context, systemSymbol string) (int, time.Time, bool, error) {
	var m GateEdgeModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	err := r.db.WithContext(ctx).
		Where("system_symbol = ? AND connected_system = ?", systemSymbol, unreadableMarker).
		Where(predicate, args...).
		First(&m).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0, time.Time{}, false, nil
		}
		return 0, time.Time{}, false, fmt.Errorf("failed to read gate backoff for %s: %w", systemSymbol, err)
	}
	if m.UnreadableSince == "" {
		return 0, time.Time{}, false, nil
	}
	lastProbe, perr := time.Parse(time.RFC3339, m.UnreadableSince)
	if perr != nil {
		return 0, time.Time{}, false, nil
	}
	return m.AttemptCount, lastProbe, true, nil
}

// MarkUnreadable records (or extends) systemSymbol's negative-result backoff (sp-ikx1):
// it upserts the marker row (connected_system = "") with an incremented attempt count
// and now as the last-probe time, returning the new count. The increment reads the
// CURRENT open-era count first, so a fresh era (whose era-scoped read misses the old
// marker) restarts the backoff at attempt 1. The old marker is deleted across ALL eras
// before insert, mirroring Replace, so a dead-era marker cannot accumulate. Persisted so
// a daemon restart resumes the backoff instead of re-storming the API (RULINGS #2).
func (r *GormGateEdgeRepository) MarkUnreadable(ctx context.Context, systemSymbol, gateWaypoint string, now time.Time) (int, error) {
	eraID := r.openEraID(ctx)
	predicate, args := eraScopePredicate(eraID)
	attempts := 0

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing GateEdgeModel
		ferr := tx.Where("system_symbol = ? AND connected_system = ?", systemSymbol, unreadableMarker).
			Where(predicate, args...).
			First(&existing).Error
		if ferr == nil {
			attempts = existing.AttemptCount
		} else if ferr != gorm.ErrRecordNotFound {
			return fmt.Errorf("failed to read gate backoff for %s: %w", systemSymbol, ferr)
		}
		attempts++

		if err := tx.Where("system_symbol = ? AND connected_system = ?", systemSymbol, unreadableMarker).
			Delete(&GateEdgeModel{}).Error; err != nil {
			return fmt.Errorf("failed to clear gate backoff marker for %s: %w", systemSymbol, err)
		}
		return tx.Create(&GateEdgeModel{
			SystemSymbol:    systemSymbol,
			ConnectedSystem: unreadableMarker,
			GateWaypoint:    gateWaypoint,
			EraID:           eraID,
			SyncedAt:        "",
			UnreadableSince: now.UTC().Format(time.RFC3339),
			AttemptCount:    attempts,
		}).Error
	})
	if err != nil {
		return 0, err
	}
	return attempts, nil
}

// Adjacency returns every stored system's neighbor edges, era-scoped, sorted by
// neighbor symbol for a stable `system gates` overview. Edges carry
// UnderConstruction so the verb can flag unbuilt gates. Pure read; the service
// layer does any live fetch-through for a specific system.
func (r *GormGateEdgeRepository) Adjacency(ctx context.Context) (map[string][]system.GateEdge, error) {
	var models []GateEdgeModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	if err := r.db.WithContext(ctx).
		// Marker rows (sp-ikx1) are not edges — a "" connected_system must never surface
		// as a neighbor in the overview or the frontier scanner's BFS.
		Where("connected_system <> ?", unreadableMarker).
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

// UnreadableGates returns every era-scoped negative-result backoff marker (sp-ywh1): the
// UNCHARTED systems whose live GetJumpGate keeps 400ing, mapped to the gate waypoint the marker
// recorded (may be "" when the gate was not yet known at mark time). These are exactly the
// traffic-touched frontier gates the gate-reconcile sweep widens onto — a marker exists ONLY
// because a hull actually tried to route THROUGH the gate (MarkUnreadable writes on a real 400),
// so a marketless dead-end no route crosses is never in this set. Selects only marker rows
// (connected_system = unreadableMarker); real edges are excluded, the mirror of Adjacency's
// filter. Pure read; era-scoped exactly like Edges/Adjacency (openEraID + eraScopePredicate) so
// a dead-era marker never leaks a stale target into the live sweep. A successful Replace clears
// a system's marker (self-heal), so a since-charted system naturally drops out of this set.
func (r *GormGateEdgeRepository) UnreadableGates(ctx context.Context) (map[string]string, error) {
	var models []GateEdgeModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	if err := r.db.WithContext(ctx).
		Where("connected_system = ?", unreadableMarker).
		Where(predicate, args...).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list unreadable gate markers: %w", err)
	}
	gates := make(map[string]string, len(models))
	for _, m := range models {
		gates[m.SystemSymbol] = m.GateWaypoint
	}
	return gates, nil
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

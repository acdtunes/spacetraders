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
// long-running daemon without hammering the API on every routing lookup. This is
// the DEFAULT; the daemon overrides it from config ([routing] gate_cache_ttl, the
// topology-cache knob) via WithFreshWindow.
const gateEdgeFreshWindow = 24 * time.Hour

// gateEdgeUnderConstructionFreshWindow is the SHORTER re-probe bound for an edge whose
// neighbour gate is still building: later completion discovery, bought with API budget.
const gateEdgeUnderConstructionFreshWindow = 6 * time.Hour

// unreadableMarker is the sentinel connected_system of a negative-result BACKOFF
// marker row: a row that records an UNREADABLE system's backoff state
// (UnreadableSince/AttemptCount) rather than a real edge. A real edge's connected
// system is always non-empty (ExtractSystemSymbol never yields ""), so "" cleanly
// separates the two: edge reads exclude markers, backoff reads select only them.
const unreadableMarker = ""

// GormGateEdgeRepository implements system.GateEdgeRepository over GORM. It is
// the persisted gate-graph adjacency store. Every read is era-scoped
// exactly like GormWaypointRepository (openEraID + eraScopePredicate) so
// dead-era rows never leak into live routing; a system's edge set is
// REPLACED atomically on each sync so a since-severed connection cannot linger.
type GormGateEdgeRepository struct {
	db *gorm.DB
	// freshWindow is the healthy-edge freshness bound (topology-cache TTL). It
	// defaults to gateEdgeFreshWindow (24h) and is overridden from config via WithFreshWindow.
	// The SHORTER under-construction window is a separate correctness bound (a build completes
	// on its own clock) and stays a const, not tuned here.
	freshWindow time.Duration
}

// GateEdgeOption customizes a GormGateEdgeRepository at construction (functional option so the
// single-arg constructor stays stable for the many existing call sites while the daemon injects
// the configured freshness TTL).
type GateEdgeOption func(*GormGateEdgeRepository)

// WithFreshWindow sets the healthy-edge freshness window (the topology-cache TTL),
// wired from [routing] gate_cache_ttl. A non-positive value is ignored (keeps the 24h default),
// so a zero/unset config can never collapse the cache to "always stale".
func WithFreshWindow(d time.Duration) GateEdgeOption {
	return func(r *GormGateEdgeRepository) {
		if d > 0 {
			r.freshWindow = d
		}
	}
}

// NewGormGateEdgeRepository creates a new GORM-backed gate edge repository. Without options the
// healthy-edge freshness window is the 24h default; WithFreshWindow overrides it from config.
func NewGormGateEdgeRepository(db *gorm.DB, opts ...GateEdgeOption) *GormGateEdgeRepository {
	r := &GormGateEdgeRepository{db: db, freshWindow: gateEdgeFreshWindow}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Edges returns systemSymbol's stored neighbor edges, era-scoped. ok is false on
// a genuine miss (no rows) OR when the set is CONDEMNED — older than the healthy
// freshness window, or of unknown age — both lazy-refresh signals the service resolves
// by fetching the live gate. A NULL synced_at is treated as stale (unknown age →
// refresh), the inverse of the waypoint cache's "unknown age is fresh" choice:
// here a routing decision rides on the data, so the safe default is to re-fetch.
//
// TWO DIFFERENT QUESTIONS, deliberately answered by two different signals.
// `ok` answers "may I trust this set for routing?" and is a WHOLE-SET verdict measured
// against the healthy window alone. Per-row `Stale` answers "does this row need a
// re-probe?" and carries the SHORTER under-construction window.
//
// Conflating them is what walled off the map. Every row of a system is written by one
// Replace under a SINGLE synced_at, so returning ok=false whenever ANY row was stale meant
// one still-building exit condemned its system's ENTIRE topology — including its built,
// static, permanently-valid exits — 2h after the last probe. GateNeighbourPort then yielded
// zero neighbours, the system became a WALL in every BFS, and routing refused
// provably-existing routes: 173 of 1,168 live systems at once, freezing 266 bought probes
// that were never dispatched. A static gate does not become unknown because a DIFFERENT
// gate in the same system is still being built, so its staleness must not condemn siblings.
//
// The re-probe signal is not lost, only moved: it rides `Stale` on exactly the expired row,
// and the fetch-through resolver (gategraph.Service.Connections) re-fetches on it. Dropping
// it would hold a "still building" verdict for a full day and refuse a route that had opened.
func (r *GormGateEdgeRepository) Edges(ctx context.Context, systemSymbol string) ([]system.GateEdge, bool, error) {
	var models []GateEdgeModel
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	if err := r.db.WithContext(ctx).
		Where("system_symbol = ?", systemSymbol).
		// Exclude the negative-result backoff marker — it is not an edge. A
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
	if r.anyCondemned(models) {
		return nil, false, nil
	}

	edges := make([]system.GateEdge, 0, len(models))
	for _, m := range models {
		edges = append(edges, system.GateEdge{
			ConnectedSystem:   m.ConnectedSystem,
			GateWaypoint:      m.GateWaypoint,
			UnderConstruction: m.UnderConstruction,
			// Per-row: the row is past ITS OWN window (the shorter one while a gate is
			// building). Routing consumers exclude it from the answer; the fetch-through
			// resolver re-probes on it. It does NOT condemn its siblings.
			Stale: r.rowStale(m),
		})
	}
	return edges, true, nil
}

// AllEdges returns EVERY era-scoped system's edges in one read, keyed by system, applying the
// same per-set condemnation and per-row staleness Edges applies to a single system.
//
// It exists so a caller that needs the SHAPE of the graph — reachability is transitive, so
// answering "can a hull walk here?" needs a walk — does not pay one round trip per system
// reached. Measured against the live graph: ~1,070 per-system reads at ~0.7ms is ~750ms, while
// the whole table is ~10k rows and arrives in ~3ms.
//
// PRESENCE IN THE MAP IS THE `ok` OF Edges. A system whose set is condemned is OMITTED entirely,
// exactly as Edges reports ok=false for it, so a caller cannot accidentally treat condemned
// topology as read topology. A system with no rows is likewise simply absent.
//
// ERA-SCOPED FAIL-CLOSED, via OpenEraScope rather than eraScopePredicate(openEraID). gate_edges
// carries rows from every era this agent has played, and the difference matters precisely when
// the era cannot be resolved: eraScopePredicate(nil) degrades to `era_id IS NULL`, which answers
// confidently from pre-backfill rows belonging to a dead universe. OpenEraScope refuses instead.
// Reading dead-era topology is the failure — ~290 API failures an hour for ten hours,
// routing against a map that no longer existed.
func (r *GormGateEdgeRepository) AllEdges(ctx context.Context) (map[string][]system.GateEdge, error) {
	predicate, args, err := OpenEraScope(ctx, r.db)
	if err != nil {
		return nil, fmt.Errorf("failed to scope gate edges to the open era: %w", err)
	}
	var models []GateEdgeModel
	if err := r.db.WithContext(ctx).
		Where("connected_system <> ?", unreadableMarker).
		Where(predicate, args...).
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list the gate graph: %w", err)
	}

	bySystem := make(map[string][]GateEdgeModel)
	for _, m := range models {
		bySystem[m.SystemSymbol] = append(bySystem[m.SystemSymbol], m)
	}

	out := make(map[string][]system.GateEdge, len(bySystem))
	for symbol, rows := range bySystem {
		if r.anyCondemned(rows) {
			continue // same verdict Edges returns as ok=false
		}
		edges := make([]system.GateEdge, 0, len(rows))
		for _, m := range rows {
			edges = append(edges, system.GateEdge{
				ConnectedSystem:   m.ConnectedSystem,
				GateWaypoint:      m.GateWaypoint,
				UnderConstruction: m.UnderConstruction,
				Stale:             r.rowStale(m),
			})
		}
		out[symbol] = edges
	}
	return out, nil
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
// backoff MARKER for the system: a gate that becomes readable again is
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

// UnreadableState returns systemSymbol's persisted negative-result backoff,
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

// MarkUnreadable records (or extends) systemSymbol's negative-result backoff:
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
		// Marker rows are not edges — a "" connected_system must never surface
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
			Stale: r.rowStale(m),
		})
	}
	for sys := range adjacency {
		sort.Slice(adjacency[sys], func(i, j int) bool {
			return adjacency[sys][i].ConnectedSystem < adjacency[sys][j].ConnectedSystem
		})
	}
	return adjacency, nil
}

// anyCondemned reports whether any row makes the WHOLE set untrustworthy for routing.
// A system's edges are written in one Replace() under a single timestamp, so in practice
// this is the one question "is this SET past the healthy window?" — and one row answering
// yes correctly condemns all of them, because they are all the same age.
func (r *GormGateEdgeRepository) anyCondemned(models []GateEdgeModel) bool {
	for _, m := range models {
		if r.rowCondemnsSet(m) {
			return true
		}
	}
	return false
}

// rowCondemnsSet reports whether one row's age makes its whole set untrustworthy. It is
// deliberately NOT rowStale: the condemnation is measured against the WHOLE-SET (healthy,
// configured) window for every row, ignoring the shorter under-construction window entirely.
//
// That is the whole fix. The short window exists so a BUILD COMPLETION is noticed same-era —
// it is a re-probe schedule for one row, not a verdict on its system's topology. Letting it
// condemn the set made every system with a still-building exit a routing WALL every 2h, and
// re-probes are rate-limited (MaxGateReads per tick) so they could never all be refreshed
// inside that window. An under-construction edge is impassable anyway and is already excluded
// as a routing candidate, so its expiry costs the walk nothing that was ever usable.
//
// Two conditions still condemn, and both are load-bearing:
//   - UNKNOWN AGE (missing or unparseable synced_at) — the deploy-time cache invalidation
//     (AutoMigrate blanking synced_at) relies on it to force a re-probe of pre-tracking rows.
//     It applies even to a row flagged under-construction: at unknown age that FLAG is
//     unverified too, so the row cannot earn an exemption on the strength of the one field
//     that cannot be trusted.
//   - PAST THE HEALTHY WINDOW — a genuinely old set has unverifiable topology and is still
//     condemned whole, exactly as before. The configured window ([routing] gate_cache_ttl) is
//     what this measures against, so shortening the topology TTL keeps applying to every
//     system rather than silently exempting any that has a gate under construction.
func (r *GormGateEdgeRepository) rowCondemnsSet(m GateEdgeModel) bool {
	if m.SyncedAt == "" {
		return true
	}
	syncedAt, err := time.Parse(time.RFC3339, m.SyncedAt)
	if err != nil {
		return true
	}
	return time.Since(syncedAt) >= r.freshWindow
}

// rowStale reports whether a single edge row's cache is stale: its synced_at is
// missing/unparseable, or older than its freshness window. The window is per-row —
// an under-construction edge uses the SHORTER window so a build
// completion is re-probed same-era, while a healthy edge keeps the 24h window. An
// EMPTY synced_at is always stale: this is what the deploy-time cache invalidation
// (AutoMigrate clearing synced_at on the column's introduction) relies on to force
// a re-probe of pre-tracking rows before they are ever trusted for routing.
//
// This is the PER-ROW verdict only. It marks a row as needing a re-probe and as unusable
// as a routing candidate; it does NOT decide whether the row's SET is trustworthy — that
// is rowCondemnsSet, which ignores the short window on purpose. Keeping the two apart is
// what stops one still-building exit from walling off its whole system.
func (r *GormGateEdgeRepository) rowStale(m GateEdgeModel) bool {
	if m.SyncedAt == "" {
		return true
	}
	syncedAt, err := time.Parse(time.RFC3339, m.SyncedAt)
	if err != nil {
		return true
	}
	return time.Since(syncedAt) >= r.freshWindowFor(m)
}

// freshWindowFor is the freshness bound for one edge: the shorter
// under-construction window when the neighbor gate is still building, the configured
// healthy window ([routing] gate_cache_ttl, 24h default) otherwise.
func (r *GormGateEdgeRepository) freshWindowFor(m GateEdgeModel) time.Duration {
	if m.UnderConstruction {
		return gateEdgeUnderConstructionFreshWindow
	}
	return r.freshWindow
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

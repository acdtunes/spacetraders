package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
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
// This is the dedicated immutable-trait path: FindBySymbol's gates were
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

// OpenEraScope is eraScopePredicate's FAIL-CLOSED face, for readers that must
// read NOTHING rather than guess when the open era cannot be resolved.
//
// On the resolved path it returns exactly what every era-scoped repository here
// already applies — the same predicate from the same helper — so a read wired
// through this and a read wired through openEraID + eraScopePredicate produce
// identical SQL and can never disagree about which rows are live.
//
// THE DIFFERENCE IS THE UNRESOLVED PATH, and it is the whole reason this exists.
// openEraID collapses two very different facts into one nil — "the eras table
// could not be read" and "every era is closed" — and eraScopePredicate then turns
// that nil into `era_id IS NULL`, which is a FALLBACK: it still answers, from the
// pre-backfill rows. For a reader whose answer costs API budget that is the wrong
// direction. sp-l0aqy: the sensing engine's yard sweep built its work list from an
// unscoped `waypoints` read and spent ten hours at ~290 failures/hour asking the
// API about systems from universes that no longer exist, while utilisation sat at
// 88% against an 85% ceiling — and because the sweep's per-tick bound counts
// ATTEMPTS rather than successes, each dead-era yard consumed a slot a live yard
// needed. Refusing outright is the safe direction: a missing yard costs discovery
// latency, a dead-era yard costs budget the live fleet needs.
//
// Both unresolved cases therefore refuse, and deliberately share one error path:
// a closed universe has no live rows to offer, so "all eras closed" is not a
// milder condition than "the ledger is unreadable" — it is the same answer.
//
// Rows with a NULL era_id remain live on the resolved path, matching every sibling
// read (pre-close transition, not yet backfilled). Measured against production
// before shipping: `waypoints` holds ZERO NULL-era rows, so the allowance costs
// nothing today and is kept only so this read cannot disagree with the others.
func OpenEraScope(ctx context.Context, db *gorm.DB) (string, []any, error) {
	var era EraModel
	if err := db.WithContext(ctx).
		Where("closed_at IS NULL").
		Order("era_id DESC").
		First(&era).Error; err != nil {
		return "", nil, fmt.Errorf("failed to resolve the open universe era, so nothing can be era-scoped: %w", err)
	}
	predicate, args := eraScopePredicate(&era.EraID)
	return predicate, args, nil
}

// ListWithTrait retrieves EVERY cached waypoint bearing the given trait across ALL
// systems, read as the IMMUTABLE physical fact it is: era-AGNOSTIC and TTL-agnostic,
// exactly like HasWaypointTrait. This is the backfill's charted-shipyard
// enumerator: an era-SCOPED read here would repeat the precise bug that
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

// ListWithTraitInOpenEra is ListWithTrait's ERA-SCOPED sibling: every cached
// waypoint bearing the trait, restricted to the universe currently open, and
// FAILING CLOSED when that universe cannot be resolved.
//
// TWO CALLERS, TWO GENUINELY DIFFERENT ERA CONTRACTS — which is why this is a
// second method rather than a filter added to ListWithTrait:
//
//   - the shipyard BACKFILL enumerator wants the era-agnostic set, because a
//     prior-era row is still proof a system physically holds a shipyard and it
//     intersects that set with the CURRENT gate-reachable frontier afterwards.
//     That downstream intersection is what makes era-agnosticism safe there.
//     ListWithTrait stays exactly as it was for it.
//   - the sensing engine's free catalogue sweep has NO such downstream filter: it
//     takes the enumeration and calls the API with it. For that caller an
//     unscoped row is not a harmless extra candidate, it is a guaranteed 404
//     against a system that no longer exists, charged against a per-tick bound
//     that counts attempts (sp-l0aqy: ~290 failures/hour for ten hours).
//
// A physical SHIPYARD trait really is immutable across eras, so nothing here
// contradicts ListWithTrait's reasoning. What era_id records is narrower and is
// the fact that matters to a caller about to spend a call: which universe's API
// last confirmed this waypoint exists. Still a cheap local read — no API budget.
func (r *GormWaypointRepository) ListWithTraitInOpenEra(ctx context.Context, trait string) ([]*shared.Waypoint, error) {
	predicate, args, err := OpenEraScope(ctx, r.db)
	if err != nil {
		return nil, fmt.Errorf("failed to list waypoints with trait %s: %w", trait, err)
	}
	pattern := fmt.Sprintf("%%\"%s\"%%", trait)
	var models []WaypointModel
	result := r.db.WithContext(ctx).
		Where("traits LIKE ?", pattern).
		Where(predicate, args...).
		Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to list open-era waypoints with trait %s: %w", trait, result.Error)
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

	result := r.db.WithContext(ctx).Save(model)
	if result.Error != nil {
		return fmt.Errorf("failed to add waypoint: %w", result.Error)
	}

	return nil
}

// UpsertFromDetail writes a single waypoint's freshly-read detail into the
// cache, stamped with the open era and a new sync time.
//
// It exists for the CHARTING path. Charting a waypoint publishes traits that
// were invisible while it was UNCHARTED, and until that row is rewritten the
// cache still reports the waypoint as uncharted — so a charting tour, which
// picks its next stop from exactly that set, would chart it again on every pass.
// This is where the loop is closed.
//
// The whole row is replaced rather than patched, which is why the detail carries
// the full physical description and not just the trait list: a partial write
// would blank the coordinates the router plans against.
func (r *GormWaypointRepository) UpsertFromDetail(ctx context.Context, detail *ports.WaypointDetail) error {
	if detail == nil {
		return fmt.Errorf("cannot persist a nil waypoint detail")
	}
	waypoint, err := shared.NewWaypoint(detail.Symbol, detail.X, detail.Y)
	if err != nil {
		return fmt.Errorf("failed to build waypoint %s from detail: %w", detail.Symbol, err)
	}
	waypoint.Type = detail.Type
	waypoint.Traits = detail.Traits
	waypoint.Orbitals = detail.Orbitals
	// Derived rather than carried: on-site fuel is a CONSEQUENCE of the traits,
	// and the domain owns that rule. Restating it here would give the cache a
	// second, drifting definition of which waypoints a router may refuel at.
	waypoint.HasFuel = shared.TraitsGrantFuel(detail.Traits)

	return r.Add(ctx, waypoint)
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

	if model.Traits != "" {
		var traits []string
		if err := json.Unmarshal([]byte(model.Traits), &traits); err != nil {
			// If parsing fails, leave empty
			traits = []string{}
		}
		waypoint.Traits = traits
	}

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

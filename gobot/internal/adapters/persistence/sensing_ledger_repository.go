package persistence

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SensingLedgerRepository is the durable placement ledger of the parked-probe
// sensing model: which systems have been screened (sensing_systems)
// and which waypoints we want a probe parked at, with how far along each
// placement is (sensing_slots). Everything the coordinator does is re-derivable
// from these two tables after a restart (RULINGS #2).
//
// ERA SCOPING — deliberately asymmetric, and the asymmetry is load-bearing:
//
//   - PLANNING reads (SystemsByVerdict, SlotsByState, SlotsBySystem) are
//     era-scoped exactly like ScoutPostModel/ShipyardInventory reads, so a
//     universe reset never resurrects a dead era's verdicts or placements as live
//     work — the waypoints they name no longer exist.
//
//   - CountOwnedProbes is era-AGNOSTIC. It is the probe_cap read that gates
//     probe SPEND, and a hull bought in a previous era is still a hull we paid
//     for. Era-scoping it would make owned probes vanish from the count at an era
//     boundary and authorise buying replacements we already own — the wrong
//     direction for a money guard, which must fail closed (RULINGS #4). Counting
//     too many only ever buys fewer probes.
type SensingLedgerRepository struct {
	db *gorm.DB
}

// NewSensingLedgerRepository creates the GORM-backed parked-probe sensing ledger.
func NewSensingLedgerRepository(db *gorm.DB) *SensingLedgerRepository {
	return &SensingLedgerRepository{db: db}
}

// sensingSystemUpdateColumns are the sensing_systems columns an upsert refreshes
// on conflict: the SCREENING columns, and deliberately NOT the two charting-seed
// ones.
//
// seed_ship and seed_state are excluded because UpsertSystem's input cannot
// express them — SensingSystemModel arrives from a caller that only knows a
// verdict, so both fields are the zero value and the upsert would write them
// NULL. That is not a cosmetic loss. The errand row is the ONLY thing naming a
// mid-tour hull (its placement row is deleted the moment the seed is claimed),
// and a seed's target system is PENDING for the whole tour, which is exactly the
// verdict the screening sweep re-screens on every tick. So a blanket list wipes
// the errand mid-tour, the next expansion tick sees no active seed, orders
// ANOTHER probe for the same system, and the original hull is left named by
// nothing at all — invisible to CountOwnedProbes forever, and re-bought every
// time the cycle repeats. Unbounded spend, which is the one direction a money
// guard must never fail (RULINGS #4).
//
// SetSeed is therefore the SOLE writer of these two columns, and the two write
// sets are disjoint BY CONSTRUCTION rather than by convention — which is what
// lets the screening sweep and the expansion engine run concurrently against the
// same row. Adding either column back here re-opens the kill chain above.
var sensingSystemUpdateColumns = []string{
	"verdict", "screened_at", "uncharted_count", "catalog_synced_at",
	"depth_credits", "era_id", "updated_at",
}

// UpsertSystem writes a system's screening verdict, stamped with the open era.
// Keyed on (player_id, system_symbol), so a re-screen updates the row in place
// rather than accumulating duplicate verdicts.
func (r *SensingLedgerRepository) UpsertSystem(ctx context.Context, m SensingSystemModel) error {
	m.EraID = r.openEraID(ctx)
	m.UpdatedAt = time.Now().UTC()
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "player_id"}, {Name: "system_symbol"}},
			DoUpdates: clause.AssignmentColumns(sensingSystemUpdateColumns),
		}).
		Create(&m).Error; err != nil {
		return fmt.Errorf("failed to upsert sensing system %q: %w", m.SystemSymbol, err)
	}
	return nil
}

// SystemsByVerdict returns the player's era-scoped systems carrying the given
// screening verdict, ordered by system symbol for deterministic planning.
func (r *SensingLedgerRepository) SystemsByVerdict(ctx context.Context, playerID int, verdict string) ([]SensingSystemModel, error) {
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var models []SensingSystemModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND verdict = ?", playerID, verdict).
		Where(predicate, args...).
		Order("system_symbol").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list sensing systems with verdict %q: %w", verdict, err)
	}
	return models, nil
}

// Systems returns every era-scoped system row the player holds, ordered by
// system symbol. It is the expansion engine's read: unlike SystemsByVerdict it
// carries the uncharted count and the charting errand, which is what decides
// whether a system needs a probe sent to it and whether one is already on the
// way.
func (r *SensingLedgerRepository) Systems(ctx context.Context, playerID int) ([]SensingSystemModel, error) {
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var models []SensingSystemModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Where(predicate, args...).
		Order("system_symbol").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list sensing systems: %w", err)
	}
	return models, nil
}

// SetSeed records the charting errand running against a system — the hull and
// how far along it is — and NOTHING else. Empty strings clear the errand.
//
// This is the SOLE writer of seed_ship and seed_state; UpsertSystem's column
// list excludes both (see sensingSystemUpdateColumns for the kill chain that
// enforces). The two write sets are therefore disjoint by construction, which is
// what lets the screening sweep and the expansion engine drive the same row
// concurrently: neither can carry the other's columns back to a stale value. A
// missing row is an error rather than an upsert — an errand on a system the
// ledger has never screened would be a hull sent somewhere nothing asked for.
func (r *SensingLedgerRepository) SetSeed(ctx context.Context, playerID int, system, shipSymbol, seedState string) error {
	res := r.db.WithContext(ctx).Model(&SensingSystemModel{}).
		Where("player_id = ? AND system_symbol = ?", playerID, system).
		Updates(map[string]any{
			"seed_ship":  nullableSeedValue(shipSymbol),
			"seed_state": nullableSeedValue(seedState),
			"updated_at": time.Now().UTC(),
		})
	if res.Error != nil {
		return fmt.Errorf("failed to set the charting seed on %q: %w", system, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("sensing system %q for player %d: %w", system, playerID, gorm.ErrRecordNotFound)
	}
	return nil
}

// ExtraSeeds returns the era-scoped charting errands of the hulls past each
// system's first, hull-ordered so a tick's crew reads reproducibly.
func (r *SensingLedgerRepository) ExtraSeeds(ctx context.Context, playerID int) ([]SensingSeedHullModel, error) {
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var models []SensingSeedHullModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Where(predicate, args...).
		Order("ship_symbol").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list sensing charting crews: %w", err)
	}
	return models, nil
}

// SetExtraSeed records the charting errand of a hull past its system's first, and
// NOTHING else. Keyed on the HULL, so re-stamping a hull onto another system MOVES
// its errand rather than leaving a second one naming the same probe.
func (r *SensingLedgerRepository) SetExtraSeed(ctx context.Context, playerID int, system, shipSymbol, seedState string) error {
	model := SensingSeedHullModel{
		PlayerID:     playerID,
		ShipSymbol:   shipSymbol,
		SystemSymbol: system,
		SeedState:    seedState,
		EraID:        r.openEraID(ctx),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "player_id"}, {Name: "ship_symbol"}},
			DoUpdates: clause.AssignmentColumns([]string{"system_symbol", "seed_state", "era_id", "updated_at"}),
		}).
		Create(&model).Error; err != nil {
		return fmt.Errorf("failed to set the charting errand of %q on %q: %w", shipSymbol, system, err)
	}
	return nil
}

// ClearExtraSeed ends one crew hull's errand by deleting its row. A missing row is
// NOT an error: the clear is idempotent, so a half-finished stand-down can be
// re-run. Era-AGNOSTIC, unlike the read, so a pre-reset errand stays clearable.
func (r *SensingLedgerRepository) ClearExtraSeed(ctx context.Context, playerID int, shipSymbol string) error {
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND ship_symbol = ?", playerID, shipSymbol).
		Delete(&SensingSeedHullModel{}).Error; err != nil {
		return fmt.Errorf("failed to end the charting errand of %q: %w", shipSymbol, err)
	}
	return nil
}

// nullableSeedValue maps the caller's "" onto a NULL column, so a cleared errand
// leaves no empty-string residue for a "is a hull on this?" check to trip over.
func nullableSeedValue(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// sameStringValue compares two nullable columns by VALUE, which is what decides
// whether a transition names the column at all. Comparing the pointers would
// report every mutate callback as a change (it hands back a fresh pointer even
// when it wrote the same symbol) and quietly restore the carry-back this narrowing
// exists to remove. NULL and a set value are always different, which is what makes
// clearing a hull a real write.
func sameStringValue(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// copyStringValue detaches a nullable column from the row it was loaded with, so
// a later comparison against that row sees the value as it WAS.
func copyStringValue(v *string) *string {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// StampCatalogSynced records that a system's waypoint list has been swept and
// persisted, and ONLY that — the verdict and the errand beside it are untouched.
//
// It is the narrow write for the SEED-side sweep, which happens mid-tour while
// the screening sweep may be re-screening the same row: another disjoint
// write set, for the same reason as SetSeed. A missing row is an error — a
// catalog stamp on a system the ledger has never heard of would be recording
// knowledge about something nothing asked to know.
func (r *SensingLedgerRepository) StampCatalogSynced(ctx context.Context, playerID int, system string, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&SensingSystemModel{}).
		Where("player_id = ? AND system_symbol = ?", playerID, system).
		Updates(map[string]any{
			"catalog_synced_at": at,
			"updated_at":        time.Now().UTC(),
		})
	if res.Error != nil {
		return fmt.Errorf("failed to stamp the waypoint catalog of %q as synced: %w", system, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("sensing system %q for player %d: %w", system, playerID, gorm.ErrRecordNotFound)
	}
	return nil
}

// --- the operator rescreen (sp-j2efq) -----------------------------------------
//
// Two things here are stamped with the goods whitelist AS OF the moment they were
// written: a system's verdict, and a slot's whitelist_goods projection. Both are
// sound while the whitelist is era-invariant, and both go silently wrong the
// moment an operator edits config.yaml's [sensing] goods_whitelist — a
// NO_WHITELIST verdict is durable, so a system judged worthless under the old
// list is never reconsidered under the new one.
//
// ONLY THE VERDICT IS INVALIDATED HERE, and the omission is deliberate rather
// than partial work. Clearing the slot projections cannot be done from this layer
// alone: recordSlots skips any waypoint that already holds a slot, so a re-screen
// never rewrites an existing projection (the clear would be PERMANENT), and
// screenMarkets' cache branch keys on the slot EXISTING rather than on its
// projection being populated, so a cleared projection is read as an authoritative
// "nothing wanted here" and suppresses the very refetch that would repopulate it.
// A blanked projection also stops the scan rotation observing spread at that
// waypoint entirely (Scanner.observe). Empty-means-authoritative is a REVIEWED
// decision pinned by TestScreenSystemTreatsEmptyProjectionAsAuthoritative, so
// re-opening the never-scanned-market case is a design change rather than a fix:
// tracked as sp-ysg8h. DO NOT "fix" it by widening this write.
//
// What the verdict reset alone DOES fix is the case that matters most: for any
// market a probe has actually scanned, market_data answers GoodsAt with the full
// goods list, so the projection is never consulted and the re-screen re-matches
// against the CURRENT whitelist correctly. That is every parked market — exactly
// where hulls are already standing.
//
// The write carries the SAME era predicate the reads do. The point is to
// invalidate what the engine can actually see; a closed era's rows are invisible
// to every planning read, so re-opening them would be churn with no reader.

// ResetVerdictsToPending re-opens every era-scoped system the player holds for
// screening, and writes NOTHING else. It reports how many rows it matched.
//
// PENDING because that is the ONLY verdict the steady-state sweep re-screens —
// a decided system is never looked at again, which is exactly the durability
// that makes a mid-era whitelist edit unrecoverable without this.
//
// catalog_synced_at is deliberately NOT cleared, and that is load-bearing rather
// than tidy: the waypoint catalog is a fact about the MAP, not a judgement made
// under a whitelist. A system whose catalog stamp is NULL reads as unswept, and
// an unswept system is a charting-seed TARGET — so clearing it would have the
// rescreen dispatch probes to re-chart a galaxy already sitting in the database.
// seed_ship/seed_state are excluded for the same class of reason: an errand in
// flight must survive an operator re-opening the map's verdicts.
func (r *SensingLedgerRepository) ResetVerdictsToPending(ctx context.Context, playerID int) (int64, error) {
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	res := r.db.WithContext(ctx).Model(&SensingSystemModel{}).
		Where("player_id = ?", playerID).
		Where(predicate, args...).
		Updates(map[string]any{
			"verdict":    "PENDING",
			"updated_at": time.Now().UTC(),
		})
	if res.Error != nil {
		return 0, fmt.Errorf("failed to re-open sensing verdicts for player %d: %w", playerID, res.Error)
	}
	return res.RowsAffected, nil
}

// openEraID mirrors the other era-scoped repositories here: the open era is the
// highest era_id with no closed_at. nil (no open era yet) scopes reads/writes to
// NULL era_id rows, matching the pre-close transition window.
func (r *SensingLedgerRepository) openEraID(ctx context.Context) *int {
	var era EraModel
	if err := r.db.WithContext(ctx).Where("closed_at IS NULL").Order("era_id DESC").First(&era).Error; err != nil {
		return nil
	}
	id := era.EraID
	return &id
}

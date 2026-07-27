package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrSlotStateConflict is returned by TransitionSlot when the slot is not in the
// expected fromState — because another writer already moved it, because the
// slot belongs to a different player, or because no such slot exists. It is the
// LOST-RACE signal: the caller's read of the ledger was stale, so its planned
// side effect (buying a probe, dispatching a hull) must NOT proceed.
var ErrSlotStateConflict = errors.New("sensing slot is not in the expected state")

// SensingLedgerRepository is the durable placement ledger of the parked-probe
// sensing model (sp-k6v8z): which systems have been screened (sensing_systems)
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

// COLUMN OWNERSHIP on sensing_slots. FOUR writers drive this table
// concurrently, and each owns a disjoint answer about the same placement:
//
//	MarkScanned         what the market last showed  — last_scan_at, spread_ewma
//	TransitionSlot      how far along the placement is — state, assigned_ship, purchase_yard
//	UpsertSlotMetadata  what the screen measured      — whitelist_goods, depth_credits
//	UpsertSpareSlot     which hull is standing here   — state, assigned_ship, slot_kind
//
// This used to be ONE blanket list naming every column, which meant every writer
// re-asserted the whole row from whatever it had loaded and the last commit won.
// The scan columns are where that actually bites: the scan pacer runs
// concurrently with the reconcile and MarkScanned can commit at any instant,
// including inside a TransitionSlot's transaction — so a blanket write reverts a
// scan that already happened and the slot reads staler than the truth.
//
// The protection used to be an invariant rather than a structure: the rotation
// only scanned PARKED MARKET/YARD slots and the only PARKED→X transition skipped
// non-SPARE kinds, so the two write sets never met on a ROW. That held only as
// long as nobody transitioned a parked market — which the filed slot reaper
// (sp-l3f3d) does by design. Ownership is now per-COLUMN, so the reaper needs no
// special-casing and no future writer can re-open the window by accident.

// sensingSlotMetadataUpdateColumns are what a SCREEN re-declaration refreshes on
// conflict: the measurements it just took, and nothing about the placement's
// progress.
//
// Excluding state and assigned_ship is a money guard, not tidiness (RULINGS #4).
// A screen row is always a WANTED with no hull behind it, so a blanket conflict
// set would write NULL over the hull of any placement that had already been
// filled — dropping a probe we paid for out of CountOwnedProbes and authorising
// the purchase of a replacement standing right there. The callers all guard
// against re-declaring an occupied waypoint; this makes the guard structural
// instead of conventional, so a miss costs a redundant write rather than a hull.
var sensingSlotMetadataUpdateColumns = []string{
	"system_symbol", "whitelist_goods", "depth_credits", "era_id", "updated_at",
}

// sensingSlotSpareUpdateColumns are what recording a HULL standing at a waypoint
// refreshes on conflict: which hull, in what state, as what kind of slot.
//
// It deliberately does NOT carry whitelist_goods or depth_credits. A hull-bearing
// write measures nothing — the caller knows where a ship is standing, not what
// the market there deals in — so a blanket set would zero the screen's goods list
// and depth, and an empty goods list drops the waypoint out of the screen's hit
// set entirely. era_id IS refreshed: the planning reads are era-scoped, so a row
// left stamped with a dead era would carry a live hull that no planner can see
// while CountOwnedProbes (era-agnostic) still counts it.
var sensingSlotSpareUpdateColumns = []string{
	"system_symbol", "slot_kind", "state", "assigned_ship", "era_id", "updated_at",
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

// DeleteSlot removes a placement row outright.
//
// It exists for ONE transition: a parked spare being handed to a charting
// errand. The hull stops belonging to the ledger and starts belonging to the
// mission, and leaving the row behind would have the buy queue re-task a hull
// that has already flown away.
//
// A missing row is NOT an error — the delete is idempotent by design, because
// its caller has already stamped the errand and cannot usefully unwind.
func (r *SensingLedgerRepository) DeleteSlot(ctx context.Context, playerID int, waypoint string) error {
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND waypoint_symbol = ?", playerID, waypoint).
		Delete(&SensingSlotModel{}).Error; err != nil {
		return fmt.Errorf("failed to delete sensing slot %q: %w", waypoint, err)
	}
	return nil
}

// UpsertSlotMetadata declares a placement the SCREEN wants, stamped with the open
// era: what the waypoint deals in and how deep its book is.
//
// On a waypoint with no row yet this writes the whole placement, state included —
// that is how a want is born. On a waypoint that already HAS one it refreshes the
// measurements only (sensingSlotMetadataUpdateColumns), because by then the
// placement's progress belongs to whoever has been advancing it.
func (r *SensingLedgerRepository) UpsertSlotMetadata(ctx context.Context, m SensingSlotModel) error {
	return r.upsertSlot(ctx, m, sensingSlotMetadataUpdateColumns)
}

// UpsertSpareSlot records a HULL standing at a waypoint as a placement, stamped
// with the open era: a charting seed standing down as a parked spare, or a probe
// adopted from a retired scout post.
//
// On conflict it refreshes which hull and what state (sensingSlotSpareUpdateColumns)
// and leaves the screen's measurements and the scan history where they are. The
// row must exist and must name the hull, or the probe cap cannot see a probe we
// have already paid for.
func (r *SensingLedgerRepository) UpsertSpareSlot(ctx context.Context, m SensingSlotModel) error {
	return r.upsertSlot(ctx, m, sensingSlotSpareUpdateColumns)
}

// upsertSlot is the shared write behind the two variants above. Keyed on
// (player_id, waypoint_symbol), so re-declaring a placement updates the row in
// place — one slot per waypoint is a structural guarantee, not a convention.
//
// It is unexported ON PURPOSE: the conflict set is the whole safety property
// here, so there is no way to reach this table with a caller-chosen one.
func (r *SensingLedgerRepository) upsertSlot(ctx context.Context, m SensingSlotModel, onConflict []string) error {
	m.EraID = r.openEraID(ctx)
	m.UpdatedAt = time.Now().UTC()
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "player_id"}, {Name: "waypoint_symbol"}},
			DoUpdates: clause.AssignmentColumns(onConflict),
		}).
		Create(&m).Error; err != nil {
		return fmt.Errorf("failed to upsert sensing slot %q: %w", m.WaypointSymbol, err)
	}
	return nil
}

// SlotsByState returns the player's era-scoped slots in any of the given states,
// ordered by waypoint symbol. An EMPTY states list matches NOTHING rather than
// everything: an accidentally-empty filter must not hand a caller the entire
// ledger and have it act on every placement at once.
func (r *SensingLedgerRepository) SlotsByState(ctx context.Context, playerID int, states ...string) ([]SensingSlotModel, error) {
	if len(states) == 0 {
		return nil, nil
	}
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var models []SensingSlotModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND state IN ?", playerID, states).
		Where(predicate, args...).
		Order("waypoint_symbol").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list sensing slots by state: %w", err)
	}
	return models, nil
}

// SlotsBySystem returns every era-scoped slot the player holds in one system,
// in any state, ordered by waypoint symbol.
func (r *SensingLedgerRepository) SlotsBySystem(ctx context.Context, playerID int, system string) ([]SensingSlotModel, error) {
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var models []SensingSlotModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND system_symbol = ?", playerID, system).
		Where(predicate, args...).
		Order("waypoint_symbol").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list sensing slots for system %q: %w", system, err)
	}
	return models, nil
}

// TransitionSlot advances one slot's state machine, optimistically: the write is
// guarded on the slot still being in fromState, so of two writers racing the
// same slot exactly one wins and the loser gets ErrSlotStateConflict instead of
// silently clobbering the winner (which, on the WANTED→QUEUED edge, would buy a
// second probe for a slot already claimed).
//
// mutate (optional) applies any additional field changes ATOMICALLY with the
// state flip — the purchase yard chosen when queueing, the ship symbol assigned
// when bought. It sees the row exactly as it is in the database, still carrying
// fromState; the flip to toState is applied afterwards and is authoritative, so
// mutate cannot redirect the transition.
//
// A transition OWNS THREE COLUMNS and writes no others: state, assigned_ship and
// purchase_yard. Anything else mutate sets on its copy is ignored — the copy is a
// GUARD, never a source for the write.
//
// That is the fix for a real lost update, not a tidiness rule. The write used to
// re-assert every column from the row loaded at the top of this transaction, so a
// MarkScanned committing in between — which the scan pacer can do at any instant,
// from its own goroutine — was reverted by a transition that had nothing to do
// with scanning. Naming only the owned columns makes the two writers disjoint by
// CONSTRUCTION, whatever rows they happen to meet on.
//
// The owned columns are narrowed further still: assigned_ship and purchase_yard
// are named only when mutate actually CHANGED them. A transition that just moves
// the state machine therefore writes one column, and cannot revert a hull another
// writer recorded while it was in flight. The filed slot reaper (sp-l3f3d) is
// exactly such a state-only transition, and is safe here without special-casing.
//
// Both statements run in one transaction and BOTH carry the state guard, so a
// concurrent transition committing between the load and the update still loses
// the race (the UPDATE matches zero rows) rather than overwriting.
func (r *SensingLedgerRepository) TransitionSlot(
	ctx context.Context,
	playerID int,
	waypoint, fromState, toState string,
	mutate func(*SensingSlotModel),
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var m SensingSlotModel
		err := tx.Where("player_id = ? AND waypoint_symbol = ? AND state = ?", playerID, waypoint, fromState).
			First(&m).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("slot %q: want state %q: %w", waypoint, fromState, ErrSlotStateConflict)
		}
		if err != nil {
			return fmt.Errorf("failed to load sensing slot %q for transition: %w", waypoint, err)
		}

		// Snapshot the owned columns by VALUE before handing the row over. A
		// plain struct copy would share the pointer targets with m, so a callback
		// that wrote THROUGH a pointer rather than replacing it would compare
		// equal to itself and have its change dropped from the write.
		loadedShip, loadedYard := copyStringValue(m.AssignedShip), copyStringValue(m.PurchaseYard)
		if mutate != nil {
			mutate(&m)
		}

		updates := map[string]any{
			"state":      toState,
			"updated_at": time.Now().UTC(),
		}
		if !sameStringValue(loadedShip, m.AssignedShip) {
			updates["assigned_ship"] = m.AssignedShip
		}
		if !sameStringValue(loadedYard, m.PurchaseYard) {
			updates["purchase_yard"] = m.PurchaseYard
		}

		res := tx.Model(&SensingSlotModel{}).
			Where("player_id = ? AND waypoint_symbol = ? AND state = ?", playerID, waypoint, fromState).
			Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("failed to transition sensing slot %q from %q to %q: %w", waypoint, fromState, toState, res.Error)
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("slot %q: want state %q: %w", waypoint, fromState, ErrSlotStateConflict)
		}
		return nil
	})
}

// CountOwnedProbes reports how many probe HULLS the player's ledger accounts
// for: slots in BOUGHT, IN_TRANSIT or PARKED that carry an assigned ship. This
// is the probe_cap read that gates probe spend.
//
// WANTED and QUEUED slots are intents, not hulls — nothing has been bought for
// them yet — and a BOUGHT slot with no ship symbol has no hull recorded either,
// so neither counts. SPARE-kind slots DO count: a parked reserve probe is still
// a probe we paid for. Deliberately era-AGNOSTIC (see the type comment): a hull
// bought last era still exists, and losing it from this count would authorise
// re-buying it.
func (r *SensingLedgerRepository) CountOwnedProbes(ctx context.Context, playerID int) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&SensingSlotModel{}).
		Where("player_id = ? AND state IN ? AND assigned_ship IS NOT NULL",
			playerID, []string{"BOUGHT", "IN_TRANSIT", "PARKED"}).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("failed to count owned sensing probes: %w", err)
	}
	return count, nil
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

// MarkScanned records a completed scan on a parked slot: the freshness stamp and
// the smoothed spread, and nothing else — the state machine and the slot's hull
// assignment are untouched. A missing slot is an error, never an upsert: a
// phantom row would later be read back as a real placement and dispatched to.
func (r *SensingLedgerRepository) MarkScanned(ctx context.Context, playerID int, waypoint string, at time.Time, spreadEWMA float64) error {
	res := r.db.WithContext(ctx).Model(&SensingSlotModel{}).
		Where("player_id = ? AND waypoint_symbol = ?", playerID, waypoint).
		Updates(map[string]any{
			"last_scan_at": at,
			"spread_ewma":  spreadEWMA,
			"updated_at":   time.Now().UTC(),
		})
	if res.Error != nil {
		return fmt.Errorf("failed to mark sensing slot %q scanned: %w", waypoint, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("sensing slot %q for player %d: %w", waypoint, playerID, gorm.ErrRecordNotFound)
	}
	return nil
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

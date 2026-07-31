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

// COLUMN OWNERSHIP on sensing_slots. FIVE writers drive this table
// concurrently, and each owns a disjoint answer about the same placement:
//
//	MarkScanned         what the market last showed  — last_scan_at, spread_ewma, last_scan_attempt_at
//	MarkScanAttempted   that the rotation took a turn — last_scan_attempt_at
//	TransitionSlot      how far along the placement is — state, assigned_ship, purchase_yard
//	UpsertSlotMetadata  what the screen measured      — whitelist_goods, depth_credits
//	UpsertSpareSlot     which hull is standing here   — state, assigned_ship
//
// The scan path is the only writer of the two scan clocks, and it writes them
// through those two methods ONLY — which is what keeps "the rotation took a
// turn" and "market data was written" independently answerable (sp-zml2u).
// MarkScanned names last_scan_attempt_at as well because a completed scan IS a
// turn; a scan path that advanced only the data stamp would leave a
// never-declined slot pacing off a NULL attempt clock.
//
// NOBODY owns slot_kind: it is part of the primary key (sp-dpfp8) and therefore
// immutable. A row's kind is decided when it is inserted and can only change by
// deleting the row, which is why every writer that names a waypoint now names a
// kind alongside it — the pair is the address, and half an address addresses an
// ambiguous set.
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
// refreshes on conflict: which hull, and in what state.
//
// It deliberately does NOT carry whitelist_goods or depth_credits. A hull-bearing
// write measures nothing — the caller knows where a ship is standing, not what
// the market there deals in — so a blanket set would zero the screen's goods list
// and depth, and an empty goods list drops the waypoint out of the screen's hit
// set entirely. era_id IS refreshed: the planning reads are era-scoped, so a row
// left stamped with a dead era would carry a live hull that no planner can see
// while CountOwnedProbes (era-agnostic) still counts it.
//
// slot_kind LEFT THIS SET when it joined the primary key (sp-dpfp8). It is no
// longer assignable in any meaningful sense — a conflict only fires on a row
// whose kind already equals the incoming one — and keeping it would advertise a
// capability the key has removed. The capability it used to provide was
// converting a MARKET placement into a SPARE in place, which is exactly the
// silent eviction that took working market placements out of the scan rotation
// (see the occupancy guard in probe_sensing_adoption.go). A kind change is now a
// different ROW, which is the honest representation: the yard is still scanning
// AND is now staging a seed.
var sensingSlotSpareUpdateColumns = []string{
	"system_symbol", "state", "assigned_ship", "era_id", "updated_at",
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

// DeleteSlot removes ONE placement row outright: the one of the given KIND.
//
// It exists for ONE transition: a parked spare being handed to a charting
// errand. The hull stops belonging to the ledger and starts belonging to the
// mission, and leaving the row behind would have the buy queue re-task a hull
// that has already flown away.
//
// THE KIND IS A MONEY GUARD, not a filter for tidiness (sp-dpfp8, RULINGS #4).
// A waypoint may now carry a MARKET row and a SPARE row at once, and the caller
// is releasing exactly one of them. Deleting by waypoint alone would take BOTH —
// so claiming a spare staged at a yard would also delete the MARKET placement of
// the probe parked there scanning. That probe is named by the row the delete just
// destroyed, so it vanishes from CountOwnedProbes while still flying: the cap
// under-reads and authorises buying a replacement for a hull we already own. That
// is the precise failure the narrow key used to make impossible for free, and
// naming the kind is what replaces it.
//
// A missing row is NOT an error — the delete is idempotent by design, because
// its caller has already stamped the errand and cannot usefully unwind.
func (r *SensingLedgerRepository) DeleteSlot(ctx context.Context, playerID int, waypoint, kind string) error {
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND waypoint_symbol = ? AND slot_kind = ?", playerID, waypoint, kind).
		Delete(&SensingSlotModel{}).Error; err != nil {
		return fmt.Errorf("failed to delete sensing slot %q (%s): %w", waypoint, kind, err)
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
// (player_id, waypoint_symbol, slot_kind), so re-declaring a placement OF THE
// SAME KIND updates the row in place — one slot per waypoint PER KIND is a
// structural guarantee, not a convention.
//
// THE KEY AND THE CONFLICT SETS ARE ORTHOGONAL, which is what let the key widen
// (sp-dpfp8) without disturbing the column-ownership split (sp-wgjb7). The key
// decides WHICH ROW a write lands on; the conflict set decides WHICH COLUMNS it
// may assert once it lands. Both callers share this one target and keep their own
// disjoint set, so neither can reach a column the other owns.
//
// WHAT THE WIDER KEY CHANGES FOR CALLERS: a kind is now part of a row's identity
// and therefore IMMUTABLE. UpsertSpareSlot used to be able to convert a MARKET
// placement into a SPARE in place, through slot_kind in its conflict set; it now
// INSERTS a separate SPARE row and leaves the MARKET row scanning. That is the
// intended behaviour — a yard that is scanning can also be staging — and it is
// strictly safer than the rewrite it replaces, which silently evicted a working
// market placement from the scan rotation.
//
// It is unexported ON PURPOSE: the conflict set is the whole safety property
// here, so there is no way to reach this table with a caller-chosen one.
func (r *SensingLedgerRepository) upsertSlot(ctx context.Context, m SensingSlotModel, onConflict []string) error {
	m.EraID = r.openEraID(ctx)
	m.UpdatedAt = time.Now().UTC()
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "player_id"}, {Name: "waypoint_symbol"}, {Name: "slot_kind"}},
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

// PlacementWorklist returns the player's era-scoped slots in any of the given
// states, LEAST RECENTLY ATTEMPTED FIRST. Like SlotsByState, an EMPTY states list
// matches NOTHING.
//
// WHY THIS IS NOT JUST SlotsByState WITH ANOTHER ORDER BY. Its order — waypoint
// symbol — is what nine other callers want, and it is the wrong one for exactly
// one of them. The placement machine works a FIXED CAP OVER A QUEUE THAT DOES NOT
// DRAIN: a slot whose move is refused stays in the state it was in, so it is back
// on the list next tick, in the same position. Any order that is stable across
// ticks therefore gives the list a PERMANENT head, and the cap means everything
// behind that head is never read at all. Measured live before this existed: 266
// BOUGHT + 52 IN_TRANSIT slots, ~40 actions' worth of ticks, zero dispatches, and
// a slot 22.5 hours old that had never been attempted once because its waypoint
// sorted behind a hundred others that were failing every tick (sp-cwnwb).
//
// The screening sweep hit the identical defect and fixed it the identical way —
// see the screened_at rotation in run_probe_sensing_coordinator.go. This is that
// rotation for placements, moved into SQL because it is pure recency with no
// policy mixed in, and because an order is the kind of guarantee this file already
// publishes for its sibling read.
//
// THREE CLAUSES, ALL LOAD-BEARING:
//
//   - Never-attempted slots first. A slot the machine has never tried is the case
//     the rotation most needs to reach, and it is the shape the live defect took.
//     Written as `(last_attempt_at IS NULL) DESC` rather than `NULLS FIRST`
//     because the two engines disagree on where NULLs land by default — Postgres
//     sorts them last on ASC, SQLite first — and this must not depend on that.
//   - Oldest attempt next: stamping a slot when it consumes a budget sends it to
//     the back, so N slots are fully covered in ceil(N/budget) ticks at unchanged
//     cost per tick.
//   - Waypoint symbol last, to make the order TOTAL. Slots stamped inside one
//     tick readily share a timestamp, and without a tie-break the order among
//     them is unspecified — which reintroduces a stable head among the ties and,
//     with it, the bug.
func (r *SensingLedgerRepository) PlacementWorklist(ctx context.Context, playerID int, states ...string) ([]SensingSlotModel, error) {
	if len(states) == 0 {
		return nil, nil
	}
	predicate, args := eraScopePredicate(r.openEraID(ctx))
	var models []SensingSlotModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND state IN ?", playerID, states).
		Where(predicate, args...).
		Order("(last_attempt_at IS NULL) DESC, last_attempt_at ASC, waypoint_symbol ASC").
		Find(&models).Error; err != nil {
		return nil, fmt.Errorf("failed to list the sensing placement worklist: %w", err)
	}
	return models, nil
}

// MarkPlacementAttempt stamps one slot as having just consumed a placement
// tick's budget, which is what moves it to the back of PlacementWorklist.
//
// IT WRITES ONE COLUMN, and that is a safety property rather than tidiness. The
// probe cap counts slots by state and assigned_ship (CountOwnedProbes), so a
// fairness stamp that touched either could drop a hull out of the count and
// authorise re-buying a probe the player already owns. Naming last_attempt_at in
// a map — never a struct, which GORM would fill from every non-zero field —
// makes that impossible by construction rather than by inspection.
//
// A MISS IS NOT AN ERROR. The slot may have been transitioned or reaped between
// the worklist read and this write, and the stamp is a hint about ordering, not a
// claim about the row's existence; nothing is owed if it has moved on. Only a
// genuine write failure is reported, and the caller logs rather than aborts —
// see markAttempt.
func (r *SensingLedgerRepository) MarkPlacementAttempt(ctx context.Context, playerID int, waypoint, kind string) error {
	if err := r.db.WithContext(ctx).Model(&SensingSlotModel{}).
		Where("player_id = ? AND waypoint_symbol = ? AND slot_kind = ?", playerID, waypoint, kind).
		Updates(map[string]any{"last_attempt_at": time.Now().UTC()}).Error; err != nil {
		return fmt.Errorf("failed to stamp the placement attempt on sensing slot %q (%s): %w", waypoint, kind, err)
	}
	return nil
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
// THE KIND IS PART OF THE ADDRESS (sp-dpfp8). A waypoint may now carry a MARKET
// row and a SPARE row at once, and they are frequently in the SAME state — a yard
// that is both a whitelisted market awaiting a probe and a staging post awaiting
// a seed holds two WANTED rows. Matched on the waypoint and state alone, the load
// would pick one of them arbitrarily while the UPDATE hit BOTH, so a single
// WANTED→QUEUED claim would queue two placements off one purchase. Naming the
// kind makes the pair addressable and keeps the optimistic guard honest: the
// row this transition guards on is the row it writes.
func (r *SensingLedgerRepository) TransitionSlot(
	ctx context.Context,
	playerID int,
	waypoint, kind, fromState, toState string,
	mutate func(*SensingSlotModel),
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var m SensingSlotModel
		err := tx.Where("player_id = ? AND waypoint_symbol = ? AND slot_kind = ? AND state = ?",
			playerID, waypoint, kind, fromState).
			First(&m).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("slot %q (%s): want state %q: %w", waypoint, kind, fromState, ErrSlotStateConflict)
		}
		if err != nil {
			return fmt.Errorf("failed to load sensing slot %q (%s) for transition: %w", waypoint, kind, err)
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
			Where("player_id = ? AND waypoint_symbol = ? AND slot_kind = ? AND state = ?",
				playerID, waypoint, kind, fromState).
			Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("failed to transition sensing slot %q (%s) from %q to %q: %w", waypoint, kind, fromState, toState, res.Error)
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("slot %q (%s): want state %q: %w", waypoint, kind, fromState, ErrSlotStateConflict)
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
//
// Scoped to the scanning row's KIND (sp-dpfp8). The scan is a fact about one
// placement — the probe standing at that waypoint watching that market — and a
// waypoint may now also carry a SPARE row for a seed staged there, which has
// scanned nothing. Stamping by waypoint alone would mark both, backdating the
// rotation's staleness ordering with a scan the spare never performed, and would
// leave RowsAffected unable to answer the question this method asks it: whether
// the slot being scanned still exists.
func (r *SensingLedgerRepository) MarkScanned(ctx context.Context, playerID int, waypoint, kind string, at time.Time, spreadEWMA float64) error {
	res := r.db.WithContext(ctx).Model(&SensingSlotModel{}).
		Where("player_id = ? AND waypoint_symbol = ? AND slot_kind = ?", playerID, waypoint, kind).
		Updates(map[string]any{
			"last_scan_at": at,
			// A completed scan is also a TURN, so it advances the pacing clock in
			// the same write. Omitting it would leave a slot that never gets
			// declined pacing off a NULL attempt stamp, which the reader coalesces
			// to last_scan_at — correct today, but only by accident, and it would
			// silently break the moment the coalesce was removed.
			"last_scan_attempt_at": at,
			"spread_ewma":          spreadEWMA,
			"updated_at":           time.Now().UTC(),
		})
	if res.Error != nil {
		return fmt.Errorf("failed to mark sensing slot %q (%s) scanned: %w", waypoint, kind, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("sensing slot %q (%s) for player %d: %w", waypoint, kind, playerID, gorm.ErrRecordNotFound)
	}
	return nil
}

// MarkScanAttempted records that the rotation spent this slot's TURN on it
// without producing any market data — the market-scan budget declined the
// request and the caller served from the store instead.
//
// IT WRITES THE PACING CLOCK AND NOTHING ELSE, and that is the entire point
// (sp-zml2u). last_scan_at is a claim that data was written, so a decline must
// not touch it; but the reconcile rebuilds the scan heap from this table every
// 30 seconds, so if a decline advanced no durable clock at all, every declined
// slot would read as due on every rebuild and the rotation would spin at full
// speed forever. At a measured 92% decline rate that is the whole rotation.
// Advancing the attempt clock alone is what keeps the pacing the budget computed
// while leaving the freshness stamp honest.
//
// spread_ewma is deliberately NOT written. A spread computed on a declined turn
// is derived from whatever the store already held — the very data whose age is
// in question — so persisting it would weight the rotation on a measurement no
// scan backs. It is written only alongside the data that justifies it.
//
// A missing slot is an error for MarkScanned's reason exactly: a phantom row
// would later be read back as a real placement and dispatched to.
func (r *SensingLedgerRepository) MarkScanAttempted(ctx context.Context, playerID int, waypoint, kind string, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&SensingSlotModel{}).
		Where("player_id = ? AND waypoint_symbol = ? AND slot_kind = ?", playerID, waypoint, kind).
		Updates(map[string]any{
			"last_scan_attempt_at": at,
			"updated_at":           time.Now().UTC(),
		})
	if res.Error != nil {
		return fmt.Errorf("failed to mark sensing slot %q (%s) attempted: %w", waypoint, kind, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("sensing slot %q (%s) for player %d: %w", waypoint, kind, playerID, gorm.ErrRecordNotFound)
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

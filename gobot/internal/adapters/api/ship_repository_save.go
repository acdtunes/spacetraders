package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// shipVersionConflicts counts Save calls whose row version moved past the
// entity's loaded version (a concurrent writer committed in between, about to be
// last-write-wins clobbered). Mirrored to prometheus; kept as a package atomic so
// tests and debuggers can read it without a registry.
var shipVersionConflicts atomic.Int64

// dedicatedFleetClobbersPrevented counts general ship Save calls that carried a
// STALE dedicated_fleet tag and were prevented from overwriting a live dedication.
// AssignFleet is the single write path for that tag; a coordinator holding a
// pre-dedication Ship snapshot must never resurrect its stale tag through the
// whole-row UpdateAll upsert. Kept as a package atomic so the WARN is not the only
// signal and tests can observe the prevention without scraping logs.
var dedicatedFleetClobbersPrevented atomic.Int64

// assignmentClobbersPrevented counts version-conflicted Save fallbacks that carried a
// STALE assignment (ownership) snapshot and were prevented from rewriting the row's
// live claim. Resurrecting a released claim orphans the hull under a container that no
// longer exists; erasing a live one takes it out from under a running worker. Kept as a
// package atomic so the WARN is not the only signal.
var assignmentClobbersPrevented atomic.Int64

// persistOwnedColumns writes ONLY the columns the calling operation just changed
// at the server, leaving the rest of the row alone.
//
// The five operation methods above each call the API, mutate one or two fields of
// a ship entity the caller may have held for an ENTIRE flight, and persist. A
// whole-row upsert built from that snapshot rewrites cargo, ownership and
// dedication from a view the operation has no reason to believe is current — the
// mechanism behind a released hull coming back claimed and permanently orphaned,
// and behind a delivered hold coming back full and drawing a liquidation worker
// onto an empty ship. Scoping the write to what the operation owns removes the
// source rather than defending each victim column as it is discovered.
//
// The ROW version is advanced: nav state is exactly what a concurrent whole-row
// writer would otherwise clobber, so a snapshot older than this write must lose
// its next CAS. The ENTITY's version is deliberately left where it is — the
// entity is still not a trustworthy source for a whole-row write (its cargo and
// ownership have moved on), and certifying it as current would let its next Save
// win the CAS outright and bypass preserveAssignmentOwnership entirely.
//
// Read and write under one row lock, for the same reason ClaimShip is: a bare
// read-then-update would leave a version TOCTOU where a writer committing in
// between is silently absorbed into the version this write stamps.
func (r *ShipRepository) persistOwnedColumns(ctx context.Context, ship *navigation.Ship, owned map[string]interface{}) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var persisted persistence.ShipModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("ship_symbol = ? AND player_id = ?", ship.ShipSymbol(), ship.PlayerID().Value()).
			First(&persisted).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Nothing persisted yet (a hull this daemon has never written): there is
			// no row to scope a write to, and no other writer's columns to protect.
			model := r.shipToModel(ship)
			return r.upsertWholeRow(ctx, tx, ship, &model)
		}
		if err != nil {
			return fmt.Errorf("failed to lock ship %s for a scoped write: %w", ship.ShipSymbol(), err)
		}

		columns := make(map[string]interface{}, len(owned)+2)
		for column, value := range owned {
			columns[column] = value
		}
		columns["version"] = persisted.Version + 1
		columns["synced_at"] = r.clock.Now()

		if err := tx.Model(&persistence.ShipModel{}).
			Where("ship_symbol = ? AND player_id = ?", ship.ShipSymbol(), ship.PlayerID().Value()).
			Updates(columns).Error; err != nil {
			return fmt.Errorf("failed to persist ship %s: %w", ship.ShipSymbol(), err)
		}

		r.shipListCache.Delete(ship.PlayerID().Value())
		return nil
	})
}

// navStatusColumns is the single column a dock/orbit transition owns.
func navStatusColumns(ship *navigation.Ship) map[string]interface{} {
	return map[string]interface{}{"nav_status": string(ship.NavStatus())}
}

// fuelColumns is the tank, written as one pair. current alone could land against
// a stale capacity and store a row violating current <= capacity; both come from
// the same value object, so together they are always coherent.
func fuelColumns(ship *navigation.Ship) map[string]interface{} {
	if ship.Fuel() == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{
		"fuel_current":  ship.Fuel().Current,
		"fuel_capacity": ship.Fuel().Capacity,
	}
}

// navigateColumns is what a completed navigate owns. The API has just moved the
// hull, so its position, transit clock, flight mode and burnt fuel are this
// operation's to state and nobody else's.
//
// Fuel is shared with Refuel, but the boundary is temporal rather than columnar:
// burning and filling are the only two things that change a tank, they cannot
// overlap on one hull (the server requires orbit to navigate and a dock to
// refuel), and each writes the value the API just handed it. Dock, orbit and
// flight-mode change no fuel at the server and so write none.
//
// origin/departure are NOT owned here: they describe where the current transit
// began, and only the API nav.route sync (shipDataToModel) ever learns them.
func navigateColumns(ship *navigation.Ship) map[string]interface{} {
	columns := navStatusColumns(ship)
	columns["arrival_time"] = ship.ArrivalTime()
	columns["flight_mode"] = ship.FlightMode()
	for column, value := range fuelColumns(ship) {
		columns[column] = value
	}
	if location := ship.CurrentLocation(); location != nil {
		columns["location_symbol"] = location.Symbol
		columns["location_x"] = location.X
		columns["location_y"] = location.Y
		columns["system_symbol"] = shared.ExtractSystemSymbol(location.Symbol)
	}
	return columns
}

// Save persists ship aggregate state (including full state) to DB.
// When the entity carries a known row version, the upsert is guarded with
// `DO UPDATE ... WHERE ships.version = <loaded>` (postgres and sqlite both
// support upsert-where): RowsAffected == 0 means another writer committed
// since this entity was loaded, and the fallback keeps last-write-wins for every
// column EXCEPT the assignment (ownership) group, which is re-read from the row.
func (r *ShipRepository) Save(ctx context.Context, ship *navigation.Ship) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	if loaded := ship.PersistedVersion(); loaded > 0 {
		committed, err := r.trySaveCAS(ctx, ship)
		if err != nil {
			return err
		}
		if committed {
			return nil
		}
		log.Printf("ERROR: ship %s save conflict — row version moved past %d (concurrent writer; sp-60ff probe); applying last-write-wins fallback",
			ship.ShipSymbol(), loaded)
		return r.saveStaleLastWriteWins(ctx, ship)
	}

	return r.saveLastWriteWins(ctx, ship)
}

// saveStaleLastWriteWins is Save's conflict-branch fallback: last-write-wins for
// every column EXCEPT the assignment (ownership) group, which is taken from the
// row under a write lock instead of from the entity.
//
// The entity reaching here has just been PROVEN stale by the version guard, so its
// ownership snapshot may predate a claim or a release. Writing it back resurrects a
// released claim — orphaning the hull under a container that no longer exists — or
// erases a live one out from under a running worker. Ownership has its own atomic
// writers (ClaimShip, ReserveForCaptain, PreemptForCaptain, ReleaseContainerClaim,
// ReleaseCaptainReservation, and ForceRelease through SaveWithRetry), every one of
// which advances ships.version; a snapshot that lost the version race is therefore
// never the authority for these columns.
//
// Deliberately NOT folded into saveLastWriteWins: that is also SaveWithRetry's
// exhaustion fallback, which re-applies its mutation on a FRESHLY loaded row and so
// holds the authoritative ownership. Preserving there would make every release a
// silent no-op.
//
// The read is a locked read in the same transaction as the write for the same reason
// ClaimShip is: a bare read-then-upsert would leave the exact TOCTOU window this
// exists to close.
func (r *ShipRepository) saveStaleLastWriteWins(ctx context.Context, ship *navigation.Ship) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model := r.shipToModel(ship)
		r.preserveDedicatedFleetTag(ctx, tx, &model)

		var persisted persistence.ShipModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("ship_symbol = ? AND player_id = ?", model.ShipSymbol, model.PlayerID).
			First(&persisted).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The row is gone (pruned under us): there is no live claim to defend,
			// and the insert's own ownership is all there is.
			return r.upsertWholeRow(ctx, tx, ship, &model)
		}
		if err != nil {
			// Unlike the dedicated_fleet preserve, a read failure here cannot fall
			// through: proceeding blindly is precisely the clobber. Fail the save and
			// let the caller retry.
			return fmt.Errorf("failed to lock ship %s for the stale write-back: %w", model.ShipSymbol, err)
		}

		r.preserveAssignmentOwnership(&model, &persisted)

		// GREATEST(row, entity): a stale entity lowering the row version makes every
		// later writer conflict against it, so one instant race turns into a run of
		// re-stamps for the rest of the entity's flight.
		if persisted.Version > model.Version {
			model.Version = persisted.Version
		}

		if err := r.upsertWholeRow(ctx, tx, ship, &model); err != nil {
			return err
		}

		// Heal the entity: it now agrees with the row on ownership, so its next
		// version-guarded save is trustworthy for these columns rather than
		// permanently conflicting.
		ship.SetAssignment(r.modelToAssignment(&persisted))
		return nil
	})
}

// preserveAssignmentOwnership takes the whole assignment column group from the
// persisted row so the outgoing upsert is a no-op for ownership. The copy is
// unconditional (the group must stay internally coherent — a status from one writer
// with a released_at from another is not a state any writer produced); only the
// telemetry is conditional, on the ownership triplet the claim/release paths key on.
func (r *ShipRepository) preserveAssignmentOwnership(model *persistence.ShipModel, persisted *persistence.ShipModel) {
	if model.AssignmentStatus != persisted.AssignmentStatus ||
		containerIDValue(model.ContainerID) != containerIDValue(persisted.ContainerID) ||
		model.AssignmentOwner != persisted.AssignmentOwner {
		assignmentClobbersPrevented.Add(1)
		log.Printf("WARN: ship %s stale save carried assignment %s/%s/%q while the row holds %s/%s/%q — preserving the persisted claim rather than resurrecting a stale one",
			model.ShipSymbol,
			model.AssignmentStatus, model.AssignmentOwner, containerIDValue(model.ContainerID),
			persisted.AssignmentStatus, persisted.AssignmentOwner, containerIDValue(persisted.ContainerID))
	}

	model.AssignmentStatus = persisted.AssignmentStatus
	model.ContainerID = persisted.ContainerID
	model.AssignedAt = persisted.AssignedAt
	model.ReleasedAt = persisted.ReleasedAt
	model.ReleaseReason = persisted.ReleaseReason
	model.AssignmentOwner = persisted.AssignmentOwner
	model.AssignmentReason = persisted.AssignmentReason
}

// containerIDValue flattens the nullable container_id for comparison and logging.
func containerIDValue(containerID *string) string {
	if containerID == nil {
		return ""
	}
	return *containerID
}

// trySaveCAS attempts the version-guarded upsert for an entity that carries a
// loaded row version (caller guarantees PersistedVersion() > 0). It returns
// committed=true when the guarded write landed — the entity's version is
// advanced and the list cache invalidated. It returns committed=false when the
// row moved past the loaded version (RowsAffected == 0): a concurrent-writer
// conflict, counted + mirrored to prometheus so the caller can decide between
// retry (SaveWithRetry) and last-write-wins (Save). The conflict counter is
// incremented per attempt, so retries keep the conflict telemetry intact.
func (r *ShipRepository) trySaveCAS(ctx context.Context, ship *navigation.Ship) (committed bool, err error) {
	loaded := ship.PersistedVersion()
	model := r.shipToModel(ship)
	r.preserveDedicatedFleetTag(ctx, r.db, &model)
	res := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			Where: clause.Where{Exprs: []clause.Expression{
				clause.Eq{Column: clause.Column{Table: "ships", Name: "version"}, Value: loaded},
			}},
			UpdateAll: true,
		}).
		Create(&model)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected > 0 {
		ship.SetPersistedVersion(loaded + 1)
		r.shipListCache.Delete(ship.PlayerID().Value())
		return true, nil
	}
	shipVersionConflicts.Add(1)
	metrics.RecordShipVersionConflict()
	return false, nil
}

// saveLastWriteWins performs the legacy unconditional upsert. It clobbers any
// concurrent writer's mutation and is the fallback both for entities with no
// loaded version (API-born inserts / first sync) and for CAS-retry exhaustion —
// so behavior never regresses below the conflict-detection tripwire.
func (r *ShipRepository) saveLastWriteWins(ctx context.Context, ship *navigation.Ship) error {
	model := r.shipToModel(ship)
	r.preserveDedicatedFleetTag(ctx, r.db, &model)
	return r.upsertWholeRow(ctx, r.db, ship, &model)
}

// upsertWholeRow issues the unconditional whole-row upsert both last-write-wins
// paths share, against the given executor (r.db, or the enclosing transaction when
// the model was built under a row lock).
func (r *ShipRepository) upsertWholeRow(ctx context.Context, tx *gorm.DB, ship *navigation.Ship, model *persistence.ShipModel) error {
	err := tx.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			UpdateAll: true,
		}).
		Create(model).Error

	if err == nil {
		ship.SetPersistedVersion(model.Version)
		// Invalidate cache to ensure assignment changes are immediately visible
		// This prevents stale assignment data from causing ships to be incorrectly
		// seen as idle when they've been assigned to containers (e.g., storage ships)
		r.shipListCache.Delete(ship.PlayerID().Value())
	}

	return err
}

// preserveDedicatedFleetTag defends the single-write-path invariant for the
// dedicated_fleet tag. AssignFleet is the ONLY writer of that column,
// but every general ship upsert (Save/CAS/last-write-wins/SaveAll) rewrites the
// WHOLE row (UpdateAll) from the domain ship's in-memory snapshot — including
// dedicated_fleet (shipToModel). A coordinator that materialised a hull BEFORE a
// live `fleet add`/`remove` therefore carries a stale tag and would silently
// resurrect it over the operator's change on its next routine write-back. The
// version guard does not catch it either: AssignFleet mutates only the
// tag column and does not bump ships.version, so the CAS `WHERE version=<loaded>`
// still matches and clobbers. This reloads the persisted tag and, when the
// outgoing snapshot disagrees, rewrites the model with the DB value so the upsert
// is a no-op for that column, then counts + WARNs the prevented drop so it is
// never silent again.
//
// Cost is one indexed primary-key lookup of a single column per save — the same
// read SyncAllFromAPI already performs per ship to preserve this exact tag,
// and cheap relative to the state-changing writes a save accompanies.
// A brand-new row (ErrRecordNotFound) has nothing persisted to protect: the
// model's own value (a fresh insert, normally "") is authoritative, left as-is.
// Counted per upsert attempt, mirroring shipVersionConflicts, so a rare
// version-conflict fallback (trySaveCAS then saveLastWriteWins) may tick twice.
func (r *ShipRepository) preserveDedicatedFleetTag(ctx context.Context, tx *gorm.DB, model *persistence.ShipModel) {
	if r.db == nil {
		return
	}

	var persisted persistence.ShipModel
	err := tx.WithContext(ctx).
		Select("dedicated_fleet").
		Where("ship_symbol = ? AND player_id = ?", model.ShipSymbol, model.PlayerID).
		First(&persisted).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return // brand-new ship insert: no persisted dedication to preserve
	}
	if err != nil {
		// Fail safe: a read failure must neither drop the tag by proceeding
		// blindly nor block the save (behavior never regresses below today's).
		// Leave the model as-is, surface the anomaly, and let the save proceed.
		log.Printf("WARN: ship %s dedicated_fleet preserve-read failed (%v); proceeding without stale-tag protection (sp-90a3)",
			model.ShipSymbol, err)
		return
	}
	if persisted.DedicatedFleet == model.DedicatedFleet {
		return // snapshot agrees with the DB — nothing to protect
	}

	dedicatedFleetClobbersPrevented.Add(1)
	log.Printf("WARN: ship %s general Save carried a stale dedicated_fleet %q while the persisted dedication is %q — preserving the live `fleet add`/`remove` value instead of silently clobbering it (sp-90a3)",
		model.ShipSymbol, model.DedicatedFleet, persisted.DedicatedFleet)
	model.DedicatedFleet = persisted.DedicatedFleet
}

// SaveWithRetry implements navigation.ShipRepository. See that interface for the
// contract. It re-finds the fresh row and re-applies mutate on every
// ships.version conflict, bounded by resolvedCASRetries(), then falls
// back to last-write-wins on exhaustion so behavior never regresses below
// today's baseline.
func (r *ShipRepository) SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	if r.db == nil {
		return nil, false, fmt.Errorf("database not configured")
	}
	maxRetries := r.resolvedCASRetries()

	var ship *navigation.Ship
	for attempt := 0; ; attempt++ {
		var err error
		ship, err = r.FindBySymbol(ctx, symbol, playerID)
		if err != nil {
			return nil, false, err
		}

		changed, err := mutate(ship)
		if err != nil {
			return ship, false, err
		}
		if !changed {
			// Already in the desired state (a concurrent writer got there
			// first) — no write, no spurious version bump.
			return ship, false, nil
		}

		// No loaded version (API-born / fresh row) → nothing to guard against;
		// the unconditional upsert is the only correct path.
		if ship.PersistedVersion() <= 0 {
			if err := r.saveLastWriteWins(ctx, ship); err != nil {
				return ship, false, err
			}
			return ship, true, nil
		}

		committed, err := r.trySaveCAS(ctx, ship)
		if err != nil {
			return ship, false, err
		}
		if committed {
			return ship, true, nil
		}

		// Conflict. Retry on fresh state unless retries are disabled/exhausted.
		if attempt >= maxRetries {
			// maxRetries==0 (disabled) lands here on the first conflict →
			// exactly the legacy last-write-wins. maxRetries>0 exhausted →
			// last-write-wins on the freshest re-applied state (never regress).
			log.Printf("ERROR: ship %s save conflict — %d CAS attempt(s) exhausted (concurrent writer; sp-01wc); applying last-write-wins fallback",
				symbol, attempt+1)
			if err := r.saveLastWriteWins(ctx, ship); err != nil {
				return ship, false, err
			}
			return ship, true, nil
		}
		// else loop: re-find fresh, re-apply mutate, retry the CAS save.
	}
}

// SaveAll batch persists multiple ship aggregates
func (r *ShipRepository) SaveAll(ctx context.Context, ships []*navigation.Ship) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}
	if len(ships) == 0 {
		return nil
	}

	models := make([]persistence.ShipModel, len(ships))
	playerIDs := make(map[int]bool)
	for i, ship := range ships {
		models[i] = r.shipToModel(ship)
		r.preserveDedicatedFleetTag(ctx, r.db, &models[i])
		playerIDs[ship.PlayerID().Value()] = true
	}

	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "ship_symbol"}, {Name: "player_id"}},
			UpdateAll: true,
		}).
		Create(&models).Error

	if err == nil {
		// Invalidate cache for all affected players
		for playerID := range playerIDs {
			r.shipListCache.Delete(playerID)
		}
	}

	return err
}

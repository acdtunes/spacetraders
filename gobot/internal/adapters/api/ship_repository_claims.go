package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ReleaseAllActive releases all active ship assignments for the given player (bulk operation)
// Used during daemon startup to clean up zombie assignments from previous runs.
//
// Captain reservations (assignment_owner="captain") are deliberately excluded:
// they use the same assignment_status="active" as a live coordinator claim, but
// a reservation's whole purpose is to survive daemon restarts, so an
// owner-blind release here would silently un-reserve a captain-held hull on
// every restart.
func (r *ShipRepository) ReleaseAllActive(ctx context.Context, playerID shared.PlayerID, reason string) (int, error) {
	if r.db == nil {
		return 0, fmt.Errorf("database not configured")
	}

	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&persistence.ShipModel{}).
		Where("player_id = ?", playerID.Value()).
		Where("assignment_status = ?", "active").
		Where("assignment_owner IS NULL OR assignment_owner != ?", string(navigation.AssignmentOwnerCaptain)).
		Updates(map[string]interface{}{
			"assignment_status": "idle",
			"container_id":      nil,
			"released_at":       now,
			"release_reason":    reason,
			// Every ownership write advances the version, so a snapshot taken
			// before the sweep can never re-assert its claim through a CAS save.
			"version": gorm.Expr("version + 1"),
		})

	if result.Error != nil {
		return 0, fmt.Errorf("failed to release all active assignments: %w", result.Error)
	}

	return int(result.RowsAffected), nil
}

// ClaimShip exclusively assigns an idle ship to a container using row-level locking.
// Returns ShipAlreadyAssignedError if ship is already assigned to another container.
//
// operation is the claiming coordinator's fleet identity ("contract",
// "manufacturing", ...). A free hull whose DedicatedFleet tag names a
// different fleet is rejected with ShipDedicatedToOtherFleetError — inside
// the same locked transaction as the other guards, so a claim racing a
// concurrent `fleet assign` cannot slip through on stale discovery data
// (layer 2; the FindIdleLightHaulers exclude filter is layer 1).
func (r *ShipRepository) ClaimShip(ctx context.Context, shipSymbol string, containerID string, playerID shared.PlayerID, operation string) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := lockShipRow(tx, shipSymbol, playerID)
		if err != nil {
			return err
		}

		// sp-3tsjz (RULINGS #7, completes sp-gvvph): the command frigate is NEVER a
		// depot hull. Reject a warehouse/stocker claim of the flagship on EVERY
		// path — free, already-claimed, same-container recovery, or dedicated —
		// BEFORE any assignment-state guard below, so an orphaned depot container
		// recovered from the container registry can never re-claim it after a
		// daemon restart (the reported bug: TORWIND-1 kept coming back as a
		// warehouse). sp-gvvph's scaler-reclaim / launch-viability guards all miss
		// the recovery path; this atomic, row-locked point does not. Checked
		// against the locked row via the SAME predicate IsCommandHull uses
		// (IsCommandHullSymbolRole), so the persistence guard can never drift from
		// the domain rule. Placed first, ahead of the transient already-assigned
		// retry, so it is a permanent fail-fast rejection: a depot op claiming the
		// frigate never enters the handoff-race retry loop for a hull it can never
		// hold. Every other operation is unaffected — the frigate stays a
		// legitimate last-resort haul candidate there.
		if domainContract.IsDepotOperation(operation) && domainContract.IsCommandHullSymbolRole(model.ShipSymbol, model.Role) {
			return shared.NewShipIsCommandHullError(shipSymbol, operation)
		}

		// A captain reservation has no container_id (it was never a
		// container claim), so it would otherwise fall through both of the
		// container-comparison guards below and get silently overwritten by
		// the unconditional assign-to-container update. Reject it explicitly,
		// before either guard runs.
		if model.AssignmentStatus == "active" && model.AssignmentOwner == string(navigation.AssignmentOwnerCaptain) {
			return shared.NewShipReservedByCaptainError(shipSymbol, model.AssignmentReason)
		}

		// Check if already assigned to another container
		if model.AssignmentStatus == "active" && model.ContainerID != nil && *model.ContainerID != containerID {
			return shared.NewShipAlreadyAssignedError(shipSymbol, *model.ContainerID)
		}

		// Already assigned to this container - idempotent success. Checked
		// BEFORE the dedication guard on purpose: dedication is ownership of
		// the NEXT acquisition, not eviction of the current holder.
		// A worker re-claiming its own hull mid-job (crash recovery) must keep
		// it even if the captain re-dedicated the ship while the job ran — the
		// new fleet takes over when this claim is released, not by yanking a
		// hull out from under a running operation.
		if model.AssignmentStatus == "active" && model.ContainerID != nil && *model.ContainerID == containerID {
			return nil
		}

		// The hull is free — a NEW acquisition. A dedicated ship may
		// only be newly claimed by its own fleet's operation. Symmetric to the
		// captain-reservation guard above, and atomic with the assignment
		// write below: the discovery-time exclude filter alone has a TOCTOU
		// window between a coordinator's read and this write.
		if model.DedicatedFleet != "" && model.DedicatedFleet != operation {
			return shared.NewShipDedicatedToOtherFleetError(shipSymbol, model.DedicatedFleet, operation)
		}

		// Assign ship to container. version advanced for the same anti-clobber
		// reason as ReleaseContainerClaim: an ownership write invisible to the
		// version guard lets any entity loaded before the claim win its next CAS
		// and rewrite these columns from a pre-claim snapshot, with no conflict
		// logged. Every ownership writer advances the version, so a stale snapshot
		// is always detected.
		now := r.clock.Now()
		err = tx.Model(&model).Updates(map[string]interface{}{
			"container_id":      containerID,
			"assignment_status": "active",
			"assigned_at":       now,
			"released_at":       nil,
			"release_reason":    "",
			"assignment_owner":  string(navigation.AssignmentOwnerContainer),
			"assignment_reason": "",
			"version":           gorm.Expr("version + 1"),
		}).Error

		if err != nil {
			return fmt.Errorf("failed to assign ship: %w", err)
		}

		// Invalidate cache since assignment changed
		r.shipListCache.Delete(playerID.Value())

		return nil
	})
}

// ReserveForCaptain atomically reserves an idle ship for the captain's direct,
// manual use, using the same row-level locking as ClaimShip so a concurrent
// coordinator claim can never be silently overwritten by a captain reservation,
// or vice versa. This is the exact claim-race class this guard exists to
// prevent, applied to the write path: a plain FindBySymbol + Save read-modify-write
// would have a TOCTOU window where a coordinator's ClaimShip could commit between
// the read and the write, and the reservation's Save (a full-row upsert) would
// silently clobber it. Returns ShipAlreadyAssignedError if a container already
// holds the claim.
func (r *ShipRepository) ReserveForCaptain(ctx context.Context, shipSymbol string, reason string, playerID shared.PlayerID) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := lockShipRow(tx, shipSymbol, playerID)
		if err != nil {
			return err
		}

		// Already reserved by the captain - reject rather than silently update the
		// reason. Mirrors Ship.ReserveByCaptain's domain rule: change the reason via
		// release + reserve, so `ship reserve`'s CLI output always means "this just
		// took effect," never a possible no-op.
		if model.AssignmentStatus == "active" && model.AssignmentOwner == string(navigation.AssignmentOwnerCaptain) {
			return fmt.Errorf("ship %s is already reserved by the captain", shipSymbol)
		}

		// Held by a container - reject. The captain must let the coordinator
		// release it first, never silently steal an active claim out from under a
		// running worker.
		if model.AssignmentStatus == "active" && model.ContainerID != nil {
			return shared.NewShipAlreadyAssignedError(shipSymbol, *model.ContainerID)
		}

		// Reserve for the captain. version advanced so the reservation is visible to
		// every entity's version guard, exactly as ClaimShip's claim is.
		now := r.clock.Now()
		err = tx.Model(&model).Updates(map[string]interface{}{
			"container_id":      nil,
			"assignment_status": "active",
			"assigned_at":       now,
			"released_at":       nil,
			"release_reason":    "",
			"assignment_owner":  string(navigation.AssignmentOwnerCaptain),
			"assignment_reason": reason,
			"version":           gorm.Expr("version + 1"),
		}).Error

		if err != nil {
			return fmt.Errorf("failed to reserve ship: %w", err)
		}

		// Invalidate cache since assignment changed
		r.shipListCache.Delete(playerID.Value())

		return nil
	})
}

// ReleaseCaptainReservation atomically clears a captain reservation, returning
// the ship to idle so normal coordinator discovery can claim it again. Uses the
// same row-level locking as ClaimShip/ReserveForCaptain.
// Returns ShipNotReservedError if the ship is not currently reserved by the
// captain — release is specifically for captain reservations, not a generic
// "clear any assignment" escape hatch (that already exists as ReleaseAllActive /
// ForceRelease for the reconciliation path).
func (r *ShipRepository) ReleaseCaptainReservation(ctx context.Context, shipSymbol string, reason string, playerID shared.PlayerID) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := lockShipRow(tx, shipSymbol, playerID)
		if err != nil {
			return err
		}

		if model.AssignmentStatus != "active" || model.AssignmentOwner != string(navigation.AssignmentOwnerCaptain) {
			return shared.NewShipNotReservedError(shipSymbol)
		}

		// version advanced so the release is visible to every entity's version
		// guard, exactly as ReleaseContainerClaim's is.
		now := r.clock.Now()
		err = tx.Model(&model).Updates(map[string]interface{}{
			"assignment_status": "idle",
			"container_id":      nil,
			"released_at":       now,
			"release_reason":    reason,
			"assignment_owner":  string(navigation.AssignmentOwnerContainer),
			"assignment_reason": "",
			"version":           gorm.Expr("version + 1"),
		}).Error

		if err != nil {
			return fmt.Errorf("failed to release captain reservation: %w", err)
		}

		// Invalidate cache since assignment changed
		r.shipListCache.Delete(playerID.Value())

		return nil
	})
}

// PreemptForCaptain atomically REVOKES a coordinator's live container claim and
// transfers ownership of the hull to the captain — the operator-authority
// preempt behind `ship reserve --force`. It is the ONLY path that may
// take a hull out from under a running coordinator, and it does so in a single
// row-locked transaction (RULING #7): the claim swap — clear container_id, set
// assignment_owner=captain — is one atomic write, so a coordinator re-grabbing
// the hull can never race in a lost update. After the swap the coordinator's own
// per-tick FindByContainer derivation no longer returns the hull, so it re-plans
// on its next tick through the same "ship went unavailable mid-plan" path it
// already handles (no crash, task deferred).
//
// Returns the container id the claim was revoked from (for the operator-facing
// "preempted from X" message), or "" when the hull was idle (nothing to preempt —
// a plain reservation). Unlike the non-force ReserveForCaptain, a live container
// claim is transferred rather than rejected; every other guard is identical, so
// `--force` is a strict superset of reserve, never a divergent code path. A hull
// the captain already holds is rejected exactly as ReserveForCaptain rejects it
// (the reason-change contract: release + reserve to change a reason).
func (r *ShipRepository) PreemptForCaptain(ctx context.Context, shipSymbol string, reason string, playerID shared.PlayerID) (string, error) {
	if r.db == nil {
		return "", fmt.Errorf("database not configured")
	}

	var preemptedFrom string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := lockShipRow(tx, shipSymbol, playerID)
		if err != nil {
			return err
		}

		// Already the captain's — nothing to preempt. Reject exactly like
		// ReserveForCaptain so `ship reserve --force`'s output always means "this
		// just took effect," never a possible silent no-op.
		if model.AssignmentStatus == "active" && model.AssignmentOwner == string(navigation.AssignmentOwnerCaptain) {
			return fmt.Errorf("ship %s is already reserved by the captain", shipSymbol)
		}

		// A live coordinator claim — record which container we are revoking it
		// from, then fall through to the transfer. This is the preempt: the
		// operator (captain) authority wins over a coordinator claim.
		if model.AssignmentStatus == "active" && model.ContainerID != nil {
			preemptedFrom = *model.ContainerID
		}

		// Transfer ownership to the captain: clear the container claim in the SAME
		// locked write that stamps captain ownership, so the swap is atomic.
		//
		// version is advanced so a coordinator that loaded this hull BEFORE the
		// preempt (and is mid-operation) loses the optimistic-concurrency CAS race
		// when it writes back through the SaveWithRetry seam: the conflict
		// forces it to RELOAD the fresh (captain-owned) row and re-apply only its
		// nav/cargo mutation, so it cannot resurrect its stale container claim. This
		// closes the clobber for every writer going through SaveWithRetry
		// (sell/purchase, siphon, mfg supply write-back, route-executor legs). The
		// operation methods (Navigate/Dock/Orbit/Refuel/SetFlightMode) cannot
		// re-assert a claim by any route: they persist only the columns they own
		// and never carry ownership at all (persistOwnedColumns). This is the
		// "no lost update" half of RULING #7.
		now := r.clock.Now()
		err = tx.Model(&model).Updates(map[string]interface{}{
			"container_id":      nil,
			"assignment_status": "active",
			"assigned_at":       now,
			"released_at":       nil,
			"release_reason":    "",
			"assignment_owner":  string(navigation.AssignmentOwnerCaptain),
			"assignment_reason": reason,
			"version":           gorm.Expr("version + 1"),
		}).Error

		if err != nil {
			return fmt.Errorf("failed to preempt ship for captain: %w", err)
		}

		// Invalidate cache since assignment changed
		r.shipListCache.Delete(playerID.Value())

		return nil
	})
	if err != nil {
		return "", err
	}
	return preemptedFrom, nil
}

// ReleaseContainerClaim atomically breaks a hull's LIVE coordinator work-claim,
// returning it to idle so the claiming coordinator stops routing it — its own
// per-tick FindByContainer derivation no longer returns the hull. This
// is the extra step `fleet unassign` performs beyond clearing the DedicatedFleet
// tag, closing the documented "unassign says success but the coordinator keeps
// routing it" gap: clearing dedication alone governs only the NEXT acquisition,
// never the current claim. Uses the same row-level locking as ClaimShip.
//
// Scoped to a CONTAINER claim: a captain reservation is left untouched (breaking
// it is `ship release`'s job, not unassign's — unassign must never silently drop
// a reservation), and an already-idle hull is a harmless no-op.
//
// Returns the container id the claim was revoked from, or "" when nothing was
// broken — mirroring PreemptForCaptain. Breaking the claim frees the HULL but
// does nothing to that container, which keeps flying it until reaped (sp-h8mbb),
// so the caller is handed the id it needs to do exactly that. Read inside the
// same row lock as the write, so there is no TOCTOU gap between the id reported
// and the claim actually revoked.
func (r *ShipRepository) ReleaseContainerClaim(ctx context.Context, shipSymbol string, playerID shared.PlayerID, reason string) (string, error) {
	if r.db == nil {
		return "", fmt.Errorf("database not configured")
	}

	var releasedFrom string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := lockShipRow(tx, shipSymbol, playerID)
		if err != nil {
			return err
		}

		// Only a live CONTAINER claim is broken. A captain reservation
		// (owner=captain) is deliberately preserved, and an idle hull is a no-op.
		if model.AssignmentStatus != "active" ||
			model.AssignmentOwner == string(navigation.AssignmentOwnerCaptain) ||
			model.ContainerID == nil {
			return nil
		}

		// Capture the losing container BEFORE the write: Updates writes its map back
		// onto the model, so reading model.ContainerID afterwards would dereference
		// the nil this very update sets. Same order PreemptForCaptain records
		// preemptedFrom in.
		brokenFrom := *model.ContainerID

		// version advanced for the same anti-clobber reason as PreemptForCaptain:
		// a coordinator mid-operation on this hull loses the CAS race on its next
		// SaveWithRetry write-back and reloads the fresh (idle) row, so it cannot
		// re-assert the broken claim. The operation methods cannot re-assert it
		// either — they never carry ownership (RULING #7).
		now := r.clock.Now()
		err = tx.Model(&model).Updates(map[string]interface{}{
			"assignment_status": "idle",
			"container_id":      nil,
			"released_at":       now,
			"release_reason":    reason,
			"version":           gorm.Expr("version + 1"),
		}).Error

		if err != nil {
			return fmt.Errorf("failed to release container claim: %w", err)
		}

		releasedFrom = brokenFrom

		// Invalidate cache since assignment changed
		r.shipListCache.Delete(playerID.Value())

		return nil
	})
	if err != nil {
		return "", err
	}
	return releasedFrom, nil
}

// AssignFleet atomically sets the ship's DedicatedFleet tag — the single
// write path for fleet dedication. fleet == "" clears it. Uses the
// same row-level locking as ClaimShip so an assignment can never interleave
// with a concurrent claim's read-check-write. Deliberately does NOT reject a
// claimed or captain-reserved hull: dedication is permanent ownership ("who
// may claim this next"), orthogonal to current occupancy — the tag takes
// effect when the present claim is released, it does not evict the holder.
// Idempotent: writing the already-persisted value performs zero DB writes,
// keeping every-restart reconciliation cheap.
func (r *ShipRepository) AssignFleet(ctx context.Context, shipSymbol string, fleet string, playerID shared.PlayerID) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := lockShipRow(tx, shipSymbol, playerID)
		if err != nil {
			return err
		}

		// Already tagged with this fleet — idempotent success, zero writes.
		if model.DedicatedFleet == fleet {
			return nil
		}

		if err := tx.Model(&model).Update("dedicated_fleet", fleet).Error; err != nil {
			return fmt.Errorf("failed to assign fleet: %w", err)
		}

		// Invalidate the ship-list cache: a freshly-dedicated ship must not
		// linger in another coordinator's discovery for a stale-cache window.
		r.shipListCache.Delete(playerID.Value())

		return nil
	})
}

// SetCargoReservation atomically sets or releases a single cargo do-not-sell
// override on a hull — the single write path behind the
// `ship reserve-cargo`/`unreserve-cargo` verbs. reserved=true force-protects the
// good; reserved=false force-allows its sale, releasing the default MODULE_/MOUNT_
// reservation for a deliberate resale. Uses the same row-level SELECT FOR UPDATE
// as AssignFleet so a reservation edit can never interleave with a concurrent ship
// write and lose the other's update, and is idempotent (writing the already-
// persisted decision performs zero DB writes). A previously-corrupt override
// column is repaired to a fresh set carrying just this decision.
func (r *ShipRepository) SetCargoReservation(ctx context.Context, shipSymbol, good string, reserved bool, playerID shared.PlayerID) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := lockShipRow(tx, shipSymbol, playerID)
		if err != nil {
			return err
		}

		overrides, corrupt := parseReservationOverrides(model.ReservationOverrides)
		if overrides == nil {
			overrides = map[string]bool{}
		}
		// Idempotent: the decision is already persisted and the column is readable.
		if existing, ok := overrides[good]; ok && existing == reserved && !corrupt {
			return nil
		}
		overrides[good] = reserved

		encoded, err := json.Marshal(overrides)
		if err != nil {
			return fmt.Errorf("failed to encode reservation overrides: %w", err)
		}
		if err := tx.Model(&model).Update("reservation_overrides", string(encoded)).Error; err != nil {
			return fmt.Errorf("failed to set cargo reservation: %w", err)
		}

		r.shipListCache.Delete(playerID.Value())
		return nil
	})
}

// SetShipRetiring atomically marks or clears a hull's retirement — the single write path
// behind `ship retire`. Like AssignFleet it deliberately does NOT reject a claimed or
// captain-reserved hull: the mark governs which job the hull is given NEXT, it does not
// evict the holder of the current one, so a hull marked mid-tour finishes that tour and
// sells its load. Same row-level lock and idempotence as AssignFleet.
func (r *ShipRepository) SetShipRetiring(ctx context.Context, shipSymbol string, retiring bool, playerID shared.PlayerID) error {
	if r.db == nil {
		return fmt.Errorf("database not configured")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := lockShipRow(tx, shipSymbol, playerID)
		if err != nil {
			return err
		}

		if (model.RetiringAt != nil) == retiring {
			return nil
		}

		var mark *time.Time
		if retiring {
			now := time.Now().UTC()
			mark = &now
		}
		if err := tx.Model(&model).Update("retiring_at", mark).Error; err != nil {
			return fmt.Errorf("failed to set ship retirement: %w", err)
		}

		r.shipListCache.Delete(playerID.Value())
		return nil
	})
}

// lockShipRow takes the row's SELECT FOR UPDATE inside an open transaction so a claim
// swap cannot interleave with a concurrent coordinator ClaimShip (RULING #7).
func lockShipRow(tx *gorm.DB, shipSymbol string, playerID shared.PlayerID) (persistence.ShipModel, error) {
	var model persistence.ShipModel
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("ship_symbol = ? AND player_id = ?", shipSymbol, playerID.Value()).
		First(&model).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model, fmt.Errorf("ship %s not found for player %d", shipSymbol, playerID.Value())
	}
	if err != nil {
		return model, fmt.Errorf("failed to lock ship: %w", err)
	}
	return model, nil
}

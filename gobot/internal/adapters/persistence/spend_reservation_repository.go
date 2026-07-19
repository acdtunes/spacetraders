package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// defaultSpendReservationStaleWindow bounds how long a factory-input spend reservation
// may live before the sweep treats it as a dead-container leak and reclaims it. A
// reservation is created immediately before a single input-buy dispatch and deleted the
// moment that buy returns (success or failure), so a healthy reservation lives only the
// few seconds of one PurchaseCargoCommand (plus its bounded empty-tranche retries). A row
// older than this window means the owning factory died mid-buy without releasing — so the
// window is set comfortably above the worst-case single buy yet short enough that a dead
// container's hold is reclaimed well within a factory's buy cadence, never wedging the
// shared budget. Mirrors the container heartbeat staleness idiom (FindStaleManufacturingWorkers).
const defaultSpendReservationStaleWindow = 5 * time.Minute

// spendAdvisoryNamespace is the fixed first key of the Postgres transaction-scoped
// advisory lock that serializes concurrent spend reservations per player. It tags this
// app's advisory-lock usage so a future second use picks a different namespace and cannot
// collide. Value is the ASCII of "SPND" (fits int4, the advisory-lock key type).
const spendAdvisoryNamespace = 0x53504e44 // "SPND"

// errSpendReservationBreach is a sentinel returned inside the reservation transaction to
// force a rollback of the just-inserted row when the combined in-flight spend would breach
// the reserve. It never escapes Reserve — it is translated to (ok=false, err=nil), keeping
// a legitimate cap breach distinct from a real database error.
var errSpendReservationBreach = errors.New("spend reservation would breach working-capital reserve")

// SpendReservationLedgerGORM is the DB-backed cross-container concurrent factory-input
// spend cap. All factory containers share one database, so a reservation ledger
// there is the only place a HARD cap can live: the per-buy floor checks live
// treasury per container, but N containers can each pass that independent check inside the
// check->buy window and collectively dip below the reserve. This ledger closes that race by
// making "record my intent, then verify total in-flight exposure still clears the reserve"
// a single serialized atomic step.
type SpendReservationLedgerGORM struct {
	db          *gorm.DB
	staleWindow time.Duration
}

// NewSpendReservationLedger creates a GORM-backed spend reservation ledger with the
// default staleness window.
func NewSpendReservationLedger(db *gorm.DB) *SpendReservationLedgerGORM {
	return &SpendReservationLedgerGORM{db: db, staleWindow: defaultSpendReservationStaleWindow}
}

// Reserve atomically records a spend intent of projectedCost for containerID and reports
// whether the reserve still holds: liveCredits − SUM(all active reservations for this
// player, INCLUDING the one just inserted) ≥ reserveFloor.
//
// liveCredits is read by the caller (a live GetAgent) BEFORE this call — the transaction
// here never makes an API call, so the DB is never held open across the network.
//
// On ok==true the returned reservationID identifies the row the caller must Release once
// the buy completes. On ok==false the reservation is rolled back (not persisted) and the
// caller must park the buy. The insert-then-sum critical section is serialized per player
// (a Postgres advisory lock; SQLite serializes writers globally) so no interleaving of two
// concurrent factory buys can let both pass a check they would jointly fail.
func (r *SpendReservationLedgerGORM) Reserve(
	ctx context.Context,
	playerID int,
	containerID string,
	projectedCost int,
	liveCredits int,
	reserveFloor int,
) (reservationID string, ok bool, err error) {
	reservationID = uuid.NewString()

	txErr := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize concurrent reservation checks for this player so the insert-then-sum
		// below is atomic across factory containers. The advisory lock is transaction-scoped
		// — auto-released on commit or rollback, so a crashing container cannot hold it. On
		// SQLite there is no analogue and none is needed: it serializes all writers globally.
		if tx.Dialector.Name() == "postgres" {
			if e := tx.Exec("SELECT pg_advisory_xact_lock(?, ?)", spendAdvisoryNamespace, playerID).Error; e != nil {
				return fmt.Errorf("acquire spend advisory lock: %w", e)
			}
		}

		// Reclaim dead-container leaks first so a crashed factory's un-released reservation
		// never inflates the sum and wedges the budget for the living.
		if e := r.expireStaleTx(tx, r.staleWindow); e != nil {
			return e
		}

		row := &SpendReservationModel{
			ID:            reservationID,
			PlayerID:      playerID,
			ContainerID:   containerID,
			ProjectedCost: projectedCost,
			CreatedAt:     time.Now(),
		}
		if e := tx.Create(row).Error; e != nil {
			return fmt.Errorf("insert spend reservation: %w", e)
		}

		var totalReserved int64
		if e := tx.Model(&SpendReservationModel{}).
			Where("player_id = ?", playerID).
			Select("COALESCE(SUM(projected_cost), 0)").
			Scan(&totalReserved).Error; e != nil {
			return fmt.Errorf("sum spend reservations: %w", e)
		}

		if liveCredits-int(totalReserved) < reserveFloor {
			// Combined in-flight spend would breach: roll back this reservation.
			return errSpendReservationBreach
		}

		ok = true
		return nil
	})

	if errors.Is(txErr, errSpendReservationBreach) {
		return "", false, nil
	}
	if txErr != nil {
		return "", false, txErr
	}
	return reservationID, true, nil
}

// Release consumes a reservation once its buy completes (success or failure). Deleting a
// missing row is not an error — a staleness sweep may already have reclaimed it.
func (r *SpendReservationLedgerGORM) Release(ctx context.Context, reservationID string) error {
	if reservationID == "" {
		return nil
	}
	if err := r.db.WithContext(ctx).
		Where("id = ?", reservationID).
		Delete(&SpendReservationModel{}).Error; err != nil {
		return fmt.Errorf("release spend reservation %s: %w", reservationID, err)
	}
	return nil
}

// ExpireStale deletes reservations older than maxAge and returns how many were reclaimed.
// Exposed for a dedicated staleness test and any external sweeper; Reserve also runs this
// (with the ledger's configured window) inside its own transaction on every call, so the
// ledger is self-cleaning without a background job.
func (r *SpendReservationLedgerGORM) ExpireStale(ctx context.Context, maxAge time.Duration) (int, error) {
	tx := r.db.WithContext(ctx).
		Where("created_at < ?", time.Now().Add(-maxAge)).
		Delete(&SpendReservationModel{})
	if tx.Error != nil {
		return 0, fmt.Errorf("expire stale spend reservations: %w", tx.Error)
	}
	return int(tx.RowsAffected), nil
}

// expireStaleTx deletes stale reservations within an existing transaction (used by Reserve).
func (r *SpendReservationLedgerGORM) expireStaleTx(tx *gorm.DB, maxAge time.Duration) error {
	if err := tx.Where("created_at < ?", time.Now().Add(-maxAge)).
		Delete(&SpendReservationModel{}).Error; err != nil {
		return fmt.Errorf("sweep stale spend reservations: %w", err)
	}
	return nil
}

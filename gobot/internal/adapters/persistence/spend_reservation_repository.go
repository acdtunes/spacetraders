package persistence

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
// shared budget.
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

// SpendReservationLedgerGORM is the DB-backed CROSS-OPERATION concurrent spend cap. Every
// spender shares one database, so a reservation ledger there is the only place a HARD cap can
// live: a per-buy floor checks live treasury per caller, but N callers can each pass that
// independent check inside the check->buy window and collectively dip below the reserve. This
// ledger closes that race by making "record my intent, then verify total in-flight exposure
// still clears the reserve" a single serialized atomic step.
//
// It is deliberately NOT scoped to a container. Reservations sum per PLAYER and the advisory
// lock is keyed per PLAYER, so buys from one container serialise against each other exactly as
// buys from N containers do, and a construction buy serialises against a contract buy. That
// matters because the operations it caps do not partition: construction_supply, contract and
// tour all draw on ONE treasury. containerID is best-effort ATTRIBUTION for logs and nothing
// more (sp-ps2oc, whose three racing buys all came from a single construction container).
type SpendReservationLedgerGORM struct {
	db          *gorm.DB
	staleWindow time.Duration

	// playerLocks serialises the balance-read + reserve pair per player WITHIN this process,
	// and is held by Release too. See Reserve for why the advisory lock alone is not enough.
	playerLocks sync.Map // playerID int -> *sync.Mutex
}

// NewSpendReservationLedger creates a GORM-backed spend reservation ledger with the
// default staleness window.
func NewSpendReservationLedger(db *gorm.DB) *SpendReservationLedgerGORM {
	return &SpendReservationLedgerGORM{db: db, staleWindow: defaultSpendReservationStaleWindow}
}

// lockPlayer serialises this player's balance-read + reserve critical section against every
// other spender in this process, and returns the unlock func.
func (r *SpendReservationLedgerGORM) lockPlayer(playerID int) func() {
	actual, _ := r.playerLocks.LoadOrStore(playerID, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Reserve atomically records a spend intent of projectedCost for containerID and reports
// whether the reserve still holds: the balance − SUM(all active reservations for this player,
// INCLUDING the one just inserted) ≥ reserveFloor.
//
// THE BALANCE IS READ HERE, THROUGH readCredits, NOT PASSED IN. That is the sp-ps2oc fix and
// the reason this takes a callback rather than an int.
//
// A pre-read balance is unsound however well the insert-then-sum is serialized, because the
// two halves of the check can describe different instants. A sibling that commits its buy AND
// releases its reservation between a caller's read and its Reserve is in NEITHER half: not in
// the caller's stale snapshot (taken before the commit) and not in the SUM (its row is gone).
// The headroom is then counted twice and both buys proceed. Measured on the real ledger before
// the fix, three concurrent buys of 99,000 against a 412,000 treasury and a 150,000 floor
// breached in roughly one run in six — a money guard that holds only probabilistically, which
// RULINGS #4 does not permit.
//
// Reading under the per-player process lock closes it: while the lock is held no sibling in
// this process can release (Release takes the same lock), so every sibling spend is either
// still reserved — and therefore in the SUM — or already committed and therefore in the fresh
// balance. Nothing falls between the two.
//
// The read happens inside the LOCK but OUTSIDE the transaction, deliberately: the production
// reader is ledger-backed but falls back to a live API call when its newest row is stale, and
// the DB must never be held open across the network.
//
// RESIDUAL, stated exactly: the process lock does not span processes. Two daemons sharing one
// database could still interleave a read against a sibling's release. Today there is exactly
// one daemon — every coordinator runs as a container inside it — so this is a note for whoever
// changes that, not a live hole. The advisory lock below is what makes the insert-then-sum
// itself atomic across processes.
//
// On ok==true the returned reservationID identifies the row the caller must Release once the
// buy completes. On ok==false the reservation is rolled back (not persisted) and the caller
// must park the buy. A readCredits error is returned as an error, never as a zero balance:
// every caller reads that as PARK (RULINGS #4).
func (r *SpendReservationLedgerGORM) Reserve(
	ctx context.Context,
	playerID int,
	containerID string,
	projectedCost int,
	readBudget func(context.Context) (int64, int, error),
) (reservationID string, ok bool, err error) {
	if readBudget == nil {
		return "", false, fmt.Errorf("spend reservation: no budget source given for player %d", playerID)
	}

	unlock := r.lockPlayer(playerID)
	defer unlock()

	credits, reserveFloor, err := readBudget(ctx)
	if err != nil {
		return "", false, fmt.Errorf("read budget for spend reservation: %w", err)
	}
	liveCredits := int(credits)

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
//
// It takes the same per-player lock Reserve holds. That is not bookkeeping hygiene, it is half
// the correctness argument: a release that landed while a sibling was between its balance read
// and its insert would erase this buy from the SUM without it yet appearing in that sibling's
// balance, which is exactly the double-counted headroom described on Reserve. The lock is held
// only for the delete — never across the buy itself, which happens between Reserve and here.
func (r *SpendReservationLedgerGORM) Release(ctx context.Context, playerID int, reservationID string) error {
	if reservationID == "" {
		return nil
	}
	unlock := r.lockPlayer(playerID)
	defer unlock()
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

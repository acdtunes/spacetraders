package api

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The core preempt (sp-w3yd): `ship reserve --force` must atomically REVOKE a
// coordinator's live container claim and transfer ownership to the captain — in
// a single row-locked swap (RULING #7), so the coordinator's next per-tick
// FindByContainer derivation sees the hull gone and re-plans. The previous
// container id is returned so the operator can be told what was preempted.
func TestPreemptForCaptain_TransfersContainerClaimToCaptain(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-8",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
	}).Error)
	seedContainerParent(t, db, "goods_factory-FAB_MATS-a6984433", playerID.Value())

	// A coordinator holds the live work-claim.
	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-8", "goods_factory-FAB_MATS-a6984433", playerID, ""))

	prev, err := repo.PreemptForCaptain(context.Background(), "TORWIND-8", "reclaim stranded FAB_MATS", playerID)
	require.NoError(t, err, "operator preempt must succeed against a coordinator-claimed hull")
	require.Equal(t, "goods_factory-FAB_MATS-a6984433", prev, "preempt must report the container it revoked the claim from")

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&model).Error)
	require.Equal(t, "active", model.AssignmentStatus, "the hull stays actively owned, now by the captain")
	require.Equal(t, string(navigation.AssignmentOwnerCaptain), model.AssignmentOwner, "ownership transfers to the captain")
	require.Nil(t, model.ContainerID, "the coordinator's container claim is cleared")
	require.Equal(t, "reclaim stranded FAB_MATS", model.AssignmentReason)

	// A cleared container_id IS the mechanism by which the coordinator drops the
	// hull: FindByContainer filters on ship.container_id == <coordinator>, so a
	// nil container_id excludes the hull from the coordinator's next per-tick
	// working set — the existing "ship went unavailable mid-plan" re-plan path
	// (no crash, task deferred).
}

// Byte-identical regression (sp-w3yd): WITHOUT --force, a reserve against a
// coordinator-claimed hull must still reject exactly as before — same typed
// ShipAlreadyAssignedError, and the row is left completely untouched. --force is
// the ONLY new bypass; the normal ownership guard is never weakened.
func TestReserveForCaptain_WithoutForceStillRejectsContainerClaim(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-8",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
	}).Error)
	seedContainerParent(t, db, "goods_factory-FAB_MATS-a6984433", playerID.Value())
	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-8", "goods_factory-FAB_MATS-a6984433", playerID, ""))

	err := repo.ReserveForCaptain(context.Background(), "TORWIND-8", "no-force reserve", playerID)
	require.Error(t, err, "a plain reserve must never silently steal a live container claim")
	var alreadyAssigned *shared.ShipAlreadyAssignedError
	require.ErrorAs(t, err, &alreadyAssigned, "the non-force path rejects with the exact same typed error as today")

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&model).Error)
	require.Equal(t, "active", model.AssignmentStatus)
	require.Equal(t, string(navigation.AssignmentOwnerContainer), model.AssignmentOwner, "a rejected reserve must not mutate ownership")
	require.NotNil(t, model.ContainerID)
	require.Equal(t, "goods_factory-FAB_MATS-a6984433", *model.ContainerID, "the coordinator keeps its claim")
}

// Preempt on an IDLE hull is just a normal reservation — nothing to revoke, so
// the reported previous-container is empty. This keeps `--force` a superset of
// plain reserve, never a different code path for the common case.
func TestPreemptForCaptain_IdleHullReservesWithNoPreemption(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-8",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
	}).Error)

	prev, err := repo.PreemptForCaptain(context.Background(), "TORWIND-8", "errand", playerID)
	require.NoError(t, err)
	require.Equal(t, "", prev, "an idle hull had no container claim to preempt")

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&model).Error)
	require.Equal(t, "active", model.AssignmentStatus)
	require.Equal(t, string(navigation.AssignmentOwnerCaptain), model.AssignmentOwner)
	require.Nil(t, model.ContainerID)
}

// Preempt is idempotent-safe on a hull the captain already holds: there is
// nothing to preempt, so it rejects the same way a plain re-reserve does (the
// reason-change contract — change a reason via release + reserve), never
// silently no-oping or double-owning.
func TestPreemptForCaptain_AlreadyCaptainReservedRejects(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-8",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
	}).Error)
	require.NoError(t, repo.ReserveForCaptain(context.Background(), "TORWIND-8", "first", playerID))

	_, err := repo.PreemptForCaptain(context.Background(), "TORWIND-8", "second", playerID)
	require.Error(t, err, "preempting a hull the captain already owns is rejected, not a silent overwrite")
}

// RULING #7 (no lost update): the preempt must advance the optimistic-
// concurrency version token so a coordinator that loaded this hull BEFORE the
// preempt and is mid-operation loses its next SaveWithRetry CAS race and is
// forced to reload the fresh (captain-owned) row — it can never resurrect its
// stale container claim and clobber the reservation. This is the exact
// mechanism (sp-01wc/sp-wa7c) that keeps the factory's in-flight "drag the hull
// around" save from undoing the preempt.
func TestPreemptForCaptain_AdvancesVersionSoConcurrentSaveCannotClobber(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-8",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
	}).Error)
	seedContainerParent(t, db, "factory-1", playerID.Value())
	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-8", "factory-1", playerID, ""))

	var beforePreempt persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&beforePreempt).Error)

	_, err := repo.PreemptForCaptain(context.Background(), "TORWIND-8", "preempt", playerID)
	require.NoError(t, err)

	var afterPreempt persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&afterPreempt).Error)
	require.Greater(t, afterPreempt.Version, beforePreempt.Version,
		"preempt must advance the CAS version so a racing coordinator SaveWithRetry conflicts and reloads instead of clobbering")
}

// RULING #2 (restart resilience): a preempted-then-reserved hull is a captain
// reservation, which by design survives a daemon restart. A fresh repository
// reading the same DB (the rehydrate-on-boot path) must still see the captain
// as owner — the reassignment persists.
func TestPreemptForCaptain_SurvivesReload(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-8",
		PlayerID:         playerID.Value(),
		AssignmentStatus: "idle",
	}).Error)
	seedContainerParent(t, db, "goods_factory-FAB_MATS-a6984433", playerID.Value())
	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-8", "goods_factory-FAB_MATS-a6984433", playerID, ""))

	_, err := repo.PreemptForCaptain(context.Background(), "TORWIND-8", "held for errand", playerID)
	require.NoError(t, err)

	// The daemon rehydrates assignment ownership from the persisted ships row on
	// boot (modelToDomain maps assignment_owner/assignment_status/container_id).
	// Re-reading that row is the reload's source of truth: a preempted-then-
	// reserved hull must still be captain-owned after a restart (RULING #2).
	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&model).Error)
	require.Equal(t, "active", model.AssignmentStatus, "the reservation persists as an active assignment across reload")
	require.Equal(t, string(navigation.AssignmentOwnerCaptain), model.AssignmentOwner, "the captain stays the owner after a reload (RULING #2)")
	require.Nil(t, model.ContainerID, "the revoked container claim does not resurrect on reload")
}

// RULING #7 (atomic claim transfer, both orderings): a preempt racing a
// coordinator re-claim must leave the operator (captain) as the deterministic
// winner, with no lost update — regardless of which committed first.
func TestPreemptForCaptain_OperatorWinsInBothOrderings(t *testing.T) {
	// Ordering A: the coordinator (re-)claims first, then the operator preempts.
	// The operator's preempt transfers the just-made claim away — captain wins.
	t.Run("claim then preempt", func(t *testing.T) {
		repo, db, playerID := newDedicationTestRepo(t)
		require.NoError(t, db.Create(&persistence.ShipModel{
			ShipSymbol: "TORWIND-8", PlayerID: playerID.Value(), AssignmentStatus: "idle",
		}).Error)
		seedContainerParent(t, db, "factory-1", playerID.Value())

		require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-8", "factory-1", playerID, ""))
		prev, err := repo.PreemptForCaptain(context.Background(), "TORWIND-8", "preempt", playerID)
		require.NoError(t, err)
		require.Equal(t, "factory-1", prev)

		var model persistence.ShipModel
		require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&model).Error)
		require.Equal(t, string(navigation.AssignmentOwnerCaptain), model.AssignmentOwner)
		require.Nil(t, model.ContainerID)
	})

	// Ordering B: the operator preempts first, then the coordinator tries to
	// re-grab. The atomic ClaimShip captain-reservation guard rejects it — the
	// operator's ownership is not lost to a poach-race.
	t.Run("preempt then reclaim", func(t *testing.T) {
		repo, db, playerID := newDedicationTestRepo(t)
		require.NoError(t, db.Create(&persistence.ShipModel{
			ShipSymbol: "TORWIND-8", PlayerID: playerID.Value(), AssignmentStatus: "idle",
		}).Error)
		seedContainerParent(t, db, "factory-1", playerID.Value())
		require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-8", "factory-1", playerID, ""))

		_, err := repo.PreemptForCaptain(context.Background(), "TORWIND-8", "preempt", playerID)
		require.NoError(t, err)

		err = repo.ClaimShip(context.Background(), "TORWIND-8", "factory-1", playerID, "")
		require.Error(t, err, "a coordinator re-claim after preempt must be rejected — the operator keeps the hull")
		var reserved *shared.ShipReservedByCaptainError
		require.ErrorAs(t, err, &reserved)

		var model persistence.ShipModel
		require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&model).Error)
		require.Equal(t, string(navigation.AssignmentOwnerCaptain), model.AssignmentOwner)
		require.Nil(t, model.ContainerID)
	})
}

// RULING #7 under -race: preempt and a coordinator re-claim launched
// concurrently must never corrupt the row (no lost update / torn write) and the
// operator must end up owning the hull. The single-writer daemon serializes the
// two writes; this pins that there is no Go data race and exactly one final
// owner.
func TestPreemptForCaptain_ConcurrentReclaimHasSingleOwner(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-8", PlayerID: playerID.Value(), AssignmentStatus: "idle",
	}).Error)
	seedContainerParent(t, db, "factory-1", playerID.Value())
	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-8", "factory-1", playerID, ""))

	var wg sync.WaitGroup
	wg.Add(2)
	// Operator preempt — always succeeds against a container/idle hull.
	go func() {
		defer wg.Done()
		_, _ = repo.PreemptForCaptain(context.Background(), "TORWIND-8", "preempt", playerID)
	}()
	// Coordinator re-claim — may win or lose the race; either way it must not
	// corrupt the row.
	go func() {
		defer wg.Done()
		_ = repo.ClaimShip(context.Background(), "TORWIND-8", "factory-1", playerID, "")
	}()
	wg.Wait()

	// The operator preempt is the authority write; whichever order the two ran,
	// the captain ends up owning the hull with the container claim cleared.
	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&model).Error)
	require.Equal(t, string(navigation.AssignmentOwnerCaptain), model.AssignmentOwner, "the operator preempt wins deterministically")
	require.Nil(t, model.ContainerID, "no lost update: the container claim is cleared, not left dangling")
}

// `fleet unassign` work-claim break (sp-w3yd): breaking the live claim must
// return a coordinator-claimed hull to idle so the coordinator stops routing it
// (its per-tick FindByContainer no longer returns it) — closing the "unassign
// says success but the coordinator keeps routing it" gap.
func TestReleaseContainerClaim_BreaksLiveContainerClaim(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-8", PlayerID: playerID.Value(), AssignmentStatus: "idle",
	}).Error)
	seedContainerParent(t, db, "factory-1", playerID.Value())
	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-8", "factory-1", playerID, ""))

	var beforeBreak persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&beforeBreak).Error)

	released, err := repo.ReleaseContainerClaim(context.Background(), "TORWIND-8", playerID, "fleet unassign")
	require.NoError(t, err)
	require.True(t, released, "breaking a live container claim reports that it acted")

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&model).Error)
	require.Equal(t, "idle", model.AssignmentStatus, "the hull returns to idle")
	require.Nil(t, model.ContainerID, "the coordinator's claim is gone — its per-tick FindByContainer no longer returns the hull")
	require.Greater(t, model.Version, beforeBreak.Version,
		"breaking the claim advances the CAS version so a racing coordinator save reloads instead of re-asserting the claim")
}

// A captain reservation is NOT a coordinator work-claim: `fleet unassign` must
// never clobber it (that is what `ship release` is for). Breaking the work-claim
// leaves a reserved hull untouched and reports it did nothing.
func TestReleaseContainerClaim_LeavesCaptainReservationUntouched(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-8", PlayerID: playerID.Value(), AssignmentStatus: "idle",
	}).Error)
	require.NoError(t, repo.ReserveForCaptain(context.Background(), "TORWIND-8", "captain errand", playerID))

	released, err := repo.ReleaseContainerClaim(context.Background(), "TORWIND-8", playerID, "fleet unassign")
	require.NoError(t, err)
	require.False(t, released, "unassign must not break a captain reservation")

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&model).Error)
	require.Equal(t, "active", model.AssignmentStatus)
	require.Equal(t, string(navigation.AssignmentOwnerCaptain), model.AssignmentOwner, "the captain reservation is preserved")
}

// Breaking the work-claim on an already-idle hull is a harmless no-op.
func TestReleaseContainerClaim_IdleHullNoOp(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "TORWIND-8", PlayerID: playerID.Value(), AssignmentStatus: "idle",
	}).Error)

	released, err := repo.ReleaseContainerClaim(context.Background(), "TORWIND-8", playerID, "fleet unassign")
	require.NoError(t, err)
	require.False(t, released)
}

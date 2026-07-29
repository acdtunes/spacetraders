package grpc

// orphaned_container_reap_test.go — sp-h8mbb daemon-side cover for ReapOrphanedContainer.
//
// The measured failure: a `fleet unassign` broke the live work-claim on TORWIND-61 and TORWIND-D7.
// The hulls were freed correctly and the coordinator relaunched them into NEW containers — but the
// OLD containers, tour-run-TORWIND-61-f0c2c82f and tour-run-TORWIND-D7-f8033506, were untouched by
// the claim write and kept running, still logging jumps and trades on hulls they no longer owned,
// for 4.0h and 2.9h. They were cleared only by the next daemon restart's recovery sweep, which
// tried to re-adopt them and failed "ship X is already assigned to container Y".
//
// These tests pin both halves of the reap: stopping a LIVE runner, and terminalizing a row whose
// runner is already gone so recovery can never re-adopt it.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// reapExitReason reads the row's persisted exit reason — the operator-facing record of WHY a
// container stopped.
func reapExitReason(t *testing.T, s *DaemonServer, id string) string {
	t.Helper()
	var model persistence.ContainerModel
	require.NoError(t, s.db.First(&model, "id = ?", id).Error)
	return model.ExitReason
}

// THE REGRESSION, live half. A container still running in this daemon whose hull was just taken
// away must be STOPPED, not left flying a hull it no longer owns.
func TestReapOrphanedContainer_StopsTheLiveRunner(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	const id = "tour-run-TORWIND-61-f0c2c82f"
	insertRunningContainer(t, s.db, id, "tour_run", "tour_run", `{"ship_symbol":"TORWIND-61"}`, playerID, nil)

	clock := &recordingClock{current: time.Date(2026, 7, 29, 3, 26, 0, 0, time.UTC)}
	entity := container.NewContainer(id, container.ContainerType("tour_run"), playerID, -1, nil, nil, clock)
	require.NoError(t, entity.Start())

	// A tour parked mid-circuit inside its iteration, exactly as f0c2c82f was: it exits only when
	// its context is cancelled — which is what the reap must do.
	med := &ctxEnteredBlockingMediator{entered: make(chan struct{})}
	r := NewContainerRunner(entity, med, nil, noopLogRepo{}, s.containerRepo, nil, clock)

	done := make(chan struct{})
	go func() { r.execute(); close(done) }()

	select {
	case <-med.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("runner never entered its iteration")
	}

	s.containersMu.Lock()
	s.containers[id] = r
	s.containersMu.Unlock()

	s.ReapOrphanedContainer(context.Background(), id, shared.MustNewPlayerID(playerID),
		"fleet unassign of TORWIND-61")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reap did not stop the orphaned runner — it is still flying a hull it no longer owns")
	}

	require.Equal(t, "STOPPED", persistedStatus(t, s, id),
		"a reaped container must terminalize; left RUNNING it corrupts utilisation and survives to the next restart's recovery sweep")
}

// THE REGRESSION, dormant half. A row whose runner is already gone must still be terminalized:
// RUNNING and INTERRUPTED are exactly the states boot recovery re-adopts, so leaving one is what
// lets a dead container come back and fight for a hull it lost.
func TestReapOrphanedContainer_TerminalizesResumableRowWithNoLiveRunner(t *testing.T) {
	for _, status := range []string{"RUNNING", "INTERRUPTED", "PENDING"} {
		t.Run(status, func(t *testing.T) {
			s, _, playerID := newRecoveryTestServer(t)
			const id = "tour-run-TORWIND-D7-f8033506"
			insertRunningContainer(t, s.db, id, "tour_run", "tour_run", `{"ship_symbol":"TORWIND-D7"}`, playerID, nil)
			require.NoError(t, s.db.Model(&persistence.ContainerModel{}).Where("id = ?", id).
				Update("status", status).Error)

			s.ReapOrphanedContainer(context.Background(), id, shared.MustNewPlayerID(playerID),
				"fleet unassign of TORWIND-D7")

			require.Equal(t, "STOPPED", persistedStatus(t, s, id),
				"a %s row that lost its hull must be terminalized — %s is inside the set boot recovery re-adopts", status, status)
			require.Contains(t, reapExitReason(t, s, id), "work_claim_broken",
				"the exit reason must name WHY the container was reaped, or an operator reads it as an unexplained loss")
		})
	}
}

// The consequence that makes this a correctness bug and not just a tidy-up: a reaped orphan is no
// longer in the recovery set, so the next boot does not try to re-adopt it and fail
// "ship already assigned to container ...". The un-reaped control proves the sweep really would
// have picked it up.
func TestReapOrphanedContainer_ReapedOrphanIsNotReadoptedByRecovery(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	const reaped = "tour-run-TORWIND-61-f0c2c82f"
	const control = "tour-run-TORWIND-D7-f8033506"
	insertRunningContainer(t, s.db, reaped, "tour_run", "tour_run", `{"ship_symbol":"TORWIND-61"}`, playerID, nil)
	insertRunningContainer(t, s.db, control, "tour_run", "tour_run", `{"ship_symbol":"TORWIND-D7"}`, playerID, nil)

	s.ReapOrphanedContainer(context.Background(), reaped, shared.MustNewPlayerID(playerID),
		"fleet unassign of TORWIND-61")

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	// The control was left RUNNING, so recovery reached it and failed it — the exact
	// recovery_failed outcome the live incident recorded, hours after the fact.
	require.Equal(t, "FAILED", persistedStatus(t, s, control),
		"precondition: an un-reaped orphan IS picked up by the recovery sweep")
	require.Contains(t, reapExitReason(t, s, control), "recovery_failed",
		"precondition: the sweep is what finally terminalizes an un-reaped orphan")

	// The reaped one was already terminal, so the sweep never touched it.
	require.Equal(t, "STOPPED", persistedStatus(t, s, reaped),
		"a reaped orphan must stay STOPPED — recovery must not resurrect it")
	require.Contains(t, reapExitReason(t, s, reaped), "work_claim_broken",
		"the reap's own reason must survive: recovery must not overwrite it with recovery_failed")
}

// An already-terminal row's exit reason is history. The reap is cleanup, not a rewrite — it must
// never overwrite the record of how a container actually ended.
func TestReapOrphanedContainer_LeavesATerminalRowAlone(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	const id = "tour-run-TORWIND-61-completed"
	insertRunningContainer(t, s.db, id, "tour_run", "tour_run", `{"ship_symbol":"TORWIND-61"}`, playerID, nil)
	require.NoError(t, s.db.Model(&persistence.ContainerModel{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": "COMPLETED", "exit_reason": "tour finished honestly"}).Error)

	s.ReapOrphanedContainer(context.Background(), id, shared.MustNewPlayerID(playerID), "fleet unassign")

	require.Equal(t, "COMPLETED", persistedStatus(t, s, id),
		"a container that already ended must keep the status it ended with")
	require.Equal(t, "tour finished honestly", reapExitReason(t, s, id),
		"the reap must not overwrite how the container actually ended")
}

// "" means the claim break was a no-op (the hull was already idle) — nothing was orphaned. Reaping
// on "" would be a daemon-wide lookup for a container that never existed.
func TestReapOrphanedContainer_EmptyContainerIDIsANoOp(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	const untouched = "tour-run-TORWIND-61-live"
	insertRunningContainer(t, s.db, untouched, "tour_run", "tour_run", `{"ship_symbol":"TORWIND-61"}`, playerID, nil)

	s.ReapOrphanedContainer(context.Background(), "", shared.MustNewPlayerID(playerID), "fleet unassign")

	require.Equal(t, "RUNNING", persistedStatus(t, s, untouched),
		"an empty container id orphans nothing, so no row may change")
}

// A container id that does not exist (already deleted) must be a silent no-op, not a failure: the
// reap runs AFTER the authoritative claim write has committed, so it can never fail the operation
// that called it.
func TestReapOrphanedContainer_UnknownContainerIsSilent(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)

	require.NotPanics(t, func() {
		s.ReapOrphanedContainer(context.Background(), "tour-run-GONE-deadbeef",
			shared.MustNewPlayerID(playerID), "fleet unassign")
	}, "reaping a container that no longer exists must never fail the unassign that triggered it")
}

// staleIndexShipRepo hands the STALE container's hull back from FindByContainer even though the
// hull has already moved on to a new container. That is deliberately adversarial: in production
// the hull's container_id was cleared by the claim break, so FindByContainer returns nothing and
// the question never arises. This fixture removes that first barrier so the SECOND one — the
// CAS closure's re-check on the fresh row — is the only thing standing between a late reap and
// the hull's new owner. Without an empty-handed FindByContainer the guard is never consulted, and
// a test that relied on it would pass while proving nothing.
type staleIndexShipRepo struct {
	navigation.ShipRepository
	hull      *navigation.Ship
	staleID   string // the container FindByContainer pretends still owns the hull
	saveCalls int
}

func (r *staleIndexShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	if containerID == r.staleID {
		return []*navigation.Ship{r.hull}, nil
	}
	return nil, nil
}

func (r *staleIndexShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.hull, nil
}

func (r *staleIndexShipRepo) SaveWithRetry(_ context.Context, _ string, _ shared.PlayerID, mutate navigation.ShipMutation) (*navigation.Ship, bool, error) {
	changed, err := mutate(r.hull) // the FRESH row — already claimed by the NEW container
	if err != nil {
		return r.hull, false, err
	}
	if changed {
		r.saveCalls++
	}
	return r.hull, changed, nil
}

// RULINGS #7. Reaping at reassignment time makes the release/re-claim race TIGHTER than the
// restart sweep's, so the guard that stops a late reap yanking the hull out from under its new
// owner has to be explicit, not incidental.
//
// The exact production shape: TORWIND-61 was flown by tour-run-...-f0c2c82f, the claim was broken,
// and tour-run-...-2821d983 re-claimed the hull. When the orphan is finally stopped, its release
// must be a NO-OP — the hull belongs to 2821d983 now, and releasing it would strand a live tour.
func TestReapedOrphansReleaseNeverTakesTheHullFromItsNewOwner(t *testing.T) {
	const stale = "tour-run-TORWIND-61-f0c2c82f"
	const newOwner = "tour-run-TORWIND-61-2821d983"
	playerID := 5

	hull := newIdleTradeShip(t, "TORWIND-61", playerID)
	require.NoError(t, hull.AssignToContainer(newOwner, shared.NewRealClock()),
		"precondition: the coordinator has already re-claimed the hull into a new container")

	repo := &staleIndexShipRepo{hull: hull, staleID: stale}
	entity := container.NewContainer(stale, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-61"}, nil)
	require.NoError(t, entity.Start())
	runner := NewContainerRunner(entity, nilCleanMediator{}, nil, noopLogRepo{}, nil, repo, nil)

	// The reap stops the orphan, and Stop()'s last act is this release.
	runner.releaseShipAssignments("stopped")

	require.True(t, hull.IsAssigned(),
		"the reaped orphan's release must not free a hull that now belongs to a live container")
	require.Equal(t, newOwner, hull.ContainerID(),
		"the hull must still belong to the container that re-claimed it — a late reap that releases it strands a running tour (RULINGS #7)")
	require.Zero(t, repo.saveCalls,
		"the release must not WRITE at all once the fresh row shows another owner")
}

// The same guard must not over-fire: an orphan that genuinely still holds its hull (the claim
// break failed, or the reap raced ahead of any re-claim) must still release it on the way out, or
// the reap would leak the claim it was meant to clean up.
func TestReapedOrphanStillReleasesAHullItGenuinelyHolds(t *testing.T) {
	const stale = "tour-run-TORWIND-61-f0c2c82f"
	playerID := 5

	hull := newIdleTradeShip(t, "TORWIND-61", playerID)
	require.NoError(t, hull.AssignToContainer(stale, shared.NewRealClock()),
		"precondition: the orphan still holds its own claim")

	repo := &staleIndexShipRepo{hull: hull, staleID: stale}
	entity := container.NewContainer(stale, container.ContainerTypeTrading, playerID, 1, nil,
		map[string]interface{}{"ship_symbol": "TORWIND-61"}, nil)
	require.NoError(t, entity.Start())
	runner := NewContainerRunner(entity, nilCleanMediator{}, nil, noopLogRepo{}, nil, repo, nil)

	runner.releaseShipAssignments("stopped")

	require.False(t, hull.IsAssigned(),
		"a hull the orphan really does own must still be released — otherwise the reap leaks the very claim it exists to clean up")
	require.Equal(t, 1, repo.saveCalls, "the genuine release must be persisted")
}

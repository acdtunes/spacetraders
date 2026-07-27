package grpc

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// syncRecorder is a race-safe captain.EventRecorder for the recovery-loss tests.
// A recovered coordinator starts a background runner goroutine, so the shared
// fakeRecorder's unguarded slice append would race the test's reads under
// `go test -race`. Record and lost() are both mutex-guarded.
type syncRecorder struct {
	mu     sync.Mutex
	events []*captain.Event
}

func (r *syncRecorder) Record(_ context.Context, e *captain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

// lost returns a snapshot of the recorded container.lost events.
func (r *syncRecorder) lost() []*captain.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*captain.Event
	for _, e := range r.events {
		if e.Type == captain.EventContainerLost {
			out = append(out, e)
		}
	}
	return out
}

// recoveryBudgetExpiredShipRepo models the recovery budget running out mid-pass:
// the first ship load cancels the recovery context — what its bounded deadline
// does in production — and fails, so every bookkeeping write that follows would
// ride a dead context. releaseCtxErr records the context state the ship-release
// lookup actually saw.
type recoveryBudgetExpiredShipRepo struct {
	navigation.ShipRepository
	cancel        context.CancelFunc
	releaseCalls  int
	releaseCtxErr error
}

func (r *recoveryBudgetExpiredShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.cancel()
	return nil, fmt.Errorf("recovery budget expired before ship %s could be loaded", symbol)
}

func (r *recoveryBudgetExpiredShipRepo) FindByContainer(ctx context.Context, _ string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	r.releaseCalls++
	r.releaseCtxErr = ctx.Err()
	return nil, nil
}

// SaveWithRetry is the seam recovery re-assigns hulls through; it loads FRESH, so
// the modeled budget expiry still fires on that first load.
func (r *recoveryBudgetExpiredShipRepo) SaveWithRetry(ctx context.Context, symbol string, playerID shared.PlayerID, _ navigation.ShipMutation) (*navigation.Ship, bool, error) {
	_, err := r.FindBySymbol(ctx, symbol, playerID)
	return nil, false, err
}

// TestRecoveryTerminalizesContainerWhenItsOwnBudgetExpiresMidPass is the zombie
// guard: recovery runs under a bounded context, and when that budget expires
// mid-pass the adoption fails. The bookkeeping that marks the container FAILED
// and releases its hull must NOT ride the same dead context — otherwise the write
// silently fails and the row is left RUNNING with a claimed hull and no runner,
// which `container list` reports as a healthy worker indefinitely.
func TestRecoveryTerminalizesContainerWhenItsOwnBudgetExpiresMidPass(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shipRepo := &recoveryBudgetExpiredShipRepo{cancel: cancel}
	s.shipRepo = shipRepo

	emptyParent := ""
	insertRunningContainer(t, db, "zombie-1", "contract_workflow", "CONTRACT_WORKFLOW",
		`{"ship_symbol":"SHIP-Z","coordinator_id":""}`, playerID, &emptyParent)

	require.NoError(t, s.RecoverRunningContainers(ctx))

	requireContainerState(t, db, "zombie-1", "FAILED", "recovery_failed")
	require.Nil(t, s.registeredRunner("zombie-1"), "no runner owns this container")
	require.Equal(t, 1, shipRepo.releaseCalls, "the hull release must still be attempted")
	require.NoError(t, shipRepo.releaseCtxErr, "the hull release rode the dead recovery context")
}

// TestRecoveryEmitsNamedLostEventForFailedContainer is the sp-tit8 core guarantee:
// a container that was expected to be RUNNING but fails to recover must announce
// itself with exactly one interrupt-class container.lost event NAMING it (id +
// type + why), while a coordinator-managed worker skipped in the same pass stays
// silent. Silent absence is the failure mode this makes impossible.
func TestRecoveryEmitsNamedLostEventForFailedContainer(t *testing.T) {
	rec := &syncRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	s, db, playerID := newRecoveryTestServer(t)

	// Fails recovery: a standalone contract_workflow whose ship cannot be loaded
	// by the recovery stub repo, so recoverContainer errors at ship reassignment
	// (recovery_failed). Empty coordinator_id + empty parent keep it off the
	// worker-skip path so it actually attempts recovery.
	emptyParent := ""
	insertRunningContainer(t, db, "goods-lost-1", "contract_workflow", "CONTRACT_WORKFLOW",
		`{"ship_symbol":"SHIP-FAIL","coordinator_id":""}`, playerID, &emptyParent)
	// By-design skip: a coordinator-managed worker (respawned after its coordinator
	// recovers). It must NOT produce a lost-event.
	coord := "coord-live"
	insertRunningContainer(t, db, "worker-live", "contract_workflow", "CONTRACT_WORKFLOW",
		`{"ship_symbol":"SHIP-W","coordinator_id":"coord-live"}`, playerID, &coord)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	// The failed container is FAILED with the recovery_failed reason and did not
	// leave a live runner behind.
	requireContainerState(t, db, "goods-lost-1", "FAILED", "recovery_failed")
	require.Nil(t, s.registeredRunner("goods-lost-1"))
	// The worker is a by-design skip, not a loss.
	requireContainerState(t, db, "worker-live", "FAILED", "worker_interrupted")

	// EXACTLY ONE container.lost fired, and it names the failed container — an
	// operator must never have to guess which container "N lost" was.
	lost := rec.lost()
	require.Len(t, lost, 1, "exactly one container.lost, for the failed container only")
	require.Equal(t, "goods-lost-1", lost[0].Ship, "lost event is scoped to the failed container id")
	require.Equal(t, playerID, lost[0].PlayerID)
	require.Contains(t, lost[0].Payload, "goods-lost-1", "payload carries the container id")
	require.Contains(t, lost[0].Payload, "contract_workflow", "payload carries the container type")
	require.Contains(t, lost[0].Payload, "recovery_failed", "payload carries the why")
}

// TestRecoveryCoordinatorManagedWorkerEmitsNoLostEvent is the sp-tit8 regression
// guard: a coordinator-managed worker is respawned by its coordinator by design,
// so it must NOT fire a container.lost — otherwise every restart false-alarms and
// the loud signal becomes noise.
func TestRecoveryCoordinatorManagedWorkerEmitsNoLostEvent(t *testing.T) {
	rec := &syncRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	s, db, playerID := newRecoveryTestServer(t)
	coord := "coord-1"
	insertRunningContainer(t, db, "worker-managed", "contract_workflow", "CONTRACT_WORKFLOW",
		`{"ship_symbol":"SHIP-A","coordinator_id":"coord-1"}`, playerID, &coord)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	requireContainerState(t, db, "worker-managed", "FAILED", "worker_interrupted")
	require.Empty(t, rec.lost(), "coordinator-managed worker respawns by design; no lost-event")
}

// TestRecoveryAllRecoveredEmitsNoLostEvent verifies the loud path stays silent
// when recovery is clean: every candidate ended RUNNING, nothing fell out, so
// zero container.lost events fire. A detector that cries loss on a healthy
// recovery is worthless (sp-tit8).
func TestRecoveryAllRecoveredEmitsNoLostEvent(t *testing.T) {
	rec := &syncRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	s, db, playerID := newRecoveryTestServer(t)
	// Two top-level coordinators with no singular ship_symbol both re-instantiate
	// live (mirrors TestRecoveryRestartsTopLevelCoordinator /
	// TestRecoveryRestartsScoutPostCoordinator).
	insertRunningContainer(t, db, "fleet-ok", "contract_fleet_coordinator", "CONTRACT_FLEET_COORDINATOR",
		`{"ship_symbols":[],"container_id":"fleet-ok"}`, playerID, nil)
	insertRunningContainer(t, db, "scoutpost-ok", "scout_post_coordinator", "SCOUT_POST_COORDINATOR",
		`{"container_id":"scoutpost-ok","tick_interval_secs":30}`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	// Both recovered live; stop their background runner goroutines before the
	// final assertion. cancelFunc emits at most a workflow event, never a
	// container.lost, so the count below is unaffected.
	for _, id := range []string{"fleet-ok", "scoutpost-ok"} {
		requireContainerState(t, db, id, "RUNNING", "")
		if r := s.registeredRunner(id); r != nil {
			r.cancelFunc()
		}
	}

	require.Empty(t, rec.lost(), "a clean, all-recovered pass must fire zero container.lost events")
}

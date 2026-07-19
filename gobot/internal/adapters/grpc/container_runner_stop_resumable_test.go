package grpc

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// sp-ovkn: a stop/shutdown REQUEST is an interruption, not a completion. The
// daemon's graceful shutdown flips the container's STOPPING flag via
// containerEntity.Stop() WITHOUT cancelling ctx (gracefulShutdownWithTimeout),
// so the runner loop sees it BETWEEN iterations — not as the ctx-cancellation it
// handles mid-iteration. A container that has NOT exhausted its iteration budget
// (a -1 goods_factory NEVER does) must exit RESUMABLE so recovery re-adopts it,
// exactly like every other container type in the same restart — never COMPLETED,
// which sits outside the INTERRUPTED+RUNNING recovery set and would be dropped.

// stopWindowMediator returns a clean nil-error response for the first iteration
// (one full cycle that increments the iteration counter while RUNNING), then
// parks inside the SECOND iteration's Send until released — giving a test a
// deterministic window to flip STOPPING between iterations, exactly as the
// daemon's graceful shutdown does.
type stopWindowMediator struct {
	calls        int32
	blockEntered chan struct{}
	release      chan struct{}
}

func (m *stopWindowMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	if atomic.AddInt32(&m.calls, 1) == 1 {
		return struct{ common.Response }{}, nil
	}
	close(m.blockEntered)
	<-m.release
	return struct{ common.Response }{}, nil
}
func (m *stopWindowMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }
func (m *stopWindowMediator) RegisterMiddleware(_ common.Middleware)                 {}

// nilCleanMediator returns a clean, non-reporter nil-error response every time —
// a factory cycle that did its work and terminated cleanly.
type nilCleanMediator struct{}

func (nilCleanMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	return struct{ common.Response }{}, nil
}
func (nilCleanMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }
func (nilCleanMediator) RegisterMiddleware(_ common.Middleware)                 {}

// ctxEnteredBlockingMediator blocks in Send until the context is cancelled,
// signalling once it is parked so a test can force-interrupt it deterministically
// (no wall-time sleep race). Models a hull mid-cycle when the daemon cancels ctx.
type ctxEnteredBlockingMediator struct {
	entered chan struct{}
	once    sync.Once
}

func (m *ctxEnteredBlockingMediator) Send(ctx context.Context, _ common.Request) (common.Response, error) {
	m.once.Do(func() { close(m.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}
func (m *ctxEnteredBlockingMediator) Register(_ reflect.Type, _ common.RequestHandler) error {
	return nil
}
func (m *ctxEnteredBlockingMediator) RegisterMiddleware(_ common.Middleware) {}

// persistedStatus reads the status the recovery path (RecoverRunningContainers)
// actually reads — the DB row. The in-memory entity is unreliable at a stop:
// finishCleanExit's Complete() silently fails on a STOPPING entity, so the
// COMPLETED terminalization shows up ONLY in the persisted row.
func persistedStatus(t *testing.T, s *DaemonServer, id string) string {
	t.Helper()
	var model persistence.ContainerModel
	require.NoError(t, s.db.First(&model, "id = ?", id).Error)
	return model.Status
}

// Regression pin (sp-ovkn): a -1 (infinite) goods_factory stopped between
// iterations by a graceful shutdown (STOPPING flag set, ctx still live) must exit
// RESUMABLE — its PERSISTED status must be one the recovery path re-adopts
// (RUNNING/INTERRUPTED), never the terminal COMPLETED.
func TestExecute_StopRequestedMidRun_InfiniteContainerExitsResumableNotCompleted(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	s, _, playerID := newRecoveryTestServer(t)
	id := "goods_factory-LAB_INSTRUMENTS-c96ce648"
	insertRunningContainer(t, s.db, id, "goods_factory_coordinator", "goods_factory_coordinator",
		`{"target_good":"LAB_INSTRUMENTS","system_symbol":"X1-JP61","max_iterations":-1}`, playerID, nil)

	// A -1 factory: ShouldContinue() is always true, so the ONLY way its runner
	// loop can exit is a stop request — never a genuine budget exhaustion.
	clock := &recordingClock{current: time.Date(2026, 7, 10, 0, 1, 0, 0, time.UTC)}
	entity := container.NewContainer(id, container.ContainerType("goods_factory_coordinator"), playerID, -1, nil, nil, clock)
	require.NoError(t, entity.Start())

	med := &stopWindowMediator{blockEntered: make(chan struct{}), release: make(chan struct{})}
	r := NewContainerRunner(entity, med, nil, noopLogRepo{}, s.containerRepo, nil, clock)

	done := make(chan struct{})
	go func() { r.execute(); close(done) }()

	// One full iteration completed; the runner is parked in the next iteration's
	// Send — the graceful-shutdown window, ctx still live.
	select {
	case <-med.blockEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("runner never reached the second iteration")
	}

	// Graceful daemon shutdown flips STOPPING WITHOUT cancelling ctx, the same
	// race-free way gracefulShutdownWithTimeout does (under the runner mutex).
	r.mu.Lock()
	require.NoError(t, r.containerEntity.Stop())
	r.mu.Unlock()

	close(med.release) // parked iteration returns; next top-of-loop sees STOPPING

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("execute did not exit after the stop request")
	}

	status := persistedStatus(t, s, id)
	require.NotEqual(t, "COMPLETED", status,
		"a -1 factory stopped mid-run must not terminalize COMPLETED — a COMPLETED row is dropped from recovery and lost at restart (sp-ovkn)")
	require.Contains(t, []string{"RUNNING", "INTERRUPTED"}, status,
		"a stopped-but-unexhausted container must persist a status the recovery path re-adopts")

	require.Zero(t, countEvents(rec.events, captain.EventWorkflowFinished),
		"a resumable stop is not a completion — it must not record the success-shaped workflow.finished")
}

// Regression guard: the fix only reclassifies a STOP; a genuine completion
// (finite iteration budget exhausted) must STILL terminalize COMPLETED.
func TestExecute_BudgetExhausted_StillTerminalizesCompleted(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	s, _, playerID := newRecoveryTestServer(t)
	id := "goods_factory-FUEL-finite"
	insertRunningContainer(t, s.db, id, "goods_factory_coordinator", "goods_factory_coordinator",
		`{"max_iterations":2}`, playerID, nil)

	clock := &recordingClock{current: time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)}
	entity := container.NewContainer(id, container.ContainerType("goods_factory_coordinator"), playerID, 2, nil, nil, clock)
	require.NoError(t, entity.Start())

	// Two clean iterations exhaust the finite budget; the loop exits via
	// !ShouldContinue, the honest-completion path.
	r := NewContainerRunner(entity, nilCleanMediator{}, nil, noopLogRepo{}, s.containerRepo, nil, clock)
	r.execute() // synchronous: 2 iterations, then budget-exhaustion clean exit

	require.Equal(t, "COMPLETED", persistedStatus(t, s, id),
		"genuine completion (finite budget exhausted) must still terminalize COMPLETED")
	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFinished),
		"a genuine completion must record workflow.finished")
}

// The mid-iteration half of the brief: a stop that arrives WHILE a normal
// iteration is in flight (the daemon's force-interrupt cancels ctx) must also
// exit resumable, never COMPLETED. Guards the existing ctx-cancellation branch.
func TestExecute_CtxCanceledMidIteration_ExitsResumableNotCompleted(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	s, _, playerID := newRecoveryTestServer(t)
	id := "goods_factory-INSTR-midflight"
	insertRunningContainer(t, s.db, id, "goods_factory_coordinator", "goods_factory_coordinator",
		`{"max_iterations":-1}`, playerID, nil)

	clock := &recordingClock{current: time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)}
	entity := container.NewContainer(id, container.ContainerType("goods_factory_coordinator"), playerID, -1, nil, nil, clock)
	require.NoError(t, entity.Start())

	med := &ctxEnteredBlockingMediator{entered: make(chan struct{})}
	r := NewContainerRunner(entity, med, nil, noopLogRepo{}, s.containerRepo, nil, clock)

	done := make(chan struct{})
	go func() { r.execute(); close(done) }()

	// Wait until the iteration is genuinely in flight, then force-interrupt exactly
	// as interruptAllContainers does.
	select {
	case <-med.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("runner never entered the iteration")
	}
	r.cancelFunc()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("execute did not exit after ctx cancel")
	}

	status := persistedStatus(t, s, id)
	require.NotEqual(t, "COMPLETED", status,
		"a stop mid-normal-iteration must not terminalize COMPLETED")
	require.Contains(t, []string{"RUNNING", "INTERRUPTED"}, status)
}

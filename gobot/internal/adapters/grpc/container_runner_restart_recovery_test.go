package grpc

import (
	"context"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// sp-v63s: a container that fails an iteration and is RESTARTED by the sp-h0kr
// restart loop keeps running in-memory (ResetForRestart + containerEntity.Start()),
// but the ONLY site that persists RUNNING is r.Start() at initial boot — the
// restart path never re-persists it. handleError, meanwhile, eagerly writes FAILED
// on the failed iteration. So a still-alive, restarted container carries a stale
// FAILED row. RecoverRunningContainers queries only INTERRUPTED+RUNNING rows, and
// the sp-tit8 lost-guard only diffs THAT candidate set — a FAILED-but-alive
// container is therefore neither recovered NOR flagged lost when the daemon
// redeploys (frequent, real-time patching), leaving the hull idle-laden with no
// container and no terminal event.

// errorThenBlockMediator fails the first iteration (a transient leg error — e.g. a
// 409 jump-cooldown), then parks inside
// the SECOND (restarted) iteration's Send until released — giving a test a
// deterministic window to read the persisted status while the container is alive and
// re-running. Iterations past the second return clean.
type errorThenBlockMediator struct {
	calls        int32
	blockEntered chan struct{}
	release      chan struct{}
}

func (m *errorThenBlockMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	switch atomic.AddInt32(&m.calls, 1) {
	case 1:
		return nil, fmt.Errorf("transient leg failure: jump on cooldown (API 409)")
	case 2:
		close(m.blockEntered)
		<-m.release
		return struct{ common.Response }{}, nil
	default:
		return struct{ common.Response }{}, nil
	}
}
func (m *errorThenBlockMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }
func (m *errorThenBlockMediator) RegisterMiddleware(_ common.Middleware)                 {}

// alwaysErrorMediator fails every iteration, driving the restart loop to exhaustion
// (the unrecoverable terminal exit).
type alwaysErrorMediator struct{ calls int32 }

func (m *alwaysErrorMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	atomic.AddInt32(&m.calls, 1)
	return nil, fmt.Errorf("unrecoverable dependency failure")
}
func (m *alwaysErrorMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }
func (m *alwaysErrorMediator) RegisterMiddleware(_ common.Middleware)                 {}

// Regression pin (sp-v63s): a container that failed one iteration and was RESTARTED
// (still alive, re-running) must persist a status the recovery path re-adopts
// (RUNNING/INTERRUPTED) — never the terminal FAILED that drops it from the recovery
// set and loses the live container at the next daemon redeploy.
func TestExecute_IterationErrorThenRestart_PersistsRecoverableNotFailed(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	s, _, playerID := newRecoveryTestServer(t)
	id := "tour-run-TORWIND-19-restarttest"
	insertRunningContainer(t, s.db, id, "tour_run", "TRADING", `{"max_iterations":-1}`, playerID, nil)

	clock := &recordingClock{current: time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)}
	entity := container.NewContainer(id, container.ContainerType("tour_run"), playerID, -1, nil, nil, clock)
	require.NoError(t, entity.Start())

	med := &errorThenBlockMediator{blockEntered: make(chan struct{}), release: make(chan struct{})}
	r := NewContainerRunner(entity, med, nil, noopLogRepo{}, s.containerRepo, nil, clock)

	done := make(chan struct{})
	go func() { r.execute(); close(done) }()

	// Wait until the container has failed iteration 1, waited its (instant, virtual)
	// backoff, restarted, and is alive inside the second iteration.
	select {
	case <-med.blockEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("runner never restarted into the second iteration")
	}

	// Snapshot the persisted status while the restarted container is alive and parked.
	status := persistedStatus(t, s, id)

	// Tear down: cancel so the post-iteration loop exits resumable, then release.
	r.cancelFunc()
	close(med.release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("execute did not exit after teardown")
	}

	require.NotEqual(t, "FAILED", status,
		"a restarted-but-alive container must not persist FAILED — a FAILED row is dropped from the INTERRUPTED+RUNNING recovery set and the live container is silently lost at the next daemon redeploy (sp-v63s)")
	require.Contains(t, []string{"RUNNING", "INTERRUPTED"}, status,
		"a restarting container must persist a status the recovery path re-adopts")
}

// Regression: a genuinely unrecoverable exit (restart budget exhausted) must STILL
// persist terminal FAILED and record exactly one workflow.failed — the terminal
// persist moved out of handleError must land on the real terminal path.
func TestExecute_RestartExhausted_PersistsFailedWithEvent(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	s, _, playerID := newRecoveryTestServer(t)
	id := "tour-run-TORWIND-2C-exhausttest"
	insertRunningContainer(t, s.db, id, "tour_run", "TRADING", `{"max_iterations":-1}`, playerID, nil)

	clock := &recordingClock{current: time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)}
	entity := container.NewContainer(id, container.ContainerType("tour_run"), playerID, -1, nil, nil, clock)
	require.NoError(t, entity.Start())

	med := &alwaysErrorMediator{}
	r := NewContainerRunner(entity, med, nil, noopLogRepo{}, s.containerRepo, nil, clock)
	r.execute() // synchronous: fails, exhausts MaxRestartAttempts, unrecoverable exit

	require.Equal(t, "FAILED", persistedStatus(t, s, id),
		"an exhausted container must persist terminal FAILED")
	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFailed),
		"the terminal failure must record exactly one workflow.failed")
	require.Equal(t, 1, countEvents(rec.events, captain.EventContainerCrashed),
		"an exhausted container is a true crash — exactly one container.crashed")
}

// Regression: the honest-completion veto (sp-7yej invariant 2) still persists FAILED
// through finishCleanExit — the moved terminal persist covers the veto exit too.
func TestFinishCleanExit_Veto_PersistsFailed(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	s, _, playerID := newRecoveryTestServer(t)
	id := "tour-run-TORWIND-2B-vetotest"
	insertRunningContainer(t, s.db, id, "tour_run", "TRADING", `{"max_iterations":1}`, playerID, nil)

	clock := &recordingClock{current: time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)}
	entity := container.NewContainer(id, container.ContainerType("tour_run"), playerID, 1, nil, nil, clock)
	require.NoError(t, entity.Start())

	med := &reporterMediator{resp: &reporterResponse{ok: false, reason: "stranded cargo: 12 FUEL aboard TORWIND-2B"}}
	r := NewContainerRunner(entity, med, nil, noopLogRepo{}, s.containerRepo, nil, clock)
	r.execute() // one iteration, then the clean-exit choke point vetoes success

	require.Equal(t, "FAILED", persistedStatus(t, s, id),
		"a vetoed completion must persist terminal FAILED")
	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFailed),
		"a vetoed completion must record workflow.failed")
	require.Zero(t, countEvents(rec.events, captain.EventContainerCrashed),
		"an honest-completion refusal is not a crash")
}

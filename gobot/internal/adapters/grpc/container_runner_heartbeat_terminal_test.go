package grpc

// container_runner_heartbeat_terminal_test.go — sp-b79f1.
//
// A container's heartbeat is a claim that it is alive. The crash path reached persistFailed
// without stopping the heartbeat goroutine, so a container that had already terminalized FAILED
// went on beating until the daemon process itself died — the row said "dead" and the heartbeat
// said "alive", and the heartbeat was newer.
//
// Measured live unguarded, : 16 FAILED rows with heartbeat_at AFTER stopped_at, worst overhang
// 2h39m40s (tour-run-TORWIND-41-e13a3217 stopped 23:49:50, still beating at 02:29:30 — right up
// to the daemon restart that killed the process). Every one of them was a
// `command execution failed:` row, i.e. execute's unrecoverable-error branch.
//
// The falsifier is the GOROUTINE, not the timestamp. A test that only asserted
// heartbeat_at <= stopped_at would pass without the fix: the ticker is 30s, so in a short test no
// beat lands after the status write anyway, and the assertion would be satisfied by a leak that
// is still very much running. What has to be true is that the heartbeat has actually STOPPED by
// the time the row is stamped dead.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// armHeartbeat starts the heartbeat goroutine the way Start does, for a test that drives execute
// directly. Without this the leak is invisible: no goroutine, nothing to leak, and the test would
// pass against the broken code.
func armHeartbeat(r *ContainerRunner) {
	r.heartbeatStarted.Store(true)
	go r.runHeartbeat()
}

// heartbeatStopped reports whether the heartbeat goroutine has returned.
func heartbeatStopped(r *ContainerRunner, within time.Duration) bool {
	select {
	case <-r.heartbeatDone:
		return true
	case <-time.After(within):
		return false
	}
}

// THE REGRESSION. A container that crashes out unrecoverably must stop beating. Without the fix
// the goroutine survives persistFailed and keeps writing heartbeat_at to a FAILED row.
func TestCrashPath_StopsTheHeartbeatBeforeStampingTheRowDead(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	const id = "tour-run-TORWIND-41-e13a3217"
	insertRunningContainer(t, s.db, id, "tour_run", "TRADING", `{"max_iterations":-1}`, playerID, nil)

	clock := &recordingClock{current: time.Date(2026, 7, 29, 23, 49, 0, 0, time.UTC)}
	entity := container.NewContainer(id, container.ContainerType("tour_run"), playerID, -1, nil, nil, clock)
	require.NoError(t, entity.Start())

	r := NewContainerRunner(entity, &alwaysErrorMediator{}, nil, noopLogRepo{}, s.containerRepo, nil, clock)
	armHeartbeat(r)

	r.execute() // fails, exhausts restarts, takes the unrecoverable-error exit

	require.Equal(t, "FAILED", persistedStatus(t, s, id),
		"precondition: the crash path must terminalize the row")
	require.True(t, heartbeatStopped(r, 2*time.Second),
		"a container that has terminalized FAILED must not still be beating — a live heartbeat on a dead "+
			"container is a corpse reporting itself alive, and it ran for 2h39m in production")
}

// The clean-completion path already stopped the heartbeat (finishCleanExit). Pin it so the
// guarantee is not silently lost by a later refactor of that path.
func TestCleanExit_AlsoStopsTheHeartbeat(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	const id = "tour-run-TORWIND-41-cleanexit"
	insertRunningContainer(t, s.db, id, "tour_run", "TRADING", `{"max_iterations":2}`, playerID, nil)

	clock := &recordingClock{current: time.Date(2026, 7, 29, 23, 49, 0, 0, time.UTC)}
	entity := container.NewContainer(id, container.ContainerType("tour_run"), playerID, 2, nil, nil, clock)
	require.NoError(t, entity.Start())

	r := NewContainerRunner(entity, nilCleanMediator{}, nil, noopLogRepo{}, s.containerRepo, nil, clock)
	armHeartbeat(r)

	r.execute() // two clean iterations, budget exhausted, clean exit

	require.Equal(t, "COMPLETED", persistedStatus(t, s, id),
		"precondition: budget exhaustion completes")
	require.True(t, heartbeatStopped(r, 2*time.Second),
		"a completed container must not still be beating")
}

// The RESUMABLE exits are deliberately different and must stay that way (sp-ovkn, RULINGS #2). A
// container stopped between iterations leaves its row non-terminal so boot recovery re-adopts it;
// terminalizing there would drop it from the INTERRUPTED+RUNNING recovery set and lose it at every
// restart. sp-b79f1 tightens the TERMINAL paths only — it must not turn a resumable exit terminal.
func TestResumableStopExit_DoesNotTerminalizeTheRow(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	const id = "tour-run-TORWIND-41-resumable"
	insertRunningContainer(t, s.db, id, "tour_run", "TRADING", `{"max_iterations":-1}`, playerID, nil)

	clock := &recordingClock{current: time.Date(2026, 7, 29, 23, 49, 0, 0, time.UTC)}
	entity := container.NewContainer(id, container.ContainerType("tour_run"), playerID, -1, nil, nil, clock)
	require.NoError(t, entity.Start())

	med := &stopWindowMediator{blockEntered: make(chan struct{}), release: make(chan struct{})}
	r := NewContainerRunner(entity, med, nil, noopLogRepo{}, s.containerRepo, nil, clock)
	armHeartbeat(r)

	done := make(chan struct{})
	go func() { r.execute(); close(done) }()

	select {
	case <-med.blockEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("runner never reached the second iteration")
	}
	r.mu.Lock()
	require.NoError(t, r.containerEntity.Stop()) // graceful stop: STOPPING set, ctx still live
	r.mu.Unlock()
	close(med.release)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("execute did not exit after the stop request")
	}

	require.Contains(t, []string{"RUNNING", "INTERRUPTED"}, persistedStatus(t, s, id),
		"a resumable stop must leave a row boot recovery re-adopts — terminalizing it here would lose the "+
			"container at every restart (sp-ovkn, RULINGS #2)")
}

// stopHeartbeat must WAIT for the goroutine to return, not merely signal it. That wait is the
// whole ordering guarantee: an in-flight UpdateContainerHeartbeat has to finish BEFORE the
// terminal status write that follows, or the beat lands after the row is stamped dead and we are
// back to heartbeat_at > stopped_at with a shorter leak instead of no leak.
//
// Driven through a stand-in goroutine rather than the real runHeartbeat because the real one
// returns in microseconds once signalled: asserting on it would race, and a signal-only
// implementation would pass most of the time. Holding the exit open makes "did it wait?"
// deterministic.
func TestStopHeartbeat_WaitsForTheGoroutineToActuallyExit(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	const id = "tour-run-TORWIND-41-waits"
	insertRunningContainer(t, s.db, id, "tour_run", "TRADING", `{"max_iterations":1}`, playerID, nil)

	entity := container.NewContainer(id, container.ContainerType("tour_run"), playerID, 1, nil, nil, nil)
	r := NewContainerRunner(entity, nilCleanMediator{}, nil, noopLogRepo{}, s.containerRepo, nil, nil)

	r.heartbeatStarted.Store(true)
	release := make(chan struct{})
	go func() {
		<-r.heartbeatStop // observe the stop signal
		<-release         // ...but stay "in flight" until the test lets go
		close(r.heartbeatDone)
	}()

	returned := make(chan struct{})
	go func() { r.stopHeartbeat(); close(returned) }()

	select {
	case <-returned:
		t.Fatal("stopHeartbeat returned while the heartbeat goroutine was still in flight — its write can " +
			"then land AFTER the terminal status write, which is exactly the heartbeat_at > stopped_at lie")
	case <-time.After(200 * time.Millisecond):
		// Correct: still waiting on the goroutine.
	}

	close(release)
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("stopHeartbeat never returned after the heartbeat goroutine exited")
	}
}

// Start must ARM the flag, or the wait above is dead code for every real container: unarmed,
// stopHeartbeat short-circuits and a production container's terminal path stops waiting for its
// heartbeat, which is the ordering guarantee gone. The tests either side of this one exercise the
// flag by setting it themselves, so nothing else notices if Start stops setting it — this is the
// link that makes the chain hold end to end: Start arms it, armed means stopHeartbeat waits,
// waiting means heartbeat_at cannot post-date stopped_at.
func TestStart_ArmsTheHeartbeat(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	const id = "tour-run-TORWIND-41-armedbystart"
	insertRunningContainer(t, s.db, id, "tour_run", "TRADING", `{"max_iterations":1}`, playerID, nil)

	entity := container.NewContainer(id, container.ContainerType("tour_run"), playerID, 1, nil,
		map[string]interface{}{}, nil)
	r := NewContainerRunner(entity, nilCleanMediator{}, nil, noopLogRepo{}, s.containerRepo, nil, nil)

	require.NoError(t, r.Start())
	t.Cleanup(func() { r.cancelFunc() })

	require.True(t, r.heartbeatStarted.Load(),
		"Start launched the heartbeat goroutine but did not arm heartbeatStarted — stopHeartbeat will then "+
			"short-circuit its wait on every real container, so an in-flight beat can land AFTER the terminal "+
			"status write")
}

// stopHeartbeat must not block when the heartbeat was never armed. Start's claim-failure exit
// reaches a terminal write before the goroutine ever launches, and waiting there burns the full
// 2s timeout on a channel nobody will ever close.
func TestStopHeartbeat_UnarmedReturnsImmediately(t *testing.T) {
	s, _, playerID := newRecoveryTestServer(t)
	const id = "tour-run-TORWIND-41-unarmed"
	insertRunningContainer(t, s.db, id, "tour_run", "TRADING", `{"max_iterations":1}`, playerID, nil)

	entity := container.NewContainer(id, container.ContainerType("tour_run"), playerID, 1, nil, nil, nil)
	r := NewContainerRunner(entity, nilCleanMediator{}, nil, noopLogRepo{}, s.containerRepo, nil, nil)
	// deliberately NOT armed

	start := time.Now()
	r.stopHeartbeat()
	require.Less(t, time.Since(start), 500*time.Millisecond,
		"stopping a heartbeat that was never started must be immediate, not a full timeout wait")
}

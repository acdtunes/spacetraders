package grpc

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// sp-h0kr: the container iteration-error restart loop must SPACE restarts with an
// escalating, clock-injected, ctx-interruptible backoff — never burn through
// MaxRestartAttempts with no wait between attempts — so a dependency that fails
// instantly (e.g. routing down => immediate connection-refused on localhost) gets
// a chance to self-heal before the restart budget is exhausted. These tests pin
// that backoff, using a MockClock-style clock so no test wall-waits.

// alwaysFailMediator fails every Send with a transient-looking dependency error,
// driving the runner's restart loop to exhaustion. callCount records how many
// iterations actually ran (the command is invoked once per attempt).
type alwaysFailMediator struct {
	mu    sync.Mutex
	calls int
}

func (m *alwaysFailMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return nil, fmt.Errorf("dependency unavailable: connection refused")
}
func (m *alwaysFailMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }
func (m *alwaysFailMediator) RegisterMiddleware(_ common.Middleware)                 {}

func (m *alwaysFailMediator) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// recordingClock is a Clock whose Sleep advances virtual time instantly (so tests
// never wall-wait) while recording every slept duration in order, letting a test
// assert the exact backoff schedule the restart loop waited.
type recordingClock struct {
	mu      sync.Mutex
	current time.Time
	sleeps  []time.Duration
}

func (c *recordingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *recordingClock) Sleep(d time.Duration) {
	c.mu.Lock()
	c.current = c.current.Add(d)
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
}

func (c *recordingClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(c.sleeps))
	copy(out, c.sleeps)
	return out
}

// newRestartTestRunner builds a started, no-iteration-limit runner around med and
// clock — the shape execute() drives through the restart loop. No shipRepo or
// containerRepo: ship claim/release and DB persistence are out of scope here.
func newRestartTestRunner(t *testing.T, med common.Mediator, clock *recordingClock) *ContainerRunner {
	t.Helper()
	entity := container.NewContainer("restart-backoff-test", container.ContainerTypeContractWorkflow, 2, -1, nil, nil, clock)
	require.NoError(t, entity.Start())
	return NewContainerRunner(entity, med, nil, noopLogRepo{}, nil, nil, clock)
}

// The restart path must wait the escalating backoff schedule between attempts,
// driven entirely by clock advance (no wall time). A fast-failing command that
// exhausts the restart budget must sleep exactly the startup jitter followed by
// one scheduled backoff before each restart.
func TestRestartBackoffWaitsScheduledDelaysBetweenAttempts(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	clock := &recordingClock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	med := &alwaysFailMediator{}
	r := newRestartTestRunner(t, med, clock)

	r.execute() // runs synchronously to the unrecoverable exit

	// The command runs once, then once per restart, then the loop gives up.
	require.Equal(t, container.MaxRestartAttempts+1, med.callCount(),
		"the command must run once, then once per restart, then stop")

	// The only clock sleeps are the single startup jitter followed by one backoff
	// before each restart; the backoffs must be exactly the escalating schedule.
	sleeps := clock.recorded()
	require.Len(t, sleeps, 1+container.MaxRestartAttempts,
		"expected the startup jitter plus one backoff per restart")

	expected := make([]time.Duration, container.MaxRestartAttempts)
	for i := range expected {
		expected[i] = restartBackoffFor(i)
	}
	require.Equal(t, expected, sleeps[1:],
		"the waits between restarts must follow the escalating backoff schedule")
}

// backoffBlockingClock lets the startup jitter (the first Sleep) pass instantly,
// then blocks inside the first restart backoff until release is closed — modelling
// an in-progress backoff. blockEntered is signaled once the runner is parked in
// that backoff, so a test can cancel mid-wait and prove the wait is interruptible.
type backoffBlockingClock struct {
	mu           sync.Mutex
	current      time.Time
	calls        int
	enteredOnce  sync.Once
	blockEntered chan struct{}
	release      chan struct{}
}

func (c *backoffBlockingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *backoffBlockingClock) Sleep(d time.Duration) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.current = c.current.Add(d)
	c.mu.Unlock()

	if n == 1 {
		return // startup jitter: instant
	}
	// The first (and any) restart backoff: announce entry, then block until the
	// test releases it — which, for the cancel test, never happens.
	c.enteredOnce.Do(func() { close(c.blockEntered) })
	<-c.release
}

// A Stop/shutdown that cancels the context while the runner is mid-backoff must
// abandon the wait and exit promptly — never hang for up to the full 120s backoff
// — and must not perform the restart it was waiting to make.
func TestRestartBackoffInterruptedByStopExitsPromptly(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	clock := &backoffBlockingClock{
		current:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		blockEntered: make(chan struct{}),
		release:      make(chan struct{}),
	}
	// Let the parked sleeper goroutine drain once the assertions are done.
	defer close(clock.release)

	med := &alwaysFailMediator{}
	entity := container.NewContainer("restart-cancel-test", container.ContainerTypeContractWorkflow, 2, -1, nil, nil, clock)
	require.NoError(t, entity.Start())
	r := NewContainerRunner(entity, med, nil, noopLogRepo{}, nil, nil, clock)

	done := make(chan struct{})
	go func() {
		r.execute()
		close(done)
	}()

	// Wait until the runner is parked inside the first restart backoff.
	select {
	case <-clock.blockEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("runner never entered the restart backoff")
	}

	// Cancel the context, as Stop() does. The backoff must race ctx.Done and let
	// execute() return without waiting for the (never-released) sleep to finish.
	r.cancelFunc()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("execute did not exit promptly after cancel during backoff — the backoff was not interruptible")
	}

	// The wait was abandoned mid-backoff: only the initial run happened, no restart
	// re-ran the command.
	require.Equal(t, 1, med.callCount(),
		"cancel during the first backoff must abort before any restart re-runs the command")
}

// Regression guard: the backoff must only SPACE the restarts, not change the
// exhaustion contract. After MaxRestartAttempts restarts a fast-failing container
// must still give up — terminal FAILED, exactly one container.crashed event, and
// no extra restart beyond the budget.
func TestRestartExhaustionAfterMaxAttemptsTerminalizes(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	clock := &recordingClock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	med := &alwaysFailMediator{}
	r := newRestartTestRunner(t, med, clock)

	r.execute()

	require.Equal(t, container.MaxRestartAttempts+1, med.callCount(),
		"exhaustion must run the command exactly once per attempt, then stop")
	require.Equal(t, container.ContainerStatusFailed, r.containerEntity.Status(),
		"an exhausted container must terminalize FAILED")
	require.Equal(t, 1, countEvents(rec.events, captain.EventContainerCrashed),
		"exhaustion must record exactly one container.crashed event")
}

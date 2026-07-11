// internal/infrastructure/supervise/supervisor_test.go
package supervise

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type fakeRecorder struct {
	mu     sync.Mutex
	events []*captain.Event
}

func (f *fakeRecorder) Record(_ context.Context, e *captain.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}

func (f *fakeRecorder) byType(t captain.EventType) []*captain.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*captain.Event
	for _, e := range f.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// waitForCalls drains the invocation-signal channel until n calls were seen
// or the deadline passes. The supervised fn signals each invocation; this is
// the only cross-goroutine synchronization the tests use.
func waitForCalls(t *testing.T, calls <-chan struct{}, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-calls:
		case <-deadline:
			t.Fatalf("timed out waiting for invocation %d/%d", i+1, n)
		}
	}
}

// A component whose run fn returns an error is restarted (with the clock-
// injected backoff), and a clean nil return ends supervision without restart.
func TestSupervisor_RestartsOnErrorThenStopsOnCleanExit(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)}
	rec := &fakeRecorder{}
	var restarts []string
	sup := New(rec, 7, clock, WithOnRestart(func(c string) { restarts = append(restarts, c) }))

	calls := make(chan struct{}, 16)
	invocation := 0
	sup.Go(context.Background(), "sweeper", func(ctx context.Context) error {
		invocation++
		calls <- struct{}{}
		if invocation <= 2 {
			return errors.New("db timeout")
		}
		return nil // third run completes cleanly
	})

	waitForCalls(t, calls, 3)
	sup.Wait()

	require.Equal(t, []string{"sweeper", "sweeper"}, restarts, "two failures → two restarts, clean exit → none")
	require.Empty(t, rec.byType(captain.EventDaemonComponentCrashLoop), "2 crashes is below the loop threshold")
}

// Context cancellation stops supervision without a restart, both when the
// run fn returns because of it and during a backoff wait.
func TestSupervisor_CtxCancelStopsSupervision(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)}
	sup := New(&fakeRecorder{}, 7, clock)

	ctx, cancel := context.WithCancel(context.Background())
	calls := make(chan struct{}, 1)
	sup.Go(ctx, "blocker", func(ctx context.Context) error {
		calls <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	waitForCalls(t, calls, 1)
	cancel()
	sup.Wait() // must return; a hang here fails via go test timeout
}

// A panicking component is restarted exactly like an erroring one — the
// panic is converted by CapturePanic, never propagated to the process.
func TestSupervisor_PanicIsConvertedAndRestarted(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)}
	var restarts int
	sup := New(&fakeRecorder{}, 7, clock, WithOnRestart(func(string) { restarts++ }))

	calls := make(chan struct{}, 16)
	invocation := 0
	sup.Go(context.Background(), "panicky", func(ctx context.Context) error {
		invocation++
		calls <- struct{}{}
		if invocation == 1 {
			panic("nil map write")
		}
		return nil
	})
	waitForCalls(t, calls, 2)
	sup.Wait()
	require.Equal(t, 1, restarts)
}

// Crash-loop escalation: the 5th crash inside the window emits ONE
// interrupt-class daemon.component_crashloop event, the window re-arms, and
// the 10th crash emits the second — edge-triggered, never per-crash
// (sp-6g96: event spam saturates the wake cap).
func TestSupervisor_CrashLoopEmitsEdgeTriggeredInterrupt(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)}
	rec := &fakeRecorder{}
	sup := New(rec, 7, clock)

	calls := make(chan struct{}, 32)
	invocation := 0
	sup.Go(context.Background(), "sweeper", func(ctx context.Context) error {
		invocation++
		calls <- struct{}{}
		if invocation <= 10 {
			return errors.New("boom")
		}
		return nil
	})
	waitForCalls(t, calls, 11)
	sup.Wait()

	loops := rec.byType(captain.EventDaemonComponentCrashLoop)
	require.Len(t, loops, 2, "5th and 10th crash each cross the threshold once")
	require.Equal(t, "daemon:sweeper", loops[0].Ship, "container-scoped Ship convention, like coordinator.error_loop")
	require.Equal(t, 7, loops[0].PlayerID)
	require.Contains(t, loops[0].Payload, `"component":"sweeper"`)
	require.Contains(t, loops[0].Payload, "boom")
}

// The new event type must be interrupt class: a crash-looping safety-net
// component (the arrival sweeper) silently degrades the whole fleet, exactly
// the class coordinator.error_loop exists for.
func TestDaemonComponentCrashLoopIsInterruptClass(t *testing.T) {
	require.Contains(t, captain.DefaultInterruptTypes(), captain.EventDaemonComponentCrashLoop)
}

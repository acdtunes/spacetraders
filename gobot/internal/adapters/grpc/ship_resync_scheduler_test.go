package grpc

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Behavior 1 (sp-p1ci): after the base interval elapses the scheduler invokes
// the injected resync core. Uses a small base + jitter with the DEFAULT rand
// seam, so it also exercises the real jitter path (a scheduler that never fired
// — or nil-panicked on the default rand — fails here). Observable outcome: the
// resync callback is called.
func TestShipResyncScheduler_FiresResyncAfterInterval(t *testing.T) {
	var calls atomic.Int32
	fired := make(chan struct{}, 8)
	resync := func(context.Context) error {
		calls.Add(1)
		select {
		case fired <- struct{}{}:
		default:
		}
		return nil
	}
	s := NewShipResyncScheduler(resync, 15*time.Millisecond, 5*time.Millisecond)

	go func() { _ = s.Run(context.Background()) }()
	defer s.Stop()

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("resync core was not invoked after the interval elapsed")
	}
	require.GreaterOrEqual(t, calls.Load(), int32(1))
}

// Behavior 2 (sp-p1ci): each cycle's wait is base +/- a random offset bounded
// by jitter. Drives the rand seam across its whole [0,1) domain and asserts the
// delay never escapes [base-jitter, base+jitter], with the extremes exact. A
// miscomputed jitter (wrong sign, wrong scale, jitter treated as a fraction)
// breaks the bounds.
func TestShipResyncScheduler_JitterStaysWithinBounds(t *testing.T) {
	base := 100 * time.Millisecond
	jitter := 40 * time.Millisecond
	s := NewShipResyncScheduler(func(context.Context) error { return nil }, base, jitter)

	for i := 0; i <= 100; i++ {
		r := float64(i) / 100.0
		s.randFloat = func() float64 { return r }
		d := s.nextDelay()
		require.GreaterOrEqual(t, d, base-jitter, "delay below lower bound at r=%v", r)
		require.LessOrEqual(t, d, base+jitter, "delay above upper bound at r=%v", r)
	}

	s.randFloat = func() float64 { return 0.0 }
	require.Equal(t, base-jitter, s.nextDelay(), "r=0 must yield base-jitter")
	s.randFloat = func() float64 { return 1.0 }
	require.Equal(t, base+jitter, s.nextDelay(), "r=1 must yield base+jitter")
}

// sp-ig6x (bead point b): a SUCCESSFUL resync pass must emit a per-run success
// log line, not just the failure line at :78. The prod resync core already
// prints its own "Ship sync complete" to stdout, but the scheduler itself only
// logged on failure — so a healthy loop was invisible and a stalled one was
// indistinguishable from a quiet-but-alive one. This asserts the loop emits a
// visible heartbeat on a clean pass. logf is injected (same seam idiom as
// randFloat) so the test observes the log without racing the global logger.
func TestShipResyncScheduler_LogsSuccessOnCleanPass(t *testing.T) {
	got := make(chan string, 8)
	s := NewShipResyncScheduler(func(context.Context) error { return nil }, 15*time.Millisecond, 5*time.Millisecond)
	s.logf = func(format string, args ...interface{}) {
		select {
		case got <- fmt.Sprintf(format, args...):
		default:
		}
	}

	go func() { _ = s.Run(context.Background()) }()
	defer s.Stop()

	select {
	case msg := <-got:
		require.Contains(t, msg, "ship resync ok",
			"a clean resync pass must emit a visible success/heartbeat log line")
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler emitted no success log after a clean resync pass — a healthy loop must be visible")
	}
}

// Behavior 3 (sp-p1ci): Stop() halts a running loop promptly and cleanly
// (returns nil — the supervise layer treats that as a clean stop). Mirrors
// TestRunSweeper_StopChAlsoStops for the state scheduler.
func TestShipResyncScheduler_StopHaltsRunCleanly(t *testing.T) {
	s := NewShipResyncScheduler(func(context.Context) error { return nil }, 20*time.Millisecond, 0)

	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	// Must still be running shortly after start (it does not return on its own).
	select {
	case err := <-done:
		t.Fatalf("Run returned before Stop: %v", err)
	case <-time.After(40 * time.Millisecond):
	}

	s.Stop()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after Stop()")
	}
}

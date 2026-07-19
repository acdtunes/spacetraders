// internal/adapters/grpc/ship_state_scheduler_sweeper_test.go
package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RunSweeper is the supervised replacement for StartBackgroundSweeper: it
// BLOCKS, sweeping every SweeperInterval, and returns nil promptly when its
// context is canceled — the supervise layer treats that as a clean stop.
// (A panic inside a sweep pass propagates to the Supervisor, which restarts
// the sweeper with backoff (sp-i01z) — an unsupervised goroutine would die
// silently on panic and arrivals would stop being swept forever.)
func TestRunSweeper_BlocksUntilCtxCancelThenReturnsNil(t *testing.T) {
	s := NewShipStateScheduler(nil, &shared.RealClock{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.RunSweeper(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("RunSweeper returned before cancel: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RunSweeper did not stop on ctx cancel")
	}
}

// Legacy Stop() must also stop RunSweeper (handleShutdown calls Stop for the
// timers; the sweeper honors both signals).
func TestRunSweeper_StopChAlsoStops(t *testing.T) {
	s := NewShipStateScheduler(nil, &shared.RealClock{}, nil)
	done := make(chan error, 1)
	go func() { done <- s.RunSweeper(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	s.Stop()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RunSweeper did not stop on Stop()")
	}
}

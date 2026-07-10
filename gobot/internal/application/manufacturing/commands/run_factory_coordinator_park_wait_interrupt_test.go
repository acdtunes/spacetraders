package commands

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// parkWaitBlockingClock lets Now() answer with a fixed instant and blocks
// inside Sleep() until the test releases it, signalling entry exactly once so
// a test can deterministically know the poll loop is parked in the wait
// before cancelling - no wall-clock race. Mirrors backoffBlockingClock in
// container_runner_restart_backoff_test.go (sp-h0kr).
type parkWaitBlockingClock struct {
	mu           sync.Mutex
	current      time.Time
	enteredOnce  sync.Once
	blockEntered chan struct{}
	release      chan struct{}
}

func (c *parkWaitBlockingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *parkWaitBlockingClock) Sleep(time.Duration) {
	c.enteredOnce.Do(func() { close(c.blockEntered) })
	<-c.release
}

// sp-l709: waitForIdleHaulers' park-poll wait used to sleep via the bare,
// non-interruptible h.clock.Sleep(shipDiscoveryInterval), so a parked factory
// noticed container shutdown up to shipDiscoveryInterval (30s) late even
// though the loop's own top-of-iteration check is instant. The wait must now
// race the clock sleep against ctx.Done() (sleepInterruptibly - the same
// helper the sp-2q2o no-work backoff already uses) so cancellation during the
// park wait is noticed immediately instead of waiting out the full interval.
func TestWaitForIdleHaulers_CancelledDuringParkWait_ReturnsPromptly(t *testing.T) {
	clock := &parkWaitBlockingClock{
		current:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		blockEntered: make(chan struct{}),
		release:      make(chan struct{}),
	}
	// Let the abandoned sleeper goroutine drain once the assertions are done.
	defer close(clock.release)

	handler, shipRepo := newFactoryHandlerAndShipRepo(t, clock)
	shipRepo.alwaysEmpty = true // the fleet never frees a hauler - always takes the park-wait path

	ctx, cancel := context.WithCancel(context.Background())

	type waitResult struct {
		ships   []*navigation.Ship
		symbols []string
		err     error
	}
	done := make(chan waitResult, 1)
	go func() {
		ships, symbols, err := handler.waitForIdleHaulers(ctx, shared.MustNewPlayerID(1), testSystem, nil, "factory-cancel-test")
		done <- waitResult{ships, symbols, err}
	}()

	// Wait until the poll loop is parked inside the interruptible sleep.
	select {
	case <-clock.blockEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForIdleHaulers never entered the park-wait sleep")
	}

	// Cancel, as container shutdown does. The park wait must race ctx.Done and
	// let waitForIdleHaulers return without waiting for the (never-released)
	// sleep to finish.
	cancel()

	select {
	case res := <-done:
		if !errors.Is(res.err, context.Canceled) {
			t.Fatalf("expected context.Canceled after cancelling during the park wait, got %v", res.err)
		}
		if res.ships != nil || res.symbols != nil {
			t.Fatalf("expected nil ships/symbols on cancellation, got ships=%v symbols=%v", res.ships, res.symbols)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForIdleHaulers did not return promptly after cancel during the park wait - the sleep was not interruptible")
	}
}

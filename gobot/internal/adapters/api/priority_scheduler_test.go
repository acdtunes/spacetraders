package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// ---- test doubles / helpers -------------------------------------------------

// raceSafeClock is a mutex-guarded controllable clock. The scheduler reads Now()
// from acquirer goroutines while the test advances time, so a plain MockClock
// would data-race under -race; this one does not.
type raceSafeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newRaceSafeClock() *raceSafeClock {
	return &raceSafeClock{t: time.Unix(0, 0).UTC()}
}

func (c *raceSafeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *raceSafeClock) Sleep(d time.Duration) { c.advance(d) }

func (c *raceSafeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// gateTokenSource is a controllable stand-in for rate.Limiter.Wait. It records
// the tier of each acquirer at the moment it ENTERS token acquisition — i.e. the
// admission order the scheduler chose — and blocks each acquirer until the test
// releases a token via grant(). Because the scheduler admits strictly one
// acquirer at a time, the recorded order is exactly the scheduler's decision.
type gateTokenSource struct {
	mu      sync.Mutex
	entries []Priority
	grants  chan struct{}
}

func newGateTokenSource() *gateTokenSource {
	return &gateTokenSource{grants: make(chan struct{})}
}

func (g *gateTokenSource) wait(ctx context.Context) error {
	g.mu.Lock()
	p, _ := priorityFromContext(ctx)
	g.entries = append(g.entries, p)
	g.mu.Unlock()
	select {
	case <-g.grants:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gateTokenSource) admissionOrder() []Priority {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Priority, len(g.entries))
	copy(out, g.entries)
	return out
}

func (g *gateTokenSource) enteredCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.entries)
}

// grant releases exactly one token to whichever acquirer currently holds the
// slot. Unbuffered, so grant blocks until that acquirer receives it, keeping the
// test in lock-step with admissions.
func (g *gateTokenSource) grant() { g.grants <- struct{}{} }

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Microsecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

// ---- behaviour 1: priority ordering under contention ------------------------

// With the limiter saturated, a HIGH acquirer enqueued AFTER several LOW
// acquirers must be admitted BEFORE all of them. Mutation: revert selection to
// FIFO and the HIGH lands last -> this test fails.
func TestPrioritySchedulerServesHighBeforeQueuedLow(t *testing.T) {
	gate := newGateTokenSource()
	// Large aging window so aging cannot interfere with this pure-ordering test.
	s := newPriorityScheduler(gate.wait, newRaceSafeClock(), time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	start := func(p Priority) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.wait(WithPriority(ctx, p), p)
		}()
	}

	// A blocker occupies the single in-flight slot (it has entered the gate).
	start(PriorityNormal)
	waitFor(t, func() bool { return gate.enteredCount() == 1 }, "blocker to occupy the slot")

	// Park several LOW acquirers, THEN one HIGH — HIGH is enqueued strictly after
	// every LOW.
	const lowCount = 5
	for i := 0; i < lowCount; i++ {
		start(PriorityLow)
	}
	waitFor(t, func() bool { return s.pendingCount() == lowCount }, "5 LOW acquirers to park")
	start(PriorityHigh)
	waitFor(t, func() bool { return s.pendingCount() == lowCount+1 }, "HIGH acquirer to park")

	// Release tokens one at a time; the gate captures admission order.
	for i := 0; i < lowCount+2; i++ { // blocker + HIGH + 5 LOW
		gate.grant()
	}
	wg.Wait()

	order := gate.admissionOrder()
	if len(order) != lowCount+2 {
		t.Fatalf("expected %d admissions, got %d: %v", lowCount+2, len(order), order)
	}
	// The blocker was already in-flight and cannot be preempted, so it is first.
	if order[0] != PriorityNormal {
		t.Fatalf("expected in-flight blocker (NORMAL) admitted first, got %v", order[0])
	}
	// The HIGH must precede EVERY queued LOW.
	if order[1] != PriorityHigh {
		t.Fatalf("HIGH must be admitted before any queued LOW; admission order = %v", order)
	}
}

// ---- behaviour 2: no starvation via bounded aging ---------------------------

// Under sustained HIGH load a LOW acquirer must still be served: once it has
// waited >= agingWindow it is promoted and, being the earliest-enqueued top-tier
// waiter, is admitted ahead of the HIGH crowd that arrived after it. Mutation:
// remove aging (effective priority = base) and the LOW never gets in -> this test
// times out and fails.
func TestPrioritySchedulerAgesLowToPreventStarvation(t *testing.T) {
	gate := newGateTokenSource()
	clk := newRaceSafeClock()
	const agingWindow = 100 * time.Millisecond
	s := newPriorityScheduler(gate.wait, clk, agingWindow)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	start := func(p Priority) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.wait(WithPriority(ctx, p), p)
		}()
	}

	// A blocker occupies the slot.
	start(PriorityNormal)
	waitFor(t, func() bool { return gate.enteredCount() == 1 }, "blocker to occupy the slot")

	// A single LOW parks FIRST (enqueuedAt = t0).
	start(PriorityLow)
	waitFor(t, func() bool { return s.pendingCount() == 1 }, "LOW to park")

	// A sustained crowd of HIGH parks AFTER it, each at a strictly later enqueue
	// time so tie-breaks are unambiguous.
	const highCount = 4
	for i := 0; i < highCount; i++ {
		clk.advance(time.Millisecond)
		start(PriorityHigh)
		want := i + 2 // 1 LOW + (i+1) HIGH
		waitFor(t, func() bool { return s.pendingCount() == want }, "HIGH to park")
	}

	// Advance past the aging window: the LOW is now promoted to the top tier and,
	// with the earliest enqueue time, must be picked next.
	clk.advance(agingWindow)

	gate.grant() // blocker releases the slot -> scheduler selects next
	waitFor(t, func() bool {
		order := gate.admissionOrder()
		return len(order) >= 2 && order[1] == PriorityLow
	}, "promoted LOW to be admitted ahead of the HIGH crowd")

	// Drain everyone so no goroutine leaks.
	cancel()
	wg.Wait()
}

// ---- behaviour 3: rate ceiling / burst preserved ----------------------------

// Priority scheduling must not leak throughput: over a window where no token
// refills, the ON path issues EXACTLY as many tokens as the raw limiter (OFF).
// Every token still comes from the same limiter, so the ceiling/burst is
// identical.
func TestPrioritySchedulingPreservesRateCeiling(t *testing.T) {
	const burst = 5
	newLimiter := func() *rate.Limiter {
		return rate.NewLimiter(rate.Every(time.Hour), burst) // effectively no refill during the test
	}

	immediateGrants := func(acquire func(context.Context) error) int {
		count := 0
		for i := 0; i < burst+3; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			err := acquire(ctx)
			cancel()
			if err != nil {
				break // first blocked acquisition => burst exhausted
			}
			count++
		}
		return count
	}

	// OFF: the legacy raw limiter path.
	offCount := immediateGrants(newLimiter().Wait)

	// ON: identical limiter config, but every token drawn through the scheduler.
	on := newLimiter()
	s := newPriorityScheduler(on.Wait, newRaceSafeClock(), time.Hour)
	onCount := immediateGrants(func(ctx context.Context) error { return s.wait(ctx, PriorityHigh) })

	if offCount != burst {
		t.Fatalf("OFF path issued %d tokens, expected exactly burst=%d", offCount, burst)
	}
	if onCount != offCount {
		t.Fatalf("ON path issued %d tokens but OFF issued %d — priority must not change the ceiling", onCount, offCount)
	}
}

// ---- behaviour 4: flag OFF is the default and byte-identical to legacy -------

func TestPrioritySchedulingDefaultsOffAndIsInert(t *testing.T) {
	c := NewSpaceTradersClient()

	// DEFAULT: no scheduler => legacy c.rateLimiter.Wait path.
	if c.scheduler.Load() != nil {
		t.Fatal("priority scheduler must be nil by default (OFF = byte-identical to legacy)")
	}
	c.SetPriorityScheduling(false)
	if c.scheduler.Load() != nil {
		t.Fatal("SetPriorityScheduling(false) must leave the scheduler nil")
	}

	// OFF: acquireRateToken still acquires from the shared limiter. Even a HIGH
	// endpoint like "Buy Cargo" triggers no scheduling — classification is inert.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.acquireRateToken(ctx, "Buy Cargo"); err != nil {
		t.Fatalf("OFF acquireRateToken should acquire a burst token, got %v", err)
	}

	// Arming then disarming round-trips cleanly.
	c.SetPriorityScheduling(true)
	if c.scheduler.Load() == nil {
		t.Fatal("SetPriorityScheduling(true) must arm the scheduler")
	}
	c.SetPriorityScheduling(false)
	if c.scheduler.Load() != nil {
		t.Fatal("SetPriorityScheduling(false) must disarm the scheduler")
	}
}

// ---- behaviour 6: cancelled waiter bails cleanly ----------------------------

// A parked acquirer whose context is cancelled must return promptly WITHOUT
// consuming a token and WITHOUT stranding the slot. Mutation: drop the ctx.Done
// branch and the waiter hangs -> this test times out and fails.
func TestPrioritySchedulerCancelledWaiterBailsWithoutConsumingToken(t *testing.T) {
	gate := newGateTokenSource()
	s := newPriorityScheduler(gate.wait, newRaceSafeClock(), time.Hour)

	// A blocker occupies the slot.
	blockerCtx, blockerCancel := context.WithCancel(context.Background())
	defer blockerCancel()
	go func() { _ = s.wait(WithPriority(blockerCtx, PriorityNormal), PriorityNormal) }()
	waitFor(t, func() bool { return gate.enteredCount() == 1 }, "blocker to occupy the slot")

	// A HIGH waiter parks, then its context is cancelled while still parked.
	waiterCtx, waiterCancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.wait(WithPriority(waiterCtx, PriorityHigh), PriorityHigh) }()
	waitFor(t, func() bool { return s.pendingCount() == 1 }, "HIGH waiter to park")

	waiterCancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter should return context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled waiter hung instead of bailing")
	}

	// It must not have consumed a token (never entered the gate).
	if got := gate.enteredCount(); got != 1 {
		t.Fatalf("cancelled waiter consumed a token: gate entered %d times, want 1 (blocker only)", got)
	}

	// The slot is intact: the blocker can still be released and the scheduler
	// goes idle.
	waitFor(t, func() bool { return s.pendingCount() == 0 }, "no waiters left after cancel")
	gate.grant()
}

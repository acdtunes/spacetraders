package api

import (
	"context"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// defaultPriorityAgingWindow bounds how long any single waiter can be bypassed by
// higher-priority waiters. Once a waiter has been parked at least this long its
// effective priority is promoted to the top tier, so it can be bypassed only by
// waiters that were ALREADY ahead of it (a finite, draining set). This is the
// no-starvation guarantee: at 2 req/s a deprioritised poll resolves within
// roughly this window plus the drain of whatever was queued ahead of it.
const defaultPriorityAgingWindow = 2 * time.Second

// priorityWaiter is one parked acquirer waiting for the token-acquisition slot.
type priorityWaiter struct {
	priority   Priority
	enqueuedAt time.Time
	// ready is buffered(1) and receives exactly one signal when this waiter is
	// selected. Buffered so leave() never blocks handing off the slot even if the
	// waiter is simultaneously bailing on a cancelled context.
	ready chan struct{}
}

// priorityScheduler serialises access to a single token source (the shared
// rate.Limiter) so that, under contention, the highest-priority parked acquirer
// is admitted next. It NEVER creates, destroys, or re-rates tokens: every token
// still comes from acquireToken (the real limiter's Wait), so the ceiling, burst,
// and refill are exactly those of the underlying limiter. The scheduler only
// reorders who calls acquireToken next.
//
// Concurrency model: at most one acquirer holds the slot (busy) and is calling
// acquireToken at a time; all others are parked in waiters. When the holder is
// done it hands the slot to the highest-priority parked waiter. Because a
// saturated limiter releases exactly one token per (1/rate) seconds and each
// held acquireToken call consumes exactly one, serial admission preserves the
// aggregate rate; when the limiter is NOT saturated, admission is a few mutex ops
// so the burst drains essentially as fast as the un-prioritised path.
type priorityScheduler struct {
	acquireToken func(context.Context) error
	clock        shared.Clock
	agingWindow  time.Duration

	mu      sync.Mutex
	busy    bool
	waiters []*priorityWaiter
}

func newPriorityScheduler(acquireToken func(context.Context) error, clock shared.Clock, agingWindow time.Duration) *priorityScheduler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	if agingWindow <= 0 {
		agingWindow = defaultPriorityAgingWindow
	}
	return &priorityScheduler{
		acquireToken: acquireToken,
		clock:        clock,
		agingWindow:  agingWindow,
	}
}

// wait admits the caller in priority order, acquires exactly one token from the
// underlying limiter, then releases the slot to the next waiter. If ctx is
// cancelled while the caller is still parked, wait returns ctx.Err() WITHOUT
// consuming a token.
func (s *priorityScheduler) wait(ctx context.Context, p Priority) error {
	if err := s.enter(ctx, p); err != nil {
		return err
	}
	defer s.leave()
	return s.acquireToken(ctx)
}

// enter blocks until the caller owns the token-acquisition slot, or ctx is done.
func (s *priorityScheduler) enter(ctx context.Context, p Priority) error {
	s.mu.Lock()
	if !s.busy {
		// Uncontended: take the slot and go straight to the limiter. This keeps
		// the common (non-saturated) case free of any queueing overhead.
		s.busy = true
		s.mu.Unlock()
		return nil
	}
	w := &priorityWaiter{priority: p, enqueuedAt: s.clock.Now(), ready: make(chan struct{}, 1)}
	s.waiters = append(s.waiters, w)
	s.mu.Unlock()

	select {
	case <-w.ready:
		// Admitted: the slot was handed to us (busy is still true); we own it.
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		if s.removeWaiterLocked(w) {
			// Still parked: nobody handed us the slot, so we can bail cleanly
			// without consuming a token or leaving the slot dangling.
			s.mu.Unlock()
			return ctx.Err()
		}
		// Raced with leave(): we were selected concurrently, so the slot WAS
		// handed to us. Drain the signal and pass the slot on so no one hangs.
		s.mu.Unlock()
		<-w.ready
		s.leave()
		return ctx.Err()
	}
}

// leave releases the slot, handing it to the highest-priority parked waiter, or
// marking the scheduler idle if none remain.
func (s *priorityScheduler) leave() {
	s.mu.Lock()
	next := s.selectNextLocked()
	if next == nil {
		s.busy = false
		s.mu.Unlock()
		return
	}
	// busy stays true: ownership moves to next.
	s.mu.Unlock()
	next.ready <- struct{}{}
}

// selectNextLocked removes and returns the highest-priority parked waiter, with
// ties broken by earliest enqueue (FIFO within a tier).
func (s *priorityScheduler) selectNextLocked() *priorityWaiter {
	if len(s.waiters) == 0 {
		return nil
	}
	now := s.clock.Now()
	bestIdx := 0
	for i := 1; i < len(s.waiters); i++ {
		if s.moreUrgent(s.waiters[i], s.waiters[bestIdx], now) {
			bestIdx = i
		}
	}
	w := s.waiters[bestIdx]
	s.waiters = append(s.waiters[:bestIdx], s.waiters[bestIdx+1:]...)
	return w
}

// effectivePriority is the waiter's tier for selection purposes. Bounded aging:
// once a waiter has been parked at least agingWindow it is promoted to the top
// tier. This is the no-starvation guarantee — a promoted waiter can then be
// bypassed only by waiters with an EARLIER enqueue time (a finite set that drains
// as tokens are issued and never grows, since enqueue times only move forward),
// so every waiter is admitted within a bounded time regardless of how much
// higher-priority load arrives after it.
func (s *priorityScheduler) effectivePriority(w *priorityWaiter, now time.Time) Priority {
	if now.Sub(w.enqueuedAt) >= s.agingWindow {
		return PriorityHigh
	}
	return w.priority
}

// moreUrgent reports whether a should be admitted before b: higher effective
// tier wins; within the same tier the earlier-enqueued waiter wins (FIFO).
func (s *priorityScheduler) moreUrgent(a, b *priorityWaiter, now time.Time) bool {
	ea, eb := s.effectivePriority(a, now), s.effectivePriority(b, now)
	if ea != eb {
		return ea > eb
	}
	return a.enqueuedAt.Before(b.enqueuedAt)
}

func (s *priorityScheduler) removeWaiterLocked(target *priorityWaiter) bool {
	for i, w := range s.waiters {
		if w == target {
			s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
			return true
		}
	}
	return false
}

// pendingCount reports how many acquirers are currently parked (not counting the
// one that holds the slot). Used for observability and for deterministic test
// synchronisation.
func (s *priorityScheduler) pendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.waiters)
}

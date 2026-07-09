package metrics

import (
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// retentionWindow bounds how long a recorded event is kept in memory. It must
// be at least as wide as the widest window apibudget.ComputeDualReport
// computes over (currently 5 minutes) or Report() would silently under-count.
const retentionWindow = 5 * time.Minute

// APIBudgetTracker is the live, in-memory adapter that accumulates API
// request events off the request path (internal/adapters/api) and answers
// apibudget.DualReport snapshots on demand (CLI/gRPC reads). It is the
// concrete collaborator SpaceTradersClient records into; internal/domain/apibudget
// stays pure and knows nothing about how events arrive or how long they live.
//
// Like the Prometheus collectors in this package, recording is best-effort: a
// nil receiver must never panic the request path it is instrumenting.
type APIBudgetTracker struct {
	mu               sync.Mutex
	events           []apibudget.Event
	clock            shared.Clock
	ceilingReqPerSec float64
}

// NewAPIBudgetTracker constructs a tracker against the given rate-limiter
// ceiling (sustained requests/sec). clock defaults to the real clock when nil,
// matching the SpaceTradersClient DI convention.
func NewAPIBudgetTracker(ceilingReqPerSec float64, clock shared.Clock) *APIBudgetTracker {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &APIBudgetTracker{
		clock:            clock,
		ceilingReqPerSec: ceilingReqPerSec,
	}
}

// Record appends one observed API attempt. Safe to call on a nil receiver.
func (t *APIBudgetTracker) Record(hull string, purpose apibudget.Purpose, rateLimited bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()
	t.events = append(t.events, apibudget.Event{
		Hull:        hull,
		Purpose:     purpose,
		Timestamp:   now,
		RateLimited: rateLimited,
	})
	t.pruneLocked(now)
}

// Report computes the current DualReport snapshot from retained events. Safe
// to call on a nil receiver (returns the zero-value DualReport).
func (t *APIBudgetTracker) Report() apibudget.DualReport {
	if t == nil {
		return apibudget.DualReport{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()
	t.pruneLocked(now)
	// ComputeDualReport takes a snapshot copy implicitly since apibudget.Event
	// is a value type iterated by value inside ComputeReport; t.events is not
	// mutated by the call.
	return apibudget.ComputeDualReport(t.events, now, t.ceilingReqPerSec)
}

// pruneLocked drops events older than retentionWindow. Caller must hold t.mu.
func (t *APIBudgetTracker) pruneLocked(now time.Time) {
	cutoff := now.Add(-retentionWindow)
	kept := t.events[:0]
	for _, e := range t.events {
		if !e.Timestamp.Before(cutoff) {
			kept = append(kept, e)
		}
	}
	t.events = kept
}

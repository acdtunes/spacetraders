package metrics

import (
	"slices"
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
	// startedAt is when this tracker began observing, and it is what keeps the reported rate honest
	// while the tracker is younger than its widest window.
	//
	// The events slice cannot supply it. An empty-then-busy tracker and a five-minute-idle-then-busy
	// tracker hold identical events, yet the first has observed almost nothing and the second has
	// observed five minutes of genuine quiet — and quiet belongs in the denominator. Only the
	// construction time distinguishes them.
	//
	// This state is per-PROCESS and in-memory by nature: the tracker is rebuilt on every daemon
	// start, so its observation window genuinely restarts. That is precisely why it must be
	// recorded rather than assumed — the assumption that it had always been running is what
	// reported 7.7% against a saturated account.
	startedAt time.Time
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
		startedAt:        clock.Now(),
	}
}

// Record appends one observed API attempt and the wait it queued for a token. Nil-safe.
func (t *APIBudgetTracker) Record(hull string, purpose apibudget.Purpose, source apibudget.Source, rateLimited bool, rateLimitWait time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()
	t.events = append(t.events, apibudget.Event{
		Hull:          hull,
		Purpose:       purpose,
		Source:        source,
		Timestamp:     now,
		RateLimited:   rateLimited,
		RateLimitWait: rateLimitWait,
	})
	t.pruneLocked(now)
}

// NonSourceRate is the observed request rate (req/s) of every attempt whose
// Source is NOT in excluded, over the trailing window. It answers "how much of
// the ceiling is everyone else using", which is the residual the sensing budget
// is sized against.
//
// Untagged attempts (apibudget.SourceUnspecified) always count as
// non-excluded, so an untagged call path can only shrink the sensing budget,
// never inflate it. window is clamped to retentionWindow — the tracker cannot
// answer for a span longer than it retains, and silently reporting a rate
// diluted by unretained time would understate the competition. Safe to call on
// a nil receiver (returns 0).
func (t *APIBudgetTracker) NonSourceRate(window time.Duration, excluded ...apibudget.Source) float64 {
	if t == nil || window <= 0 {
		return 0
	}
	if window > retentionWindow {
		window = retentionWindow
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()
	t.pruneLocked(now)

	cutoff := now.Add(-window)
	count := 0
	for _, e := range t.events {
		if e.Timestamp.Before(cutoff) || e.Timestamp.After(now) {
			continue
		}
		if slices.Contains(excluded, e.Source) {
			continue
		}
		count++
	}
	return float64(count) / window.Seconds()
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
	return apibudget.ComputeDualReport(t.events, now, t.ceilingReqPerSec, t.startedAt)
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

// globalAPIBudgetTracker is the singleton API request-budget tracker. Set
// by SetGlobalAPIBudgetTracker() at daemon startup; the API
// client falls back to it when no per-instance tracker was injected, the
// same pattern getMetricsCollector() uses for globalAPICollector.
var globalAPIBudgetTracker *APIBudgetTracker

// SetGlobalAPIBudgetTracker sets the global API request-budget tracker.
// Pass nil to clear it (e.g. in test cleanup).
func SetGlobalAPIBudgetTracker(tracker *APIBudgetTracker) {
	globalAPIBudgetTracker = tracker
}

// GetGlobalAPIBudgetTracker returns the global API request-budget tracker.
// Returns nil if it was never set.
func GetGlobalAPIBudgetTracker() *APIBudgetTracker {
	return globalAPIBudgetTracker
}

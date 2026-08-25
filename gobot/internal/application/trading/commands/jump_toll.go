package commands

import (
	"context"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// jump_toll.go serves the tour solver the fleet's REAL per-gate-hop travel cost, learned
// from the hops it has flown, so a crossing is priced by what jumping currently costs rather
// than by a constant fitted once. The estimator and the reasoning behind its shape live in
// trading.EstimatePerHopTollSeconds; this file is the caching read path.
//
// FAIL-OPEN AT EVERY LAYER. No samples, too few samples, an unreadable table, an unwired
// reader: all yield 0, the caller drops a zero from the request, and the solver falls back to
// its env override or its fitted default. A pricing refinement, not a money guard — there is
// no spend to fail closed on (RULINGS #4 untouched).

// JumpTollReader supplies the measured per-gate-hop travel charge, in seconds, a tour is
// planned against. It returns a plain int rather than an error because an unknown toll is not
// a failure: 0 means "no opinion" and is the fail-open value.
type JumpTollReader interface {
	PerHopTollSeconds(ctx context.Context, playerID int) int
}

// jumpTollCacheTTL is how long a computed toll is reused before the window is re-read.
// SHORTER THAN THE GATE-FEE TABLE'S 30 MINUTES: a gate fee is a constant of the map, so a
// stale table is not a wrong one, but a toll is a reading of current traffic and the whole
// reason it is measured is that it moves. Five minutes tracks a shift within one estimator
// bucket while still collapsing the many solves a minute the fleet runs into one read.
const jumpTollCacheTTL = 5 * time.Minute

// jumpTollReadLimit bounds one window read. The estimator decays by age, so the oldest rows
// of a busy day contribute almost nothing and the cap costs accuracy that rounds to zero.
// Sized well above a day's observed hop count, so it is a backstop, not routine truncation.
const jumpTollReadLimit = 20000

// LedgerJumpTollReader estimates the toll from recorded hops and caches it.
//
// NO PERSISTED ESTIMATE (RULINGS #2). The rolling value is never written down: it is a pure
// function of durable rows inside a window, so a restart recovers it on the first solve after
// boot with no reload path to get wrong. The cache is an optimisation with a TTL, not state.
type LedgerJumpTollReader struct {
	repo   trading.JumpTollRepository
	clock  shared.Clock
	params trading.JumpTollParams

	mu     sync.Mutex
	cached map[int]jumpTollSnapshot
}

type jumpTollSnapshot struct {
	seconds   int
	expiresAt time.Time
}

// NewLedgerJumpTollReader wires the sample-backed reader. A nil clock means the real one,
// matching the `nil = use RealClock` idiom the daemon's other constructors take.
func NewLedgerJumpTollReader(
	repo trading.JumpTollRepository, clock shared.Clock, params trading.JumpTollParams,
) *LedgerJumpTollReader {
	if clock == nil {
		clock = &shared.RealClock{}
	}
	return &LedgerJumpTollReader{
		repo:   repo,
		clock:  clock,
		params: params,
		cached: make(map[int]jumpTollSnapshot),
	}
}

// PerHopTollSeconds returns the cached toll, re-estimating it when the entry has expired.
// 0 means the fleet has not measured enough hops to have an opinion.
func (r *LedgerJumpTollReader) PerHopTollSeconds(ctx context.Context, playerID int) int {
	if r == nil || r.repo == nil || r.clock == nil {
		return 0
	}
	now := r.clock.Now()

	r.mu.Lock()
	snap, ok := r.cached[playerID]
	r.mu.Unlock()
	if ok && now.Before(snap.expiresAt) {
		return snap.seconds
	}

	samples, err := r.repo.RecentJumpTolls(ctx, playerID, now.Add(-r.params.Window), jumpTollReadLimit)
	if err != nil {
		// A read that failed proves nothing about how long jumps take. Serve the last
		// estimate if we still hold one — it was measured, which the fitted default was too,
		// only more recently — and otherwise say nothing and let the default price the tour.
		if ok {
			return snap.seconds
		}
		return 0
	}

	seconds, estimated := trading.EstimatePerHopTollSeconds(samples, now, r.params)
	if !estimated {
		seconds = 0
	}

	r.mu.Lock()
	r.cached[playerID] = jumpTollSnapshot{seconds: seconds, expiresAt: now.Add(jumpTollCacheTTL)}
	r.mu.Unlock()
	return seconds
}

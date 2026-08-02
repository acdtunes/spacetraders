package commands

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// gate_fees.go learns the per-DEPARTURE-SYSTEM jump-gate fee from the ledger and hands it
// to the tour solver, so a crossing is priced by the gate it actually leaves instead of by
// one fleet-wide constant.
//
// WHY THIS IS A TABLE AND NOT A BETTER CONSTANT (sp-9idvn). The fee is a property of the
// departure gate: corr(fee, distance) = 0.124, the origin system explains 99.7% of the
// variance, and the same edge measured 6,760 credits one way against 5,313 the other. The
// flat charge is unbiased in aggregate (+236/jump against realised tour crossings) but
// carries a 15.2% mean absolute error on any INDIVIDUAL crossing, which is the error that
// orders candidates against each other.
//
// WHERE THE MODEL HOLDS, AND WHERE IT DOES NOT. A per-gate CONSTANT is only as good as the
// within-gate spread, so that was measured rather than assumed: 71% of jump traffic crosses
// gates whose fee varies under 5% about their own mean, 24% between 5-15%, and 4.7% — three
// gates — above 15%. Traffic-weighted that is roughly a 5% error against the flat charge's
// 15.2%, so the table is a clear improvement in aggregate WITHOUT being uniformly better.
// On those three noisy gates a per-gate mean buys little and may cost a little.
//
// THE FAILURE MODE IS NOT WHAT IT LOOKS LIKE. The obvious worry is that a table concentrates
// error on rarely-crossed gates. It does the opposite: a gate is in the table only if we have
// actually flown it, so anything rare is ABSENT and falls back to the flat charge — exactly
// what it was priced at before. Coverage is traffic-weighted by construction. The residual
// error sits on a few heavily-crossed, genuinely variable gates, which is why this reader
// averages a 7-day window rather than trusting a single observation.

// GateFeeReader supplies the per-departure-system fee table a tour is planned against.
//
// It returns a map rather than an error because a missing table is not a failure: every
// consumer already falls back to the flat charge, and a tour must never fail to plan
// because a pricing refinement could not be read.
type GateFeeReader interface {
	GateFees(ctx context.Context, playerID int) map[string]int64
}

// gateFeeLookbackWindow bounds how far back the aggregate scans. It is a COST bound, not a
// freshness one — see LedgerGateFeeReader's staleness note.
const gateFeeLookbackWindow = 7 * 24 * time.Hour

// gateFeeCacheTTL is how long a learned table is reused before it is re-read.
//
// GENEROUS ON PURPOSE. A gate's fee is a constant of the map, not a market price, so a
// stale table is not a wrong one — the only thing staleness costs is that a gate crossed
// for the first time within the last TTL is still priced at the flat fallback, which is
// exactly what it was priced at before this file existed. Set against that, a tour solve
// happens many times a minute per hull, and re-running a grouped aggregate over a week of
// ledger rows on every solve would be a self-inflicted load problem on a box that is
// already oversubscribed.
const gateFeeCacheTTL = 30 * time.Minute

// LedgerGateFeeReader learns the table from recorded jumps and caches it.
//
// FAIL-OPEN, AND DELIBERATELY SO. Every failure path — an unreadable ledger, an empty
// ledger, a player with no jump history — yields an empty table, and an empty table means
// every crossing prices at the flat charge. That is byte-identical to the behaviour before
// this reader existed, so the worst case of the whole feature is that it does nothing.
// This is a pricing refinement, not a money guard; there is no spend to fail closed on.
type LedgerGateFeeReader struct {
	repo  ledger.GateFeeAggregator
	clock shared.Clock

	mu     sync.Mutex
	cached map[int]gateFeeSnapshot
}

type gateFeeSnapshot struct {
	fees      map[string]int64
	expiresAt time.Time
}

// NewLedgerGateFeeReader wires the ledger-backed reader. A nil clock means the real one,
// matching the `nil = use RealClock` idiom the daemon's other constructors take.
func NewLedgerGateFeeReader(repo ledger.GateFeeAggregator, clock shared.Clock) *LedgerGateFeeReader {
	if clock == nil {
		clock = &shared.RealClock{}
	}
	return &LedgerGateFeeReader{
		repo:   repo,
		clock:  clock,
		cached: make(map[int]gateFeeSnapshot),
	}
}

// GateFees returns the cached table, re-reading it when the entry has expired.
func (r *LedgerGateFeeReader) GateFees(ctx context.Context, playerID int) map[string]int64 {
	if r == nil || r.repo == nil || r.clock == nil {
		return nil
	}
	// The coordinator carries a plain-int identity; the repository wants the value object.
	// An identity that will not parse cannot address a ledger, so there is nothing to learn.
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil
	}
	now := r.clock.Now()

	r.mu.Lock()
	snap, ok := r.cached[playerID]
	r.mu.Unlock()
	if ok && now.Before(snap.expiresAt) {
		return snap.fees
	}

	fees, err := r.repo.PerOriginGateFees(ctx, pid, now.Add(-gateFeeLookbackWindow))
	if err != nil {
		// A read that failed proves nothing about gate prices. Serve the last good table
		// if we still hold one — it is a table of constants, so an expired copy is a
		// better estimate than none — and otherwise fall back to the flat charge.
		if ok {
			return snap.fees
		}
		return nil
	}

	r.mu.Lock()
	r.cached[playerID] = gateFeeSnapshot{fees: fees, expiresAt: now.Add(gateFeeCacheTTL)}
	r.mu.Unlock()
	return fees
}

// gateFeeConstraints converts a learned table into the wire shape, sorted for reproducible
// payloads.
//
// A non-positive fee is DROPPED rather than sent. The solver already ignores one, so this
// is belt-and-braces on the single value that must never reach the objective: a zero fee
// makes a crossing free and biases every candidate toward crossing, which is the exact
// defect the fee term exists to close.
func gateFeeConstraints(fees map[string]int64) []routing.GateFee {
	if len(fees) == 0 {
		return nil
	}
	out := make([]routing.GateFee, 0, len(fees))
	for system, fee := range fees {
		if system == "" || fee <= 0 {
			continue
		}
		out = append(out, routing.GateFee{System: system, FeeCredits: fee})
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].System < out[j].System })
	return out
}

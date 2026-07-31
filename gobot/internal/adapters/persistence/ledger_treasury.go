package persistence

// ledger_treasury.go — the money guards' treasury read, served from the LEDGER instead of
// the API (sp-muq66).
//
// WHY. `Get Agent` measured 0.167 req/s — 8.3% of the 2.00 req/s ceiling — and did NOT
// fall when request coalescing (singleflight) landed. That was predicted: the reads are
// invalidation-driven, not concurrent duplicates. Every buy, sell, refuel and jump empties
// the agent cache, so nearly every money-guard read is forced cold and no window or TTL
// collapses them. The only way to remove them is to stop asking the API.
//
// WHY THE LEDGER IS ALLOWED TO ANSWER. Every credit-moving transaction records the agent's
// balance AFTER it, re-anchored in-band from the API's own `data.agent.credits` whenever
// the call returns it (see RecordTransactionHandler's authoritative-balance path). The
// completeness question — is some credit-decreasing path NOT recorded? — was settled by
// measurement rather than argument: across 3,086 consecutive transaction intervals over
// 3 hours the unrecorded-spend gap was exactly ZERO and the ledger balance matched the
// live API to the credit. (The same measurement before the jump-fee accounting fix read
// −7,036,882 over 4 hours, which is what a genuinely incomplete ledger looks like.)
//
// WHAT IS STILL A RESIDUAL, and why the freshness bound exists anyway:
//
//   - Nothing outside this daemon is in the ledger. A manual CLI spend, or a second
//     writer, moves credits the ledger never sees.
//   - The row is written just AFTER the API call it describes returns, so a transaction in
//     flight is momentarily unrecorded and the ledger reads stale-HIGH by that one
//     transaction. Milliseconds wide, and bounded by a single tranche — but it is the
//     stale direction that matters to a money guard, so it is named here rather than
//     assumed away. The absolute reserve floor and the per-buy cap still sit underneath.
//
// The freshness bound is what keeps both bounded: past it, the ledger is not trusted and
// the live read answers.
//
// FAILURE IS NEVER CONVERTED INTO A STALE SUCCESS (RULINGS #4). When the ledger cannot be
// trusted AND the live read fails, Credits returns an ERROR. It never returns the stale
// ledger value, and it never returns 0. Its callers read a treasury failure as "trade
// nothing this episode" — a guard that went blind must not size a buy.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// DefaultTreasuryMaxAge is how old the newest ledger row may be before the treasury read
// stops trusting it and falls back to the live API call.
//
// 30s, from 1,202 measured inter-transaction gaps on a 52-hull fleet: p50 3.51s, p95
// 18.98s, p99 40.84s, max 141.32s. At 30s about 2% of reads fall back; at 60s under 1%.
//
// CARRY THE CAVEAT: those are BUSY-fleet gaps. On a quiet or cold fleet the gaps are
// unbounded, so nearly every read correctly falls back and the live path becomes the
// COMMON path — which is exactly why the fallback is the coalesced, short-TTL-cached
// GetAgent rather than anything more expensive. Do not re-tune this bound on busy-fleet
// data alone; the treasury_reads_total{source} split is there to show which regime the
// fleet is actually in.
const DefaultTreasuryMaxAge = 30 * time.Second

// LiveCredits is the live fallback: the agent's credits as the API reports them right now.
// Narrow on purpose — the ledger reader has no business knowing about tokens, agents or
// HTTP. The daemon satisfies it with the coalesced, short-TTL-cached GetAgent path.
type LiveCredits interface {
	Credits(ctx context.Context) (int64, error)
}

// LedgerTreasury answers "how many credits does this player have" from the transaction
// ledger, falling back to a live read when the ledger is too old, empty or unreadable.
//
//	latest balance + age ──▶ within bound ? use it (NO API call)
//	                                      : coalesced live read
//	                     ──▶ both fail    ? ERROR (never 0, never the stale value)
type LedgerTreasury struct {
	db     *gorm.DB
	live   LiveCredits
	clock  shared.Clock
	maxAge time.Duration
}

// NewLedgerTreasury wires the ledger-first treasury reader. maxAge <= 0 selects
// DefaultTreasuryMaxAge (the same resolve-at-use idiom the client's agent-cache TTL uses).
// Either dependency may be nil — a nil db reads live only, a nil live reader has no
// fallback and fails closed once the ledger goes stale — but a reader with neither can
// only ever return an error, which is the safe direction.
func NewLedgerTreasury(db *gorm.DB, live LiveCredits, clock shared.Clock, maxAge time.Duration) *LedgerTreasury {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &LedgerTreasury{db: db, live: live, clock: clock, maxAge: maxAge}
}

// resolvedMaxAge reports the effective freshness bound.
func (t *LedgerTreasury) resolvedMaxAge() time.Duration {
	if t.maxAge <= 0 {
		return DefaultTreasuryMaxAge
	}
	return t.maxAge
}

// Credits reports the player's current credit balance.
//
// On the common path it makes NO API call: the newest ledger row is within the freshness
// bound and its balance_after is returned as-is. Otherwise the live read answers. If both
// are unavailable it returns an error — never a zero, never the stale ledger value.
func (t *LedgerTreasury) Credits(ctx context.Context, playerID int) (int64, error) {
	credits, fresh, why := t.fromLedger(ctx, playerID)
	if fresh {
		metrics.RecordTreasuryRead(metrics.TreasuryReadLedger)
		return credits, nil
	}

	if t.live != nil {
		live, err := t.live.Credits(ctx)
		if err == nil {
			metrics.RecordTreasuryRead(metrics.TreasuryReadLive)
			return live, nil
		}
		metrics.RecordTreasuryRead(metrics.TreasuryReadError)
		return 0, fmt.Errorf("treasury unreadable: ledger %s; live read failed: %w", why, err)
	}

	metrics.RecordTreasuryRead(metrics.TreasuryReadError)
	return 0, fmt.Errorf("treasury unreadable: ledger %s; no live fallback wired", why)
}

// fromLedger reads the newest transaction for the player and reports whether its
// balance_after may be trusted. The returned reason describes why it may NOT be, and is
// carried into the error when the live fallback also fails, so a blind guard says which
// half of the read was missing rather than just "unreadable".
//
// EMPTY IS NOT ZERO. A fresh era legitimately has an empty ledger, and 0 credits is itself
// a legitimate balance, so the two must be distinguishable — hence First + ErrRecordNotFound
// rather than Find. Find into a single struct returns (zero value, nil error) on an empty
// result, which here would report a bankrupt agent as a SUCCESSFUL read. Both existing
// precedents in this repo have that shape (creditsFromLatestBalance and EraRepository's
// latest-balance fallback); they are harmless where they sit — a wake gate and era
// accounting — and must not be copied into a money guard. Find into a SLICE is fine: len()
// makes the empty case observable.
func (t *LedgerTreasury) fromLedger(ctx context.Context, playerID int) (credits int64, fresh bool, why string) {
	if t.db == nil {
		return 0, false, "not wired"
	}

	var latest TransactionModel
	err := t.db.WithContext(ctx).
		Where("player_id = ?", playerID).
		Order("timestamp DESC, created_at DESC, id DESC").
		First(&latest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, "empty"
	}
	if err != nil {
		return 0, false, fmt.Sprintf("read failed: %v", err)
	}

	// Age off the transaction's own timestamp, which is the instant the balance was
	// true. A row inserted with a timestamp behind its insert time therefore reads
	// OLDER and falls back to live — the safe direction. A future-stamped row (clock
	// skew) reads as age <= 0, i.e. fresh; it is still the newest known balance, so
	// there is nothing staler to prefer over it.
	age := t.clock.Now().Sub(latest.Timestamp)
	if bound := t.resolvedMaxAge(); age > bound {
		return int64(latest.BalanceAfter), false, fmt.Sprintf("stale (%s old, bound %s)", age.Truncate(time.Millisecond), bound)
	}
	return int64(latest.BalanceAfter), true, ""
}

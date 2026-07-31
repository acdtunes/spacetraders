package persistence_test

// ledger_treasury_test.go — the acceptance battery for the ledger-backed treasury read
// (sp-muq66). The falsifier is NOT "fewer Get Agent calls"; it is the four properties
// below, one test each, plus the traps that drew blood in prior lanes.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// countingLive is the live fallback, instrumented so a test can assert the COMMON PATH
// MAKES NO API CALL rather than merely assert the value it returned.
type countingLive struct {
	credits int64
	err     error
	calls   int
}

func (c *countingLive) Credits(context.Context) (int64, error) {
	c.calls++
	if c.err != nil {
		return 0, c.err
	}
	return c.credits, nil
}

// treasuryFixture builds an in-memory ledger with a player row and a clock pinned to now.
func treasuryFixture(t *testing.T) (*gorm.DB, int, *shared.MockClock) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := persistence.PlayerModel{AgentSymbol: "LEDGER-TREASURY", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)}
	return db, p.ID, clock
}

// record inserts one ledger row at a controlled timestamp with a known balance_after.
func record(t *testing.T, db *gorm.DB, id string, playerID int, balanceAfter int, at time.Time) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: id, PlayerID: playerID, Timestamp: at, CreatedAt: at,
		TransactionType: "SELL_CARGO", Category: "trading", Amount: 1,
		BalanceBefore: balanceAfter - 1, BalanceAfter: balanceAfter,
	}).Error)
}

// ACCEPTANCE 1: the common path makes NO API call and returns the ledger value.
func TestLedgerTreasury_FreshLedgerServesWithoutCallingTheAPI(t *testing.T) {
	db, pid, clock := treasuryFixture(t)
	live := &countingLive{credits: 999_999_999}
	record(t, db, "tx-fresh", pid, 46_151_915, clock.Now().Add(-5*time.Second))

	tr := persistence.NewLedgerTreasury(db, live, clock, 0)
	got, err := tr.Credits(context.Background(), pid)

	require.NoError(t, err)
	require.Equal(t, int64(46_151_915), got, "the ledger's balance_after, not the live reader's value")
	require.Zero(t, live.calls, "the common path must make NO API call")
}

// ACCEPTANCE 2a: a ledger row OLDER than the bound triggers the live fallback.
func TestLedgerTreasury_StaleLedgerFallsBackToLive(t *testing.T) {
	db, pid, clock := treasuryFixture(t)
	live := &countingLive{credits: 7_000_000}
	record(t, db, "tx-stale", pid, 1_234_567, clock.Now().Add(-persistence.DefaultTreasuryMaxAge-time.Second))

	tr := persistence.NewLedgerTreasury(db, live, clock, 0)
	got, err := tr.Credits(context.Background(), pid)

	require.NoError(t, err)
	require.Equal(t, int64(7_000_000), got, "the LIVE value answers; the stale ledger value must not")
	require.Equal(t, 1, live.calls, "exactly one fallback read")
}

// ACCEPTANCE 2b: probe the boundary. Exactly AT the bound is fresh (age > bound is the
// rejection test); one nanosecond past it is stale. A mutation from > to >= flips the
// first of these, and a mutation of the bound itself flips the second.
func TestLedgerTreasury_FreshnessBoundaryIsProbedOnBothSides(t *testing.T) {
	bound := persistence.DefaultTreasuryMaxAge

	t.Run("exactly at the bound is FRESH", func(t *testing.T) {
		db, pid, clock := treasuryFixture(t)
		live := &countingLive{credits: 42}
		record(t, db, "tx-at-bound", pid, 500_000, clock.Now().Add(-bound))

		got, err := persistence.NewLedgerTreasury(db, live, clock, 0).Credits(context.Background(), pid)

		require.NoError(t, err)
		require.Equal(t, int64(500_000), got)
		require.Zero(t, live.calls, "age == bound is within the bound — no API call")
	})

	t.Run("one nanosecond past the bound is STALE", func(t *testing.T) {
		db, pid, clock := treasuryFixture(t)
		live := &countingLive{credits: 42}
		record(t, db, "tx-past-bound", pid, 500_000, clock.Now().Add(-bound-time.Nanosecond))

		got, err := persistence.NewLedgerTreasury(db, live, clock, 0).Credits(context.Background(), pid)

		require.NoError(t, err)
		require.Equal(t, int64(42), got, "the live value answers")
		require.Equal(t, 1, live.calls)
	})
}

// The bound is a MEASURED number, not a free parameter: 30s sits between p95 (18.98s) and
// p99 (40.84s) of 1,202 inter-transaction gaps on a busy 52-hull fleet, which is what puts
// the fallback rate near 2% there. Pinned explicitly because every OTHER test in this file
// expresses ages relative to the symbol and so cannot see the value move — widening it
// silently widens how stale a balance a money guard will spend against.
func TestLedgerTreasury_DefaultBoundIsTheMeasuredThirtySeconds(t *testing.T) {
	require.Equal(t, 30*time.Second, persistence.DefaultTreasuryMaxAge,
		"30s is fitted to measured inter-transaction gaps (p95 18.98s, p99 40.84s); changing it changes how stale a balance the money guards will spend against")
}

// ...and the same claim stated in ABSOLUTE ages, so it holds independently of the symbol:
// a 20s-old balance (inside p95) is trusted, a 45s-old one (past p99) is not.
func TestLedgerTreasury_AbsoluteAgesEitherSideOfTheMeasuredBound(t *testing.T) {
	t.Run("20s old is trusted", func(t *testing.T) {
		db, pid, clock := treasuryFixture(t)
		live := &countingLive{credits: 1}
		record(t, db, "tx-20s", pid, 20_000_000, clock.Now().Add(-20*time.Second))

		got, err := persistence.NewLedgerTreasury(db, live, clock, 0).Credits(context.Background(), pid)

		require.NoError(t, err)
		require.Equal(t, int64(20_000_000), got)
		require.Zero(t, live.calls, "inside p95 of the measured gap distribution — no API call")
	})

	t.Run("45s old is not", func(t *testing.T) {
		db, pid, clock := treasuryFixture(t)
		live := &countingLive{credits: 1}
		record(t, db, "tx-45s", pid, 20_000_000, clock.Now().Add(-45*time.Second))

		got, err := persistence.NewLedgerTreasury(db, live, clock, 0).Credits(context.Background(), pid)

		require.NoError(t, err)
		require.Equal(t, int64(1), got, "past p99 — the live read answers")
		require.Equal(t, 1, live.calls)
	})
}

// ACCEPTANCE 3 — THE MONEY-GUARD TEST. Ledger unreadable AND the fallback failing must
// propagate an ERROR. A retained value or a 0 here silently re-opens the guard: callers
// read a treasury failure as "trade nothing this episode", and a 0 or a stale number
// instead lets a buy be sized against a treasury nobody read.
func TestLedgerTreasury_TotalFailurePropagatesErrorNeverZero(t *testing.T) {
	t.Run("empty ledger + failing live", func(t *testing.T) {
		db, pid, clock := treasuryFixture(t)
		live := &countingLive{err: errors.New("429 rate limited")}

		got, err := persistence.NewLedgerTreasury(db, live, clock, 0).Credits(context.Background(), pid)

		require.Error(t, err, "an unreadable treasury must ERROR, never report a balance")
		require.Zero(t, got, "the value is meaningless on error and must not be mistaken for a balance")
		require.Contains(t, err.Error(), "empty", "the error names which half of the read was missing")
		require.Contains(t, err.Error(), "429 rate limited", "and carries the live cause")
	})

	t.Run("STALE ledger + failing live is never converted into a stale success", func(t *testing.T) {
		db, pid, clock := treasuryFixture(t)
		live := &countingLive{err: errors.New("connection reset")}
		record(t, db, "tx-stale-nofallback", pid, 88_888_888, clock.Now().Add(-10*time.Minute))

		got, err := persistence.NewLedgerTreasury(db, live, clock, 0).Credits(context.Background(), pid)

		require.Error(t, err, "RULINGS #4: a failed read must never become a stale success")
		require.NotEqual(t, int64(88_888_888), got, "the stale ledger balance must NOT be handed back")
		require.Zero(t, got)
	})

	t.Run("stale ledger + no live fallback wired", func(t *testing.T) {
		db, pid, clock := treasuryFixture(t)
		record(t, db, "tx-stale-nil-live", pid, 55_555, clock.Now().Add(-time.Hour))

		got, err := persistence.NewLedgerTreasury(db, nil, clock, 0).Credits(context.Background(), pid)

		require.Error(t, err)
		require.Zero(t, got)
	})
}

// TRAP: EMPTY MUST BE DISTINGUISHABLE FROM ZERO. A fresh era legitimately has an empty
// ledger, and 0 credits is itself a legitimate balance. `Find` into a single struct
// returns (zero value, nil error) on an empty result — zero credits reported as a
// SUCCESSFUL read — and BOTH existing precedents in this repo have that shape. This test
// fails if this reader is ever "cleaned up" to match them.
func TestLedgerTreasury_EmptyLedgerIsNotAZeroBalance(t *testing.T) {
	db, pid, clock := treasuryFixture(t)
	live := &countingLive{credits: 3_000_000}

	got, err := persistence.NewLedgerTreasury(db, live, clock, 0).Credits(context.Background(), pid)

	require.NoError(t, err)
	require.Equal(t, int64(3_000_000), got, "an empty ledger defers to live; it does not report 0 credits")
	require.Equal(t, 1, live.calls)
}

// ...and the converse: a genuine 0 balance IS a successful read, served from the ledger.
func TestLedgerTreasury_AGenuineZeroBalanceIsAFreshLedgerSuccess(t *testing.T) {
	db, pid, clock := treasuryFixture(t)
	live := &countingLive{credits: 1_000_000}
	record(t, db, "tx-broke", pid, 0, clock.Now().Add(-time.Second))

	got, err := persistence.NewLedgerTreasury(db, live, clock, 0).Credits(context.Background(), pid)

	require.NoError(t, err)
	require.Zero(t, got, "a real 0 balance is a real answer")
	require.Zero(t, live.calls, "and it is served from the ledger, without an API call")
}

// The newest row by TIMESTAMP answers — not the lowest/highest primary key. GORM's First
// appends a primary-key ordering of its own, so the ids here are deliberately ordered
// AGAINST the timestamps: a read that fell back to key order would return the stale row.
func TestLedgerTreasury_ReadsTheNewestRowNotThePrimaryKeyOrder(t *testing.T) {
	db, pid, clock := treasuryFixture(t)
	live := &countingLive{credits: -1}
	record(t, db, "aaa-oldest", pid, 111, clock.Now().Add(-20*time.Second))
	record(t, db, "zzz-newest", pid, 999, clock.Now().Add(-1*time.Second))

	got, err := persistence.NewLedgerTreasury(db, live, clock, 0).Credits(context.Background(), pid)

	require.NoError(t, err)
	require.Equal(t, int64(999), got, "the newest row by timestamp")
	require.Zero(t, live.calls)
}

// The read is player-scoped: another player's newer row must never answer, and its
// presence must not make an empty ledger look populated.
func TestLedgerTreasury_IsScopedToThePlayer(t *testing.T) {
	db, pid, clock := treasuryFixture(t)
	other := persistence.PlayerModel{AgentSymbol: "OTHER-AGENT", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&other).Error)
	record(t, db, "tx-other-newer", other.ID, 777_777, clock.Now())

	live := &countingLive{credits: 2_500_000}
	got, err := persistence.NewLedgerTreasury(db, live, clock, 0).Credits(context.Background(), pid)

	require.NoError(t, err)
	require.Equal(t, int64(2_500_000), got, "our player's ledger is empty — fall back, do not read the other agent's balance")
	require.Equal(t, 1, live.calls)
}

// A nil db (no ledger surface at all) reads live rather than fabricating a balance.
func TestLedgerTreasury_NoLedgerSurfaceReadsLive(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now().UTC()}
	live := &countingLive{credits: 4_242}

	got, err := persistence.NewLedgerTreasury(nil, live, clock, 0).Credits(context.Background(), 1)

	require.NoError(t, err)
	require.Equal(t, int64(4_242), got)
	require.Equal(t, 1, live.calls)
}

// An explicit maxAge overrides the default; 0 selects the default.
func TestLedgerTreasury_ExplicitMaxAgeOverridesTheDefault(t *testing.T) {
	db, pid, clock := treasuryFixture(t)
	live := &countingLive{credits: 60}
	record(t, db, "tx-10s", pid, 10_000, clock.Now().Add(-10*time.Second))

	// 10s old, bound 5s → stale under the override, fresh under the 30s default.
	got, err := persistence.NewLedgerTreasury(db, live, clock, 5*time.Second).Credits(context.Background(), pid)
	require.NoError(t, err)
	require.Equal(t, int64(60), got)
	require.Equal(t, 1, live.calls)

	live.calls = 0
	got, err = persistence.NewLedgerTreasury(db, live, clock, 0).Credits(context.Background(), pid)
	require.NoError(t, err)
	require.Equal(t, int64(10_000), got)
	require.Zero(t, live.calls)
}

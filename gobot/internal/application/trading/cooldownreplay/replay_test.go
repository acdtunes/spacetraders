package cooldownreplay_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/trading/cooldownreplay"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	testSource = "X1-KP46-H51"
	testGood   = "IRON"
)

type stubHistory struct {
	rows []*ledger.Transaction
	err  error
	opts ledger.QueryOptions
}

func (s *stubHistory) FindByPlayer(_ context.Context, _ shared.PlayerID, opts ledger.QueryOptions) ([]*ledger.Transaction, error) {
	s.opts = opts
	return s.rows, s.err
}

type stubMarkets struct {
	volume int
	err    error
}

func (s *stubMarkets) GetMarketData(_ context.Context, waypoint string, _ int) (*market.Market, error) {
	if s.err != nil {
		return nil, s.err
	}
	good, err := market.NewTradeGood(testGood, nil, nil, 100, 50, s.volume, market.TradeType("EXPORT"))
	if err != nil {
		return nil, err
	}
	mkt, err := market.NewMarket(waypoint, []market.TradeGood{*good}, time.Now())
	if err != nil {
		return nil, err
	}
	return mkt, nil
}

func purchase(t *testing.T, operation string, units int, at time.Time) *ledger.Transaction {
	t.Helper()
	txn, err := ledger.NewTransaction(
		shared.MustNewPlayerID(1), at, ledger.TransactionType("PURCHASE_CARGO"),
		-1000, 100000, 99000, "PURCHASE cargo",
		map[string]interface{}{"waypoint": testSource, "good_symbol": testGood, "units": units},
		"", "", operation,
	)
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	return txn
}

func run(t *testing.T, l *trading.LaneCooldownLedger, h *stubHistory, m *stubMarkets, now time.Time) int {
	t.Helper()
	applied, err := cooldownreplay.Rebuild(context.Background(), l, h, m, shared.MustNewPlayerID(1), 24*time.Hour, now)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	return applied
}

// The point of the whole package: a fresh ledger comes back from boot already knowing what the fleet
// took out of this market, instead of waving the next buy through.
func TestRebuild_RestoresDrainAFreshLedgerWouldHaveForgotten(t *testing.T) {
	now := time.Now()
	l := trading.NewLaneCooldownLedger(0, 0, 0)
	key := trading.SourceDrainKey(testSource, testGood)
	if before := l.Debt(key, now); before != 0 {
		t.Fatalf("a fresh ledger must start empty, got %v", before)
	}

	applied := run(t, l,
		&stubHistory{rows: []*ledger.Transaction{
			purchase(t, "construction_supply", 60, now.Add(-30*time.Minute)),
			purchase(t, "construction_supply", 60, now.Add(-20*time.Minute)),
		}},
		&stubMarkets{volume: 60}, now)

	if applied != 2 {
		t.Fatalf("applied %d rows, want 2", applied)
	}
	// Two undecayed tranches must clear the pacing bound, or the replay restored nothing usable.
	if debt := l.Debt(key, now); debt <= l.TrancheDebt() {
		t.Fatalf("restored debt %v does not clear the bound %v — the guard is nominally persistent and effectively still amnesiac", debt, l.TrancheDebt())
	}
}

// Only the operation the pacing consult itself accrues from is replayed; rebuilding a wider debt
// than the running fleet accrues would make the replay disagree with live behaviour immediately.
func TestRebuild_ReplaysOnlyTheOperationThatAccrues(t *testing.T) {
	now := time.Now()
	l := trading.NewLaneCooldownLedger(0, 0, 0)

	applied := run(t, l,
		&stubHistory{rows: []*ledger.Transaction{
			purchase(t, "trade_route", 60, now.Add(-30*time.Minute)),
			purchase(t, "arb_run", 60, now.Add(-25*time.Minute)),
			purchase(t, "construction_supply", 60, now.Add(-20*time.Minute)),
		}},
		&stubMarkets{volume: 60}, now)

	if applied != 1 {
		t.Fatalf("applied %d rows, want 1 — only the construction feed's purchases accrue to this key", applied)
	}
}

// THE DOUBLE-COUNT GUARD. A key the running fleet has already touched is left alone, so the replay
// cannot inflate live debt — the over-arm arriving through the fix rather than the bug.
func TestRebuild_NeverAddsToAKeyThatAlreadyCarriesDebt(t *testing.T) {
	now := time.Now()
	l := trading.NewLaneCooldownLedger(0, 0, 0)
	key := trading.SourceDrainKey(testSource, testGood)
	l.Accrue(key, 60, 60, now)
	live := l.Debt(key, now)

	applied := run(t, l,
		&stubHistory{rows: []*ledger.Transaction{purchase(t, "construction_supply", 60, now.Add(-10*time.Minute))}},
		&stubMarkets{volume: 60}, now)

	if applied != 0 {
		t.Fatalf("applied %d rows onto a key already carrying debt", applied)
	}
	if after := l.Debt(key, now); after != live {
		t.Fatalf("debt moved from %v to %v; a replay must never add to live debt", live, after)
	}
}

// Running it twice must be a no-op the second time, so the correctness does not depend on the boot
// ordering holding forever.
func TestRebuild_IsIdempotent(t *testing.T) {
	now := time.Now()
	l := trading.NewLaneCooldownLedger(0, 0, 0)
	key := trading.SourceDrainKey(testSource, testGood)
	rows := []*ledger.Transaction{purchase(t, "construction_supply", 60, now.Add(-10*time.Minute))}

	run(t, l, &stubHistory{rows: rows}, &stubMarkets{volume: 60}, now)
	first := l.Debt(key, now)
	applied := run(t, l, &stubHistory{rows: rows}, &stubMarkets{volume: 60}, now)

	if applied != 0 {
		t.Fatalf("second replay applied %d rows", applied)
	}
	if second := l.Debt(key, now); second != first {
		t.Fatalf("debt moved from %v to %v on a second replay", first, second)
	}
}

// The trade-volume floor: a purchase larger than today's listed volume proves the volume was once
// bigger, and the replay must size against the LARGER figure — overstating volume understates debt,
// which is the only safe direction when the true figure is unrecoverable.
func TestRebuild_SizesAgainstTheLargestObservedCallNotAShrunkenVolume(t *testing.T) {
	now := time.Now()
	loose := trading.NewLaneCooldownLedger(0, 0, 0)
	tight := trading.NewLaneCooldownLedger(0, 0, 0)
	rows := []*ledger.Transaction{purchase(t, "construction_supply", 180, now.Add(-10*time.Minute))}

	// Today's volume has collapsed to 60; the row proves 180 was once possible in one call.
	run(t, loose, &stubHistory{rows: rows}, &stubMarkets{volume: 60}, now)
	// A market whose volume genuinely is 180 sizes identically — the floor is not an inflation.
	run(t, tight, &stubHistory{rows: rows}, &stubMarkets{volume: 180}, now)

	key := trading.SourceDrainKey(testSource, testGood)
	if got, want := loose.Debt(key, now), tight.Debt(key, now); got != want {
		t.Fatalf("debt %v vs %v; a shrunken listed volume must not inflate the replayed debt", got, want)
	}
}

// A market whose depth cannot be read is skipped rather than sized against a guess.
func TestRebuild_SkipsAMarketItCannotSize(t *testing.T) {
	now := time.Now()
	l := trading.NewLaneCooldownLedger(0, 0, 0)

	applied := run(t, l,
		&stubHistory{rows: []*ledger.Transaction{purchase(t, "construction_supply", 0, now.Add(-10*time.Minute))}},
		&stubMarkets{err: errors.New("no market")}, now)

	if applied != 0 {
		t.Fatalf("applied %d rows for a market with no readable depth", applied)
	}
}

// A history read that fails is returned, never swallowed into a silently empty replay.
func TestRebuild_PropagatesAHistoryReadFailure(t *testing.T) {
	_, err := cooldownreplay.Rebuild(context.Background(),
		trading.NewLaneCooldownLedger(0, 0, 0),
		&stubHistory{err: errors.New("db down")}, &stubMarkets{volume: 60},
		shared.MustNewPlayerID(1), 24*time.Hour, time.Now())
	if err == nil {
		t.Fatalf("a failed history read must be reported, not read as 'nothing was drained'")
	}
}

// The window is asked of the repository rather than filtered afterwards, so a long history does not
// have to be pulled into memory to be discarded.
func TestRebuild_BoundsTheQueryToTheWindow(t *testing.T) {
	now := time.Now()
	h := &stubHistory{}
	run(t, trading.NewLaneCooldownLedger(0, 0, 0), h, &stubMarkets{volume: 60}, now)

	if h.opts.StartDate == nil {
		t.Fatalf("the replay must bound its own query window")
	}
	if want := now.Add(-24 * time.Hour); !h.opts.StartDate.Equal(want) {
		t.Fatalf("query window starts %v, want %v", h.opts.StartDate, want)
	}
	if h.opts.TransactionType == nil || string(*h.opts.TransactionType) != "PURCHASE_CARGO" {
		t.Fatalf("the replay must ask only for purchases, got %v", h.opts.TransactionType)
	}
}

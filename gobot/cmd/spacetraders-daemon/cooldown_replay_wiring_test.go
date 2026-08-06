package main

// THE REPLAY MUST NOT BE ABLE TO FAIL THE BOOT.
//
// The first version of this wiring read the configured player id through the panicking
// constructor. cfg.Captain.PlayerID is config-only and is never assigned at runtime, so on any
// deployment without a captain player it is zero — and the daemon panicked in run() before it ever
// listened. Every unit test of the replay itself was green, because they all call Rebuild directly
// with a valid id: thorough coverage of a function says nothing about where it is plugged in.
//
// So these test the WIRING, not the function. The source check below pins the call site; these pin
// that the seam survives the inputs a real boot can hand it.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainTrading "github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

type replayStubHistory struct {
	rows []*ledger.Transaction
	err  error
}

func (s *replayStubHistory) FindByPlayer(context.Context, shared.PlayerID, ledger.QueryOptions) ([]*ledger.Transaction, error) {
	return s.rows, s.err
}

type replayStubMarkets struct{ err error }

func (s *replayStubMarkets) GetMarketData(_ context.Context, waypoint string, _ int) (*market.Market, error) {
	if s.err != nil {
		return nil, s.err
	}
	good, err := market.NewTradeGood("IRON", nil, nil, 100, 50, 60, market.TradeType("EXPORT"))
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypoint, []market.TradeGood{*good}, time.Now())
}

func replayPurchase(t *testing.T, units int, at time.Time) *ledger.Transaction {
	t.Helper()
	txn, err := ledger.NewTransaction(
		shared.MustNewPlayerID(1), at, ledger.TransactionType("PURCHASE_CARGO"),
		-1000, 100000, 99000, "PURCHASE cargo",
		map[string]interface{}{"waypoint": "X1-KP46-H51", "good_symbol": "IRON", "units": units},
		"", "", "construction_supply",
	)
	require.NoError(t, err)
	return txn
}

// THE REGRESSION. An unconfigured player id is an ordinary deployment, not an error — the daemon
// runs with captain.enabled false and no player_id in staging — and it must boot.
func TestReplayLaneCooldown_AnUnconfiguredPlayerIDDoesNotPanic(t *testing.T) {
	l := domainTrading.NewLaneCooldownLedger(0, 0, 0)

	require.NotPanics(t, func() {
		replayed := replayLaneCooldown(context.Background(), l,
			&replayStubHistory{}, &replayStubMarkets{}, 0, 750*time.Minute, time.Now())
		require.Zero(t, replayed, "no player means nothing to replay, not a panic")
	}, "a zero player id is what every captain-less deployment passes; panicking here kills the daemon before it listens")
}

// Best-effort means best-effort: a history that cannot be read leaves the ledger empty, which is
// exactly the behaviour before the replay existed.
func TestReplayLaneCooldown_AnUnreadableHistoryStillBoots(t *testing.T) {
	l := domainTrading.NewLaneCooldownLedger(0, 0, 0)

	require.NotPanics(t, func() {
		replayed := replayLaneCooldown(context.Background(), l,
			&replayStubHistory{err: errors.New("db down")}, &replayStubMarkets{}, 1, 750*time.Minute, time.Now())
		require.Zero(t, replayed)
	})
}

// A market whose depth cannot be read is skipped, not fatal.
func TestReplayLaneCooldown_AnUnreadableMarketStillBoots(t *testing.T) {
	now := time.Now()
	l := domainTrading.NewLaneCooldownLedger(0, 0, 0)

	require.NotPanics(t, func() {
		replayLaneCooldown(context.Background(), l,
			&replayStubHistory{rows: []*ledger.Transaction{replayPurchase(t, 60, now.Add(-time.Hour))}},
			&replayStubMarkets{err: errors.New("no market")}, 1, 750*time.Minute, now)
	})
}

// NON-VACUITY. The three tests above would all pass against a helper that did nothing at all, so
// this pins that a well-formed boot actually restores debt.
func TestReplayLaneCooldown_AWellFormedBootRestoresDebt(t *testing.T) {
	now := time.Now()
	l := domainTrading.NewLaneCooldownLedger(0, 0, 0)

	replayed := replayLaneCooldown(context.Background(), l,
		&replayStubHistory{rows: []*ledger.Transaction{
			replayPurchase(t, 60, now.Add(-30*time.Minute)),
			replayPurchase(t, 60, now.Add(-20*time.Minute)),
		}},
		&replayStubMarkets{}, 1, 750*time.Minute, now)

	require.Equal(t, 2, replayed)
	key := domainTrading.SourceDrainKey("X1-KP46-H51", "IRON")
	require.Greater(t, l.Debt(key, now), l.TrancheDebt(),
		"a boot that restores nothing usable is the amnesia this exists to repair")
}

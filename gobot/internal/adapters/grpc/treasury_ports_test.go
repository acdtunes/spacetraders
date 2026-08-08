package grpc

// treasury_ports_test.go — the autosizer/contract-scaler treasury port reads the LEDGER
// (sp-muq66), and an unreadable treasury still fails its cushion guard CLOSED.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// countingAgentReader stands in for the API client so a test can assert the ledger path
// made NO call rather than merely returned the right number.
type countingAgentReader struct {
	credits int
	err     error
	calls   int
}

func (c *countingAgentReader) GetAgent(context.Context, string) (*player.AgentData, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return &player.AgentData{Credits: c.credits}, nil
}

// ledgerWithRow builds an in-memory ledger holding one transaction of the given age.
func ledgerWithRow(t *testing.T, balanceAfter int, age time.Duration, api *countingAgentReader) (*persistence.LedgerTreasury, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := persistence.PlayerModel{AgentSymbol: "AUTOSIZER-TREASURY", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)}
	require.NoError(t, db.Create(&persistence.TransactionModel{
		ID: "tx-1", PlayerID: p.ID, Timestamp: clock.Now().Add(-age), CreatedAt: clock.Now().Add(-age),
		TransactionType: "SELL_CARGO", Category: "trading", Amount: 1,
		BalanceBefore: balanceAfter - 1, BalanceAfter: balanceAfter,
	}).Error)
	return persistence.NewLedgerTreasury(db, &liveAgentCredits{api: api}, clock, 0), p.ID
}

// A fresh ledger answers the cushion guard, and the API is not called.
func TestAutosizerTreasuryReader_PrefersTheLedger(t *testing.T) {
	api := &countingAgentReader{credits: 999_999_999}
	ledger, pid := ledgerWithRow(t, 12_345_678, 5*time.Second, api)
	r := &fleetTreasuryReader{api: api, ledger: ledger}

	credits, readable, err := r.Treasury(auth.WithPlayerToken(context.Background(), "TOK"), pid)

	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, int64(12_345_678), credits, "the ledger's balance, not the API client's")
	require.Zero(t, api.calls, "the common path must make NO API call")
}

// A stale ledger falls through to the live read, which is why the port keeps an API client
// at all — on a quiet fleet this is the common path.
func TestAutosizerTreasuryReader_StaleLedgerFallsBackToLive(t *testing.T) {
	api := &countingAgentReader{credits: 3_000_000}
	ledger, pid := ledgerWithRow(t, 1, time.Hour, api)
	r := &fleetTreasuryReader{api: api, ledger: ledger}

	credits, readable, err := r.Treasury(auth.WithPlayerToken(context.Background(), "TOK"), pid)

	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, int64(3_000_000), credits)
	require.Equal(t, 1, api.calls)
}

// RULINGS #4: both sources down reports readable=false, so the cushion guard refuses to
// buy. It must never report a readable 0 — that would be a balance the guard would size
// against.
func TestAutosizerTreasuryReader_TotalFailureIsUnreadableNotZero(t *testing.T) {
	api := &countingAgentReader{err: errors.New("429")}
	ledger, pid := ledgerWithRow(t, 9_000_000, time.Hour, api)
	r := &fleetTreasuryReader{api: api, ledger: ledger}

	credits, readable, err := r.Treasury(auth.WithPlayerToken(context.Background(), "TOK"), pid)

	require.NoError(t, err, "the port reports unreadability through the flag, not an error")
	require.False(t, readable, "an unreadable treasury must fail the guard CLOSED")
	require.Zero(t, credits)
}

// With no ledger wired the port keeps the direct live read it has always made — the path
// every test that constructs this type bare still exercises.
func TestAutosizerTreasuryReader_NoLedgerKeepsTheDirectLiveRead(t *testing.T) {
	api := &countingAgentReader{credits: 777}
	r := &fleetTreasuryReader{api: api}

	credits, readable, err := r.Treasury(auth.WithPlayerToken(context.Background(), "TOK"), 1)

	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, int64(777), credits)
	require.Equal(t, 1, api.calls)
}

// liveAgentCredits turns every failure into an ERROR — never a zero — because the ledger
// reader treats a nil error as a usable balance.
func TestLiveAgentCredits_EveryFailureIsAnError(t *testing.T) {
	withToken := auth.WithPlayerToken(context.Background(), "TOK")

	_, err := (&liveAgentCredits{api: &countingAgentReader{err: errors.New("boom")}}).Credits(withToken)
	require.Error(t, err, "an erroring GetAgent")

	_, err = (&liveAgentCredits{api: &countingAgentReader{credits: 5}}).Credits(context.Background())
	require.Error(t, err, "no player token in ctx")

	_, err = (&liveAgentCredits{}).Credits(withToken)
	require.Error(t, err, "no API client wired")

	got, err := (&liveAgentCredits{api: &countingAgentReader{credits: 5}}).Credits(withToken)
	require.NoError(t, err)
	require.Equal(t, int64(5), got)
}

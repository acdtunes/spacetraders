package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// RealizedCashRate sums SELL_CARGO(+) / PURCHASE_CARGO(−) / REFUEL(−) over [since, now),
// player- and window-scoped, and derives the duty-cycle $/hr. It reconciles to the
// treasury — the cash-true KPI sp-461l will switch the telemetry-netting consumers onto.
func TestRealizedCashRate_SumsCargoFlowsInWindow(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormTransactionRepository(db)
	ctx := context.Background()

	// A player row must exist (transactions FK players.id, enforced in the test harness).
	player := persistence.PlayerModel{AgentSymbol: "CASH-RATE", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	pid := player.ID

	base := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	since := base
	now := base.Add(4 * time.Hour)

	// createRow inserts one cargo/refuel row at a controlled created_at.
	createRow := func(id, txType, opType string, amount int, at time.Time) {
		require.NoError(t, db.Create(&persistence.TransactionModel{
			ID: id, PlayerID: pid, Timestamp: at, CreatedAt: at,
			TransactionType: txType, Category: "test", Amount: amount, OperationType: opType,
		}).Error)
	}

	// In-window tour round-trip: +100k sell, −40k buy, −1k refuel ⇒ net 59,000 over 4h.
	createRow("tx-sell-tour", "SELL_CARGO", "tour", 100_000, base.Add(1*time.Hour))
	createRow("tx-buy-tour", "PURCHASE_CARGO", "tour", -40_000, base.Add(90*time.Minute))
	createRow("tx-refuel-tour", "REFUEL", "tour", -1_000, base.Add(2*time.Hour))
	// In-window but a DIFFERENT operation (arbitrage) — included only in the unscoped read.
	createRow("tx-sell-arb", "SELL_CARGO", "arbitrage", 50_000, base.Add(1*time.Hour))
	// Out of window (before since / at-or-after now) — excluded from both reads.
	createRow("tx-sell-old", "SELL_CARGO", "tour", 999_999, base.Add(-1*time.Hour))
	createRow("tx-sell-future", "SELL_CARGO", "tour", 888_888, base.Add(5*time.Hour))
	// Wrong type (not a cargo/refuel cash flow) — excluded from both reads.
	createRow("tx-contract", "CONTRACT_ACCEPTED", "tour", 7_000, base.Add(1*time.Hour))

	// Tour-scoped: the drop-in replacement for the tour telemetry-netting rate.
	tour, err := repo.RealizedCashRate(ctx, pid, since, now, "tour")
	require.NoError(t, err)
	require.True(t, tour.Readable, "a window with tour cargo transactions must be readable")
	require.Equal(t, int64(59_000), tour.NetCredits, "net = +100k sell −40k buy −1k refuel")
	require.Equal(t, 3, tour.TxCount, "only the three in-window tour cargo/refuel rows")
	require.InDelta(t, 14_750.0, tour.CreditsPerHour, 1e-6, "59,000 / 4h")

	// Unscoped (operationType ""): includes the arbitrage sell too ⇒ net 109,000, count 4.
	all, err := repo.RealizedCashRate(ctx, pid, since, now, "")
	require.NoError(t, err)
	require.Equal(t, int64(109_000), all.NetCredits, "adds the +50k arbitrage sell")
	require.Equal(t, 4, all.TxCount)
	require.InDelta(t, 27_250.0, all.CreditsPerHour, 1e-6, "109,000 / 4h")
}

// An empty window (no cargo transactions) fails closed — Readable false, zero rate — so a
// consumer never steers on a fabricated 0/hr.
func TestRealizedCashRate_EmptyWindowFailsClosed(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormTransactionRepository(db)
	ctx := context.Background()

	player := persistence.PlayerModel{AgentSymbol: "CASH-RATE-EMPTY", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)

	base := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	got, err := repo.RealizedCashRate(ctx, player.ID, base, base.Add(6*time.Hour), "tour")
	require.NoError(t, err)
	require.False(t, got.Readable, "an empty window must fail closed")
	require.Equal(t, int64(0), got.NetCredits)
	require.Equal(t, 0, got.TxCount)
	require.Zero(t, got.CreditsPerHour)
}

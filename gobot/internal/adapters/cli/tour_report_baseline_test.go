package cli

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// The single-lane graduation baseline is net trade credits over the window ÷ hours,
// filtered operation_type <> 'tour'. This is the end-to-end proof the bead exists for
// (sp-lgnh): with tour-tagged and non-tour trade rows both present, the baseline
// counts exactly the non-tour rows — the tour's own trades no longer inflate the
// number the tour is measured against.
func TestGormTourReportSource_TradeCreditsPerHourExcludesTourRows(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	p := persistence.PlayerModel{AgentSymbol: "AGT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&p).Error)
	playerID := p.ID

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	since := now.Add(-10 * time.Hour) // 10-hour window
	at := now.Add(-1 * time.Hour)     // inside the window

	seed := func(id, txType, opType string, amount int) {
		require.NoError(t, db.Create(&persistence.TransactionModel{
			ID: id, PlayerID: playerID, Timestamp: at, TransactionType: txType,
			Category: "TRADING_REVENUE", Amount: amount, BalanceBefore: 0, BalanceAfter: amount,
			OperationType: opType,
		}).Error)
	}
	// Non-tour single-lane trade activity: net +4000 across the window.
	seed("t-trade-sell", "SELL_CARGO", "trade_route", 5000)
	seed("t-manual-buy", "PURCHASE_CARGO", "manual", -1000)
	// Tour activity (net +6000) — must be EXCLUDED so the tour isn't measured against itself.
	seed("t-tour-sell", "SELL_CARGO", "tour", 9000)
	seed("t-tour-buy", "PURCHASE_CARGO", "tour", -3000)

	src := &gormTourReportSource{db: db, now: now}
	baseline, ok, err := src.TradeCreditsPerHour(context.Background(), playerID, since)
	require.NoError(t, err)
	require.True(t, ok, "non-tour trade activity is present, so a baseline is available")

	// (5000 - 1000) / 10h = 400. Had the tour rows leaked in it would be 10000/10 = 1000.
	require.Equal(t, 400.0, baseline, "baseline must exclude the +6000 of tour activity")
}

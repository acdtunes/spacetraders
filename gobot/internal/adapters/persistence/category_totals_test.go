package persistence_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

type categorizedTx struct {
	playerID shared.PlayerID
	at       time.Time
	category string
	amount   int
}

func seedCategorizedTransactions(t *testing.T, rows ...categorizedTx) *persistence.GormTransactionRepository {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	for _, pid := range []shared.PlayerID{player, otherPlayer} {
		require.NoError(t, db.Create(&persistence.PlayerModel{
			ID:          pid.Value(),
			AgentSymbol: fmt.Sprintf("CATEGORY-TOTALS-%d", pid.Value()),
			Token:       "tok",
			CreatedAt:   time.Now(),
		}).Error)
	}

	for i, row := range rows {
		require.NoError(t, db.Create(&persistence.TransactionModel{
			ID:              fmt.Sprintf("tx-category-totals-%d", i),
			PlayerID:        row.playerID.Value(),
			Timestamp:       row.at,
			CreatedAt:       row.at,
			TransactionType: "SELL_CARGO",
			Category:        row.category,
			Amount:          row.amount,
			BalanceBefore:   0,
			BalanceAfter:    row.amount,
		}).Error)
	}

	return persistence.NewGormTransactionRepository(db)
}

// The aggregate must match what summing every row by hand would give — this is the whole
// point of trading a full fetch for a GROUP BY.
func TestCategoryTotals_SumsSignedAmountsPerCategory(t *testing.T) {
	repo := seedCategorizedTransactions(t,
		categorizedTx{player, hoursAgo(0.5), "TRADING_REVENUE", 100_000},
		categorizedTx{player, hoursAgo(0.4), "TRADING_REVENUE", 50_000},
		categorizedTx{player, hoursAgo(0.3), "TRADING_COSTS", -80_000},
	)
	totals, err := repo.CategoryTotals(ctx(), player, time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(150_000), totals["TRADING_REVENUE"])
	require.Equal(t, int64(-80_000), totals["TRADING_COSTS"])
}

// A row outside [since, until] must not move the total — the aggregate has to windowize the
// same way the old row-by-row fetch did.
func TestCategoryTotals_IgnoresRowsOutsideTheWindow(t *testing.T) {
	repo := seedCategorizedTransactions(t,
		categorizedTx{player, hoursAgo(3), "TRADING_REVENUE", 9_000_000}, // long gone
		categorizedTx{player, hoursAgo(0.5), "TRADING_REVENUE", 400_000},
	)
	totals, err := repo.CategoryTotals(ctx(), player, time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(400_000), totals["TRADING_REVENUE"])
}

// Scoped per player, like every other read on this table.
func TestCategoryTotals_IsPlayerScoped(t *testing.T) {
	repo := seedCategorizedTransactions(t,
		categorizedTx{player, hoursAgo(0.5), "TRADING_REVENUE", 400_000},
		categorizedTx{otherPlayer, hoursAgo(0.5), "TRADING_REVENUE", 9_000_000},
	)
	totals, err := repo.CategoryTotals(ctx(), player, time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(400_000), totals["TRADING_REVENUE"])
}

// A category absent from the window is absent from the map — not a zero entry, matching
// CategoryTotalsReader's documented contract.
func TestCategoryTotals_EmptyWindowReturnsEmptyMap(t *testing.T) {
	repo := seedCategorizedTransactions(t)
	totals, err := repo.CategoryTotals(ctx(), player, time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	require.Empty(t, totals)
}

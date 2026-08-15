package queries_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type fakeCategoryTotalsReader struct {
	totals map[string]int64
	err    error
}

func (f *fakeCategoryTotalsReader) CategoryTotals(
	_ context.Context, _ shared.PlayerID, _, _ time.Time,
) (map[string]int64, error) {
	return f.totals, f.err
}

// The handler's revenue/expense split must match Category.IsIncome, not the sign of the
// aggregate — TRADING_REVENUE is income even though the row-level amounts that built it were
// positive, and TRADING_COSTS is an expense even though its total arrives already negative.
func TestGetProfitLoss_SplitsRevenueAndExpenseByCategory(t *testing.T) {
	reader := &fakeCategoryTotalsReader{totals: map[string]int64{
		"TRADING_REVENUE":  150_000,
		"CONTRACT_REVENUE": 20_000,
		"TRADING_COSTS":    -80_000,
		"FUEL_COSTS":       -5_000,
	}}
	handler := queries.NewGetProfitLossHandler(reader)

	resp, err := handler.Handle(context.Background(), &queries.GetProfitLossQuery{
		PlayerID:  7,
		StartDate: time.Now().Add(-time.Hour),
		EndDate:   time.Now(),
	})
	require.NoError(t, err)

	pl, ok := resp.(*queries.GetProfitLossResponse)
	require.True(t, ok)
	require.Equal(t, 170_000, pl.TotalRevenue)
	require.Equal(t, 85_000, pl.TotalExpenses)
	require.Equal(t, 85_000, pl.NetProfit)
	require.Equal(t, 150_000, pl.RevenueBreakdown["TRADING_REVENUE"])
	require.Equal(t, 20_000, pl.RevenueBreakdown["CONTRACT_REVENUE"])
	require.Equal(t, 80_000, pl.ExpenseBreakdown["TRADING_COSTS"])
	require.Equal(t, 5_000, pl.ExpenseBreakdown["FUEL_COSTS"])
}

// An empty window is a valid zero statement, not an error.
func TestGetProfitLoss_EmptyTotalsIsZeroNotError(t *testing.T) {
	handler := queries.NewGetProfitLossHandler(&fakeCategoryTotalsReader{totals: map[string]int64{}})

	resp, err := handler.Handle(context.Background(), &queries.GetProfitLossQuery{
		PlayerID: 7, StartDate: time.Now().Add(-time.Hour), EndDate: time.Now(),
	})
	require.NoError(t, err)
	pl := resp.(*queries.GetProfitLossResponse)
	require.Zero(t, pl.TotalRevenue)
	require.Zero(t, pl.TotalExpenses)
	require.Zero(t, pl.NetProfit)
}

func TestGetProfitLoss_RejectsInvalidPlayerID(t *testing.T) {
	handler := queries.NewGetProfitLossHandler(&fakeCategoryTotalsReader{totals: map[string]int64{}})
	_, err := handler.Handle(context.Background(), &queries.GetProfitLossQuery{PlayerID: 0})
	require.Error(t, err)
}

func TestGetProfitLoss_RejectsWrongRequestType(t *testing.T) {
	handler := queries.NewGetProfitLossHandler(&fakeCategoryTotalsReader{totals: map[string]int64{}})
	_, err := handler.Handle(context.Background(), struct{}{})
	require.Error(t, err)
}

// A repository failure must surface, not silently report a zero statement.
func TestGetProfitLoss_PropagatesRepositoryError(t *testing.T) {
	handler := queries.NewGetProfitLossHandler(&fakeCategoryTotalsReader{err: errors.New("db unavailable")})
	_, err := handler.Handle(context.Background(), &queries.GetProfitLossQuery{
		PlayerID: 7, StartDate: time.Now().Add(-time.Hour), EndDate: time.Now(),
	})
	require.Error(t, err)
}

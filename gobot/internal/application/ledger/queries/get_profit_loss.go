package queries

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// GetProfitLossQuery represents a query to generate a profit & loss statement
type GetProfitLossQuery struct {
	PlayerID  int
	StartDate time.Time
	EndDate   time.Time
}

// GetProfitLossResponse represents the profit & loss statement result
type GetProfitLossResponse struct {
	Period           string
	TotalRevenue     int
	TotalExpenses    int
	NetProfit        int
	RevenueBreakdown map[string]int // category -> amount
	ExpenseBreakdown map[string]int // category -> amount
}

// GetProfitLossHandler handles the GetProfitLoss query
type GetProfitLossHandler struct {
	categoryTotals ledger.CategoryTotalsReader
}

// NewGetProfitLossHandler creates a new GetProfitLossHandler
func NewGetProfitLossHandler(categoryTotals ledger.CategoryTotalsReader) *GetProfitLossHandler {
	return &GetProfitLossHandler{
		categoryTotals: categoryTotals,
	}
}

// Handle executes the GetProfitLoss query
func (h *GetProfitLossHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*GetProfitLossQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *GetProfitLossQuery")
	}

	playerID, err := shared.NewPlayerID(query.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID: %w", err)
	}

	totals, err := h.categoryTotals.CategoryTotals(ctx, playerID, query.StartDate, query.EndDate)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate category totals: %w", err)
	}

	return h.calculateProfitLoss(query, totals), nil
}

func (h *GetProfitLossHandler) calculateProfitLoss(
	query *GetProfitLossQuery,
	totals map[string]int64,
) *GetProfitLossResponse {
	revenueBreakdown := make(map[string]int)
	expenseBreakdown := make(map[string]int)
	totalRevenue := 0
	totalExpenses := 0

	for category, total := range totals {
		amount := int(total)

		if ledger.Category(category).IsIncome() {
			revenueBreakdown[category] += amount
			totalRevenue += amount
		} else {
			// Store as positive value for clarity in expense breakdown
			expenseBreakdown[category] += -amount
			totalExpenses += -amount // Keep as positive for total expenses
		}
	}

	netProfit := totalRevenue - totalExpenses

	return &GetProfitLossResponse{
		Period:           formatPeriod(query.StartDate, query.EndDate),
		TotalRevenue:     totalRevenue,
		TotalExpenses:    totalExpenses,
		NetProfit:        netProfit,
		RevenueBreakdown: revenueBreakdown,
		ExpenseBreakdown: expenseBreakdown,
	}
}

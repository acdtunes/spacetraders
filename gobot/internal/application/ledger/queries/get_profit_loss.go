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
	transactionRepo ledger.TransactionRepository
}

// NewGetProfitLossHandler creates a new GetProfitLossHandler
func NewGetProfitLossHandler(transactionRepo ledger.TransactionRepository) *GetProfitLossHandler {
	return &GetProfitLossHandler{
		transactionRepo: transactionRepo,
	}
}

// Handle executes the GetProfitLoss query
func (h *GetProfitLossHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*GetProfitLossQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *GetProfitLossQuery")
	}

	// Resolve player ID
	playerID, err := shared.NewPlayerID(query.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID: %w", err)
	}

	// Query all transactions in date range
	opts := ledger.QueryOptions{
		StartDate: &query.StartDate,
		EndDate:   &query.EndDate,
		Limit:     0, // No limit - get all transactions
	}

	transactions, err := h.transactionRepo.FindByPlayer(ctx, playerID, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query transactions: %w", err)
	}

	// Calculate P&L
	return h.calculateProfitLoss(query, transactions), nil
}

func (h *GetProfitLossHandler) calculateProfitLoss(
	query *GetProfitLossQuery,
	transactions []*ledger.Transaction,
) *GetProfitLossResponse {
	revenueBreakdown := make(map[string]int)
	expenseBreakdown := make(map[string]int)
	totalRevenue := 0
	totalExpenses := 0

	// Group and sum by category
	for _, tx := range transactions {
		category := tx.Category().String()
		amount := tx.Amount()

		if tx.IsIncome() {
			revenueBreakdown[category] += amount
			totalRevenue += amount
		} else {
			// Store as positive value for clarity in expense breakdown
			expenseBreakdown[category] += -amount
			totalExpenses += -amount // Keep as positive for total expenses
		}
	}

	// Calculate net profit (revenue - expenses)
	netProfit := totalRevenue - totalExpenses

	// Format period string
	period := fmt.Sprintf("%s to %s",
		query.StartDate.Format("2006-01-02"),
		query.EndDate.Format("2006-01-02"))

	return &GetProfitLossResponse{
		Period:           period,
		TotalRevenue:     totalRevenue,
		TotalExpenses:    totalExpenses,
		NetProfit:        netProfit,
		RevenueBreakdown: revenueBreakdown,
		ExpenseBreakdown: expenseBreakdown,
	}
}

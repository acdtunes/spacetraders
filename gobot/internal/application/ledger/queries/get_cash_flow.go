package queries

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// GetCashFlowQuery represents a query to generate a cash flow statement
type GetCashFlowQuery struct {
	PlayerID  int
	StartDate time.Time
	EndDate   time.Time
	GroupBy   string // "category", "day", "week", "month" (currently only "category" is implemented)
}

// GetCashFlowResponse represents the cash flow statement result
type GetCashFlowResponse struct {
	Period     string
	Categories []*CategoryCashFlow
}

// CategoryCashFlow represents cash flow for a specific category
type CategoryCashFlow struct {
	Category     string
	TotalInflow  int
	TotalOutflow int
	NetFlow      int
	Transactions int // count
}

// GetCashFlowHandler handles the GetCashFlow query
type GetCashFlowHandler struct {
	transactionRepo ledger.TransactionRepository
}

// NewGetCashFlowHandler creates a new GetCashFlowHandler
func NewGetCashFlowHandler(transactionRepo ledger.TransactionRepository) *GetCashFlowHandler {
	return &GetCashFlowHandler{
		transactionRepo: transactionRepo,
	}
}

// Handle executes the GetCashFlow query
func (h *GetCashFlowHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*GetCashFlowQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *GetCashFlowQuery")
	}

	// Validate group by
	if query.GroupBy == "" {
		query.GroupBy = "category"
	}
	if query.GroupBy != "category" {
		return nil, fmt.Errorf("only 'category' grouping is currently supported")
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

	// Calculate cash flow
	return h.calculateCashFlow(query, transactions), nil
}

func (h *GetCashFlowHandler) calculateCashFlow(
	query *GetCashFlowQuery,
	transactions []*ledger.Transaction,
) *GetCashFlowResponse {
	// Group by category
	categoryMap := make(map[string]*CategoryCashFlow)

	// Initialize all categories to ensure they appear even with zero transactions
	for _, cat := range ledger.AllCategories() {
		categoryMap[cat.String()] = &CategoryCashFlow{
			Category:     cat.String(),
			TotalInflow:  0,
			TotalOutflow: 0,
			NetFlow:      0,
			Transactions: 0,
		}
	}

	// Aggregate transactions by category
	for _, tx := range transactions {
		category := tx.Category().String()
		amount := tx.Amount()

		flow := categoryMap[category]
		flow.Transactions++

		if amount > 0 {
			flow.TotalInflow += amount
		} else {
			flow.TotalOutflow += -amount // Store as positive value
		}

		flow.NetFlow = flow.TotalInflow - flow.TotalOutflow
	}

	// Convert map to slice (only include categories with transactions)
	categories := make([]*CategoryCashFlow, 0)
	for _, flow := range categoryMap {
		if flow.Transactions > 0 {
			categories = append(categories, flow)
		}
	}

	// Format period string
	period := fmt.Sprintf("%s to %s",
		query.StartDate.Format("2006-01-02"),
		query.EndDate.Format("2006-01-02"))

	return &GetCashFlowResponse{
		Period:     period,
		Categories: categories,
	}
}

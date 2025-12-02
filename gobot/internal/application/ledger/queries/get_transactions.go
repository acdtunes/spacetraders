package queries

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// GetTransactionsQuery represents a query to retrieve transactions
type GetTransactionsQuery struct {
	PlayerID          int
	StartDate         *time.Time
	EndDate           *time.Time
	Category          *string
	TransactionType   *string
	RelatedEntityType *string
	RelatedEntityID   *string
	Limit             int
	Offset            int
	OrderBy           string
}

// GetTransactionsResponse represents the result of the query
type GetTransactionsResponse struct {
	Transactions []*TransactionDTO
	Total        int
}

// TransactionDTO represents a transaction data transfer object
type TransactionDTO struct {
	ID                string
	PlayerID          int
	Timestamp         time.Time
	Type              string
	Category          string
	Amount            int
	BalanceBefore     int
	BalanceAfter      int
	Description       string
	Metadata          map[string]interface{}
	RelatedEntityType string
	RelatedEntityID   string
}

// GetTransactionsHandler handles the GetTransactions query
type GetTransactionsHandler struct {
	transactionRepo ledger.TransactionRepository
	playerResolver  *player.PlayerResolver
}

// NewGetTransactionsHandler creates a new GetTransactionsHandler
func NewGetTransactionsHandler(
	transactionRepo ledger.TransactionRepository,
	playerResolver *player.PlayerResolver,
) *GetTransactionsHandler {
	return &GetTransactionsHandler{
		transactionRepo: transactionRepo,
		playerResolver:  playerResolver,
	}
}

// Handle executes the GetTransactions query
func (h *GetTransactionsHandler) Handle(ctx context.Context, request mediator.Request) (mediator.Response, error) {
	query, ok := request.(*GetTransactionsQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *GetTransactionsQuery")
	}

	// Resolve player ID
	playerID, err := h.resolvePlayerID(query.PlayerID)
	if err != nil {
		return nil, err
	}

	// Build query options
	opts, err := h.buildQueryOptions(query)
	if err != nil {
		return nil, err
	}

	// Query transactions
	transactions, err := h.transactionRepo.FindByPlayer(ctx, playerID, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to query transactions: %w", err)
	}

	// Get total count
	total, err := h.transactionRepo.CountByPlayer(ctx, playerID, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to count transactions: %w", err)
	}

	// Convert to DTOs
	dtos := make([]*TransactionDTO, len(transactions))
	for i, tx := range transactions {
		dtos[i] = h.toDTO(tx)
	}

	return &GetTransactionsResponse{
		Transactions: dtos,
		Total:        total,
	}, nil
}

func (h *GetTransactionsHandler) resolvePlayerID(playerID int) (shared.PlayerID, error) {
	return shared.NewPlayerID(playerID)
}

func (h *GetTransactionsHandler) buildQueryOptions(query *GetTransactionsQuery) (ledger.QueryOptions, error) {
	opts := ledger.DefaultQueryOptions()

	// Date range
	if query.StartDate != nil {
		opts.StartDate = query.StartDate
	}
	if query.EndDate != nil {
		opts.EndDate = query.EndDate
	}

	// Category filter
	if query.Category != nil {
		category, err := ledger.ParseCategory(*query.Category)
		if err != nil {
			return opts, fmt.Errorf("invalid category: %w", err)
		}
		opts.Category = &category
	}

	// Transaction type filter
	if query.TransactionType != nil {
		txType, err := ledger.ParseTransactionType(*query.TransactionType)
		if err != nil {
			return opts, fmt.Errorf("invalid transaction type: %w", err)
		}
		opts.TransactionType = &txType
	}

	// Related entity filters
	if query.RelatedEntityType != nil {
		opts.RelatedEntityType = query.RelatedEntityType
	}
	if query.RelatedEntityID != nil {
		opts.RelatedEntityID = query.RelatedEntityID
	}

	// Pagination
	if query.Limit > 0 {
		opts.Limit = query.Limit
	}
	opts.Offset = query.Offset

	// Sorting
	if query.OrderBy != "" {
		opts.OrderBy = query.OrderBy
	}

	return opts, nil
}

func (h *GetTransactionsHandler) toDTO(tx *ledger.Transaction) *TransactionDTO {
	return &TransactionDTO{
		ID:                tx.ID().String(),
		PlayerID:          tx.PlayerID().Value(),
		Timestamp:         tx.Timestamp(),
		Type:              tx.TransactionType().String(),
		Category:          tx.Category().String(),
		Amount:            tx.Amount(),
		BalanceBefore:     tx.BalanceBefore(),
		BalanceAfter:      tx.BalanceAfter(),
		Description:       tx.Description(),
		Metadata:          tx.Metadata(),
		RelatedEntityType: tx.RelatedEntityType(),
		RelatedEntityID:   tx.RelatedEntityID(),
	}
}

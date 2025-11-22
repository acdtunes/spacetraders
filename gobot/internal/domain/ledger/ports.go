package ledger

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TransactionRepository defines persistence operations for transactions
type TransactionRepository interface {
	// Create persists a new transaction
	Create(ctx context.Context, transaction *Transaction) error

	// FindByID retrieves a transaction by its ID
	FindByID(ctx context.Context, id TransactionID, playerID shared.PlayerID) (*Transaction, error)

	// FindByPlayer retrieves transactions for a player with optional filtering
	FindByPlayer(ctx context.Context, playerID shared.PlayerID, opts QueryOptions) ([]*Transaction, error)

	// CountByPlayer returns the count of transactions matching the criteria
	CountByPlayer(ctx context.Context, playerID shared.PlayerID, opts QueryOptions) (int, error)
}

// QueryOptions defines filtering and pagination options for transaction queries
type QueryOptions struct {
	// Date range filtering
	StartDate *time.Time
	EndDate   *time.Time

	// Category filtering
	Category *Category

	// Transaction type filtering
	TransactionType *TransactionType

	// Related entity filtering
	RelatedEntityType *string
	RelatedEntityID   *string

	// Pagination
	Limit  int
	Offset int

	// Sorting
	OrderBy string // "timestamp ASC" or "timestamp DESC" (default DESC)
}

// DefaultQueryOptions returns default query options
func DefaultQueryOptions() QueryOptions {
	return QueryOptions{
		Limit:   50,
		Offset:  0,
		OrderBy: "timestamp DESC",
	}
}

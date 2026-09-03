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

// GateFeeAggregator reads the LATEST recorded jump-gate fee, in credits, keyed by the system
// the hull DEPARTED from (sp-9idvn).
//
// DELIBERATELY SEPARATE FROM TransactionRepository, though the same GORM type satisfies
// both. This is a single-caller analytical read, and folding it into the broad repository
// interface would oblige roughly a dozen unrelated test fakes — expansion, probebuy,
// scouting, frontier — to grow a method none of them will ever call. The narrow port keeps
// the cost of the read with the code that wants it.
//
// Only rows carrying a non-empty metadata.origin_system are aggregated: a fee whose origin
// is unknown cannot be attributed to a gate, and guessing one would poison the table.
//
// A gate's fee TRENDS with cumulative traffic rather than sitting still (sp-htzl1.5), so the
// answer is the most recent observation per origin and a cached copy goes wrong with age.
// `since` bounds the scan for cost, not for freshness.
//
// An empty map is a valid, non-error answer: nothing has been recorded yet, and every
// caller must already handle that by falling back to its flat charge.
type GateFeeAggregator interface {
	PerOriginGateFees(ctx context.Context, playerID shared.PlayerID, since time.Time) (map[string]int64, error)
}

// TreasuryHighWaterReader reports the PEAK balance a player held across a trailing window — the
// fleet's capacity, not its instant. EMPTY IS NOT ZERO: no rows is readable=false, never a zero peak.
type TreasuryHighWaterReader interface {
	TreasuryHighWaterSince(
		ctx context.Context, playerID shared.PlayerID, since time.Time,
	) (highWater int64, readable bool, err error)
}

// OwnTradeRecencyReader reports when the fleet itself last traded cargo at each waypoint, telling
// ground it just worked from ground it left alone. Keyed by WAYPOINT so the caller picks its grain.
type OwnTradeRecencyReader interface {
	LastTradeByWaypoint(
		ctx context.Context, playerID shared.PlayerID, since time.Time,
	) (map[string]time.Time, error)
}

// CategoryTotalsReader sums signed amounts per category over a window in SQL, mirroring
// GateFeeAggregator's separation from TransactionRepository above.
type CategoryTotalsReader interface {
	CategoryTotals(
		ctx context.Context, playerID shared.PlayerID, since, until time.Time,
	) (map[string]int64, error)
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

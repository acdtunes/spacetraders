package ledger

import (
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Transaction is the aggregate root representing a financial transaction
// Transactions are immutable once created and follow strict invariants
type Transaction struct {
	id                TransactionID
	playerID          shared.PlayerID
	timestamp         time.Time
	transactionType   TransactionType
	category          Category
	amount            int // Positive for income, negative for expenses
	balanceBefore     int
	balanceAfter      int
	description       string
	metadata          map[string]interface{}
	relatedEntityType string // e.g., "contract", "factory", "ship_purchase"
	relatedEntityID   string // ID of related entity
	operationType     string // e.g., "contract", "arbitrage", "rebalancing", "factory"
}

// NewTransaction creates a new transaction with validation
func NewTransaction(
	playerID shared.PlayerID,
	timestamp time.Time,
	transactionType TransactionType,
	amount int,
	balanceBefore int,
	balanceAfter int,
	description string,
	metadata map[string]interface{},
	relatedEntityType string,
	relatedEntityID string,
	operationType string,
) (*Transaction, error) {
	// Generate new transaction ID
	id := NewTransactionID()

	// Validate player ID
	if playerID.IsZero() {
		return nil, &ErrInvalidTransaction{
			Field:  "player_id",
			Reason: "player_id cannot be zero",
		}
	}

	// Validate transaction type
	if !transactionType.IsValid() {
		return nil, &ErrInvalidTransaction{
			Field:  "transaction_type",
			Reason: fmt.Sprintf("invalid transaction type: %s", transactionType),
		}
	}

	// Determine category from type
	category, err := transactionType.ToCategory()
	if err != nil {
		return nil, &ErrInvalidTransaction{
			Field:  "category",
			Reason: err.Error(),
		}
	}

	// Create transaction
	t := &Transaction{
		id:                id,
		playerID:          playerID,
		timestamp:         timestamp,
		transactionType:   transactionType,
		category:          category,
		amount:            amount,
		balanceBefore:     balanceBefore,
		balanceAfter:      balanceAfter,
		description:       description,
		metadata:          metadata,
		relatedEntityType: relatedEntityType,
		relatedEntityID:   relatedEntityID,
		operationType:     operationType,
	}

	// Validate invariants
	if err := t.Validate(); err != nil {
		return nil, err
	}

	return t, nil
}

// ReconstructTransaction reconstructs a transaction from persistence
// This bypasses some validations and is used by the repository
func ReconstructTransaction(
	id TransactionID,
	playerID shared.PlayerID,
	timestamp time.Time,
	transactionType TransactionType,
	category Category,
	amount int,
	balanceBefore int,
	balanceAfter int,
	description string,
	metadata map[string]interface{},
	relatedEntityType string,
	relatedEntityID string,
	operationType string,
) *Transaction {
	return &Transaction{
		id:                id,
		playerID:          playerID,
		timestamp:         timestamp,
		transactionType:   transactionType,
		category:          category,
		amount:            amount,
		balanceBefore:     balanceBefore,
		balanceAfter:      balanceAfter,
		description:       description,
		metadata:          metadata,
		relatedEntityType: relatedEntityType,
		relatedEntityID:   relatedEntityID,
		operationType:     operationType,
	}
}

// Validate checks that the transaction satisfies all invariants
func (t *Transaction) Validate() error {
	// Amount cannot be zero
	if t.amount == 0 {
		return &ErrInvalidTransaction{
			Field:  "amount",
			Reason: "amount cannot be zero",
		}
	}

	// Balance invariant: balance_after must equal balance_before + amount
	expected := t.balanceBefore + t.amount
	if t.balanceAfter != expected {
		return &ErrBalanceInvariantViolation{
			BalanceBefore: t.balanceBefore,
			Amount:        t.amount,
			BalanceAfter:  t.balanceAfter,
			Expected:      expected,
		}
	}

	// Timestamp cannot be in the future (allow 1 minute buffer for clock skew)
	now := time.Now().Add(1 * time.Minute)
	if t.timestamp.After(now) {
		return &ErrInvalidTransaction{
			Field:  "timestamp",
			Reason: fmt.Sprintf("timestamp cannot be in the future: %s", t.timestamp),
		}
	}

	return nil
}

// Getters (all fields are immutable)

func (t *Transaction) ID() TransactionID {
	return t.id
}

func (t *Transaction) PlayerID() shared.PlayerID {
	return t.playerID
}

func (t *Transaction) Timestamp() time.Time {
	return t.timestamp
}

func (t *Transaction) TransactionType() TransactionType {
	return t.transactionType
}

func (t *Transaction) Category() Category {
	return t.category
}

func (t *Transaction) Amount() int {
	return t.amount
}

func (t *Transaction) BalanceBefore() int {
	return t.balanceBefore
}

func (t *Transaction) BalanceAfter() int {
	return t.balanceAfter
}

func (t *Transaction) Description() string {
	return t.description
}

func (t *Transaction) Metadata() map[string]interface{} {
	// Return a copy to prevent external modification
	if t.metadata == nil {
		return nil
	}
	copy := make(map[string]interface{}, len(t.metadata))
	for k, v := range t.metadata {
		copy[k] = v
	}
	return copy
}

func (t *Transaction) RelatedEntityType() string {
	return t.relatedEntityType
}

func (t *Transaction) RelatedEntityID() string {
	return t.relatedEntityID
}

func (t *Transaction) OperationType() string {
	return t.operationType
}

// Business logic methods

// IsIncome returns true if the transaction represents income
func (t *Transaction) IsIncome() bool {
	return t.amount > 0
}

// IsExpense returns true if the transaction represents an expense
func (t *Transaction) IsExpense() bool {
	return t.amount < 0
}

// GetCategory returns the transaction's category
func (t *Transaction) GetCategory() Category {
	return t.category
}

// String provides a human-readable representation
func (t *Transaction) String() string {
	return fmt.Sprintf("Transaction[%s, type=%s, amount=%d, balance=%d->%d]",
		t.id.String(), t.transactionType, t.amount, t.balanceBefore, t.balanceAfter)
}

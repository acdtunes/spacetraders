package ledger

import (
	"fmt"

	"github.com/google/uuid"
)

// TransactionID is a value object representing a transaction's unique identifier
type TransactionID struct {
	value string
}

// NewTransactionID creates a new TransactionID with a generated UUID
func NewTransactionID() TransactionID {
	return TransactionID{value: uuid.New().String()}
}

// NewTransactionIDFromString creates a TransactionID from an existing UUID string
func NewTransactionIDFromString(id string) (TransactionID, error) {
	if id == "" {
		return TransactionID{}, fmt.Errorf("transaction_id cannot be empty")
	}

	// Validate UUID format
	_, err := uuid.Parse(id)
	if err != nil {
		return TransactionID{}, fmt.Errorf("invalid transaction_id format: %w", err)
	}

	return TransactionID{value: id}, nil
}

// Value returns the string value of the TransactionID
func (t TransactionID) Value() string {
	return t.value
}

// String returns a string representation of the TransactionID
func (t TransactionID) String() string {
	return t.value
}

// Equals checks if two TransactionIDs are equal
func (t TransactionID) Equals(other TransactionID) bool {
	return t.value == other.value
}

// IsZero checks if the TransactionID is the zero value (uninitialized)
func (t TransactionID) IsZero() bool {
	return t.value == ""
}

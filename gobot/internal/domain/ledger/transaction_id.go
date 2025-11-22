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

// MustNewTransactionIDFromString creates a TransactionID from a string, panicking if invalid
// Use this only when you're certain the ID is valid (e.g., from database)
func MustNewTransactionIDFromString(id string) TransactionID {
	tid, err := NewTransactionIDFromString(id)
	if err != nil {
		panic(err)
	}
	return tid
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

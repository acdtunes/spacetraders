package ledger

import "fmt"

// TransactionType represents the type of financial transaction
type TransactionType string

const (
	// TransactionTypeRefuel represents a ship refueling operation
	TransactionTypeRefuel TransactionType = "REFUEL"

	// TransactionTypePurchaseCargo represents purchasing cargo/goods
	TransactionTypePurchaseCargo TransactionType = "PURCHASE_CARGO"

	// TransactionTypeSellCargo represents selling cargo/goods
	TransactionTypeSellCargo TransactionType = "SELL_CARGO"

	// TransactionTypePurchaseShip represents purchasing a new ship
	TransactionTypePurchaseShip TransactionType = "PURCHASE_SHIP"

	// TransactionTypeContractAccepted represents payment received when accepting a contract
	TransactionTypeContractAccepted TransactionType = "CONTRACT_ACCEPTED"

	// TransactionTypeContractFulfilled represents payment received when fulfilling a contract
	TransactionTypeContractFulfilled TransactionType = "CONTRACT_FULFILLED"
)

// AllTransactionTypes returns all valid transaction types
func AllTransactionTypes() []TransactionType {
	return []TransactionType{
		TransactionTypeRefuel,
		TransactionTypePurchaseCargo,
		TransactionTypeSellCargo,
		TransactionTypePurchaseShip,
		TransactionTypeContractAccepted,
		TransactionTypeContractFulfilled,
	}
}

// String returns the string representation of the TransactionType
func (t TransactionType) String() string {
	return string(t)
}

// IsValid checks if the transaction type is valid
func (t TransactionType) IsValid() bool {
	switch t {
	case TransactionTypeRefuel,
		TransactionTypePurchaseCargo,
		TransactionTypeSellCargo,
		TransactionTypePurchaseShip,
		TransactionTypeContractAccepted,
		TransactionTypeContractFulfilled:
		return true
	default:
		return false
	}
}

// ToCategory maps the transaction type to its category
func (t TransactionType) ToCategory() (Category, error) {
	category, exists := TypeToCategoryMap[t]
	if !exists {
		return "", fmt.Errorf("unknown transaction type: %s", t)
	}
	return category, nil
}

// ParseTransactionType parses a string into a TransactionType
func ParseTransactionType(s string) (TransactionType, error) {
	t := TransactionType(s)
	if !t.IsValid() {
		return "", fmt.Errorf("invalid transaction type: %s", s)
	}
	return t, nil
}

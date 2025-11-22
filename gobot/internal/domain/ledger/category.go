package ledger

import "fmt"

// Category represents the cash flow category for financial reporting
type Category string

const (
	// CategoryFuelCosts represents fuel expenses
	CategoryFuelCosts Category = "FUEL_COSTS"

	// CategoryTradingRevenue represents income from selling cargo
	CategoryTradingRevenue Category = "TRADING_REVENUE"

	// CategoryTradingCosts represents expenses from purchasing cargo
	CategoryTradingCosts Category = "TRADING_COSTS"

	// CategoryShipInvestments represents expenses from purchasing ships
	CategoryShipInvestments Category = "SHIP_INVESTMENTS"

	// CategoryContractRevenue represents income from contracts
	CategoryContractRevenue Category = "CONTRACT_REVENUE"
)

// AllCategories returns all valid categories
func AllCategories() []Category {
	return []Category{
		CategoryFuelCosts,
		CategoryTradingRevenue,
		CategoryTradingCosts,
		CategoryShipInvestments,
		CategoryContractRevenue,
	}
}

// TypeToCategoryMap maps transaction types to their categories
var TypeToCategoryMap = map[TransactionType]Category{
	TransactionTypeRefuel:            CategoryFuelCosts,
	TransactionTypePurchaseCargo:     CategoryTradingCosts,
	TransactionTypeSellCargo:         CategoryTradingRevenue,
	TransactionTypePurchaseShip:      CategoryShipInvestments,
	TransactionTypeContractAccepted:  CategoryContractRevenue,
	TransactionTypeContractFulfilled: CategoryContractRevenue,
}

// String returns the string representation of the Category
func (c Category) String() string {
	return string(c)
}

// IsValid checks if the category is valid
func (c Category) IsValid() bool {
	switch c {
	case CategoryFuelCosts,
		CategoryTradingRevenue,
		CategoryTradingCosts,
		CategoryShipInvestments,
		CategoryContractRevenue:
		return true
	default:
		return false
	}
}

// IsIncome returns true if the category represents income
func (c Category) IsIncome() bool {
	switch c {
	case CategoryTradingRevenue, CategoryContractRevenue:
		return true
	default:
		return false
	}
}

// IsExpense returns true if the category represents an expense or investment
func (c Category) IsExpense() bool {
	return !c.IsIncome()
}

// ParseCategory parses a string into a Category
func ParseCategory(s string) (Category, error) {
	c := Category(s)
	if !c.IsValid() {
		return "", fmt.Errorf("invalid category: %s", s)
	}
	return c, nil
}

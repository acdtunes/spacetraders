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

	// CategoryTravelCosts represents the NON-FUEL cost of moving a hull —
	// today only jump gate fees. Deliberately separate from
	// FUEL_COSTS: the two are different unit economics (fuel scales with
	// distance burned, a gate fee is charged per jump), and the existing
	// fuel-cost analytics would silently absorb a ~6.3k/jump line if these
	// shared a bucket. It is an operating expense, so the briefing's ex-capex
	// slope (category != SHIP_INVESTMENTS) correctly counts it.
	CategoryTravelCosts Category = "TRAVEL_COSTS"
)

// AllCategories returns all valid categories
func AllCategories() []Category {
	return []Category{
		CategoryFuelCosts,
		CategoryTradingRevenue,
		CategoryTradingCosts,
		CategoryShipInvestments,
		CategoryContractRevenue,
		CategoryTravelCosts,
	}
}

// TypeToCategoryMap maps transaction types to their categories.
//
// category is a pure function of type — category = f(type) — a fixed,
// deterministic relabel. It is stored/read purely as a reporting convenience;
// it is NOT an independent axis. Do not set or vary category apart from type.
var TypeToCategoryMap = map[TransactionType]Category{
	TransactionTypeRefuel:            CategoryFuelCosts,
	TransactionTypePurchaseCargo:     CategoryTradingCosts,
	TransactionTypeSellCargo:         CategoryTradingRevenue,
	TransactionTypePurchaseShip:      CategoryShipInvestments,
	TransactionTypeContractAccepted:  CategoryContractRevenue,
	TransactionTypeContractFulfilled: CategoryContractRevenue,
	TransactionTypeJump:              CategoryTravelCosts,
	// Outfitting a hull is capital work ON that hull, so both directions of a
	// module modification land in SHIP_INVESTMENTS beside the hull purchases
	// they upgrade — and are correctly excluded from the ex-capex operating slope.
	TransactionTypeModuleInstall: CategoryShipInvestments,
	TransactionTypeModuleRemove:  CategoryShipInvestments,
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
		CategoryContractRevenue,
		CategoryTravelCosts:
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

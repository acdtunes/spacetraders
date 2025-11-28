package manufacturing

// SupplyLevel represents market supply abundance and encapsulates
// business rules about purchasing and collection viability.
type SupplyLevel string

const (
	SupplyLevelAbundant SupplyLevel = "ABUNDANT"
	SupplyLevelHigh     SupplyLevel = "HIGH"
	SupplyLevelModerate SupplyLevel = "MODERATE"
	SupplyLevelLimited  SupplyLevel = "LIMITED"
	SupplyLevelScarce   SupplyLevel = "SCARCE"
)

// purchaseMultipliers defines safe purchase fractions to prevent supply crashes.
// Key business rule: Never deplete supply beyond safe thresholds.
var purchaseMultipliers = map[SupplyLevel]float64{
	SupplyLevelAbundant: 0.80, // Plenty of buffer
	SupplyLevelHigh:     0.60, // Sweet spot - maintain stability
	SupplyLevelModerate: 0.40, // Careful - could drop to LIMITED
	SupplyLevelLimited:  0.20, // Very careful - critical supply
	SupplyLevelScarce:   0.10, // Minimal - supply nearly depleted
}

// DefaultPurchaseMultiplier is used when supply level is unknown
const DefaultPurchaseMultiplier = 0.40

// PurchaseMultiplier returns the safe purchase fraction based on supply level.
func (s SupplyLevel) PurchaseMultiplier() float64 {
	if mult, ok := purchaseMultipliers[s]; ok {
		return mult
	}
	return DefaultPurchaseMultiplier
}

// IsFavorableForCollection returns true if supply is HIGH or ABUNDANT,
// indicating the factory has produced enough output to collect.
func (s SupplyLevel) IsFavorableForCollection() bool {
	return s == SupplyLevelHigh || s == SupplyLevelAbundant
}

// IsSaturated returns true if market already has high supply,
// making it a poor target for selling.
func (s SupplyLevel) IsSaturated() bool {
	return s == SupplyLevelHigh || s == SupplyLevelAbundant
}

// AllowsPurchase returns true if supply level permits buying.
// SCARCE supply should not be depleted further.
func (s SupplyLevel) AllowsPurchase() bool {
	return s != SupplyLevelScarce
}

// Order returns numeric ordering for comparison.
// Higher order = more supply available.
func (s SupplyLevel) Order() int {
	switch s {
	case SupplyLevelAbundant:
		return 5
	case SupplyLevelHigh:
		return 4
	case SupplyLevelModerate:
		return 3
	case SupplyLevelLimited:
		return 2
	case SupplyLevelScarce:
		return 1
	default:
		return 0
	}
}

// IsHigherThan returns true if this supply level is higher than the other.
func (s SupplyLevel) IsHigherThan(other SupplyLevel) bool {
	return s.Order() > other.Order()
}

// IsLowerThan returns true if this supply level is lower than the other.
func (s SupplyLevel) IsLowerThan(other SupplyLevel) bool {
	return s.Order() < other.Order()
}

// ParseSupplyLevel converts string to SupplyLevel with validation.
func ParseSupplyLevel(s string) SupplyLevel {
	switch s {
	case "ABUNDANT":
		return SupplyLevelAbundant
	case "HIGH":
		return SupplyLevelHigh
	case "MODERATE":
		return SupplyLevelModerate
	case "LIMITED":
		return SupplyLevelLimited
	case "SCARCE":
		return SupplyLevelScarce
	default:
		return SupplyLevelModerate
	}
}

// String returns the string representation of the supply level.
func (s SupplyLevel) String() string {
	return string(s)
}

// CalculateSupplyAwareLimit determines safe purchase quantity based on supply level.
// Returns the maximum units that should be purchased to avoid crashing supply.
func (s SupplyLevel) CalculateSupplyAwareLimit(tradeVolume int) int {
	if tradeVolume <= 0 {
		return 0
	}
	return int(float64(tradeVolume) * s.PurchaseMultiplier())
}

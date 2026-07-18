package manufacturing

import "github.com/andrescamacho/spacetraders-go/internal/domain/shared"

// SupplyLevel is DEFINED once in internal/domain/shared and re-exported here as a
// type alias so the (many) existing manufacturing.SupplyLevel references across the
// codebase compile and behave identically. Consolidating the definition into the
// shared leaf package removes the cross-package supply-enum drift that produced the
// sp-9mkf supply-semantics bug class (market and manufacturing each carrying their
// own copy of the SCARCE..ABUNDANT vocabulary). See shared/supply_level.go for the
// business rules (purchase multipliers, collection/saturation predicates, ordering).
type SupplyLevel = shared.SupplyLevel

const (
	SupplyLevelAbundant = shared.SupplyLevelAbundant
	SupplyLevelHigh     = shared.SupplyLevelHigh
	SupplyLevelModerate = shared.SupplyLevelModerate
	SupplyLevelLimited  = shared.SupplyLevelLimited
	SupplyLevelScarce   = shared.SupplyLevelScarce
)

// DefaultPurchaseMultiplier is used when supply level is unknown (re-exported from shared).
const DefaultPurchaseMultiplier = shared.DefaultPurchaseMultiplier

// ParseSupplyLevel converts string to SupplyLevel with validation (delegates to shared).
func ParseSupplyLevel(s string) SupplyLevel {
	return shared.ParseSupplyLevel(s)
}

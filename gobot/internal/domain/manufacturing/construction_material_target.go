package manufacturing

// ConstructionMaterialTarget tracks delivery progress for a single construction material.
// A construction pipeline may have multiple materials (e.g., FAB_MATS and ADVANCED_CIRCUITRY).
type ConstructionMaterialTarget struct {
	tradeSymbol       string // e.g., "FAB_MATS"
	targetQuantity    int    // e.g., 1600 (remaining units needed)
	deliveredQuantity int    // e.g., 500 (delivered so far by this pipeline)
}

// NewConstructionMaterialTarget creates a new material target
func NewConstructionMaterialTarget(tradeSymbol string, targetQuantity int) *ConstructionMaterialTarget {
	return &ConstructionMaterialTarget{
		tradeSymbol:       tradeSymbol,
		targetQuantity:    targetQuantity,
		deliveredQuantity: 0,
	}
}

// ReconstructConstructionMaterialTarget rebuilds from persistence
func ReconstructConstructionMaterialTarget(tradeSymbol string, targetQuantity, deliveredQuantity int) *ConstructionMaterialTarget {
	return &ConstructionMaterialTarget{
		tradeSymbol:       tradeSymbol,
		targetQuantity:    targetQuantity,
		deliveredQuantity: deliveredQuantity,
	}
}

// Getters
func (m *ConstructionMaterialTarget) TradeSymbol() string    { return m.tradeSymbol }
func (m *ConstructionMaterialTarget) TargetQuantity() int    { return m.targetQuantity }
func (m *ConstructionMaterialTarget) DeliveredQuantity() int { return m.deliveredQuantity }

// RemainingQuantity returns how many units still need to be delivered
func (m *ConstructionMaterialTarget) RemainingQuantity() int {
	return m.targetQuantity - m.deliveredQuantity
}

// IsComplete returns true if all required units have been delivered
func (m *ConstructionMaterialTarget) IsComplete() bool {
	return m.deliveredQuantity >= m.targetQuantity
}

// Progress returns completion percentage (0-100)
func (m *ConstructionMaterialTarget) Progress() float64 {
	if m.targetQuantity == 0 {
		return 100.0
	}
	return float64(m.deliveredQuantity) / float64(m.targetQuantity) * 100
}

// RecordDelivery adds delivered units to the count
func (m *ConstructionMaterialTarget) RecordDelivery(units int) {
	m.deliveredQuantity += units
}

// ReconcileDelivered RAISES the delivered count to observed and reports whether it moved. The
// counter is a cache of what the construction site itself holds: it is written only after a supply
// the server already accepted, so a lost write leaves it permanently BEHIND and the pipeline then
// sources material the site no longer needs.
//
// Raise-only, deliberately. A lower observed value cannot be told apart from a site read that raced
// a delivery landing between the read and this call, so lowering would drop units that really were
// delivered — the same lost update RecordDelivery is serialized to prevent. Raise-only also makes
// the operation monotonic and idempotent, so it converges to the same value under any interleaving
// with RecordDelivery. A genuine local-ahead-of-site divergence is a different defect and belongs in
// a log, not in a silent correction that erases its own evidence.
func (m *ConstructionMaterialTarget) ReconcileDelivered(observed int) bool {
	if observed <= m.deliveredQuantity {
		return false
	}
	m.deliveredQuantity = observed
	return true
}

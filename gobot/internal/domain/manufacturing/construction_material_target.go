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

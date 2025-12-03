package manufacturing

// ConstructionSite represents a construction project (e.g., jump gate under construction).
// This is a domain entity that captures construction site state from the SpaceTraders API.
type ConstructionSite struct {
	waypointSymbol string
	waypointType   string // JUMP_GATE, etc.
	materials      []ConstructionMaterial
	isComplete     bool
}

// ConstructionMaterial represents a required material for construction.
type ConstructionMaterial struct {
	tradeSymbol string // FAB_MATS, ADVANCED_CIRCUITRY, etc.
	required    int    // Total required
	fulfilled   int    // Already delivered
}

// NewConstructionSite creates a new ConstructionSite entity.
func NewConstructionSite(waypointSymbol, waypointType string, materials []ConstructionMaterial, isComplete bool) *ConstructionSite {
	return &ConstructionSite{
		waypointSymbol: waypointSymbol,
		waypointType:   waypointType,
		materials:      materials,
		isComplete:     isComplete,
	}
}

// NewConstructionMaterial creates a new ConstructionMaterial value object.
func NewConstructionMaterial(tradeSymbol string, required, fulfilled int) ConstructionMaterial {
	return ConstructionMaterial{
		tradeSymbol: tradeSymbol,
		required:    required,
		fulfilled:   fulfilled,
	}
}

// ReconstructConstructionSite rebuilds a ConstructionSite from API data.
func ReconstructConstructionSite(waypointSymbol, waypointType string, materials []ConstructionMaterial, isComplete bool) *ConstructionSite {
	return &ConstructionSite{
		waypointSymbol: waypointSymbol,
		waypointType:   waypointType,
		materials:      materials,
		isComplete:     isComplete,
	}
}

// Getters for ConstructionSite

func (cs *ConstructionSite) WaypointSymbol() string              { return cs.waypointSymbol }
func (cs *ConstructionSite) WaypointType() string                { return cs.waypointType }
func (cs *ConstructionSite) Materials() []ConstructionMaterial   { return cs.materials }
func (cs *ConstructionSite) IsComplete() bool                    { return cs.isComplete }

// GetMaterial returns the material for a given trade symbol, or nil if not found.
func (cs *ConstructionSite) GetMaterial(tradeSymbol string) *ConstructionMaterial {
	for i := range cs.materials {
		if cs.materials[i].tradeSymbol == tradeSymbol {
			return &cs.materials[i]
		}
	}
	return nil
}

// RemainingForMaterial returns the remaining quantity needed for a specific material.
func (cs *ConstructionSite) RemainingForMaterial(tradeSymbol string) int {
	mat := cs.GetMaterial(tradeSymbol)
	if mat == nil {
		return 0
	}
	return mat.Remaining()
}

// Progress returns the overall construction progress as a percentage (0-100).
func (cs *ConstructionSite) Progress() float64 {
	totalRequired := 0
	totalFulfilled := 0
	for _, mat := range cs.materials {
		totalRequired += mat.required
		totalFulfilled += mat.fulfilled
	}
	if totalRequired == 0 {
		return 100.0
	}
	return float64(totalFulfilled) / float64(totalRequired) * 100
}

// UnfulfilledMaterials returns only materials that still need delivery.
func (cs *ConstructionSite) UnfulfilledMaterials() []ConstructionMaterial {
	var unfulfilled []ConstructionMaterial
	for _, mat := range cs.materials {
		if mat.Remaining() > 0 {
			unfulfilled = append(unfulfilled, mat)
		}
	}
	return unfulfilled
}

// Getters for ConstructionMaterial

func (cm *ConstructionMaterial) TradeSymbol() string { return cm.tradeSymbol }
func (cm *ConstructionMaterial) Required() int       { return cm.required }
func (cm *ConstructionMaterial) Fulfilled() int      { return cm.fulfilled }

// Remaining returns the quantity still needed.
func (cm *ConstructionMaterial) Remaining() int {
	remaining := cm.required - cm.fulfilled
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Progress returns the material's progress as a percentage (0-100).
func (cm *ConstructionMaterial) Progress() float64 {
	if cm.required == 0 {
		return 100.0
	}
	return float64(cm.fulfilled) / float64(cm.required) * 100
}

// IsComplete returns true if this material is fully delivered.
func (cm *ConstructionMaterial) IsComplete() bool {
	return cm.fulfilled >= cm.required
}

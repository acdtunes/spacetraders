package goods

// ExportToImportMap defines the supply chain relationships for goods production.
// Maps output goods to their required input goods.
//
// This data structure represents the SpaceTraders economy's production relationships.
// Each key is a good that can be produced, and the value is the list of goods
// required as inputs to produce it.
//
// Raw materials (ores, crystals) have no entries in this map because they
// cannot be fabricated - they can only be mined or purchased.
//
// Source: SpaceTraders API documentation on trade goods and production chains
var ExportToImportMap = map[string][]string{
	// Basic Materials Processing (Tier 1)
	"IRON":               {"IRON_ORE"},
	"COPPER":             {"COPPER_ORE"},
	"ALUMINUM":           {"ALUMINUM_ORE"},
	"SILVER":             {"SILVER_ORE"},
	"GOLD":               {"GOLD_ORE"},
	"PLATINUM":           {"PLATINUM_ORE"},
	"URANITE":            {"URANITE_ORE"},
	"MERITIUM":           {"MERITIUM_ORE"},
	"QUARTZ_SAND":        {"QUARTZ"},
	"SILICON_CRYSTALS":   {"QUARTZ_SAND"},
	"PRECIOUS_STONES":    {"DIAMONDS"},
	"ICE_WATER":          {"ICE"},
	"LIQUID_HYDROGEN":    {"ICE_WATER"},
	"LIQUID_NITROGEN":    {"ICE_WATER"},
	"HYDROCARBON":        {"LIQUID_HYDROGEN"},

	// Manufactured Goods (Tier 2)
	"PLASTICS":           {"HYDROCARBON"},
	"POLYNUCLEOTIDES":    {"HYDROCARBON"},
	"FERTILIZERS":        {"LIQUID_NITROGEN"},
	"FABRICS":            {"POLYNUCLEOTIDES"},
	"EXPLOSIVES":         {"LIQUID_NITROGEN", "HYDROCARBON"},

	// Advanced Components (Tier 3)
	"ELECTRONICS":        {"SILICON_CRYSTALS", "COPPER"},
	"MICROPROCESSORS":    {"SILICON_CRYSTALS", "COPPER"},
	"MACHINERY":          {"IRON"},
	"QUANTUM_DRIVES":     {"PLATINUM", "URANITE"},
	"SUPERGRAINS":        {"FERTILIZERS"},
	"ANTIMATTER":         {"MERITIUM", "LIQUID_HYDROGEN"},

	// Complex Products (Tier 4)
	"ADVANCED_CIRCUITRY": {"ELECTRONICS", "MICROPROCESSORS"},
	"BIOCOMPOSITES":      {"FABRICS", "SUPERGRAINS"},
	"QUANTUM_STABILIZERS": {"QUANTUM_DRIVES", "ANTIMATTER"},
	"NANOTECH":           {"MICROPROCESSORS", "PRECIOUS_STONES"},

	// Refined Goods
	"QUANTUM_ENTANGLERS": {"QUANTUM_STABILIZERS", "ADVANCED_CIRCUITRY"},
	"GRAVITY_WAVE_GENERATORS": {"QUANTUM_DRIVES", "ADVANCED_CIRCUITRY"},
	"AI_MAINFRAMES":      {"ADVANCED_CIRCUITRY", "NANOTECH"},
	"REACTOR_CORES":      {"URANITE", "MACHINERY"},

	// Luxury and Specialty Goods
	"JEWELRY":            {"PRECIOUS_STONES", "GOLD"},
	"DRUGS":              {"POLYNUCLEOTIDES", "FERTILIZERS"},
	"CLOTHING":           {"FABRICS"},
	"EQUIPMENT":          {"IRON", "ELECTRONICS"},
	"FOOD":               {"SUPERGRAINS"},
	"MEDICINE":           {"DRUGS", "EQUIPMENT"},

	// Ship Components
	"SHIP_PLATING":       {"IRON", "ALUMINUM"},
	"SHIP_PARTS":         {"IRON", "ELECTRONICS"},
	"ENGINE_PARTS":       {"MACHINERY", "MICROPROCESSORS"},

	// Advanced Manufacturing
	"LASER_RIFLES":       {"ADVANCED_CIRCUITRY", "EQUIPMENT"},
	"MOOD_REGULATORS":    {"DRUGS", "ELECTRONICS"},
	"VIRAL_AGENTS":       {"POLYNUCLEOTIDES", "DRUGS"},
	"MICRO_FUSION_GENERATORS": {"REACTOR_CORES", "ADVANCED_CIRCUITRY"},
	"PHASE_SHIELDS":      {"QUANTUM_STABILIZERS", "ELECTRONICS"},

	// Construction Materials
	"ASSEMBLED_COMPONENTS": {"MACHINERY", "ELECTRONICS"},
	"MODULAR_HOUSING":    {"PLASTICS", "IRON"},

	// Research and Development
	"LAB_INSTRUMENTS":    {"ELECTRONICS", "PRECIOUS_STONES"},
	"HOLOGRAPHICS":       {"ADVANCED_CIRCUITRY", "PRECIOUS_STONES"},
	"NANOBOTS":           {"NANOTECH", "MICROPROCESSORS"},
	"GENE_THERAPEUTICS":  {"POLYNUCLEOTIDES", "LAB_INSTRUMENTS"},
	"NEURAL_CHIPS":       {"MICROPROCESSORS", "BIOCOMPOSITES"},

	// Exotic Materials
	"EXOTIC_MATTER":      {"ANTIMATTER", "MERITIUM"},
	"GRAVITON_EMITTERS":  {"GRAVITY_WAVE_GENERATORS", "EXOTIC_MATTER"},
	"QUANTUM_FOAM":       {"QUANTUM_ENTANGLERS", "EXOTIC_MATTER"},
	"RELIC_TECH":         {"NANOTECH", "ANTIMATTER"},
}

// IsRawMaterial returns true if the good is a raw material (cannot be fabricated)
func IsRawMaterial(good string) bool {
	_, exists := ExportToImportMap[good]
	return !exists
}

// GetRequiredInputs returns the list of inputs required to produce a good.
// Returns empty slice if the good is a raw material.
func GetRequiredInputs(good string) []string {
	inputs, exists := ExportToImportMap[good]
	if !exists {
		return []string{}
	}
	return inputs
}

// ValidateSupplyChain checks if a good can be produced given the supply chain map.
// Returns error if the good is unknown and cannot be produced or purchased.
func ValidateSupplyChain(good string) error {
	// Raw materials are always valid (can be mined/purchased)
	if IsRawMaterial(good) {
		return nil
	}

	// Check if the good exists in the supply chain
	_, exists := ExportToImportMap[good]
	if !exists {
		return &UnknownGoodError{Good: good}
	}

	return nil
}

// UnknownGoodError indicates a good is not in the supply chain map
type UnknownGoodError struct {
	Good string
}

func (e *UnknownGoodError) Error() string {
	return "unknown good: " + e.Good
}

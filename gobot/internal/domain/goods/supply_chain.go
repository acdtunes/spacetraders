package goods

// ExportToImportMap defines the supply chain relationships for goods production.
// Maps output goods to their required input goods.
//
// This data structure represents the SpaceTraders economy's production relationships.
// Each key is a good that can be produced, and the value is the list of goods
// required as inputs to produce it.
//
// Raw materials (ores, raw crystals like QUARTZ, ICE, etc.) have no entries in this map
// because they cannot be fabricated - they can only be mined or purchased.
//
// Source: Official SpaceTraders API exportToImportMap data
var ExportToImportMap = map[string][]string{
	// Liquids and Gases (processed with MACHINERY)
	"LIQUID_HYDROGEN": {"MACHINERY"},
	"LIQUID_NITROGEN": {"MACHINERY"},
	"HYDROCARBON":     {"MACHINERY"},
	"AMMONIA_ICE":     {"MACHINERY"},
	"ICE_WATER":       {"MACHINERY"},

	// Minerals and Ores (processed with EXPLOSIVES)
	"PRECIOUS_STONES":  {"EXPLOSIVES"},
	"QUARTZ_SAND":      {"EXPLOSIVES"},
	"SILICON_CRYSTALS": {"EXPLOSIVES"},
	"IRON_ORE":         {"EXPLOSIVES"},
	"ALUMINUM_ORE":     {"EXPLOSIVES"},
	"SILVER_ORE":       {"EXPLOSIVES"},
	"COPPER_ORE":       {"EXPLOSIVES"},
	"PLATINUM_ORE":     {"EXPLOSIVES"},
	"GOLD_ORE":         {"EXPLOSIVES"},
	"URANITE_ORE":      {"EXPLOSIVES"},
	"MERITIUM_ORE":     {"EXPLOSIVES"},
	"DIAMONDS":         {"EXPLOSIVES"},

	// Salvage and Artifacts
	"SHIP_SALVAGE":        {"MACHINERY"},
	"CULTURAL_ARTIFACTS":  {"LAB_INSTRUMENTS"},

	// Basic Manufactured Goods
	"PLASTICS":         {"LIQUID_HYDROGEN"},
	"FERTILIZERS":      {"LIQUID_NITROGEN"},
	"FUEL":             {"HYDROCARBON"},
	"POLYNUCLEOTIDES":  {"LIQUID_HYDROGEN", "LIQUID_NITROGEN"},
	"EXPLOSIVES":       {"LIQUID_HYDROGEN", "LIQUID_NITROGEN"},

	// Refined Metals
	"IRON":     {"IRON_ORE"},
	"ALUMINUM": {"ALUMINUM_ORE"},
	"COPPER":   {"COPPER_ORE"},
	"SILVER":   {"SILVER_ORE"},
	"PLATINUM": {"PLATINUM_ORE"},
	"GOLD":     {"GOLD_ORE"},
	"URANITE":  {"URANITE_ORE"},
	"MERITIUM": {"MERITIUM_ORE"},

	// Intermediate Goods
	"AMMUNITION":     {"IRON", "LIQUID_NITROGEN"},
	"FAB_MATS":       {"IRON", "QUARTZ_SAND"},
	"FOOD":           {"FERTILIZERS"},
	"FABRICS":        {"FERTILIZERS"},
	"ELECTRONICS":    {"SILICON_CRYSTALS", "COPPER"},
	"MACHINERY":      {"IRON"},
	"EQUIPMENT":      {"ALUMINUM", "PLASTICS"},
	"MICROPROCESSORS": {"SILICON_CRYSTALS", "COPPER"},

	// Luxury Goods
	"JEWELRY": {"GOLD", "SILVER", "PRECIOUS_STONES", "DIAMONDS"},
	"CLOTHING": {"FABRICS"},

	// Weapons
	"FIREARMS":       {"IRON", "AMMUNITION"},
	"ASSAULT_RIFLES": {"ALUMINUM", "AMMUNITION"},

	// Ship Components
	"SHIP_PLATING": {"ALUMINUM", "MACHINERY"},
	"SHIP_PARTS":   {"EQUIPMENT", "ELECTRONICS"},

	// Medical and Drugs
	"MEDICINE": {"FABRICS", "POLYNUCLEOTIDES"},
	"DRUGS":    {"AMMONIA_ICE", "POLYNUCLEOTIDES"},

	// Military and Research
	"MILITARY_EQUIPMENT": {"ALUMINUM", "ELECTRONICS"},
	"LAB_INSTRUMENTS":    {"ELECTRONICS", "EQUIPMENT"},
	"BIOCOMPOSITES":      {"FABRICS", "POLYNUCLEOTIDES"},
	"ADVANCED_CIRCUITRY": {"ELECTRONICS", "MICROPROCESSORS"},

	// Reactors
	"REACTOR_SOLAR_I":     {"IRON", "MACHINERY"},
	"REACTOR_FUSION_I":    {"IRON", "MACHINERY"},
	"REACTOR_FISSION_I":   {"IRON", "MACHINERY"},
	"REACTOR_CHEMICAL_I":  {"IRON", "MACHINERY"},
	"REACTOR_ANTIMATTER_I": {"IRON", "MACHINERY"},

	// Engines
	"ENGINE_IMPULSE_DRIVE_I": {"IRON", "MACHINERY"},
	"ENGINE_ION_DRIVE_I":     {"IRON", "MACHINERY"},
	"ENGINE_ION_DRIVE_II":    {"PLATINUM", "ADVANCED_CIRCUITRY"},
	"ENGINE_HYPER_DRIVE_I":   {"PLATINUM", "ADVANCED_CIRCUITRY"},

	// Modules - Cargo
	"MODULE_CARGO_HOLD_I":  {"IRON", "MACHINERY"},
	"MODULE_CARGO_HOLD_II": {"ALUMINUM", "MACHINERY"},
	"MODULE_CARGO_HOLD_III": {"PLATINUM", "MACHINERY", "ADVANCED_CIRCUITRY"},

	// Modules - Processing
	"MODULE_MINERAL_PROCESSOR_I": {"IRON", "MACHINERY"},
	"MODULE_GAS_PROCESSOR_I":     {"IRON", "MACHINERY"},

	// Modules - Crew
	"MODULE_CREW_QUARTERS_I":    {"IRON", "MACHINERY", "FABRICS"},
	"MODULE_ENVOY_QUARTERS_I":   {"IRON", "MACHINERY", "FABRICS"},
	"MODULE_PASSENGER_CABIN_I":  {"IRON", "MACHINERY", "FABRICS"},

	// Modules - Science and Refining
	"MODULE_SCIENCE_LAB_I":   {"PLATINUM", "MACHINERY", "ADVANCED_CIRCUITRY"},
	"MODULE_ORE_REFINERY_I":  {"PLATINUM", "MACHINERY"},
	"MODULE_FUEL_REFINERY_I": {"PLATINUM", "MACHINERY"},
	"MODULE_MICRO_REFINERY_I": {"PLATINUM", "MACHINERY"},

	// Modules - Jump and Warp Drives
	"MODULE_JUMP_DRIVE_I":   {"IRON", "ADVANCED_CIRCUITRY"},
	"MODULE_JUMP_DRIVE_II":  {"PLATINUM", "ADVANCED_CIRCUITRY", "GOLD"},
	"MODULE_JUMP_DRIVE_III": {"PLATINUM", "ADVANCED_CIRCUITRY", "GOLD", "MERITIUM"},
	"MODULE_WARP_DRIVE_I":   {"IRON", "ADVANCED_CIRCUITRY"},
	"MODULE_WARP_DRIVE_II":  {"PLATINUM", "ADVANCED_CIRCUITRY", "URANITE"},
	"MODULE_WARP_DRIVE_III": {"PLATINUM", "ADVANCED_CIRCUITRY", "MERITIUM"},

	// Modules - Shields
	"MODULE_SHIELD_GENERATOR_I": {"IRON", "MACHINERY", "URANITE"},
	"MODULE_SHIELD_GENERATOR_II": {"ALUMINUM", "MACHINERY", "URANITE"},

	// Mounts - Gas Siphon
	"MOUNT_GAS_SIPHON_I":  {"IRON", "MACHINERY"},
	"MOUNT_GAS_SIPHON_II": {"ALUMINUM", "MACHINERY"},
	"MOUNT_GAS_SIPHON_III": {"PLATINUM", "MACHINERY", "ADVANCED_CIRCUITRY"},

	// Mounts - Surveyor
	"MOUNT_SURVEYOR_I":  {"IRON", "MACHINERY", "ELECTRONICS"},
	"MOUNT_SURVEYOR_II": {"ALUMINUM", "MACHINERY", "ELECTRONICS"},
	"MOUNT_SURVEYOR_III": {"PLATINUM", "MACHINERY", "ADVANCED_CIRCUITRY"},

	// Mounts - Sensor Array
	"MOUNT_SENSOR_ARRAY_I":  {"IRON", "MACHINERY", "ELECTRONICS"},
	"MOUNT_SENSOR_ARRAY_II": {"ALUMINUM", "MACHINERY", "ELECTRONICS"},
	"MOUNT_SENSOR_ARRAY_III": {"PLATINUM", "MACHINERY", "ADVANCED_CIRCUITRY", "URANITE"},

	// Mounts - Mining Laser
	"MOUNT_MINING_LASER_I":  {"IRON", "MACHINERY", "DIAMONDS"},
	"MOUNT_MINING_LASER_II": {"ALUMINUM", "MACHINERY", "DIAMONDS"},
	"MOUNT_MINING_LASER_III": {"PLATINUM", "MACHINERY", "ADVANCED_CIRCUITRY", "URANITE"},

	// Mounts - Weapons
	"MOUNT_TURRET_I":            {"IRON", "MACHINERY"},
	"MOUNT_LASER_CANNON_I":      {"IRON", "MACHINERY", "DIAMONDS"},
	"MOUNT_MISSILE_LAUNCHER_I":  {"IRON", "MACHINERY"},

	// Advanced Technology
	"QUANTUM_STABILIZERS":       {"PLATINUM", "ADVANCED_CIRCUITRY"},
	"ANTIMATTER":                {"LAB_INSTRUMENTS", "ADVANCED_CIRCUITRY"},
	"EXOTIC_MATTER":             {"LAB_INSTRUMENTS", "ADVANCED_CIRCUITRY"},
	"RELIC_TECH":                {"LAB_INSTRUMENTS", "EQUIPMENT"},
	"NOVEL_LIFEFORMS":           {"LAB_INSTRUMENTS", "EQUIPMENT"},
	"BOTANICAL_SPECIMENS":       {"LAB_INSTRUMENTS", "EQUIPMENT"},
	"AI_MAINFRAMES":             {"ADVANCED_CIRCUITRY", "MICROPROCESSORS"},
	"QUANTUM_DRIVES":            {"ADVANCED_CIRCUITRY", "DIAMONDS"},
	"GRAVITON_EMITTERS":         {"ADVANCED_CIRCUITRY", "GOLD"},
	"ROBOTIC_DRONES":            {"ADVANCED_CIRCUITRY", "ALUMINUM"},
	"CYBER_IMPLANTS":            {"ADVANCED_CIRCUITRY", "BIOCOMPOSITES"},

	// Biotech
	"NANOBOTS":           {"POLYNUCLEOTIDES", "LAB_INSTRUMENTS"},
	"GENE_THERAPEUTICS":  {"POLYNUCLEOTIDES", "LAB_INSTRUMENTS"},
	"NEURAL_CHIPS":       {"POLYNUCLEOTIDES", "ADVANCED_CIRCUITRY"},
	"MOOD_REGULATORS":    {"POLYNUCLEOTIDES", "LAB_INSTRUMENTS"},
	"VIRAL_AGENTS":       {"POLYNUCLEOTIDES", "LAB_INSTRUMENTS"},

	// Advanced Components
	"MICRO_FUSION_GENERATORS": {"ADVANCED_CIRCUITRY", "PLATINUM", "DIAMONDS"},
	"SUPERGRAINS":             {"FERTILIZERS", "POLYNUCLEOTIDES", "LAB_INSTRUMENTS"},
	"LASER_RIFLES":            {"DIAMONDS", "PLATINUM", "ADVANCED_CIRCUITRY"},
	"HOLOGRAPHICS":            {"GOLD", "SILVER", "ADVANCED_CIRCUITRY"},

	// Ships
	"SHIP_PROBE":              {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_MINING_DRONE":       {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_SIPHON_DRONE":       {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_LIGHT_HAULER":       {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_COMMAND_FRIGATE":    {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_INTERCEPTOR":        {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_EXPLORER":           {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_LIGHT_SHUTTLE":      {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_HEAVY_FREIGHTER":    {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_ORE_HOUND":          {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_REFINING_FREIGHTER": {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_SURVEYOR":           {"SHIP_PLATING", "SHIP_PARTS"},
	"SHIP_BULK_FREIGHTER":     {"SHIP_PLATING", "SHIP_PARTS"},
}

// IsRawMaterial returns true if the good is a raw material (cannot be fabricated)
func IsRawMaterial(good string) bool {
	_, exists := ExportToImportMap[good]
	return !exists
}

// MineableRawMaterials defines goods that can be mined/extracted directly.
// These bypass supply gating because:
// 1. They can only be obtained by mining or purchasing (no alternative fabrication path)
// 2. Their supply levels depend on mining activity, not factory production
// 3. Waiting for HIGH/ABUNDANT might never happen for these goods
var MineableRawMaterials = map[string]bool{
	// Ores (mined from asteroids)
	"IRON_ORE":     true,
	"COPPER_ORE":   true,
	"ALUMINUM_ORE": true,
	"PLATINUM_ORE": true,
	"GOLD_ORE":     true,
	"SILVER_ORE":   true,
	"URANITE_ORE":  true,
	"MERITIUM_ORE": true,

	// Crystals (mined from asteroids)
	"SILICON_CRYSTALS": true,
	"QUARTZ_SAND":      true,
	"DIAMONDS":         true,
	"PRECIOUS_STONES":  true,

	// Ice/Gases (extracted from gas giants/ice belts)
	// Note: LIQUID_HYDROGEN and LIQUID_NITROGEN are NOT included here
	// because they require MACHINERY to produce and should be supply-gated
	// to avoid purchasing at high prices when supply is low.
	"AMMONIA_ICE":      true,
	"HYDROCARBON":      true,
	"ICE_WATER":        true,

	// Biological (gathered from specific locations)
	"BOTANICAL_SPECIMENS": true,
	"EXOTIC_MATTER":       true,
}

// IsMineableRawMaterial returns true if the good is a mineable raw material.
// These goods bypass supply gating for ACQUIRE_DELIVER tasks because they
// cannot be fabricated from other inputs - they must be purchased or mined.
func IsMineableRawMaterial(good string) bool {
	return MineableRawMaterials[good]
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

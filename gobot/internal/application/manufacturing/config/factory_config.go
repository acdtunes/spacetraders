package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// FactoryConfig holds configuration for the goods factory system
type FactoryConfig struct {
	// Supply chain mapping: export good -> list of required inputs
	SupplyChainMap map[string][]string `json:"supply_chain_map"`

	// Polling intervals for production monitoring
	PollingIntervals PollingConfig `json:"polling_intervals"`

	// Parallel execution settings
	ParallelExecution ParallelConfig `json:"parallel_execution"`
}

// PollingConfig defines intervals for polling production
type PollingConfig struct {
	// Initial poll interval (in seconds) - faster check right after delivery
	InitialSeconds int `json:"initial_seconds"`

	// Settled poll interval (in seconds) - after first poll
	SettledSeconds int `json:"settled_seconds"`
}

// ParallelConfig defines settings for parallel execution
type ParallelConfig struct {
	// Enable parallel execution (default: true)
	Enabled bool `json:"enabled"`

	// Max concurrent workers per level (0 = unlimited)
	MaxWorkersPerLevel int `json:"max_workers_per_level"`
}

// DefaultConfig returns the default factory configuration
func DefaultConfig() *FactoryConfig {
	return &FactoryConfig{
		SupplyChainMap: DefaultSupplyChainMap(),
		PollingIntervals: PollingConfig{
			InitialSeconds: 30,
			SettledSeconds: 60,
		},
		ParallelExecution: ParallelConfig{
			Enabled:            true,
			MaxWorkersPerLevel: 0, // Unlimited
		},
	}
}

// LoadConfig loads configuration from environment variables and optional file
// Environment variables:
//   - GOODS_SUPPLY_CHAIN_PATH: Path to supply chain JSON file
//   - GOODS_POLL_INTERVAL_INITIAL: Initial polling interval in seconds
//   - GOODS_POLL_INTERVAL_SETTLED: Settled polling interval in seconds
//   - GOODS_PARALLEL_ENABLED: Enable parallel execution (true/false)
//   - GOODS_MAX_WORKERS_PER_LEVEL: Max workers per level (0 = unlimited)
func LoadConfig() (*FactoryConfig, error) {
	cfg := DefaultConfig()

	// Load supply chain map from file if specified
	if path := os.Getenv("GOODS_SUPPLY_CHAIN_PATH"); path != "" {
		if err := loadSupplyChainFromFile(path, cfg); err != nil {
			return nil, fmt.Errorf("failed to load supply chain from %s: %w", path, err)
		}
	}

	// Override polling intervals from environment
	if initial, ok := envInt("GOODS_POLL_INTERVAL_INITIAL"); ok && initial > 0 {
		cfg.PollingIntervals.InitialSeconds = initial
	}

	if settled, ok := envInt("GOODS_POLL_INTERVAL_SETTLED"); ok && settled > 0 {
		cfg.PollingIntervals.SettledSeconds = settled
	}

	// Override parallel execution settings
	if enabledStr := os.Getenv("GOODS_PARALLEL_ENABLED"); enabledStr != "" {
		cfg.ParallelExecution.Enabled = enabledStr == "true" || enabledStr == "1"
	}

	if maxWorkers, ok := envInt("GOODS_MAX_WORKERS_PER_LEVEL"); ok && maxWorkers >= 0 {
		cfg.ParallelExecution.MaxWorkersPerLevel = maxWorkers
	}

	return cfg, nil
}

func envInt(name string) (int, bool) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, false
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil {
		return 0, false
	}
	return value, true
}

// loadSupplyChainFromFile loads the supply chain map from a JSON file
func loadSupplyChainFromFile(path string, cfg *FactoryConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	var fileConfig struct {
		SupplyChainMap map[string][]string `json:"supply_chain_map"`
	}

	if err := json.Unmarshal(data, &fileConfig); err != nil {
		return fmt.Errorf("failed to parse JSON: %w", err)
	}

	if len(fileConfig.SupplyChainMap) == 0 {
		return fmt.Errorf("supply_chain_map is empty in config file")
	}

	cfg.SupplyChainMap = fileConfig.SupplyChainMap
	return nil
}

// DefaultSupplyChainMap returns the embedded default supply chain mapping
// This is the same data as in supply_chain_data.go but structured for config
func DefaultSupplyChainMap() map[string][]string {
	return map[string][]string{
		// Tier 3 - Advanced Components
		"ADVANCED_CIRCUITRY": {"ELECTRONICS", "MICROPROCESSORS"},

		// Tier 2 - Intermediate Components
		"ELECTRONICS":         {"SILICON_CRYSTALS", "COPPER"},
		"MICROPROCESSORS":     {"SILICON_CRYSTALS"},
		"FERTILIZERS":         {"AMMONIA_ICE"},
		"FABRICS":             {"EXOTIC_MATTER"},
		"FOOD":                {"BOTANICAL_SPECIMENS"},
		"JEWELRY":             {"PRECIOUS_STONES"},
		"MACHINERY":           {"IRON"},
		"MOOD_REGULATORS":     {"HYDROCARBON"},
		"PLASTICS":            {"HYDROCARBON"},
		"POLYNUCLEOTIDES":     {"LIQUID_NITROGEN"},
		"BIOCOMPOSITES":       {"BOTANICAL_SPECIMENS"},
		"QUANTUM_DRIVES":      {"QUARTZ_SAND"},
		"VIRAL_AGENTS":        {"BOTANICAL_SPECIMENS"},
		"MEDICINE":            {"BOTANICAL_SPECIMENS"},
		"DRUGS":               {"BOTANICAL_SPECIMENS"},
		"NANOBOTS":            {"SILICON_CRYSTALS"},
		"AI_MAINFRAMES":       {"MICROPROCESSORS"},
		"QUANTUM_STABILIZERS": {"EXOTIC_MATTER"},
		"ROBOTIC_DRONES":      {"MACHINERY"},

		// Tier 1 - Basic Components (some require inputs)
		"COPPER":   {},
		"IRON":     {},
		"ALUMINUM": {},

		// Tier 0 - Raw Materials (no inputs)
		"SILICON_CRYSTALS":    {},
		"QUARTZ_SAND":         {},
		"AMMONIA_ICE":         {},
		"LIQUID_NITROGEN":     {},
		"LIQUID_HYDROGEN":     {},
		"HYDROCARBON":         {},
		"EXOTIC_MATTER":       {},
		"PRECIOUS_STONES":     {},
		"BOTANICAL_SPECIMENS": {},
		"URANITE_ORE":         {},
		"MERITIUM_ORE":        {},
		"GOLD_ORE":            {},
		"PLATINUM_ORE":        {},
		"DIAMONDS":            {},
		"SILVER_ORE":          {},
	}
}

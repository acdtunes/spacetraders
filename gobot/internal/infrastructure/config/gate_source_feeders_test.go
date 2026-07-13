package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// sp-hoc6: the gate source-factory feeder set must be CONFIG-DRIVEN (RULINGS #5 — the
// Analyst owns which materials get a standing InputsOnly feeder), never a hardcoded
// F48/D42 literal in the launch code. These tests pin the config surface: a live-by-default
// set covering the current gate, expressed as a config-field DEFAULT (a parameter) that
// config.yaml overrides.

// The default (applied by SetDefaults) covers the current gate's buy-direct materials so
// the construction drain never buys the gate output with zero feeding (RULINGS: no
// dark-shipping). The default carries an empty System — resolved to the home system at
// launch (single-system, RULINGS #14) — so it is not era-specific.
func TestGateSourceFeedersDefault(t *testing.T) {
	cfg := &Config{}
	SetDefaults(cfg)

	require.Equal(t, []GateSourceFeeder{
		{Good: "FAB_MATS"},
		{Good: "ADVANCED_CIRCUITRY"},
	}, cfg.Bootstrap.GateSourceFeeders)
}

// The set is DERIVED from config, not hardcoded: an explicit [bootstrap] gate_source_feeders
// list wins outright over the default, and each entry's good + system are carried through.
// Changing the config changes the feeder set.
func TestGateSourceFeedersConfigOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := "bootstrap:\n" +
		"  gate_source_feeders:\n" +
		"    - good: ELECTRONICS\n" +
		"      system: X1-CUSTOM\n"
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, []GateSourceFeeder{
		{Good: "ELECTRONICS", System: "X1-CUSTOM"},
	}, cfg.Bootstrap.GateSourceFeeders)
}

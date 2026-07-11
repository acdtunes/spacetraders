package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// sp-13tl round-trip pin: the pre-positioning capital-ceiling ENABLEMENT knob must
// travel from config.yaml into the loaded config unchanged (the seam the enablement
// runbook depends on), and an ABSENT key must resolve to the parked sentinel 0 — never
// a silent 10% default at the config layer. Mirrors the LoadConfig cwd-config pattern in
// resolution_test.go, exercising the REAL viper mapstructure pipeline
// (contract.pre_positioning.capital_ceiling_pct -> Config.Contract.PrePositioning.CapitalCeilingPct).

func TestLoadConfig_PrePositioningCapitalCeilingPct_RoundTrips(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"contract:\n"+
			"  pre_positioning:\n"+
			"    enabled: true\n"+
			"    capital_ceiling_pct: 15\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.True(t, cfg.Contract.PrePositioning.Enabled)
	require.Equal(t, 15, cfg.Contract.PrePositioning.CapitalCeilingPct,
		"capital_ceiling_pct must reach the config struct so the captain can enable pre-positioning by editing config.yaml + restarting")
}

func TestLoadConfig_PrePositioningCapitalCeilingPct_AbsentIsParkedZero(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	// enabled but NO capital_ceiling_pct — exactly the live config.yaml shape.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"contract:\n"+
			"  pre_positioning:\n"+
			"    enabled: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.True(t, cfg.Contract.PrePositioning.Enabled)
	require.Equal(t, 0, cfg.Contract.PrePositioning.CapitalCeilingPct,
		"an absent ceiling must be the parked sentinel 0 (dormant, fail closed), never a config-layer default")
}

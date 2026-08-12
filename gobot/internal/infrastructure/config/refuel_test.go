package config

// Round-trip pin on the ONE key an operator edits. A typo in the mapstructure tag, or a
// mis-nested section, ships a knob that reads as configurable and moves nothing — the
// failure mode config.yaml's own capacity_reconciler note warns about. Exercises the REAL
// viper pipeline (refuel.threshold -> RefuelConfig.Threshold), which is the only place
// that can be seen.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfig_RefuelThreshold_RoundTrips(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"refuel:\n"+
			"  threshold: 0.9\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.InDelta(t, 0.9, cfg.Refuel.Threshold, 1e-9,
		"refuel.threshold must reach the config struct, or the refuel rate cannot be retuned by editing config.yaml + restarting")
}

// An absent section must leave the sentinel 0, never a config-layer default: the floor
// lives in ONE place, the strategy that enforces it.
func TestLoadConfig_RefuelThreshold_AbsentIsZeroSentinel(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Zero(t, cfg.Refuel.Threshold,
		"an absent [refuel] section must stay 0 so the strategy's floor is the single source of the default")
}

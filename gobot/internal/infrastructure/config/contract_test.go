package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestContractDefaults proves SetDefaults applies a sane, non-zero value
// floor (sp-snmb) even when the operator has configured nothing: 10,000
// credits sits comfortably above the bead's cited micro-contract examples
// (2,500cr, 7,840cr) and well below the normal 104k-287k range, so it
// rejects garbage contracts without threatening real ones.
func TestContractDefaults(t *testing.T) {
	cfg := &Config{}
	SetDefaults(cfg)

	require.Equal(t, 10000, cfg.Contract.ValueFloor)
}

// TestContractValueFloorConfigurableViaConfigFile proves the captain/operator
// can retune the floor (e.g. after a duty-cycle KPI review) via config.yaml
// without a code change.
func TestContractValueFloorConfigurableViaConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("contract:\n  value_floor: 25000\n"), 0o644))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, 25000, cfg.Contract.ValueFloor)
}

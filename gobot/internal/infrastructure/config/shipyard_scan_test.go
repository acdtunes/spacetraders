package config

// Round-trip pin on the knob an operator edits, plus the precedence between it and the older
// scouting-section window. A typo in the mapstructure tag ships a knob that reads as
// configurable and moves nothing, which only the REAL viper pipeline can show.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadConfig_ShipyardRescanTTLMinutes_RoundTrips(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"shipyard_scan:\n"+
			"  rescan_ttl_minutes: 720\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, 720, cfg.ShipyardScan.RescanTTLMinutes,
		"shipyard_scan.rescan_ttl_minutes must reach the config struct, or the per-yard window cannot be retuned by editing config.yaml + restarting")
}

func TestLoadConfig_ShipyardRescanTTLMinutes_AbsentIsZeroSentinel(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"shipyard_scan:\n"+
			"  budget_req_per_sec: 0.2\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Zero(t, cfg.ShipyardScan.RescanTTLMinutes,
		"an absent rescan_ttl_minutes must stay 0 so the scanner's own default is the single source of the window")
}

// Both knobs govern one field, so their order must be pinned: the new one wins where set,
// the older seconds knob still governs where it does not, and neither set changes nothing.
func TestShipyardScanConfig_ResolvedRescanTTL_Precedence(t *testing.T) {
	tests := []struct {
		name           string
		minutes        int
		scoutingSecond int
		want           time.Duration
	}{
		{"minutes knob wins", 720, 900, 12 * time.Hour},
		{"scouting seconds still govern when unset", 0, 900, 15 * time.Minute},
		{"neither set defers to the scanner default", 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ShipyardScanConfig{RescanTTLMinutes: tc.minutes}
			require.Equal(t, tc.want, cfg.ResolvedRescanTTL(tc.scoutingSecond))
		})
	}
}

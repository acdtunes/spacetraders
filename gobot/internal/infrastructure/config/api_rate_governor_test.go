package config

// Round-trip pins on the two knobs that arm the API rate governor. A typo in either
// mapstructure tag ships a knob that reads as configurable and moves nothing — only the
// REAL viper pipeline can show that.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadConfig_APIRateGovernorKnobs_RoundTrip(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"daemon:\n"+
			"  api_target_req_per_sec: 2.2\n"+
			"  api_governor_cooldown_minutes: 45\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, 2.2, cfg.Daemon.APITargetReqPerSec,
		"daemon.api_target_req_per_sec must reach the config struct, or the sustained rate cannot be raised by editing config.yaml + restarting")
	require.Equal(t, 45, cfg.Daemon.APIGovernorCooldownMinutes,
		"daemon.api_governor_cooldown_minutes must reach the config struct, or the 429 hold cannot be retuned")
}

// Absent must stay the zero sentinel: the client's own 2.0 floor and built-in cooldown are
// the single source of the defaults, so nothing here may invent one.
func TestLoadConfig_APIRateGovernorKnobs_AbsentAreZeroSentinels(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"daemon:\n"+
			"  socket_path: /tmp/st.sock\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Zero(t, cfg.Daemon.APITargetReqPerSec)
	require.Zero(t, cfg.Daemon.APIGovernorCooldownMinutes)
}

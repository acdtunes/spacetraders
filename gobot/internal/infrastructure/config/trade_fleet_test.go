package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// sp-686e round-trip pin: the stranded-hull detector threshold must travel from
// config.yaml's [trade_fleet] section into the loaded config unchanged (the seam the
// stranded-detection knob depends on), and an ABSENT key must resolve to the sentinel 0 —
// never a silent config-layer default. The tour coordinator's resolveStrandedThreshold
// turns 0/absent into the documented default 3, so the default lives in ONE place (the
// consumer), not smeared across the config layer. Exercises the REAL viper mapstructure
// pipeline (trade_fleet.stranded_consecutive_threshold -> TradeFleetConfig).

func TestLoadConfig_StrandedConsecutiveThreshold_RoundTrips(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"+
			"  stranded_consecutive_threshold: 5\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, 5, cfg.TradeFleet.StrandedConsecutiveThreshold,
		"stranded_consecutive_threshold must reach the config struct so the captain can retune the stranded page threshold by editing config.yaml + restarting")
}

func TestLoadConfig_StrandedConsecutiveThreshold_AbsentIsZeroSentinel(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	// enabled but NO stranded_consecutive_threshold — the default config.yaml shape.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, 0, cfg.TradeFleet.StrandedConsecutiveThreshold,
		"an absent threshold must be the sentinel 0 (the consumer resolves 0 -> default 3), never a config-layer default")
}

// closed_tours round-trip pin: the closed-tour arming knob (im74 solver support, this
// bead's config plumbing) must travel from config.yaml's [trade_fleet] section into the
// loaded config unchanged, so a captain arms closed-circuit tours by editing config.yaml
// + restarting — no code redeploy. This exercises the REAL viper mapstructure pipeline
// (trade_fleet.closed_tours -> TradeFleetConfig.ClosedTours), the ONE seam the grpc
// stamp/rebuild tests cannot cover (they set the struct field directly). A typo in the
// mapstructure tag would ship a silently-inert knob; this test catches it.
func TestLoadConfig_ClosedTours_RoundTrips(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"+
			"  closed_tours: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.True(t, cfg.TradeFleet.ClosedTours,
		"closed_tours must reach the config struct so the captain can arm closed-circuit tours by editing config.yaml + restarting")
}

// closed_tours default-safety companion: an ABSENT closed_tours key resolves to false —
// the Go zero value viper leaves untouched — so a daemon that never sets the knob runs
// OPEN tours, byte-identical to today. This is the config-layer half of the default-safe
// proof (the grpc rebuild test proves the false reaches cmd.ClosedTours).
func TestLoadConfig_ClosedTours_AbsentIsFalse(t *testing.T) {
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	// enabled but NO closed_tours — the default config.yaml shape.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(
		"trade_fleet:\n"+
			"  enabled: true\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.False(t, cfg.TradeFleet.ClosedTours,
		"an absent closed_tours must default false (OPEN tours), never a config-layer default that silently arms closed mode")
}

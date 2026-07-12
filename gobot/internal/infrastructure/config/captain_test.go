package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCaptainMetaReviewDaysZeroDisablesViaConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("captain:\n  meta_review_days: 0\n"), 0o644))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Captain.MetaReviewDays)
	require.Equal(t, 0, *cfg.Captain.MetaReviewDays)
}

func TestCaptainMetaReviewDaysDefaultsWhenUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("captain:\n  enabled: false\n"), 0o644))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Captain.MetaReviewDays)
	require.Equal(t, 7, *cfg.Captain.MetaReviewDays)
}

func TestCaptainDefaults(t *testing.T) {
	cfg := &Config{}
	SetDefaults(cfg)

	require.False(t, cfg.Captain.Enabled)
	require.Equal(t, 30, cfg.Captain.PollIntervalSeconds)
	require.Equal(t, 45, cfg.Captain.HeartbeatMinutes)
	require.Equal(t, 6, cfg.Captain.MaxSessionsPerHour)
	require.Equal(t, 10, cfg.Captain.SessionTimeoutMinutes)
	require.Equal(t, 30, cfg.Captain.ShipIdleMinutes)
	require.Equal(t, 5, cfg.Captain.StaleHeartbeatMinutes)
	require.Equal(t, "claude", cfg.Captain.ClaudeBin)
	require.Equal(t, "opus", cfg.Captain.Model)
	require.Equal(t, "../captain", cfg.Captain.WorkspaceDir)
	require.Equal(t, []int{100000, 250000, 500000, 1000000}, cfg.Captain.CreditsThresholds)
	require.False(t, cfg.Captain.AutoMerge)
	require.Equal(t, 3, cfg.Captain.MaxFixesPerDay)
	require.Equal(t, 1, cfg.Captain.MaxFeaturesPerDay)
	require.Equal(t, 30, cfg.Captain.FixSessionTimeoutMinutes)
	require.Equal(t, 400, cfg.Captain.MaxFeatureDiffLines)
	require.Equal(t, ".", cfg.Captain.RepoDir)
	require.Equal(t, "make restart-daemon", cfg.Captain.RestartCmd)
}

func TestCaptainBridgeDefaults(t *testing.T) {
	cfg := &Config{}
	SetDefaults(cfg)

	require.Equal(t, "legacy", cfg.Captain.EngineMode)
	require.Equal(t, "captain", cfg.Captain.CaptainAgent)
	require.Equal(t, 10, cfg.Captain.AckTimeoutMinutes)
	require.Equal(t, 3, cfg.Captain.EscalateAfterRenudges)
	require.Equal(t, "human", cfg.Captain.AdmiralAlias)
	require.Equal(t, "gc", cfg.Captain.GCBin)
	require.Equal(t, "bd", cfg.Captain.BDBin)
	require.Equal(t, "../city", cfg.Captain.CityDir)
	require.Equal(t, 24, cfg.Captain.UniverseCheckHours)
	require.NotNil(t, cfg.Captain.MetaReviewDays)
	require.Equal(t, 7, *cfg.Captain.MetaReviewDays)
	require.Equal(t, 180, cfg.Captain.MaxWakeIntervalMinutes, "sp-sk68 never-wake safety ceiling")
}

func TestCaptainDetectorDefaults(t *testing.T) {
	cfg := &Config{}
	SetDefaults(cfg)

	require.Equal(t, 2, cfg.Captain.IncomeStallHours)
	require.Equal(t, 30, cfg.Captain.StreamDownMinutes)
	require.Equal(t, 5, cfg.Captain.PinnedHullContainerlessMinutes, "sp-h88r: promoted watchdog threshold preserves the historical 5m default")
	require.Empty(t, cfg.Captain.ExpectedStreams)
}

// TestCaptainPinnedHullContainerlessTunable proves the sp-v63s watchdog threshold
// is now a live CaptainConfig knob (sp-h88r): a set value is honored and tunes the
// window without a rebuild, while an explicit zero resolves back to the 5m default
// exactly as an unset value does — the detector must never be silently disabled by
// a zero that a partial config left behind.
func TestCaptainPinnedHullContainerlessTunable(t *testing.T) {
	t.Run("set value is honored", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte("captain:\n  pinned_hull_containerless_minutes: 12\n"), 0o644))

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		require.Equal(t, 12, cfg.Captain.PinnedHullContainerlessMinutes)
	})

	t.Run("explicit zero preserves the 5m default", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte("captain:\n  pinned_hull_containerless_minutes: 0\n"), 0o644))

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		require.Equal(t, 5, cfg.Captain.PinnedHullContainerlessMinutes)
	})

	t.Run("unset preserves the 5m default", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte("captain:\n  enabled: false\n"), 0o644))

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		require.Equal(t, 5, cfg.Captain.PinnedHullContainerlessMinutes)
	})
}

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
}

func TestCaptainDetectorDefaults(t *testing.T) {
	cfg := &Config{}
	SetDefaults(cfg)

	require.Equal(t, 2, cfg.Captain.IncomeStallHours)
	require.Equal(t, 30, cfg.Captain.StreamDownMinutes)
	require.Empty(t, cfg.Captain.ExpectedStreams)
}

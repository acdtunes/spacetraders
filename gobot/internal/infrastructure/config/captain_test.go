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
}

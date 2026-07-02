package config

// CaptainConfig configures the autonomous captain supervisor (cmd/captain).
type CaptainConfig struct {
	Enabled               bool   `mapstructure:"enabled"`
	PlayerID              int    `mapstructure:"player_id" validate:"omitempty,min=1"`
	WorkspaceDir          string `mapstructure:"workspace_dir"`
	ClaudeBin             string `mapstructure:"claude_bin"`
	Model                 string `mapstructure:"model"`
	PollIntervalSeconds   int    `mapstructure:"poll_interval_seconds" validate:"omitempty,min=5"`
	HeartbeatMinutes      int    `mapstructure:"heartbeat_minutes" validate:"omitempty,min=1"`
	MaxSessionsPerHour    int    `mapstructure:"max_sessions_per_hour" validate:"omitempty,min=1"`
	SessionTimeoutMinutes int    `mapstructure:"session_timeout_minutes" validate:"omitempty,min=1"`
	ShipIdleMinutes       int    `mapstructure:"ship_idle_minutes" validate:"omitempty,min=1"`
	StaleHeartbeatMinutes int    `mapstructure:"stale_heartbeat_minutes" validate:"omitempty,min=1"`
	CreditsThresholds     []int  `mapstructure:"credits_thresholds"`
}

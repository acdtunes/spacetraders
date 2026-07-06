package config

// CaptainConfig configures the autonomous captain supervisor (cmd/captain).
type CaptainConfig struct {
	Enabled               bool   `mapstructure:"enabled"`
	PlayerID              int    `mapstructure:"player_id" validate:"omitempty,min=1"`
	WorkspaceDir          string `mapstructure:"workspace_dir"`
	ClaudeBin             string `mapstructure:"claude_bin"`
	Model                 string `mapstructure:"model"`
	FixModel              string `mapstructure:"fix_model"` // model for fix/feature/automation builds; defaults to Model
	PollIntervalSeconds   int    `mapstructure:"poll_interval_seconds" validate:"omitempty,min=5"`
	HeartbeatMinutes      int    `mapstructure:"heartbeat_minutes" validate:"omitempty,min=1"`
	MaxSessionsPerHour    int    `mapstructure:"max_sessions_per_hour" validate:"omitempty,min=1"`
	SessionTimeoutMinutes int    `mapstructure:"session_timeout_minutes" validate:"omitempty,min=1"`
	ShipIdleMinutes       int    `mapstructure:"ship_idle_minutes" validate:"omitempty,min=1"`
	StaleHeartbeatMinutes int    `mapstructure:"stale_heartbeat_minutes" validate:"omitempty,min=1"`
	CreditsThresholds     []int  `mapstructure:"credits_thresholds"`

	// Self-improvement pipeline (plan 2 of 2)
	AutoMerge                bool   `mapstructure:"auto_merge"`
	MaxFixesPerDay           int    `mapstructure:"max_fixes_per_day" validate:"omitempty,min=1"`
	MaxFeaturesPerDay        int    `mapstructure:"max_features_per_day" validate:"omitempty,min=1"`
	FixSessionTimeoutMinutes int    `mapstructure:"fix_session_timeout_minutes" validate:"omitempty,min=1"`
	MaxFeatureDiffLines      int    `mapstructure:"max_feature_diff_lines" validate:"omitempty,min=10"`
	RepoDir                  string `mapstructure:"repo_dir"`
	RestartCmd               string `mapstructure:"restart_cmd"`

	// City-bridge engine (plan 2026-07-06-ai-engine-city-bridge)
	EngineMode            string `mapstructure:"engine_mode"`   // "legacy" | "bridge"; default "legacy"
	CaptainAgent          string `mapstructure:"captain_agent"` // default "captain"
	AckTimeoutMinutes     int    `mapstructure:"ack_timeout_minutes" validate:"omitempty,min=1"`
	EscalateAfterRenudges int    `mapstructure:"escalate_after_renudges" validate:"omitempty,min=1"`
	AdmiralAlias          string `mapstructure:"admiral_alias"` // default "human"
	GCBin                 string `mapstructure:"gc_bin"`        // default "gc"
	BDBin                 string `mapstructure:"bd_bin"`        // default "bd"
	CityDir               string `mapstructure:"city_dir"`      // default "../city"
}

package config

// CaptainConfig configures the autonomous watchkeeper supervisor (cmd/watchkeeper).
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

	IncomeStallHours  int      `mapstructure:"income_stall_hours" validate:"omitempty,min=1"`
	StreamDownMinutes int      `mapstructure:"stream_down_minutes" validate:"omitempty,min=1"`
	ExpectedStreams   []string `mapstructure:"expected_streams"`

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

	UniverseCheckHours int `mapstructure:"universe_check_hours" validate:"omitempty,min=1"`

	MetaReviewDays *int `mapstructure:"meta_review_days" validate:"omitempty,min=0"`

	// MaxWakeIntervalMinutes is the never-wake safety ceiling (spec: sp-sk68
	// wake model): a captain-declared NextWakeAt can delay a wake past the
	// default heartbeat cadence, but it is always capped at
	// LastSession+MaxWakeIntervalMinutes so a wake policy can never suppress
	// a session indefinitely.
	MaxWakeIntervalMinutes int `mapstructure:"max_wake_interval_minutes" validate:"omitempty,min=1"`

	// RolloverNudgeHours is the session-age threshold (sp-0zx9) past which the
	// watchkeeper nudges a standing session to hand off and restart with a
	// fresh context (mirrors the surveyor cadence's MetaReviewDays, but keyed
	// on a session's own age rather than a fixed-agent timer). Nil/0 disables
	// the nudge entirely. Default 24h.
	RolloverNudgeHours *int `mapstructure:"rollover_nudge_hours" validate:"omitempty,min=0"`

	// WeeklyTokenBudget is the configured weekly-quota PROXY (sp-1vkr): the
	// Anthropic/claude billing layer exposes nothing machine-readable to this
	// CLI, so this is an operator-set token budget the fleet's cumulative
	// usage (captain tokens/report, same window) is compared against.
	// Nil/unset disables the quota block entirely — captain tokens/report
	// simply omit it rather than asserting an ungrounded number.
	WeeklyTokenBudget *int64 `mapstructure:"weekly_token_budget" validate:"omitempty,min=0"`

	// QuotaAlertThresholdPct is the percent-of-budget crossing that flips the
	// quota block's Alert flag — the credits.threshold alerting shape
	// (EventCreditsThreshold in internal/domain/captain/events.go) applied to
	// the token-budget proxy instead of live in-game agent credits. Default 80.
	QuotaAlertThresholdPct *int `mapstructure:"quota_alert_threshold_pct" validate:"omitempty,min=1,max=100"`
}

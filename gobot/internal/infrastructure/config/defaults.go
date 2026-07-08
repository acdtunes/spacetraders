package config

import "time"

// SetDefaults sets default values for all configuration fields
func SetDefaults(cfg *Config) {
	// Database defaults
	if cfg.Database.Type == "" {
		cfg.Database.Type = "postgres"
	}
	if cfg.Database.Host == "" {
		cfg.Database.Host = "localhost"
	}
	if cfg.Database.Port == 0 {
		cfg.Database.Port = 5432
	}
	if cfg.Database.User == "" {
		cfg.Database.User = "spacetraders"
	}
	if cfg.Database.Name == "" {
		cfg.Database.Name = "spacetraders"
	}
	if cfg.Database.SSLMode == "" {
		cfg.Database.SSLMode = "disable"
	}
	if cfg.Database.Pool.MaxOpen == 0 {
		cfg.Database.Pool.MaxOpen = 25
	}
	if cfg.Database.Pool.MaxIdle == 0 {
		cfg.Database.Pool.MaxIdle = 5
	}
	if cfg.Database.Pool.MaxLifetime == 0 {
		cfg.Database.Pool.MaxLifetime = 5 * time.Minute
	}

	// API defaults
	if cfg.API.BaseURL == "" {
		cfg.API.BaseURL = "https://api.spacetraders.io/v2"
	}
	if cfg.API.Timeout == 0 {
		cfg.API.Timeout = 30 * time.Second
	}
	if cfg.API.RateLimit.Requests == 0 {
		cfg.API.RateLimit.Requests = 2
	}
	if cfg.API.RateLimit.Burst == 0 {
		cfg.API.RateLimit.Burst = 10
	}
	if cfg.API.Retry.MaxAttempts == 0 {
		cfg.API.Retry.MaxAttempts = 3
	}
	if cfg.API.Retry.BackoffBase == 0 {
		cfg.API.Retry.BackoffBase = 1 * time.Second
	}

	// Routing defaults
	if cfg.Routing.Address == "" {
		cfg.Routing.Address = "localhost:50051"
	}
	if cfg.Routing.Timeout.Connect == 0 {
		cfg.Routing.Timeout.Connect = 10 * time.Second
	}
	if cfg.Routing.Timeout.Dijkstra == 0 {
		cfg.Routing.Timeout.Dijkstra = 30 * time.Second
	}
	if cfg.Routing.Timeout.TSP == 0 {
		cfg.Routing.Timeout.TSP = 60 * time.Second
	}
	if cfg.Routing.Timeout.VRP == 0 {
		cfg.Routing.Timeout.VRP = 120 * time.Second
	}

	// Daemon defaults
	if cfg.Daemon.Address == "" {
		cfg.Daemon.Address = "localhost:50052"
	}
	if cfg.Daemon.SocketPath == "" {
		cfg.Daemon.SocketPath = "/tmp/spacetraders-daemon.sock"
	}
	if cfg.Daemon.PIDFile == "" {
		cfg.Daemon.PIDFile = "/tmp/spacetraders-daemon.pid"
	}
	if cfg.Daemon.MaxContainers == 0 {
		cfg.Daemon.MaxContainers = 100
	}
	if cfg.Daemon.HealthCheckInterval == 0 {
		cfg.Daemon.HealthCheckInterval = 30 * time.Second
	}
	if cfg.Daemon.ShutdownTimeout == 0 {
		cfg.Daemon.ShutdownTimeout = 30 * time.Second
	}
	if cfg.Daemon.RestartPolicy.MaxAttempts == 0 {
		cfg.Daemon.RestartPolicy.MaxAttempts = 3
	}
	if cfg.Daemon.RestartPolicy.Delay == 0 {
		cfg.Daemon.RestartPolicy.Delay = 5 * time.Second
	}
	if cfg.Daemon.RestartPolicy.BackoffMultiplier == 0 {
		cfg.Daemon.RestartPolicy.BackoffMultiplier = 2.0
	}

	// Logging defaults
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "json"
	}
	if cfg.Logging.Output == "" {
		cfg.Logging.Output = "stdout"
	}
	if cfg.Logging.Rotation.MaxSize == 0 {
		cfg.Logging.Rotation.MaxSize = 100 // MB
	}
	if cfg.Logging.Rotation.MaxBackups == 0 {
		cfg.Logging.Rotation.MaxBackups = 3
	}
	if cfg.Logging.Rotation.MaxAge == 0 {
		cfg.Logging.Rotation.MaxAge = 28 // days
	}

	// Metrics defaults
	// Note: Metrics are disabled by default (opt-in via ST_METRICS_ENABLED=true)
	if cfg.Metrics.Port == 0 {
		cfg.Metrics.Port = 9090
	}
	if cfg.Metrics.Host == "" {
		cfg.Metrics.Host = "localhost"
	}
	if cfg.Metrics.Path == "" {
		cfg.Metrics.Path = "/metrics"
	}

	// Captain defaults
	if cfg.Captain.PollIntervalSeconds == 0 {
		cfg.Captain.PollIntervalSeconds = 30
	}
	if cfg.Captain.HeartbeatMinutes == 0 {
		cfg.Captain.HeartbeatMinutes = 45
	}
	if cfg.Captain.MaxSessionsPerHour == 0 {
		cfg.Captain.MaxSessionsPerHour = 6
	}
	if cfg.Captain.SessionTimeoutMinutes == 0 {
		cfg.Captain.SessionTimeoutMinutes = 10
	}
	if cfg.Captain.ShipIdleMinutes == 0 {
		cfg.Captain.ShipIdleMinutes = 30
	}
	if cfg.Captain.StaleHeartbeatMinutes == 0 {
		cfg.Captain.StaleHeartbeatMinutes = 5
	}
	if cfg.Captain.IncomeStallHours == 0 {
		cfg.Captain.IncomeStallHours = 2
	}
	if cfg.Captain.StreamDownMinutes == 0 {
		cfg.Captain.StreamDownMinutes = 30
	}
	if cfg.Captain.ClaudeBin == "" {
		cfg.Captain.ClaudeBin = "claude"
	}
	if cfg.Captain.Model == "" {
		cfg.Captain.Model = "opus"
	}
	if cfg.Captain.FixModel == "" {
		cfg.Captain.FixModel = cfg.Captain.Model
	}
	if cfg.Captain.WorkspaceDir == "" {
		cfg.Captain.WorkspaceDir = "../captain"
	}
	if len(cfg.Captain.CreditsThresholds) == 0 {
		cfg.Captain.CreditsThresholds = []int{100000, 250000, 500000, 1000000}
	}
	if cfg.Captain.MaxFixesPerDay == 0 {
		cfg.Captain.MaxFixesPerDay = 3
	}
	if cfg.Captain.MaxFeaturesPerDay == 0 {
		cfg.Captain.MaxFeaturesPerDay = 1
	}
	if cfg.Captain.FixSessionTimeoutMinutes == 0 {
		cfg.Captain.FixSessionTimeoutMinutes = 30
	}
	if cfg.Captain.MaxFeatureDiffLines == 0 {
		cfg.Captain.MaxFeatureDiffLines = 400
	}
	if cfg.Captain.RepoDir == "" {
		cfg.Captain.RepoDir = "."
	}
	if cfg.Captain.RestartCmd == "" {
		cfg.Captain.RestartCmd = "make restart-daemon"
	}
	if cfg.Captain.EngineMode == "" {
		cfg.Captain.EngineMode = "legacy"
	}
	if cfg.Captain.CaptainAgent == "" {
		cfg.Captain.CaptainAgent = "captain"
	}
	if cfg.Captain.AckTimeoutMinutes == 0 {
		cfg.Captain.AckTimeoutMinutes = 10
	}
	if cfg.Captain.EscalateAfterRenudges == 0 {
		cfg.Captain.EscalateAfterRenudges = 3
	}
	if cfg.Captain.AdmiralAlias == "" {
		cfg.Captain.AdmiralAlias = "human"
	}
	if cfg.Captain.GCBin == "" {
		cfg.Captain.GCBin = "gc"
	}
	if cfg.Captain.BDBin == "" {
		cfg.Captain.BDBin = "bd"
	}
	if cfg.Captain.CityDir == "" {
		cfg.Captain.CityDir = "../city"
	}
	if cfg.Captain.UniverseCheckHours == 0 {
		cfg.Captain.UniverseCheckHours = 24
	}
	if cfg.Captain.MetaReviewDays == nil {
		metaReviewDaysDefault := 7
		cfg.Captain.MetaReviewDays = &metaReviewDaysDefault
	}
	if cfg.Captain.MaxWakeIntervalMinutes == 0 {
		cfg.Captain.MaxWakeIntervalMinutes = 180
	}
	if cfg.Captain.RolloverNudgeHours == nil {
		rolloverNudgeHoursDefault := 24
		cfg.Captain.RolloverNudgeHours = &rolloverNudgeHoursDefault
	}
	// WeeklyTokenBudget intentionally has NO default: nil means the operator
	// has not configured a weekly-quota proxy, and captain tokens/report must
	// omit the quota block rather than assert an ungrounded number.
	if cfg.Captain.QuotaAlertThresholdPct == nil {
		quotaAlertThresholdPctDefault := 80
		cfg.Captain.QuotaAlertThresholdPct = &quotaAlertThresholdPctDefault
	}
}

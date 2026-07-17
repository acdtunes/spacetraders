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
		// Mirror the limiter's real burst (api.RateLimitBurst = 30). This value
		// is display-only today (surfaced in `config show`, not plumbed to the
		// client); the prior default of 10 lied about the live burst (sp-a5dq).
		cfg.API.RateLimit.Burst = 30
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
	// Gate-graph negative-result backoff (sp-ikx1). Defaults yield the ruled
	// 5m → 30m → 2h re-probe schedule for an unreadable jump gate (5m, 5m×6=30m,
	// 30m×6=180m capped to 2h, then 2h). RULINGS #5: knobs, not constants.
	if cfg.Routing.GateBackoff.Initial == 0 {
		cfg.Routing.GateBackoff.Initial = 5 * time.Minute
	}
	if cfg.Routing.GateBackoff.Multiplier == 0 {
		cfg.Routing.GateBackoff.Multiplier = 6
	}
	if cfg.Routing.GateBackoff.Max == 0 {
		cfg.Routing.GateBackoff.Max = 2 * time.Hour
	}
	// Chart-on-gate-arrival (sp-bcsu): default ON. A nil switch means the captain has not
	// configured [routing] chart_gate_on_arrival, and its intent is to chart every jump gate
	// a hull lands on (the one moment it is readable) so the frontier never strands hulls on
	// empty gate_edges. An explicit `chart_gate_on_arrival: false` is preserved as the
	// reversibility off-switch (restores the pre-sp-bcsu hot path). Charting is best-effort +
	// idempotent, so default-on adds no burst (RULINGS #5: a knob, not a code literal).
	if cfg.Routing.ChartGateOnArrival == nil {
		chartGateOnArrivalDefault := true
		cfg.Routing.ChartGateOnArrival = &chartGateOnArrivalDefault
	}
	// Gate topology-cache TTL (sp-jgcache): default 24h — the near-static gate graph is a
	// comfortable day-long freshness bound (per-tick neighbor scans hit the cache, the graph
	// still self-heals). Zero => unset => the safe default.
	if cfg.Routing.GateCacheTTL == 0 {
		cfg.Routing.GateCacheTTL = 24 * time.Hour
	}
	// Doomed-call precondition (sp-jgcache): default ON. A nil switch means [routing]
	// skip_uncharted_gate_fetch is unconfigured; its intent is to skip the guaranteed-400
	// live read on an uncharted origin gate. An explicit `false` is preserved as the
	// staged-rollout off-switch (restores probe-then-backoff).
	if cfg.Routing.SkipUnchartedGateFetch == nil {
		skipUnchartedDefault := true
		cfg.Routing.SkipUnchartedGateFetch = &skipUnchartedDefault
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
	if cfg.Captain.PinnedHullContainerlessMinutes == 0 {
		cfg.Captain.PinnedHullContainerlessMinutes = 5 // sp-h88r: preserve the historical sp-v63s watchdog default
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
	// WeeklyTokenBudget intentionally has NO default: nil means the operator
	// has not configured a weekly-quota proxy, and captain tokens/report must
	// omit the quota block rather than assert an ungrounded number.
	if cfg.Captain.QuotaAlertThresholdPct == nil {
		quotaAlertThresholdPctDefault := 80
		cfg.Captain.QuotaAlertThresholdPct = &quotaAlertThresholdPctDefault
	}

	// Trade fleet coordinator defaults (sp-1278): default ON. A nil Enabled means the
	// captain has not configured the coordinator, and its intent is to keep continuous
	// tours alive on every trade hull — so an absent [trade_fleet] section runs ON. An
	// explicit `enabled: false` is preserved as the off-switch. The cooldown, tick,
	// concurrency, and per-tour caps default at the coordinator (0 => its own default),
	// so they need no SetDefaults entry here.
	if cfg.TradeFleet.Enabled == nil {
		tradeFleetEnabledDefault := true
		cfg.TradeFleet.Enabled = &tradeFleetEnabledDefault
	}

	// Gate source-factory feeders (sp-hoc6): a live-by-default set covering the current gate's
	// buy-direct materials, so the construction drain never buys the gate output with ZERO feeding
	// (RULINGS: no dark-shipping). This is a config-field DEFAULT — a parameter the Analyst retunes in
	// [bootstrap] gate_source_feeders (RULINGS #5), not a launch-code literal. Each entry carries an
	// empty System, resolved to the home system at launch (single-system, RULINGS #14), so the default
	// is not era-specific. Inputs come from goods.ExportToImportMap automatically — none are listed here.
	if len(cfg.Bootstrap.GateSourceFeeders) == 0 {
		cfg.Bootstrap.GateSourceFeeders = []GateSourceFeeder{
			{Good: "FAB_MATS"},
			{Good: "ADVANCED_CIRCUITRY"},
		}
	}
}

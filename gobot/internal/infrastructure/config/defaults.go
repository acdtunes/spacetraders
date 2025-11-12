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
}

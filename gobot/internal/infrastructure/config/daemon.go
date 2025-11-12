package config

import "time"

// DaemonConfig holds daemon service configuration
type DaemonConfig struct {
	// gRPC server address for daemon (host:port)
	Address string `mapstructure:"address" validate:"required"`

	// Unix socket path for IPC
	SocketPath string `mapstructure:"socket_path"`

	// PID file location
	PIDFile string `mapstructure:"pid_file"`

	// Maximum number of concurrent containers
	MaxContainers int `mapstructure:"max_containers" validate:"min=1"`

	// Health check interval for containers
	HealthCheckInterval time.Duration `mapstructure:"health_check_interval" validate:"required"`

	// Container restart policy
	RestartPolicy RestartPolicyConfig `mapstructure:"restart_policy"`

	// Graceful shutdown timeout
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout" validate:"required"`
}

// RestartPolicyConfig holds container restart policy configuration
type RestartPolicyConfig struct {
	// Enable automatic restart on failure
	Enabled bool `mapstructure:"enabled"`

	// Maximum restart attempts before giving up
	MaxAttempts int `mapstructure:"max_attempts" validate:"min=0"`

	// Delay between restart attempts
	Delay time.Duration `mapstructure:"delay"`

	// Backoff multiplier for retry delays
	BackoffMultiplier float64 `mapstructure:"backoff_multiplier" validate:"min=1"`
}

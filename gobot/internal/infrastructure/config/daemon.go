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

	// MaxCASRetries bounds the optimistic-concurrency retry on a ships.version
	// conflict (sp-01wc): when a concurrent writer commits between a scheduler
	// mutation's find and its save, the mutation is re-loaded on the fresh row,
	// re-applied, and the CAS save retried up to this many times before falling
	// back to last-write-wins — so both writers' mutations survive instead of the
	// loser being clobbered. 0/unset selects the built-in default. Live by
	// default (RULINGS #5); see CASRetryDisabled for the escape hatch.
	MaxCASRetries int `mapstructure:"max_cas_retries"`

	// CASRetryDisabled is the escape hatch (RULINGS #5): true reverts ship saves
	// to the legacy last-write-wins-on-conflict behavior (sp-60ff), disabling
	// re-apply retry entirely. Absent/false = retry ACTIVE.
	CASRetryDisabled bool `mapstructure:"cas_retry_disabled"`

	// AgentCacheTTLSeconds bounds how long the shared API client may serve a
	// cached agent before re-reading /my/agent live (sp-oszc): GetAgent was the
	// #2 API consumer (343 calls / 1306s rate-limit wait) and agent data changes
	// rarely, so a short TTL cuts the redundant reads. 0/unset selects the
	// built-in default (15s). This is only a staleness FLOOR — money safety comes
	// from invalidating the cache on every credit-decreasing call, not the TTL —
	// so tuning it never risks an over-spend. Sticky across restart via config.
	AgentCacheTTLSeconds int `mapstructure:"agent_cache_ttl_seconds"`
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

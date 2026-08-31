package config

// DaemonConfig holds daemon service configuration
type DaemonConfig struct {
	// Unix socket path for IPC
	SocketPath string `mapstructure:"socket_path"`

	// PID file location
	PIDFile string `mapstructure:"pid_file"`

	// MaxCASRetries bounds the optimistic-concurrency retry on a ships.version
	// conflict (sp-01wc): when a concurrent writer commits between a scheduler
	// mutation's find and its save, the mutation is re-loaded on the fresh row,
	// re-applied, and the CAS save retried up to this many times before falling
	// back to last-write-wins — so both writers' mutations survive instead of the
	// loser being clobbered. 0/unset selects the built-in default. Live by
	// default (RULINGS #5).
	MaxCASRetries int `mapstructure:"max_cas_retries"`

	// AgentCacheTTLSeconds bounds how long the shared API client may serve a
	// cached agent before re-reading /my/agent live: GetAgent was the
	// #2 API consumer (343 calls / 1306s rate-limit wait) and agent data changes
	// rarely, so a short TTL cuts the redundant reads. 0/unset selects the
	// built-in default (15s). This is only a staleness FLOOR — money safety comes
	// from invalidating the cache on every credit-decreasing call, not the TTL —
	// so tuning it never risks an over-spend. Sticky across restart via config.
	AgentCacheTTLSeconds int `mapstructure:"agent_cache_ttl_seconds"`

	// LimiterPressureHalfLifeSeconds is the smoothing half-life of the API
	// client's rate-limiter-wait EWMA — the pressure signal the probe-sensing
	// coordinator sheds scanning against. It governs how fast the fleet reacts
	// to (and forgives) limiter contention: shorter reacts within a tick,
	// longer rides out bursts. 0/unset selects the built-in default (30s).
	// Wired at boot via SpaceTradersClient.SetLimiterPressureHalfLife
	// (RULINGS #5: an operational tuning number, not a rebuild).
	LimiterPressureHalfLifeSeconds int `mapstructure:"limiter_pressure_half_life_seconds"`

	// 0/unset = default; see SpaceTradersClient.SetFleetIsolationAbortStreak (RULINGS #5).
	FleetIsolationAbortStreak int `mapstructure:"fleet_isolation_abort_streak"`

	// 0/unset = default; see SpaceTradersClient.SetFleetIsolationProbeBudget (RULINGS #5).
	FleetIsolationProbeBudget int `mapstructure:"fleet_isolation_probe_budget"`
}

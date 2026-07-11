package config

// WorkerRebalancerConfig holds the worker-rebalancer coordinator's knobs (sp-f5pr). The
// daemon injects these into the coordinator container's launch config on every build —
// creation AND restart recovery, via resolveWorkerRebalancerConfig — so a captain retunes
// the standing ferry loop (the on/off switch, the vacancy clock, the source floor, the
// cooldown, the concurrency cap, the per-system light cap) by editing config.yaml and
// restarting the daemon, with NO code redeploy (sp-ts82 live-config pattern, RULINGS
// #2/#5).
//
// A zero value means "unset" and defers to the coordinator's documented default for that
// knob, so the daemon injects only the keys the captain actually set — it never hardcodes
// an operational value. Enabled is the one exception (a *bool) so an unset config defaults
// ON while an explicit `enabled: false` is a real off-switch.
type WorkerRebalancerConfig struct {
	// Enabled turns the coordinator ON (default true). A *bool so an unset config (nil)
	// is distinct from an explicit `enabled: false`: the captain parks the whole ferry
	// loop without touching any hull. EnabledOrDefault resolves nil to true.
	Enabled *bool `mapstructure:"enabled"`

	// TickSeconds is the reconcile cadence (0 => the coordinator default, 60s).
	TickSeconds int `mapstructure:"tick_seconds"`

	// VacancyMinMinutes is how long the OLDEST factory container in a system must have run
	// before that system counts as a hub-vacancy (0 => the coordinator default, 15m) — the
	// restart-safe clock that exempts a just-launched / just-restarted factory.
	VacancyMinMinutes int `mapstructure:"vacancy_min_minutes"`

	// SourceMinIdle is the minimum idle undedicated lights a system must hold to donate one
	// (0 => the coordinator default, 2). At >= 2, ferrying one per vacancy per tick never
	// strips a source below one idle.
	SourceMinIdle int `mapstructure:"source_min_idle"`

	// FerryCooldownSeconds suppresses a NEW ferry to a system within this window of the
	// most-recent ferry that targeted it (0 => the coordinator default, 600s).
	FerryCooldownSeconds int `mapstructure:"ferry_cooldown_seconds"`

	// MaxConcurrentFerries caps simultaneously-running ferries (0 => the coordinator
	// default, 2).
	MaxConcurrentFerries int `mapstructure:"max_concurrent_ferries"`

	// MaxLightsPerSystem caps the light-haulers (in-system + in-flight inbound) a system
	// may accumulate (0 => uncapped, the coordinator default).
	MaxLightsPerSystem int `mapstructure:"max_lights_per_system"`
}

// EnabledOrDefault reports whether the coordinator is enabled, treating an unset (nil)
// value as true — the default-ON behavior the bead intends (sp-f5pr).
func (c WorkerRebalancerConfig) EnabledOrDefault() bool {
	return c.Enabled == nil || *c.Enabled
}

package config

// TradeFleetConfig holds the trade-fleet coordinator's knobs (sp-1278). The daemon
// injects these into the coordinator container's launch config on every build —
// creation AND restart recovery, via resolveTradeFleetConfig — so a captain retunes
// the standing relaunch loop (the on/off switch, the per-hull cooldown, the
// concurrency cap, and the per-tour spend/reserve caps) by editing config.yaml and
// restarting the daemon, with NO code redeploy (sp-ts82 live-config pattern, RULINGS
// #2/#5).
//
// A zero value means "unset" and defers to the coordinator's documented default for
// that knob, so the daemon injects only the keys the captain actually set — it never
// hardcodes an operational value. Enabled is the one exception (a *bool) so an unset
// config defaults ON while an explicit `enabled: false` is a real off-switch.
type TradeFleetConfig struct {
	// Enabled turns the coordinator ON (default true, sp-1278 intent). A *bool so an
	// unset config (nil) is distinct from an explicit `enabled: false`: the captain
	// parks the entire relaunch loop without unpinning any hull. SetDefaults resolves
	// nil to true.
	Enabled *bool `mapstructure:"enabled"`

	// CooldownSeconds is the per-hull wait after an honest tour exit before relaunch
	// (0 => the coordinator default, 180s). It lets the local ground breathe through
	// the rich->tapped->rich cycle so the next tour re-plans against a recovered market.
	CooldownSeconds int `mapstructure:"cooldown_seconds"`

	// MaxConcurrentTours caps simultaneously-running trade tours (0 => unlimited,
	// bounded naturally by fleet size — every idle trade hull is relaunched). Set a
	// positive cap to bound concurrent capital deployment / API load.
	MaxConcurrentTours int `mapstructure:"max_concurrent_tours"`

	// TickSeconds is the reconcile cadence (0 => the coordinator default, 30s).
	TickSeconds int `mapstructure:"tick_seconds"`

	// Per-tour launch caps, passed verbatim to each StartTourRun. 0 => the tour's own
	// documented default for that knob (MaxHops->6, MaxSpend->25% of live treasury,
	// ReplanLimit->2, WorkingCapitalReserve->the non-tunable floor). Iterations is NOT
	// configurable: every relaunched tour is continuous (-1) by construction — a finite
	// tour would exit and park the hull, the sink this coordinator retires.
	MaxHops               int   `mapstructure:"max_hops"`
	MaxSpend              int64 `mapstructure:"max_spend"`
	MinMargin             int   `mapstructure:"min_margin"`
	ReplanLimit           int   `mapstructure:"replan_limit"`
	WorkingCapitalReserve int64 `mapstructure:"working_capital_reserve"`
}

// EnabledOrDefault reports whether the coordinator is enabled, treating an unset (nil)
// value as true — the default-ON behavior the bead intends (sp-1278).
func (c TradeFleetConfig) EnabledOrDefault() bool {
	return c.Enabled == nil || *c.Enabled
}

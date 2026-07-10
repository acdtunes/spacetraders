package config

// ContractConfig holds contract-coordinator configuration. Today it carries the
// idle-gap arbitrage harvest knobs (sp-1z2h / sp-uohe): the daemon injects these
// into the contract fleet coordinator container's launch config at creation
// (DaemonServer.ContractFleetCoordinator), so a captain tunes the harvest —
// including the money-guard blacklist — by editing config.yaml and restarting
// the daemon (recovery-safe, RULINGS #2), with NO code redeploy.
type ContractConfig struct {
	IdleArb IdleArbSettings `mapstructure:"idle_arb"`
}

// IdleArbSettings are the yaml-tunable idle-arb knobs. They mirror
// contract.IdleArbConfig. A zero value means "unset" and defers to the contract
// package's documented defaults (contract.IdleArbConfig.WithDefaults) — the
// daemon injects only the keys the captain actually set. Blacklist is the one
// exception: a nil (absent) list defers to the default [ELECTRONICS], while an
// explicit empty list (`blacklist: []`) disables the blacklist entirely.
type IdleArbSettings struct {
	Disabled        bool     `mapstructure:"disabled"`
	ReserveHulls    int      `mapstructure:"reserve_hulls"`
	HubRadius       int      `mapstructure:"hub_radius"`
	LeashRadius     int      `mapstructure:"leash_radius"`
	MaxLegSeconds   int      `mapstructure:"max_leg_seconds"`
	MaxSpend        int      `mapstructure:"max_spend"`
	MinMargin       int      `mapstructure:"min_margin"`
	MarginVerifyPct int      `mapstructure:"margin_verify_pct"`
	IntervalSeconds int      `mapstructure:"interval_seconds"`
	Blacklist       []string `mapstructure:"blacklist"`
}

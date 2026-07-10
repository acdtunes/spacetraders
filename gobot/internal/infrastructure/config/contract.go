package config

// ContractConfig holds contract-coordinator configuration. Today it carries the
// idle-gap arbitrage harvest knobs (sp-1z2h / sp-uohe): the daemon injects these
// into the contract fleet coordinator container's launch config at creation
// (DaemonServer.ContractFleetCoordinator), so a captain tunes the harvest —
// including the money-guard blacklist — by editing config.yaml and restarting
// the daemon (recovery-safe, RULINGS #2), with NO code redeploy.
type ContractConfig struct {
	IdleArb        IdleArbSettings        `mapstructure:"idle_arb"`
	PrePositioning PrePositioningSettings `mapstructure:"pre_positioning"`
}

// PrePositioningSettings are the yaml-tunable knobs for haul-to-storage
// contract-goods pre-positioning (sp-dchv Lane C). The daemon resolves these
// from config.yaml when it builds the tour coordinator (live-config pattern,
// sp-ts82: edit + restart, no code redeploy), and the coordinator assembles
// deposit candidates from the Lane A demand miner + the running warehouse op,
// capped by the capital ceiling. A zero value means "unset" and defers to the
// documented default. Pre-positioning is OFF unless Enabled is true — it must be
// opted into (a warehouse hull has to exist first, Lane B).
//
// Allowlist/Blocklist follow the idle-arb blacklist semantics: a nil (absent)
// list is "no filter of this kind"; an explicit list restricts (allowlist) or
// excludes (blocklist) exactly those goods. Blocklist wins over allowlist.
type PrePositioningSettings struct {
	// Enabled turns pre-positioning deposit legs ON (default OFF). Even ON, no
	// deposit candidates are offered unless a warehouse op is running in the
	// tour's home system and the demand miner returns stock-eligible goods.
	Enabled bool `mapstructure:"enabled"`
	// TopN caps how many stock-eligible goods (ranked by projected savings) are
	// offered as deposit candidates (<=0 => default).
	TopN int `mapstructure:"top_n"`
	// MinRecurrence is the demand-miner floor: a good must be demanded by at least
	// this many distinct contracts to be a candidate (never speculative, RULINGS
	// #6). <1 => the miner's own default.
	MinRecurrence int `mapstructure:"min_recurrence"`
	// CapitalCeilingPct is the pre-positioning capital ceiling as a percent of LIVE
	// treasury (<=0 => default 10). Junior to the 50k working-capital reserve and
	// the sp-w3he cross-container cap; when the live balance is unreadable the
	// ceiling is ZERO and no candidates are offered (fail closed, RULINGS #4).
	CapitalCeilingPct int `mapstructure:"capital_ceiling_pct"`
	// MinSavingsPerUnit is the static per-unit savings floor a candidate must clear
	// to be worth stocking (home_ask - foreign_ask >= this). <=0 => default 1.
	MinSavingsPerUnit int `mapstructure:"min_savings_per_unit"`
	// Allowlist, when non-nil, restricts candidates to exactly these goods (after
	// the eligibility + savings gates). Nil => every stock-eligible good qualifies.
	Allowlist []string `mapstructure:"allowlist"`
	// Blocklist names goods that are NEVER deposited (checked last, wins over the
	// allowlist). Nil => no goods blocked.
	Blocklist []string `mapstructure:"blocklist"`
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

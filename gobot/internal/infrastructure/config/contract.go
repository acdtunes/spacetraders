package config

// ContractConfig holds contract-coordinator configuration. Today it carries the
// idle-gap arbitrage harvest knobs (sp-1z2h / sp-uohe): the daemon injects these
// into the contract fleet coordinator container's launch config at creation
// (DaemonServer.ContractFleetCoordinator), so a captain tunes the harvest —
// including the money-guard blacklist — by editing config.yaml and restarting
// the daemon (recovery-safe, RULINGS #2), with NO code redeploy.
type ContractConfig struct {
	IdleArb           IdleArbSettings           `mapstructure:"idle_arb"`
	PrePositioning    PrePositioningSettings    `mapstructure:"pre_positioning"`
	SourcePreposition SourcePrepositionSettings `mapstructure:"source_preposition"`
	AutoLiquidation   AutoLiquidationSettings   `mapstructure:"auto_liquidation"`
}

// SourcePrepositionSettings are the yaml-tunable knobs for contract source
// pre-positioning (sp-1ef0): during a delivery leg, an idle hull is nudged toward the
// market that near-certainly sources the contract's next same-good delivery, so it is
// closer when that delivery starts. The signal is restricted to same-contract /
// same-good / multi-delivery-remaining; the source market is resolved from live scanned
// availability (NOT the persisted purchase-history tracking removed in 71aceda).
//
// Distinct from PrePositioningSettings above, which is the sp-dchv haul-to-storage
// warehouse-deposit feature — different mechanism, different knobs.
type SourcePrepositionSettings struct {
	// Disabled turns source pre-positioning OFF (default: ON — live by default, RULINGS
	// #5). Pre-positioning only ever biases an ALREADY-rebalancing idle hull toward a
	// near-certain next source, so it is on by default; an absent key reads as enabled so
	// the default-ON intent survives a recovery from a config predating the key.
	Disabled bool `mapstructure:"disabled"`
	// ConfidenceThreshold is the same-good-remaining signal confidence a prediction must
	// clear to move an idle hull (bounds wasted-move risk). <=0 => the package default
	// (0.8), which admits the near-certain single-good case and rejects the ambiguous
	// multi-good case.
	ConfidenceThreshold float64 `mapstructure:"confidence_threshold"`
}

// AutoLiquidationSettings are the yaml-tunable knobs for the contract coordinator's
// parked-hull auto-liquidation (sp-39oi): a hull the spawn filter parks for holding cargo
// unrelated to the active contract self-clears via a one-shot cargo_liquidation worker,
// so the pool never jams to zero fulfillments on a crop of strands. Like the idle-arb
// knobs these are resolved LIVE from config.yaml on every coordinator build (sp-ts82).
type AutoLiquidationSettings struct {
	// Disabled turns auto-liquidation OFF (default: ON). Liquidation-by-sale only converts
	// a stranded hold to treasury, so it is on by default; an absent key reads as enabled,
	// so the default-ON intent survives a recovery from a config predating the key.
	Disabled bool `mapstructure:"disabled"`
	// MinJettisonValue is the value floor (bid * units) below which a leftover lot may be
	// jettisoned as a LAST resort. 0 (the default) disables jettison entirely — nothing is
	// destroyed without an explicit floor; a lot with a bid is always sold, never dumped.
	MinJettisonValue int `mapstructure:"min_jettison_value"`
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
	// treasury AND the enablement knob for opportunistic tour deposits (sp-13tl):
	// 0/absent => PARKED (dormant, fail closed, no deposits — money movement is a
	// captain/analyst decision, RULINGS #5), NOT an auto-10% default. A positive value
	// authorizes deposits up to that % of live treasury, held JUNIOR to the
	// working-capital reserve and the sp-w3he cross-container cap; when the live balance
	// is unreadable the ceiling is ZERO and no candidates are offered (fail closed,
	// RULINGS #4). (The dedicated stocker hull keeps its own default; see
	// defaultDepositCeilingPct.)
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
	// RecoveryHoldSeconds (sp-lbbm) is the lane mutex's post-termination hold: how
	// long a (good, sink) lane stays closed after its leg terminates before another
	// hull may work it. 0 → the contract package default (1200s = 20min).
	RecoveryHoldSeconds int `mapstructure:"recovery_hold_seconds"`
}

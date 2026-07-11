package config

// SitingConfig holds the factory SITING coordinator's knobs (sp-vdld). It nests
// under [manufacturing.siting] and is injected into the siting_coordinator
// container's launch config on every build — creation AND restart recovery, via
// resolveSitingConfig — so a captain retunes the siting weights/caps by editing
// config.yaml and restarting, with NO code redeploy (the sp-ts82 live-config
// pattern, RULINGS #2/#5).
//
// Every knob follows the manufacturing idiom: a zero value means "unset" and
// defers to the coordinator's documented protective default (resolved once in
// the handler's resolveSitingConfig, the frontier-coordinator pattern). The
// Analyst owns these weights — they are all config, never constants (RULINGS #5).
type SitingConfig struct {
	// SitingDisabled is the RULINGS #5 escape hatch. The coordinator is LIVE BY
	// DEFAULT (Admiral: no dark-shipping) — absent/false keeps it ACTIVE; set true
	// only to stand the whole siting brain down in an emergency. Absent-config
	// therefore boots ACTIVE, pinned by test.
	SitingDisabled bool `mapstructure:"siting_disabled"`

	// DryRun evaluates every siting decision and logs what it WOULD launch/retire,
	// but takes no ACT action (the captain watches a cycle before arming). Not
	// dark-shipping — the default (false) is live; this is the opt-in watch mode.
	DryRun bool `mapstructure:"dry_run"`

	// TickIntervalSecs is the slow siting cadence. Siting decisions are strategic,
	// not per-second. 0/absent → the 900s (15min) default.
	TickIntervalSecs int `mapstructure:"tick_interval_secs"`

	// TopK pins the target portfolio size (number of standing chains) directly. 0/absent
	// → derived from the worker pool: floor(workers / WorkersPerChain) per C3 rotation math.
	TopK int `mapstructure:"top_k"`

	// WorkersPerChain is the C3 rotation divisor: derived K = floor(workers / this). 0/absent
	// → the 3.5 default (workers × 3-4 rotation slots per chain). Ignored when TopK is set.
	WorkersPerChain float64 `mapstructure:"workers_per_chain"`

	// FreshnessMaxSecs is the SCAN staleness gate: a (good,system) whose market data is older
	// than this is excluded from candidacy entirely (siting on stale data is guessing). 0/absent
	// → the 7200s (2h) default.
	FreshnessMaxSecs int `mapstructure:"freshness_max_secs"`

	// EmitStalenessSecs is the EMIT band's lower edge: a candidate that would rank into the
	// portfolio but whose data age exceeds this (yet is still within FreshnessMaxSecs) is
	// NOT launched on stale data — instead the coordinator emits scout-demand so coverage
	// refreshes it. 0/absent → the 1800s (30min) default.
	EmitStalenessSecs int `mapstructure:"emit_staleness_secs"`

	// WeightTourAlignment scales the tour-alignment multiplier in the score
	// (score = branchPL × alignment − competition − staleness). 0/absent → 1.0.
	WeightTourAlignment float64 `mapstructure:"weight_tour_alignment"`

	// WeightInputCompetition scales the input-competition penalty (chains sharing one feed
	// source starve each other). 0/absent → 1.0.
	WeightInputCompetition float64 `mapstructure:"weight_input_competition"`

	// WeightStaleness scales the staleness discount (older data → lower confidence in the
	// projection). 0/absent → 1.0.
	WeightStaleness float64 `mapstructure:"weight_staleness"`

	// WeightWorkerReachability scales the worker-unreachability penalty (sp-3vg8): a candidate
	// system with no in-system idle worker AND no ferry path in is deprioritized so vdld stops
	// launching chains it cannot man (the C81/GS93 workerless-launch fix). LIVE BY DEFAULT —
	// 0/absent → 1.0 (a fully-unstaffable site loses ~its whole projected value); set a smaller
	// value to soften the penalty, or a larger one to make it near-hard-gate. The Analyst owns it.
	WeightWorkerReachability float64 `mapstructure:"weight_worker_reachability"`

	// MaxChainsPerSystem caps standing chains concentrated in one system (concentration cap).
	// 0/absent → the 3 default.
	MaxChainsPerSystem int `mapstructure:"max_chains_per_system"`

	// MaxChainsPerInputMarket caps standing chains drawing the same feed market (input-poison
	// concentration cap). 0/absent → the 2 default.
	MaxChainsPerInputMarket int `mapstructure:"max_chains_per_input_market"`

	// RetireHysteresisTicks is how many consecutive ticks a running chain must fall out of the
	// top-K before it is retired (anti-thrash: a chain flickering at the K boundary is not
	// stopped/started every tick). 0/absent → the 2 default.
	RetireHysteresisTicks int `mapstructure:"retire_hysteresis_ticks"`

	// EffectSelfcheckTicks is the effect self-check horizon: when candidates exist but zero ACT
	// actions fire for this many consecutive ticks, the coordinator emits ONE WARN naming why
	// (the reposition sp-686e idiom). 0/absent → the 4 default.
	EffectSelfcheckTicks int `mapstructure:"effect_selfcheck_ticks"`

	// ScoutDemandCooldownSecs debounces scout-demand emission per candidate system (one proposal
	// per onset, not per tick). 0/absent → the 3600s (1h) default.
	ScoutDemandCooldownSecs int `mapstructure:"scout_demand_cooldown_secs"`
}

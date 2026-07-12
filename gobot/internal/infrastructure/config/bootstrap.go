package config

// BootstrapConfig holds the captain bootstrap coordinator's knobs (sp-3nbe). It nests under the
// top-level [bootstrap] section and is injected into the bootstrap container's launch config on
// every build — creation AND restart recovery, via resolveBootstrapConfig — so a captain retunes
// the cold-start behaviour by editing config.yaml and restarting, with NO code redeploy (the
// sp-ts82 live-config pattern, RULINGS #2/#5).
//
// Every knob follows the codebase idiom: a zero value means "unset" and defers to the coordinator's
// documented default (resolved once in the handler's resolveBootstrapConfig). The Analyst/Admiral
// own these numbers — they are all config, never call-site constants (RULINGS #5).
//
// Slice 1 knobs only. The INCOME/GATE knobs (hauler_target, income_bar, gate_worker_target,
// min_contract_earners) are deliberately deferred to the slice that first reads them, to keep this
// section free of dead config.
type BootstrapConfig struct {
	// BootstrapDisabled stands the WHOLE coordinator down. Absent/false = ACTIVE, so an
	// absent-config boots LIVE (pinned by test — Admiral: no dark-shipping). Set true only in an
	// emergency; the container stays resident so a flip + restart re-arms it.
	BootstrapDisabled bool `mapstructure:"bootstrap_disabled"`
	// DryRun evaluates every decision and logs what it WOULD do (probe buys, scout assignments) but
	// acts on nothing. NOT dark-shipping — it WARNs loudly every tick (no-silent-dry-run rule).
	DryRun bool `mapstructure:"dry_run"`

	// ProbeTarget is the DATA-phase probe count the coordinator ramps to (staged, capital-gated).
	// 0/absent → 3.
	ProbeTarget int `mapstructure:"probe_target"`
	// CoverageBar is the DATA→exit threshold: the fraction (0..1) of home-system marketplaces that
	// must have fresh market data before the arc leaves DATA. 0/absent → 0.9.
	CoverageBar float64 `mapstructure:"coverage_bar"`
	// ReserveMargin is the ≤-fraction-of-treasury-per-decision guardrail (the money-guard: a buy may
	// spend at most this fraction of live treasury, leaving the rest as the working buffer). It also
	// paces the acquisition ramp. 0/absent → 0.5.
	ReserveMargin float64 `mapstructure:"reserve_margin"`
	// TickSeconds is the reconcile cadence. 0/absent → 300 (5min); cold-start is a slow ramp.
	TickSeconds int `mapstructure:"tick_seconds"`
	// ProbeShipType is the shipyard ship-type symbol bought for a probe (RULINGS #5: even the asset
	// is a knob). 0/absent → SHIP_PROBE.
	ProbeShipType string `mapstructure:"probe_ship_type"`
}

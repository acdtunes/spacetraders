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
// Slice 1 + Slice 2 + Slice 3 knobs. The INCOME knobs (hauler_target, income_bar, min_contract_earners,
// hauler_ship_type) landed with Slice 2; the GATE knob (gate_worker_target) landed with Slice 3, the
// last deferred field, now that the GATE phase reads it.
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

	// --- INCOME-phase knobs (Slice 2, sp-ysgb.1). ---

	// HaulerTarget is the INCOME hull cap: the coordinator buys one light hauler per viable contract
	// hub, up to this many (staged, capital-gated). 0/absent → 4 (spec range 4–5).
	HaulerTarget int `mapstructure:"hauler_target"`
	// IncomeBar is the INCOME→GATE exit: the realized NET credits/hour the contract fleet must clear
	// before the arc drives gate construction. Deliberately conservative — the Phase-1 objective is
	// building the gate, so a too-HIGH bar (arc never reaches GATE) is the worse failure. This is the
	// primary field-calibration knob. 0/absent → 10000.
	IncomeBar float64 `mapstructure:"income_bar"`
	// MinContractEarners is how many haulers stay on contracts through GATE to keep funding material
	// acquisition (consumed by the GATE phase in Slice 3; plumbed here with the INCOME ramp). 0/absent → 1.
	MinContractEarners int `mapstructure:"min_contract_earners"`
	// HaulerShipType is the shipyard ship-type bought for a contract hauler (RULINGS #5: the asset is a
	// knob). 0/absent → SHIP_LIGHT_HAULER.
	HaulerShipType string `mapstructure:"hauler_ship_type"`

	// --- GATE-phase knob (Slice 3, sp-ysgb.2). ---

	// GateWorkerTarget is the GATE-phase worker cap: the coordinator sizes gate-construction workers to
	// ~one per active gate-material chain + a delivery hauler, up to this many — repurposing idle contract
	// haulers first (the seed workforce) and buying the delta (staged, capital-gated) only if the pool is
	// short. It caps the top-up so a wide pipeline never runs the treasury dry (min_contract_earners still
	// stays on contracts to fund material acquisition). 0/absent → 6. Gate workers reuse hauler_ship_type.
	GateWorkerTarget int `mapstructure:"gate_worker_target"`
}

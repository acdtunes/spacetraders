package config

// BootstrapConfig holds the captain bootstrap coordinator's operator controls (sp-3nbe). It
// nests under the top-level [bootstrap] section and is injected into the bootstrap container's
// launch config on every build — creation AND restart recovery, via resolveBootstrapConfig — so a
// captain changes them by editing config.yaml and restarting, with NO code redeploy (the sp-ts82
// live-config pattern, RULINGS #2).
//
// The cold-start SEQUENCE is fixed in the coordinator; the two FLEET-SIZE bounds are not (RULINGS
// #5). A zero value means "unset" and defers to the coordinator's documented default.
type BootstrapConfig struct {
	// BootstrapDisabled stands the WHOLE coordinator down. Absent/false = ACTIVE, so an
	// absent-config boots LIVE (pinned by test — Admiral: no dark-shipping). Set true only in an
	// emergency; the container stays resident so a flip + restart re-arms it.
	BootstrapDisabled bool `mapstructure:"bootstrap_disabled"`
	// TickSeconds is the reconcile cadence. 0/absent → 45. Also live-tunable (tick_secs), which
	// lands on the next tick with no restart.
	TickSeconds int `mapstructure:"tick_seconds"`
	// ContractStartTreasuryThreshold: FLAT treasury at which contract ops start (0 → 500000; live-tunable).
	ContractStartTreasuryThreshold int `mapstructure:"contract_start_treasury_threshold"`
	// HaulerTarget caps the contract-hull ramp (0 → 4; live-tunable, itself capped by the era's slots).
	HaulerTarget int `mapstructure:"hauler_target"`
	// GateWorkerTarget is the GATE construction workforce, one hull per tick (0 → 3; live-tunable).
	GateWorkerTarget int `mapstructure:"gate_worker_target"`
}

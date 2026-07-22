package config

// ManufacturingConfig holds the [manufacturing] knobs SHARED with the construction-supply drain.
// The daemon injects these into the construction coordinator container's launch config on every
// build — creation AND restart recovery, via resolveConstructionUnifiedGateFill — so a captain
// retunes them by editing config.yaml and restarting, with NO code redeploy (sp-ts82 live-config
// pattern, RULINGS #2/#5).
//
// The goods_factory_coordinator's own working-capital/guard knobs (and the [manufacturing.siting]
// sub-config) were retired with the factory ops (sp-hoj8u); only the construction-shared keys below
// remain.
//
// A zero value means "unset" and defers to the coordinator's documented default for that knob, so
// the daemon injects only the key the captain actually set — it never hardcodes an operational value.
type ManufacturingConfig struct {
	// UnifiedGateFill is the sp-vh1s master toggle (CONTRACT #1).
	// OFF (the default): gate materials are filled by today's construction drain honoring the
	// planner's frozen buy-vs-fabricate decision per material — byte-identical to today.
	// ON: the drain drives the resolver's full scarcity-gated tree for every gate material and marks
	// the run a gate node (WithUnifiedGateFill + a construction-site DeliveryTarget derived from the
	// task's own site). Threaded into the construction coordinator and read directly by the boot-time
	// short-circuits. Fed from unified_gate_fill; a captain flips it live by editing config.yaml and
	// restarting (RULINGS #5). false is the zero value, so an absent key keeps today's behavior.
	UnifiedGateFill bool `mapstructure:"unified_gate_fill"`
}

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

	// FabricationEfficiency is the sp-to2v master toggle for the executor feeding-efficiency policy —
	// balanced-to-limiting input feeding (the ~4x lever), saturation-capped delivery tranches,
	// taproot-first ordering, and buy-or-skip for feed-unresponsive goods. It is executor DELIVERY
	// policy, threaded into the construction coordinator; absent/false leaves the greedy byte-identical
	// feeding (the whole layer dark). HIGH confidence on the mechanics, MEDIUM on the coefficients.
	FabricationEfficiency bool `mapstructure:"fabrication_efficiency"`

	// FeedSaturationMaxUnits / FeedSaturationMinUnits are the sp-to2v per-input delivery saturation
	// window: a tranche is capped at max (Δactivity rolls off past ~200u) and never sized below min
	// (<25u moves activity nothing). 0/absent → 200 / 25 at the point of use (RULINGS #5).
	FeedSaturationMaxUnits int `mapstructure:"feed_saturation_max_units"`
	FeedSaturationMinUnits int `mapstructure:"feed_saturation_min_units"`

	// FeedNonResponsiveGoods REPLACES the default set of OUTPUT goods whose activity does not respond to
	// feeding and are therefore BUY-OR-SKIPed (sp-to2v #4). Nil/empty keeps the verified default
	// {EQUIPMENT,LAB_INSTRUMENTS,FOOD,MEDICINE}; a list lets the analyst retune it live.
	FeedNonResponsiveGoods []string `mapstructure:"feed_non_responsive_goods"`

	// ConstructionSupplyTaskTimeoutSeconds bounds a SINGLE construction supplyTask (claim→source→route-
	// to-gate-with-refuel-hops→dock→supply→record) before the drain abandons it and retries next tick
	// (sp-ubwi). 0/absent → the drain's raised 30m default; the old hardcoded 10m abandoned legit
	// multi-hop light-hauler hauls at the finish line. Threaded into the construction_coordinator command
	// (RunConstructionCoordinatorCommand.SupplyTaskTimeoutSeconds) so a captain retunes it live by
	// editing config.yaml and restarting (sp-ts82 / RULINGS #5).
	ConstructionSupplyTaskTimeoutSeconds int `mapstructure:"construction_supply_task_timeout_seconds"`
}

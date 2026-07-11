package config

// ManufacturingConfig holds the manufacturing coordinators' knobs (sp-kk61). The
// daemon injects these into a coordinator container's launch config on every
// build — creation AND restart recovery, via resolveManufacturingConfig — so a
// captain retunes the factory input-buy spend floor by editing config.yaml and
// restarting the daemon, with NO code redeploy (sp-ts82 live-config pattern,
// RULINGS #2/#5).
//
// A zero value means "unset" and defers to the coordinator's documented default
// for that knob, so the daemon injects only the key the captain actually set —
// it never hardcodes an operational value.
type ManufacturingConfig struct {
	// WorkingCapitalReserve is the per-run spend floor threaded into both
	// goods_factory_coordinator (RunFactoryCoordinatorCommand) and
	// manufacturing_coordinator (RunParallelManufacturingCoordinatorCommand)
	// commands. 0/absent leaves goods_factory's own immutable 50000 lower bound
	// untouched (sp-agzj: effectiveReserveFloor = max(50000, WorkingCapitalReserve));
	// a configured value raises it — e.g. matching the fleet's [trade_fleet]
	// working_capital_reserve so a factory input buy can no longer ride the 50k
	// floor into a fleet-wide treasury trough (the 682k-buy / 53k-trough incident
	// this bead follows up on).
	WorkingCapitalReserve int64 `mapstructure:"working_capital_reserve"`
}

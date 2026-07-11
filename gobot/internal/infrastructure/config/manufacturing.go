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

	// WorkingCapitalReserveTreasuryPct is the sp-yqx4 counter-cyclical floor as a percent of
	// LIVE treasury, threaded into goods_factory_coordinator (RunFactoryCoordinatorCommand):
	// each input buy is floored at max(50k, min(working_capital_reserve, pct% × treasury)) so
	// a reserve above the treasury can no longer park every factory buy — the same deadlock
	// that idled the tour fleet. 0/absent → the 40% default (common.DefaultReserveTreasuryPct,
	// pending the trade-analyst's ruling). Config, not a constant (RULINGS #5).
	WorkingCapitalReserveTreasuryPct int `mapstructure:"working_capital_reserve_treasury_pct"`

	// InputPriceCeilingMultiplier is the ladder-chase ceiling on factory INPUT buys (sp-iv65),
	// threaded into goods_factory_coordinator (RunFactoryCoordinatorCommand): an input buy
	// aborts when its live ask exceeds this multiple of the good's trailing-median ask,
	// stopping a chain from chasing its own supply ladder up (the ADV_CIRC 4x-market leak,
	// −2.2M/hr). 0/absent → the 1.5 default the guard resolves at the point of use — a
	// protective default that turns a GUARD on, not money movement, so a default is correct
	// (RULINGS #5). Config, not a constant, so a captain retunes it live.
	InputPriceCeilingMultiplier float64 `mapstructure:"input_price_ceiling_multiplier"`

	// InputPriceCeilingDisabled is the emergency off-switch for the ladder-chase ceiling
	// (RULINGS #5): true skips the guard entirely, for a captain who needs a factory to buy
	// through a genuine price spike. Absent/false keeps the guard on at its default multiplier.
	InputPriceCeilingDisabled bool `mapstructure:"input_price_ceiling_disabled"`

	// InputSupplyGateParkLevel is the cached supply level at or below which a factory INPUT buy
	// parks (sp-a5j7), threaded into goods_factory_coordinator (RunFactoryCoordinatorCommand):
	// the LEADING guard to the LAGGING price ceiling above. Supply is the CAUSAL signal — a
	// depleted market is what ladders the ask up — so a buy into a SCARCE market is refused
	// before it pushes the ladder another rung (the D39/ADV_CIRC supply event). "" → the SCARCE
	// default the guard resolves at the point of use; raise to "LIMITED" to park one state
	// earlier. A buy into a parked level still proceeds when the feed leg clears at the live ask.
	InputSupplyGateParkLevel string `mapstructure:"input_supply_gate_park_level"`

	// InputSupplyGateDisabled is the emergency off-switch for the supply-state gate (RULINGS #5):
	// true skips the guard entirely, for a captain who must feed a factory through a genuine
	// supply crunch. Absent/false keeps the guard on at its default (park SCARCE, warn LIMITED).
	InputSupplyGateDisabled bool `mapstructure:"input_supply_gate_disabled"`
}

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

	// InputRescueMultiplier caps the supply-first sourcing rescue clause (sp-a5j7 Phase 2, wedx
	// restoration): when NO eligible (MODERATE+) source exists and the chain is blocked, a
	// SCARCE/LIMITED source is bought ONLY if its ask is within this multiple of the good's
	// trailing median. 0/absent → the 1.2 default the selector resolves at the point of use
	// (tighter than the 1.5x ceiling — a rescue buy is already into a depleted market). Threaded
	// into goods_factory_coordinator (RunFactoryCoordinatorCommand).
	InputRescueMultiplier float64 `mapstructure:"input_rescue_multiplier"`

	// InputEraEndPriceFirst flips input sourcing to PRICE-FIRST for the era-end window (< T-6h),
	// the wedx exception: mean-reversion has no time to work, so a cheap ask that clears margin
	// NOW beats waiting for supply to regenerate. Absent/false keeps supply-first sourcing on;
	// the daemon toggles this at the same boundary as the stocker rundown.
	InputEraEndPriceFirst bool `mapstructure:"input_era_end_price_first"`

	// InputSourcingDisabled is the RULINGS #5 escape hatch that reverts input sourcing to pure
	// PRICE-FIRST (the pre-restoration behavior) — for a captain who must override the
	// supply-first policy in an emergency. Absent/false keeps supply-first sourcing on.
	InputSourcingDisabled bool `mapstructure:"input_sourcing_disabled"`

	// ChainPnLKillThresholdPerHour is the realized-P&L/hr floor below which a chain auto-pauses
	// (sp-rh2z, analyst redesign C2), threaded into goods_factory_coordinator: the coordinator
	// computes each chain's realized P&L over the rolling window (factory local sells + tour
	// realized net − input cost − lift) and pauses the chain pre-spend when it falls below this
	// per-hour figure, making the portfolio self-pruning. 0/absent → the 30000 default the
	// coordinator resolves at the point of use (the kill-switch runs ON in production without
	// the captain naming it — it can only STOP spend, so a protective default is correct,
	// RULINGS #5). Config, not a constant, so a captain retunes it live.
	ChainPnLKillThresholdPerHour int `mapstructure:"chain_pnl_kill_threshold_per_hour"`

	// ChainPnLWindowHours is the trailing window the realized P&L is measured over (sp-rh2z).
	// 0/absent → the 6h default. Config so a captain widens/narrows the pruning horizon live.
	ChainPnLWindowHours int `mapstructure:"chain_pnl_window_hours"`

	// ChainPnLKillDisabled is the emergency off-switch for the chain P&L kill-switch (RULINGS #5):
	// true skips it entirely, for a captain who must keep a chain running through an accounting
	// gap. Absent/false keeps the kill-switch on at its defaults.
	ChainPnLKillDisabled bool `mapstructure:"chain_pnl_kill_disabled"`

	// PlannerStockDisabled is the emergency escape hatch for planner-visible stock (C1,
	// sp-64je): harvested root output deposits into a co-located warehouse at cost basis
	// instead of selling at market, so the tour solver withdraws it at basis. The feature
	// is LIVE BY DEFAULT (Admiral: no dark-shipping) — absent/false keeps it ACTIVE; set
	// true only to force the pre-C1 sell-at-market behavior in an emergency. It always
	// fails safe to market-sell when no warehouse/space/treasury is available.
	PlannerStockDisabled bool `mapstructure:"planner_stock_disabled"`

	// InputRecoveryReattemptMinutes is the input-poison anti-cycle's recovery half-life (sp-r5a6),
	// threaded into goods_factory_coordinator: when a chain's input layer goes ineligible (no
	// MODERATE+ in-system supply source for a required input), the coordinator pauses it and holds
	// it OFF the market for this many minutes before a one-iteration re-attempt through the launch
	// guards. 0/absent → the 194min default the coordinator resolves at the point of use (the
	// analyst's measured input recovery half-life; the anti-cycle runs ON in production without the
	// captain naming it — it can only STOP spend, RULINGS #5). Config, not a constant, so the
	// analyst retunes the number live.
	InputRecoveryReattemptMinutes int `mapstructure:"input_recovery_reattempt_minutes"`

	// AntiCycleDisabled is the emergency off-switch for the input-poison anti-cycle (RULINGS #5):
	// true skips detection/pause entirely, reverting to the a5j7 selector's park-and-retry.
	// Absent/false keeps the anti-cycle on at its default half-life.
	AntiCycleDisabled bool `mapstructure:"anti_cycle_disabled"`

	// Siting nests the factory SITING coordinator's knobs (sp-vdld) under
	// [manufacturing.siting] — the standing brain that scans/scores/sizes/launches
	// factory chains. Injected into the siting_coordinator container's launch config
	// via resolveSitingConfig. LIVE BY DEFAULT; siting_disabled is the escape hatch.
	Siting SitingConfig `mapstructure:"siting"`
}

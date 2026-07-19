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
	// WorkingCapitalReserve is the per-run spend floor threaded into the
	// goods_factory_coordinator (RunFactoryCoordinatorCommand) command.
	// 0/absent leaves goods_factory's own immutable 50000 lower bound
	// untouched (effectiveReserveFloor = max(50000, WorkingCapitalReserve));
	// a configured value raises it — e.g. matching the fleet's [trade_fleet]
	// working_capital_reserve so a factory input buy can no longer ride the 50k
	// floor into a fleet-wide treasury trough.
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
	// stopping a chain from chasing its own supply ladder up. 0/absent → the 1.5 default the guard resolves at the point of use — a
	// protective default that turns a GUARD on, not money movement, so a default is correct
	// (RULINGS #5). Config, not a constant, so a captain retunes it live.
	InputPriceCeilingMultiplier float64 `mapstructure:"input_price_ceiling_multiplier"`

	// InputPriceCeilingDisabled is the emergency off-switch for the ladder-chase ceiling
	// (RULINGS #5): true skips the guard entirely, for a captain who needs a factory to buy
	// through a genuine price spike. Absent/false keeps the guard on at its default multiplier.
	InputPriceCeilingDisabled bool `mapstructure:"input_price_ceiling_disabled"`

	// FabricateMaxDepth caps how deep the SupplyChainResolver fabricates (sp-jav2 / FACTORY_DOCTRINE
	// X1). Root is depth 0, its direct inputs depth 1; a node past this depth resolves to a
	// market-BUY instead of a recursive sub-chain. 0/absent → the depth-1 default at the point of
	// use — the resolver fabricates the output and buys its inputs, which the analyst brief
	// established captures the entire realizable margin (raw inputs ~0.29% of spend, market-buy
	// ruled correct sp-naw6, and the furnace class lived in the recursion). A protective default
	// that turns a GUARD on, not money movement, so a live-by-default is correct (RULINGS #5).
	FabricateMaxDepth int `mapstructure:"fabricate_max_depth"`

	// FabricateDepthCapDisabled is the emergency off-switch for the fabricate depth cap (RULINGS
	// #5): true restores the original unbounded recursion. Absent/false keeps the cap on at its
	// default depth.
	FabricateDepthCapDisabled bool `mapstructure:"fabricate_depth_cap_disabled"`

	// ProductionStrategy is the SupplyChainResolver acquisition strategy the PRODUCTION coordinators
	// (goods_factory + construction) resolve their trees on (sp-yfzi):
	// "smart" (fabricate a SCARCE/LIMITED intermediate that has a factory, buy an abundant one — the
	// scarcity-gated recursion this bead re-enables fleet-wide), "prefer-buy" (the sp-jav2 X1
	// buy-all-inputs posture, the dial-back), or "prefer-fabricate". Empty/absent → the coordinators'
	// "smart" default (resolveProductionStrategy at the launch build), so recursive production runs
	// ON without the captain naming it; a captain pins "prefer-buy" here to reverse it live (RULINGS
	// #5). Threaded into goods_factory_coordinator (and construction) via the launch config.
	ProductionStrategy string `mapstructure:"production_strategy"`

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

	// RestWindowMinutes is the export-ask-subsidy rest recovery window (sp-xdk6, redesign C4),
	// threaded into goods_factory_coordinator: when a chain's OWN output market's ask ladders above
	// the eligible cross-source median (the mechanized 8w40 signal), the coordinator rests it and
	// holds it OFF the next lift for this many minutes before a one-iteration re-attempt. 0/absent →
	// the 90min default the coordinator resolves at the point of use (the K2 rotation "one recovery
	// window"; the signal runs ON in production without the captain naming it — it can only STOP a
	// lift, RULINGS #5). Config, not a constant, so the analyst retunes the number live.
	RestWindowMinutes int `mapstructure:"rest_window_minutes"`

	// RestSignalDisabled is the emergency off-switch for the export-ask-subsidy rest signal (RULINGS
	// #5): true skips detection/rest entirely, for a captain who must keep a chain lifting through a
	// genuine own-market premium. Absent/false keeps the signal on at its default window.
	RestSignalDisabled bool `mapstructure:"rest_signal_disabled"`

	// UnifiedGateFill is the sp-vh1s master toggle (CONTRACT #1).
	// OFF (the default): gate materials are filled by today's thin construction drain + bootstrap
	// InputsOnly feeders, and the siting portfolio may harvest gate goods — byte-identical to today.
	// ON: gate construction runs as a generic goods-factory run whose terminal DELIVERS the root
	// output to the construction site instead of selling it at a resale sink; feeding is inherent in
	// the recursive tree; gate nodes buy MARGIN-BLIND, bounded only by solvency (9aoc) + physical
	// production THROUGHPUT-PACING (the dropped price ceiling's replacement); and the old
	// bootstrap-feeder + siting-harvester paths for gate goods are short-circuited. Threaded into the
	// factory/construction coordinators (WithUnifiedGateFill → IsUnifiedGateNode) and read directly by
	// the boot-time siting/feeder short-circuits. Fed from unified_gate_fill; a captain flips it live
	// by editing config.yaml and restarting (RULINGS #5). false is the zero value, so an absent key
	// keeps every path on today's behavior — no SetDefaults entry needed.
	UnifiedGateFill bool `mapstructure:"unified_gate_fill"`

	// GateOutputBuyRateMultiple is k in the gate output-buy THROUGHPUT-PACING (sp-vh1s): the trailing-
	// hour ceiling on buying a gate source factory's output, as a multiple of that factory's export
	// trade volume (k × tv per hour). This is the ONLY safety limit on the margin-blind gate output buy
	// (it replaces the dropped price ceiling), so it lands with the toggle. 0/absent → the 2.0
	// analyst-validated default at the point of use (MEDIUM confidence — retuned live against observed
	// F48/D42 production vs draw, RULINGS #5). Threaded into goods_factory_coordinator; only ever
	// consulted for a gate node, so a profit factory is unaffected.
	GateOutputBuyRateMultiple float64 `mapstructure:"gate_output_buy_rate_multiple"`

	// GateOutputPerLotMultiple caps a single gate output-buy lot at this multiple of tv (0/absent → 1.0,
	// per-lot ≤ tv), so a burst cannot front-load the hourly budget in one oversized buy (sp-vh1s).
	GateOutputPerLotMultiple float64 `mapstructure:"gate_output_per_lot_multiple"`

	// GateOutputPacingDisabled is the emergency off-switch for the gate output-buy throughput pacing
	// (RULINGS #5): true reverts a gate output-buy to the plain min(cargo space, tv) cap. Absent/false
	// keeps the pacing on at its default coefficient.
	GateOutputPacingDisabled bool `mapstructure:"gate_output_pacing_disabled"`

	// FabricationEfficiency is the sp-to2v master toggle for the executor feeding-efficiency policy —
	// balanced-to-limiting input feeding (the ~4x lever), saturation-capped delivery tranches,
	// taproot-first ordering, and buy-or-skip for feed-unresponsive goods. It is executor DELIVERY
	// policy, so ON it applies to profit factories AND gate fills alike; absent/false leaves the greedy
	// byte-identical feeding (the whole layer dark). Threaded into both the factory and construction
	// coordinators. HIGH confidence on the mechanics, MEDIUM on the coefficients (tuned live).
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
	// multi-hop light-hauler hauls at the finish line (and the retry re-bought on a fresh empty hull,
	// stranding the laden one). Threaded into the construction_coordinator command
	// (RunConstructionCoordinatorCommand.SupplyTaskTimeoutSeconds) so a captain retunes it live by
	// editing config.yaml and restarting (sp-ts82 / RULINGS #5).
	ConstructionSupplyTaskTimeoutSeconds int `mapstructure:"construction_supply_task_timeout_seconds"`

	// Siting nests the factory SITING coordinator's knobs (sp-vdld) under
	// [manufacturing.siting] — the standing brain that scans/scores/sizes/launches
	// factory chains. Injected into the siting_coordinator container's launch config
	// via resolveSitingConfig. LIVE BY DEFAULT; siting_disabled is the escape hatch.
	Siting SitingConfig `mapstructure:"siting"`
}

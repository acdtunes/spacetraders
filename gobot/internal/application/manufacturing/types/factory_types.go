package types

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// RunFactoryWorkerCommand initiates a factory worker to produce a good
type RunFactoryWorkerCommand struct {
	PlayerID       int
	ShipSymbol     string
	ProductionNode *goods.SupplyChainNode
	FactoryID      string
	SystemSymbol   string
	CoordinatorID  string              // Optional: for signaling completion back to coordinator
	CompletionChan chan<- WorkerResult // Optional: channel for async completion signaling
}

// RunFactoryWorkerResponse contains the result of the worker operation
type RunFactoryWorkerResponse struct {
	FactoryID        string
	Good             string
	QuantityAcquired int
	TotalCost        int
	Completed        bool
	Error            string
}

// WorkerResult is sent via channel when worker completes
type WorkerResult struct {
	FactoryID        string
	Good             string
	QuantityAcquired int
	TotalCost        int
	Error            error
}

// RunFactoryCoordinatorCommand initiates a factory coordinator for fleet-based production
type RunFactoryCoordinatorCommand struct {
	PlayerID      int
	TargetGood    string
	SystemSymbol  string // Where to produce (defaults to current system)
	ContainerID   string // Container ID for ship assignment tracking
	MaxIterations int    // Maximum iterations to run (-1 for infinite, 0 for single run, >0 for specific count)
	// InputsOnly, when true, feeds the dependency tree but does NOT harvest the
	// fabricated output: the factory produces the target good and leaves it in its
	// export stock for a construction pipeline to source. This is the era-2 gate-fill
	// fix — a harvesting factory bought back its own 149 FAB_MATS and froze the fill
	// (sp-q02m). Default (false) preserves the original harvest-the-output behavior.
	InputsOnly bool
	// WorkingCapitalReserve unifies the factory input-buy spend floor with the fleet's
	// per-run reserve (sp-agzj): the effective floor enforced at each input buy is
	// max(50000, WorkingCapitalReserve), the 50k an immutable lower bound (RULINGS #5).
	// 0/absent leaves the standing 50k floor. Fed from the goods_factory launch config's
	// working_capital_reserve key, the same knob the tour/trade/arb coordinators run — so
	// a fleet reserving 1M no longer leaves its factories draining to 50k.
	WorkingCapitalReserve int
	// WorkingCapitalReserveTreasuryPct engages the sp-yqx4 counter-cyclical floor at each
	// input buy: the enforced floor becomes max(50k, min(WorkingCapitalReserve, pct% × live
	// treasury)) so a reserve above the treasury can no longer park every factory buy (the
	// deadlock that idled the tour fleet applies identically to factories). 0 leaves the
	// absolute floor in force; the goods_factory launch build resolves 0/absent to
	// common.DefaultReserveTreasuryPct (40) so production runs the proportional floor while a
	// command built directly (tests) keeps the absolute behavior.
	WorkingCapitalReserveTreasuryPct int
	// InputPriceCeilingMultiplier is the ladder-chase ceiling on factory INPUT buys (sp-iv65):
	// an input aborts when its live ask exceeds this multiple of the good's trailing-median ask.
	// 0/absent resolves to defaultInputPriceCeilingMultiplier (1.5) at the point of use — a
	// protective default that turns the GUARD on, not money movement (RULINGS #5). Fed from the
	// goods_factory launch config's input_price_ceiling_multiplier key.
	InputPriceCeilingMultiplier float64
	// InputPriceCeilingDisabled is the emergency off-switch for the ladder-chase ceiling
	// (RULINGS #5): true skips the guard entirely. Fed from input_price_ceiling_disabled.
	InputPriceCeilingDisabled bool
	// FabricateMaxDepth caps how deep the SupplyChainResolver fabricates (sp-jav2 X1): a node past
	// this depth (root == 0, inputs == 1) resolves to a market-BUY instead of a recursive sub-chain.
	// 0/absent resolves to defaultFabricateMaxDepth (1) at the point of use — "buy inputs, lift
	// output" (RULINGS #5). Fed from fabricate_max_depth.
	FabricateMaxDepth int
	// FabricateDepthCapDisabled is the emergency off-switch for the fabricate depth cap (RULINGS #5):
	// true restores the original unbounded recursion. Fed from fabricate_depth_cap_disabled.
	FabricateDepthCapDisabled bool
	// InputRescueMultiplier caps the supply-first sourcing rescue clause (sp-a5j7 Phase 2): a
	// SCARCE/LIMITED source is bought only when no eligible source exists AND its ask is within
	// this multiple of the trailing median. 0/absent → the 1.2 default. Fed from
	// input_rescue_multiplier.
	InputRescueMultiplier float64
	// InputEraEndPriceFirst flips sourcing to price-first for the era-end window (< T-6h), the
	// wedx exception. Fed from input_era_end_price_first.
	InputEraEndPriceFirst bool
	// InputSourcingDisabled is the RULINGS #5 escape hatch reverting sourcing to pure price-first.
	// Fed from input_sourcing_disabled.
	InputSourcingDisabled bool
	// ChainPnLKillThresholdPerHour is the realized-P&L/hr floor below which the chain
	// auto-pauses (sp-rh2z): the coordinator computes this chain's realized P&L over the
	// rolling window (factory local sells + tour realized net − input cost − lift) and pauses
	// the chain — pre-spend, next iteration — when it falls below this per-hour figure. 0/absent
	// resolves to the 30000 default at the point of use (the kill-switch runs ON in production
	// without the captain naming it). Fed from chain_pnl_kill_threshold_per_hour.
	ChainPnLKillThresholdPerHour int
	// ChainPnLWindowHours is the trailing window the realized P&L is measured over (sp-rh2z).
	// 0/absent resolves to the 6h default. Fed from chain_pnl_window_hours.
	ChainPnLWindowHours int
	// ChainPnLKillDisabled is the emergency off-switch for the chain P&L kill-switch (RULINGS #5):
	// true skips it entirely, for a captain who must keep a chain running through an accounting
	// gap. Absent/false keeps the kill-switch on at its defaults. Fed from chain_pnl_kill_disabled.
	ChainPnLKillDisabled bool
	// PlannerStockDisabled is the emergency escape hatch for planner-visible stock (C1,
	// sp-64je): harvested root output deposits into a co-located warehouse at cost basis
	// instead of selling at market. LIVE BY DEFAULT — false keeps it ACTIVE; true forces
	// the pre-C1 sell-at-market path. Fed from planner_stock_disabled.
	PlannerStockDisabled bool
	// InputRecoveryReattemptMinutes is how long the input-poison anti-cycle holds a chain paused
	// before its one-iteration re-attempt (sp-r5a6): keyed to the analyst's measured input
	// recovery half-life. 0/absent resolves to the 194min default at the point of use (the
	// anti-cycle runs ON in production without the captain naming it — it can only STOP spend, so
	// a protective default is correct, RULINGS #5). Fed from input_recovery_reattempt_minutes.
	InputRecoveryReattemptMinutes int
	// AntiCycleDisabled is the emergency off-switch for the input-poison anti-cycle (RULINGS #5):
	// true skips detection/pause entirely, reverting to the a5j7 selector's park-and-retry (which
	// still refuses depleted sources — the anti-cycle only escalates that park to a recovery-clock
	// pause). Absent/false keeps the anti-cycle on. Fed from anti_cycle_disabled.
	AntiCycleDisabled bool
	// RestWindowMinutes is how long the export-ask-subsidy rest signal (sp-xdk6, redesign C4) holds a
	// chain OFF its next lift after its OWN output market's ask laddered above the eligible
	// cross-source median — the mechanized 8w40 signal. 0/absent resolves to the 90min default at the
	// point of use (the K2 rotation "one recovery window"; the signal runs ON in production without
	// the captain naming it — it can only STOP a lift, so a protective default is correct, RULINGS
	// #5). Fed from rest_window_minutes.
	RestWindowMinutes int
	// RestSignalDisabled is the emergency off-switch for the export-ask-subsidy rest signal (RULINGS
	// #5): true skips detection/rest entirely, for a captain who must keep a chain lifting through a
	// genuine own-market premium. Absent/false keeps the signal on at its default window. Fed from
	// rest_signal_disabled.
	RestSignalDisabled bool
	// GoodGatingOverrides is the per-good buy-gating override map (sp-sdyo): a surgical knob that
	// loosens (or tightens) the supply-strategy and the ladder-chase input-price-ceiling for a
	// SINGLE bottleneck good while every other good keeps the global default. Consumed on the
	// factory-coordinator engine — the Strategy override rides the SupplyChainResolver and the
	// PriceCeilingMult override the executor's per-tranche ceiling (both ctx-stamped in
	// executeCoordination). The PriceCeilingMult is hard-capped so it can loosen but never disable
	// the ceiling (RULINGS #4). Fed from the good_gating_overrides launch-config key (a per-launch
	// key that persists in the container config and reloads on restart, RULINGS #2). Absent/nil
	// leaves every good on the global gates — byte-identical to today.
	GoodGatingOverrides manufacturing.GoodGatingOverrides
	// WorkerCap bounds how many hulls this chain runs CONCURRENTLY per production pass
	// (sp-ev0n): the coordinator never fans out more than WorkerCap node workers at once,
	// so a captain caps a factory's hull draw live without a daemon restart. This is the
	// build-time resolved value — the live per-op override (worker_cap in the container
	// config, set by the `goods factory workers` RPC) is re-read fresh each pass via the
	// injected FactoryWorkerCapProvider and wins over this snapshot. 0/absent → unbounded
	// (the pre-sp-ev0n emergent min(nodes, idle hulls) behavior); the goods_factory build
	// resolves 0 to the global [manufacturing.siting] workers_per_chain default when the
	// captain set one, so an unconfigured fleet is unchanged (RULINGS #5). A live-set value
	// survives a restart because worker_cap is persisted in the container config and is NOT
	// among the config.yaml-reinjected keys (RULINGS #2).
	WorkerCap int
}

// RunFactoryCoordinatorResponse contains the result of the coordinator operation
type RunFactoryCoordinatorResponse struct {
	FactoryID        string
	TargetGood       string
	QuantityAcquired int
	TotalCost        int
	NodesCompleted   int
	NodesTotal       int
	ShipsUsed        int
	Completed        bool
	Error            string
	// NoWorkReason is set when the iteration completed cleanly (Error == "")
	// but performed no work at all — pre-spend guard park, or every claimable
	// node parked for lack of a claimable hull (sp-2q2o). A -1 (infinite)
	// caller uses this to back off before the next iteration instead of
	// spinning; it stays empty on any iteration that produced something.
	NoWorkReason string
}

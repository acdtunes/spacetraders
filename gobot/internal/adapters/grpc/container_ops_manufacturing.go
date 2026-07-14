package grpc

import "math"

// This file holds the shared [manufacturing] launch-config plumbing for the
// goods_factory_coordinator. The parallel task-style manufacturing coordinator and its task
// worker that once also lived here were retired in sp-jav2 (X2) — their launch/persist/cleanup
// methods and the services/manufacturing subpackage they drove are gone; only the config
// resolve/inject helpers below (shared with the survivor) remain.

// manufacturingConfigKeys enumerates every launch-config key the goods_factory_coordinator's
// working-capital reserve and guard knobs occupy. resolveManufacturingConfig clears these before
// re-injecting the live value, so a stale persisted copy from a prior boot can never shadow the
// current config.yaml (sp-ts82). Keep in lockstep with injectManufacturingConfig and
// buildGoodsFactoryCoordinatorCommand's reads.
var manufacturingConfigKeys = []string{
	"working_capital_reserve",
	"working_capital_reserve_treasury_pct",
	"input_price_ceiling_multiplier",
	"input_price_ceiling_disabled",
	"fabricate_max_depth",
	"fabricate_depth_cap_disabled",
	"production_strategy",
	"input_rescue_multiplier",
	"input_era_end_price_first",
	"input_sourcing_disabled",
	"chain_pnl_kill_threshold_per_hour",
	"chain_pnl_window_hours",
	"chain_pnl_kill_disabled",
	"planner_stock_disabled",
	"input_recovery_reattempt_minutes",
	"anti_cycle_disabled",
	"rest_window_minutes",
	"rest_signal_disabled",
	// sp-vh1s: the unified gate-fill master toggle + the gate output-buy throughput-pacing knobs.
	// Cleared+reinjected from config.yaml on every build (sp-ts82) so flipping the toggle off reverts a
	// recovered coordinator rather than shadowing it with a stale persisted copy. NOTE the per-launch
	// construction_site_waypoint key is deliberately NOT listed — like good_gating_overrides it is set
	// per-launch (a gate-fill factory launch names its site), not a global config.yaml knob, so it must
	// survive a rebuild untouched.
	"unified_gate_fill",
	"gate_output_buy_rate_multiple",
	"gate_output_per_lot_multiple",
	"gate_output_pacing_disabled",
	// sp-ev0n: the GLOBAL worker-cap default derived from [manufacturing.siting]
	// workers_per_chain. Cleared+reinjected from config.yaml on every build (sp-ts82) so a
	// retune reaches a recovered coordinator. NOTE the per-op live override key `worker_cap`
	// is deliberately NOT listed here — it is the authoritative live value the `goods factory
	// workers` RPC writes, and must survive a restart untouched (RULINGS #2), exactly as
	// sp-jcke's standby_stations is excluded from any reinjection.
	"factory_worker_cap_default",
}

// resolveManufacturingConfig makes config.yaml the single LIVE source of truth for
// the manufacturing coordinators' working-capital reserve (sp-kk61). It clears any
// working_capital_reserve key already in the launch config (a stale copy persisted
// at a prior boot) and re-injects the daemon's boot-loaded value, so the rebuilt
// command reflects the CURRENT config.yaml on every build — creation and restart
// recovery alike, for the goods_factory_coordinator.
//
// This is what makes the factory input-buy spend floor operator-reachable: before
// this, no CLI flag or config key populated RunFactoryCoordinatorCommand's
// WorkingCapitalReserve field at all, so every factory was stuck at the
// coordinator's immutable 50k lower bound (sp-agzj) with no way to raise it — the
// gap tonight's 682k factory input buy rode into a fleet-wide 53k treasury trough.
// The clear is essential to honesty: dropping the knob from config.yaml must fall
// back to the 50k floor, and that can only happen if the stale persisted key is
// removed rather than left to shadow the now-absent live value.
func (s *DaemonServer) resolveManufacturingConfig(config map[string]interface{}) {
	for _, key := range manufacturingConfigKeys {
		delete(config, key)
	}
	s.injectManufacturingConfig(config)
}

// injectManufacturingConfig writes the working-capital reserve knob from
// config.yaml (s.manufacturingConfig) into a coordinator container's launch config
// (sp-kk61). Only written when the captain actually set it (non-zero), so an unset
// knob defers to goods_factory_coordinator's documented 50k floor (RULINGS #5) —
// the daemon never hardcodes the operational value. manufacturing_coordinator
// (parallel task-based pipelines) reads the same key so the value is uniformly
// reachable across both command types, though its purchaser has no floor
// enforcement of its own yet (tracked separately). Callers go through
// resolveManufacturingConfig so any stale persisted key is cleared first (sp-ts82).
func (s *DaemonServer) injectManufacturingConfig(config map[string]interface{}) {
	if s.manufacturingConfig.WorkingCapitalReserve != 0 {
		config["working_capital_reserve"] = int(s.manufacturingConfig.WorkingCapitalReserve)
	}
	// sp-yqx4: only when the captain set an override — an unset key defers to the
	// goods_factory build's 40% default (resolveReserveTreasuryPct), so the counter-cyclical
	// floor is ON in production without the captain naming it.
	if s.manufacturingConfig.WorkingCapitalReserveTreasuryPct != 0 {
		config["working_capital_reserve_treasury_pct"] = s.manufacturingConfig.WorkingCapitalReserveTreasuryPct
	}
	// sp-iv65: the ladder-chase input price ceiling. Only written when the captain set a
	// non-zero multiplier — an unset key defers to the goods_factory build's 1.5 default
	// (the guard runs ON in production without the captain naming it, a protective default,
	// RULINGS #5). The disable flag is written only when true, so absent/false keeps the
	// guard on; the clear in resolveManufacturingConfig makes turning it back on take effect.
	if s.manufacturingConfig.InputPriceCeilingMultiplier != 0 {
		config["input_price_ceiling_multiplier"] = s.manufacturingConfig.InputPriceCeilingMultiplier
	}
	if s.manufacturingConfig.InputPriceCeilingDisabled {
		config["input_price_ceiling_disabled"] = true
	}
	// sp-a5j7 Phase 2: supply-first sourcing (the wedx restoration). Only written when the
	// captain set an override — an unset rescue multiplier defers to the goods_factory build's
	// 1.2 default (supply-first runs ON in production without the captain naming it, RULINGS #5).
	// The era-end and disable flags are written only when true, so absent/false keeps supply-first
	// on; the clear in resolveManufacturingConfig makes turning them back off take effect.
	if s.manufacturingConfig.InputRescueMultiplier != 0 {
		config["input_rescue_multiplier"] = s.manufacturingConfig.InputRescueMultiplier
	}
	if s.manufacturingConfig.InputEraEndPriceFirst {
		config["input_era_end_price_first"] = true
	}
	if s.manufacturingConfig.InputSourcingDisabled {
		config["input_sourcing_disabled"] = true
	}
	// sp-rh2z: the chain P&L kill-switch. Only written when the captain set a non-zero
	// threshold/window — an unset key defers to the goods_factory build's 30000/hr + 6h defaults
	// (the kill-switch runs ON in production without the captain naming it, a protective default
	// that can only STOP spend, RULINGS #5). The disable flag is written only when true, so
	// absent/false keeps the switch on; the clear in resolveManufacturingConfig makes turning it
	// back on take effect.
	if s.manufacturingConfig.ChainPnLKillThresholdPerHour != 0 {
		config["chain_pnl_kill_threshold_per_hour"] = s.manufacturingConfig.ChainPnLKillThresholdPerHour
	}
	if s.manufacturingConfig.ChainPnLWindowHours != 0 {
		config["chain_pnl_window_hours"] = s.manufacturingConfig.ChainPnLWindowHours
	}
	if s.manufacturingConfig.ChainPnLKillDisabled {
		config["chain_pnl_kill_disabled"] = true
	}
	if s.manufacturingConfig.PlannerStockDisabled {
		config["planner_stock_disabled"] = true
	}
	// sp-r5a6: the input-poison anti-cycle. Only written when the captain set a non-zero recovery
	// half-life — an unset key defers to the goods_factory build's 194min default (the anti-cycle
	// runs ON in production without the captain naming it, a protective default that can only STOP
	// spend, RULINGS #5). The disable flag is written only when true, so absent/false keeps the
	// anti-cycle on; the clear in resolveManufacturingConfig makes turning it back on take effect.
	if s.manufacturingConfig.InputRecoveryReattemptMinutes != 0 {
		config["input_recovery_reattempt_minutes"] = s.manufacturingConfig.InputRecoveryReattemptMinutes
	}
	if s.manufacturingConfig.AntiCycleDisabled {
		config["anti_cycle_disabled"] = true
	}
	// sp-xdk6: the export-ask-subsidy rest signal. Only written when the captain set a non-zero
	// rest window — an unset key defers to the goods_factory build's 90min default (the signal runs
	// ON in production without the captain naming it, a protective default that can only STOP a lift,
	// RULINGS #5). The disable flag is written only when true, so absent/false keeps the signal on;
	// the clear in resolveManufacturingConfig makes turning it back on take effect.
	if s.manufacturingConfig.RestWindowMinutes != 0 {
		config["rest_window_minutes"] = s.manufacturingConfig.RestWindowMinutes
	}
	if s.manufacturingConfig.RestSignalDisabled {
		config["rest_signal_disabled"] = true
	}
	// sp-vh1s (Admiral sign-off 2026-07-14): the unified gate-fill master toggle + gate output-buy
	// throughput-pacing knobs. The toggle and the pacing-disabled flag are written only when true, the
	// pacing coefficients only when non-zero — so an absent [manufacturing] section is byte-identical to
	// today: the toggle stays OFF (the whole feature dark, IsUnifiedGateNode false), and the coefficients
	// resolve to their 2.0/1.0 defaults downstream but are only ever consulted for a gate node, so an
	// OFF/profit factory never sees them. The clear in resolveManufacturingConfig makes flipping the
	// toggle back off take effect on a recovered coordinator (sp-ts82). construction_site_waypoint is a
	// per-launch key, not a global knob, so it is NOT injected here (see manufacturingConfigKeys).
	if s.manufacturingConfig.UnifiedGateFill {
		config["unified_gate_fill"] = true
	}
	if s.manufacturingConfig.GateOutputBuyRateMultiple != 0 {
		config["gate_output_buy_rate_multiple"] = s.manufacturingConfig.GateOutputBuyRateMultiple
	}
	if s.manufacturingConfig.GateOutputPerLotMultiple != 0 {
		config["gate_output_per_lot_multiple"] = s.manufacturingConfig.GateOutputPerLotMultiple
	}
	if s.manufacturingConfig.GateOutputPacingDisabled {
		config["gate_output_pacing_disabled"] = true
	}
	// sp-jav2 / FACTORY_DOCTRINE X1: the fabricate depth cap. Only written when the captain set a
	// non-zero depth — an unset key defers to the resolver's depth-3 default (sp-yfzi; the cap runs
	// ON in production without the captain naming it, a protective default that only redirects an
	// input from fabrication to a market-buy, RULINGS #5). The disable flag is written only when true,
	// so absent/false keeps the cap on; the clear in resolveManufacturingConfig makes turning it back
	// on take effect.
	if s.manufacturingConfig.FabricateMaxDepth != 0 {
		config["fabricate_max_depth"] = s.manufacturingConfig.FabricateMaxDepth
	}
	if s.manufacturingConfig.FabricateDepthCapDisabled {
		config["fabricate_depth_cap_disabled"] = true
	}
	// sp-yfzi: the production acquisition strategy. Only written when the captain set it — an unset
	// key defers to the goods_factory build's "smart" default (resolveProductionStrategy), so
	// scarcity-gated recursion runs ON in production without the captain naming it. Cleared+reinjected
	// via resolveManufacturingConfig so dropping it from config.yaml reverts to the smart default
	// rather than shadowing it with a stale persisted copy (sp-ts82). A captain pins "prefer-buy" here
	// to dial back to the sp-jav2 buy-all-inputs posture (RULINGS #5).
	if s.manufacturingConfig.ProductionStrategy != "" {
		config["production_strategy"] = s.manufacturingConfig.ProductionStrategy
	}
	// sp-ev0n: the GLOBAL concurrent-hull default for factories, derived from the
	// [manufacturing.siting] workers_per_chain rotation divisor (the value siting already
	// assumes each chain occupies). Only written when the captain set it (non-zero) —
	// an unset knob leaves factories UNBOUNDED (resolveFactoryWorkerCap → 0), so a fleet
	// that never configured workers_per_chain keeps the pre-sp-ev0n emergent fan-out
	// (RULINGS #5). Rounded to a whole hull count. The per-op live override (`worker_cap`)
	// is written separately by the RPC and takes precedence in resolveFactoryWorkerCap.
	if wpc := s.manufacturingConfig.Siting.WorkersPerChain; wpc > 0 {
		config["factory_worker_cap_default"] = int(math.Round(wpc))
	}
}

// resolveConstructionUnifiedGateFill threads the SAME [manufacturing] unified_gate_fill toggle into
// the construction-supply drain (sp-vh1s): the drain's RunConstructionCoordinatorCommand carries only
// UnifiedGateFill (it derives its own construction site per-task, and its pacing/terminal live on the
// factory command it delegates to), so this deliberately injects ONLY that one key rather than running
// the full resolveManufacturingConfig — which would clear+reinject the drain's launch-config
// production_strategy (a construction behavior change unrelated to this bead). Cleared then reinjected
// so config.yaml is the single live source of truth (sp-ts82): dropping the toggle reverts a recovered
// drain to OFF. Absent/false injects nothing → byte-identical to today.
func (s *DaemonServer) resolveConstructionUnifiedGateFill(config map[string]interface{}) {
	delete(config, "unified_gate_fill")
	if s.manufacturingConfig.UnifiedGateFill {
		config["unified_gate_fill"] = true
	}
}

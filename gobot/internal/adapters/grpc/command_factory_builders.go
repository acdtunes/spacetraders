package grpc

import (
	"strings"
	"time"

	autooutfitCmd "github.com/andrescamacho/spacetraders-go/internal/application/autooutfit"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	liquidationCmd "github.com/andrescamacho/spacetraders-go/internal/application/liquidation"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	shipCargoCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNavCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypesCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	storageCmd "github.com/andrescamacho/spacetraders-go/internal/application/storage/commands"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func buildScoutTourCommand(cfg *configReader, playerID int, containerID string) interface{} {
	// Unified iteration semantics (sp-7yej invariant 3): 0 means "the type's
	// default" (one tour, matching the CLI flag's default), never "zero work".
	// Normalized here so creation and restart recovery (both build through this
	// factory) agree.
	iterations := cfg.RequiredInt("iterations")
	if iterations == 0 {
		iterations = 1
	}
	return &scoutingCmd.ScoutTourCommand{
		PlayerID:           shared.MustNewPlayerID(playerID),
		ShipSymbol:         cfg.RequiredString("ship_symbol"),
		Markets:            cfg.RequiredStringSlice("markets"),
		Iterations:         iterations,
		ScanInterval:       time.Duration(cfg.OptionalInt("scan_interval_secs", 0)) * time.Second,
		StartJitterMaxSecs: cfg.OptionalInt("tour_start_jitter_max_seconds", 0),
	}
}

// buildTradeFleetCoordinatorCommand rebuilds the standing trade-fleet coordinator
// command from a persisted launch config so a daemon restart re-adopts it.
// The [trade_fleet] knobs are resolved LIVE from config.yaml just before this runs
// (resolveTradeFleetConfig in buildCommandForType), so the persisted trade_fleet_*
// keys are transient — the reads below see the current config.yaml. Enabled is
// reconstructed as the negation of trade_fleet_disabled: an absent key reads as
// enabled, preserving the default-ON intent across a recovery from an old config that
// predates the key. The int64 caps are read via OptionalInt (JSON numbers round-trip
// through float64/int), mirroring buildTourCoordinatorCommand.
func buildTradeFleetCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.RunTradeFleetCoordinatorCommand{
		PlayerID:              shared.MustNewPlayerID(playerID),
		ContainerID:           cfg.RequiredNonEmptyString("container_id"),
		AgentSymbol:           cfg.OptionalString("agent_symbol"),
		Enabled:               !cfg.OptionalBool("trade_fleet_disabled"),
		CooldownSecs:          cfg.OptionalInt("trade_fleet_cooldown_secs", 0),
		MaxConcurrentTours:    cfg.OptionalInt("trade_fleet_max_concurrent", 0),
		TickIntervalSecs:      cfg.OptionalInt("trade_fleet_tick_secs", 0),
		MaxHops:               cfg.OptionalInt("trade_fleet_max_hops", 0),
		MaxSpend:              int64(cfg.OptionalInt("trade_fleet_max_spend", 0)),
		MinMargin:             cfg.OptionalInt("trade_fleet_min_margin", 0),
		ReplanLimit:           cfg.OptionalInt("trade_fleet_replan_limit", 0),
		WorkingCapitalReserve: int64(cfg.OptionalInt("trade_fleet_reserve", 0)),
		// sp-1pli: minutes on config, seconds on the command (matches CooldownSecs) — converted
		// here, the one crossing point, so every downstream read is uniformly in seconds.
		RelaunchBackoffMaxSecs: cfg.OptionalInt("trade_fleet_relaunch_backoff_max_minutes", 0) * 60,
		// sp-nkci: the restart-mass-park exemption is live by default — an absent disable key
		// reads as false (exemption ON), like Enabled. Window/threshold defer to the
		// coordinator's own defaults (120s / 4 hulls) when unset.
		MassParkExemptDisabled: cfg.OptionalBool("trade_fleet_masspark_exempt_disabled"),
		MassParkWindowSecs:     cfg.OptionalInt("trade_fleet_masspark_window_seconds", 0),
		MassParkMinHulls:       cfg.OptionalInt("trade_fleet_masspark_min_hulls", 0),
		// sp-m3122: the liveness watchdog is always ARMED — only the stall threshold is
		// configurable. 0/absent ⇒ the coordinator's own 12-min default.
		WatchdogStallSecs: cfg.OptionalInt("trade_fleet_watchdog_stall_secs", 0),
		// sp-tgll8: the inventory-pressure governor is always ARMED — only the FULL-hull pause
		// threshold is configurable. 0/absent ⇒ the coordinator's own 65% default.
		FullHullPausePct: cfg.OptionalInt("trade_fleet_full_hull_pause_pct", 0),
		// MVT specialist pool: always ARMED (no arm key); an absent tuning is the spec default.
		SpecialistFractionPct:    cfg.OptionalInt("specialist_fraction_pct", tradingCmd.DefaultSpecialistFractionPct),
		FatLaneMultiplePct:       cfg.OptionalInt("fat_lane_multiple_pct", tradingCmd.DefaultFatLaneMultiplePct),
		SpecialistCadenceMinutes: cfg.OptionalInt("specialist_cadence_minutes", tradingCmd.DefaultSpecialistCadenceMinutes),
	}
}

// buildLongHaulArbFleetCoordinatorCommand rebuilds the standing long-haul arb coordinator
// (sp-mepj) from its persisted launch config, so creation and restart recovery share one
// builder. It carries only the coordinator's IDENTITY (container/agent) plus the
// Admiral-authorized money-envelope caps + cadence; unset caps defer to the command's own
// aggressive defaults (~1M/haul, ~2M exposure). Like trade_fleet_coordinator it loops
// forever inside one Handle(), so the container's iteration budget is irrelevant.
func buildLongHaulArbFleetCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.LongHaulArbFleetCoordinatorCommand{
		PlayerID:         shared.MustNewPlayerID(playerID),
		ContainerID:      cfg.RequiredNonEmptyString("container_id"),
		AgentSymbol:      cfg.OptionalString("agent_symbol"),
		TickIntervalSecs: cfg.OptionalInt("longhaul_tick_secs", 0),
		// Admiral-authorized aggressive SIZING; 0/absent defers to the command's own
		// defaultLongHaulPerHaulCap / defaultLongHaulTotalExposureCap.
		PerHaulCap:        int64(cfg.OptionalInt("longhaul_per_haul_cap", 0)),
		TotalExposureCap:  int64(cfg.OptionalInt("longhaul_total_exposure_cap", 0)),
		WatchdogStallSecs: cfg.OptionalInt("longhaul_watchdog_stall_secs", 0),
	}
}

// buildLongHaulArbWorkerCommand rebuilds one per-hull long-haul worker (sp-mepj) from its
// persisted launch config — the recovery-safe rebuild LaunchLongHaul and restart recovery
// share. The operation="long-haul" claim identity lives in the config (read by
// createShipAssignments), so it is deliberately not a command field here. Iterations defaults
// to -1 (continuous) so a recovered worker resumes its episode loop.
func buildLongHaulArbWorkerCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.RunLongHaulArbCommand{
		ShipSymbol:       cfg.RequiredNonEmptyString("ship_symbol"),
		AgentSymbol:      cfg.OptionalString("agent_symbol"),
		PlayerID:         playerID,
		ContainerID:      containerID,
		Iterations:       cfg.OptionalInt("iterations", -1),
		PerHaulCap:       int64(cfg.OptionalInt("per_haul_cap", 0)),
		TotalExposureCap: int64(cfg.OptionalInt("total_exposure_cap", 0)),
		MinMargin:        cfg.OptionalInt("min_margin", 0),
		IdleBackoffSecs:  cfg.OptionalInt("idle_backoff_secs", 0),
	}
}

// buildWorkerFerryCommand rebuilds a one-shot cross-system ferry from its persisted launch
// config so restart recovery re-adopts it (twin of buildWorkerFerryCommand). A
// coordinator-spawned ferry (coordinator_id present) is skipped by recovery and reclaimed
// by the worker_rebalancer_coordinator, but the command is still rebuilt here so the
// coordinator's StartWorkerFerry path can reconstruct it. Re-running after a restart is
// safe: travel() waits out any in-transit leg and re-plans the gate path from the hull's
// CURRENT position, so a mid-ferry restart resumes rather than strands.
func buildWorkerFerryCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.WorkerFerryCommand{
		PlayerID:            shared.MustNewPlayerID(playerID),
		ShipSymbol:          cfg.RequiredString("ship_symbol"),
		DestinationWaypoint: cfg.RequiredString("destination"),
		CoordinatorID:       cfg.OptionalString("coordinator_id"),
		// Reload the ferry-reposition jump bound stamped at PersistWorkerFerryWorker (the
		// [trade_fleet].reposition_jump_bound) so it survives the persist→rebuild boundary the ferry
		// crosses on every start (the o34q read side). Absent → 0, which the ferry's Handle resolves
		// to the default 12 (resolveRepositionJumpBound) — never a persist-layer magic value.
		RepositionJumpBound: cfg.OptionalInt("reposition_jump_bound", 0),
	}
}

// buildCargoLiquidationCommand rebuilds a one-shot cargo-liquidation worker from its
// persisted launch config so restart recovery re-adopts it (sp-39oi, twin of
// buildWorkerFerryCommand). A coordinator-spawned worker (coordinator_id present) is
// skipped by recovery and reclaimed by the contract fleet coordinator, but the command is
// still rebuilt here so the coordinator's start path can reconstruct it. Re-running after a
// restart is safe: the worker reconciles the hull against the server, so an already-cleared
// hold is an idempotent no-op. min_jettison_value defaults to 0 (jettison OFF — never
// destroy value without an explicit floor, RULINGS #5).
func buildCargoLiquidationCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &liquidationCmd.LiquidateCargoCommand{
		PlayerID:         shared.MustNewPlayerID(playerID),
		ShipSymbol:       cfg.RequiredString("ship_symbol"),
		MinJettisonValue: cfg.OptionalInt("min_jettison_value", 0),
		CoordinatorID:    cfg.OptionalString("coordinator_id"),
	}
}

// buildProbeSensingCoordinatorCommand rebuilds the standing parked-probe sensing coordinator
// from its persisted launch config so restart recovery re-adopts it byte-identically
// (RULINGS #2). Like the scout-post coordinator it is a reconcile-loop coordinator (NOT a
// CoordinatorOwnsIterations type). Every knob is optional (0/absent → the coordinator's own
// documented default, RULINGS #5), so the creation op and recovery share one construction and
// can never drift. goods_whitelist arrives as the [sensing] config.yaml CSV, injected by
// resolveSensingConfig just before this runs (the int-only tune mechanism carries no
// strings); an explicit slice is also accepted for forward compatibility.
//
// The touring model's keys are read into the retired command fields and IGNORED. That is the
// recovery contract, not leftovers: a container persisted by the old core still carries
// probe_budget and freshness_target_secs in its config column, and OptionalInt tolerates keys
// nothing asks for — so an old container must still BUILD and come up on the new core's
// defaults rather than failing recovery. They are read here so the tolerance is visible and
// pinned by a test, instead of resting on a property of configReader.
func buildProbeSensingCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	goods := cfg.OptionalStringSlice("goods_whitelist")
	if len(goods) == 0 {
		goods = csvValues(cfg.OptionalString("goods_whitelist"))
	}
	return &scoutingCmd.RunProbeSensingCoordinatorCommand{
		PlayerID:       shared.MustNewPlayerID(playerID),
		ContainerID:    cfg.RequiredNonEmptyString("container_id"),
		GoodsWhitelist: goods,
		TickSecs:       cfg.OptionalInt("tick_secs", 0),
		WaitLowMs:      cfg.OptionalInt("wait_low_ms", 0),
		WaitHighMs:     cfg.OptionalInt("wait_high_ms", 0),

		ProbeCap:                cfg.OptionalInt("probe_cap", 0),
		ExpansionEnabled:        cfg.OptionalInt("expansion_enabled", 0),
		TargetUtilPct:           cfg.OptionalInt("target_util_pct", 0),
		MinScanRateMilli:        cfg.OptionalInt("min_scan_rate_milli", 0),
		ExpansionMinBudgetMilli: cfg.OptionalInt("expansion_min_budget_milli", 0),
		ValueClampR:             cfg.OptionalInt("value_clamp_r", 0),
		InflightCap:             cfg.OptionalInt("inflight_cap", 0),
		CapitalMultiplierKMilli: cfg.OptionalInt("capital_multiplier_k_milli", 0),
		CapexReserveCredits:     cfg.OptionalInt("capex_reserve_credits", 0),
		QuartermasterCadence:    cfg.OptionalInt("quartermaster_cadence_secs", 0),
		SurgeInFlightCap:        cfg.OptionalInt("surge_inflight_cap", 0),
		CoverageReserve:         cfg.OptionalInt("coverage_reserve", 0),
		WalkAwayMult:            cfg.OptionalInt("procurement_walkaway_mult", 0),
		JumpPenaltyCredits:      cfg.OptionalInt("procurement_jump_penalty_credits", 0),
		ChartHullCap:            cfg.OptionalInt("chart_hull_cap", 0),
		SecondChartHullAt:       cfg.OptionalInt("chart_hull_2_at", 0),
		ThirdChartHullAt:        cfg.OptionalInt("chart_hull_3_at", 0),

		// Retired: read for recovery tolerance, never consulted by the loop.
		DepthFloor:               int64(cfg.OptionalInt("depth_floor", 0)),
		ProbeBudget:              cfg.OptionalInt("probe_budget", 0),
		SecondProbeThreshold:     cfg.OptionalInt("second_probe_threshold", 0),
		PurchaseCooldownSecs:     cfg.OptionalInt("purchase_cooldown_secs", 0),
		FreshnessTargetSecs:      cfg.OptionalInt("freshness_target_secs", 0),
		MaxSpendPerCycle:         cfg.OptionalInt("max_spend_per_cycle", 0),
		SpendWindowSecs:          cfg.OptionalInt("spend_window_secs", 0),
		DiscoveryDeclaresPerTick: cfg.OptionalInt("discovery_declares_per_tick", 0),
	}
}

// csvValues splits a comma-separated launch value into its trimmed, non-empty
// entries; an empty/absent value yields nil so the consumer's default applies.
func csvValues(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildAutoOutfitCoordinatorCommand rebuilds the standing guarded auto-outfit coordinator
// from its persisted launch config so restart recovery re-adopts it byte-identically
// (RULINGS #2, sp-buyd). Like the autosizer it is a reconcile-loop coordinator (NOT a
// CoordinatorOwnsIterations type). Every tunable knob is optional (0 → the coordinator's
// own default, RULINGS #5) and live-tunable via `tune --operation autooutfit`, so the flat
// launch config carries only identity + the sticky dry-run flag. auto_outfit_launch_dry_run
// is IDENTITY (set once at creation, preserved across restart, mirrors
// capacity_launch_dry_run) so a dry-run launch stays observe-only through recovery.
func buildAutoOutfitCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &autooutfitCmd.RunAutoOutfitCoordinatorCommand{
		PlayerID:               shared.MustNewPlayerID(playerID),
		ContainerID:            cfg.RequiredNonEmptyString("container_id"),
		TickIntervalSecs:       cfg.OptionalInt("tick_interval_secs", 0),
		DryRun:                 cfg.OptionalBool("auto_outfit_launch_dry_run"),
		MinTelemetrySamples:    cfg.OptionalInt("min_telemetry_samples", 0),
		PriceCeiling:           cfg.OptionalInt("price_ceiling", 0),
		MaxInstallsPerTick:     cfg.OptionalInt("max_installs_per_tick", 0),
		PaybackHorizonHours:    cfg.OptionalInt("payback_horizon_hours", 0),
		MaxTreasuryFractionPct: cfg.OptionalInt("max_treasury_fraction_pct", 0),
		InstallFeeEstimate:     cfg.OptionalInt("install_fee_estimate", 0),
		HopCost:                cfg.OptionalInt("hop_cost", 0),
		TelemetryWindowSecs:    cfg.OptionalInt("telemetry_window_secs", 0),
	}
}

// buildNavigateShipCommand rebuilds a one-shot navigate from its persisted
// launch config so restart recovery re-adopts a RUNNING navigate instead of
// orphaning it (sp-7yej invariant 4). Re-running the command is safe:
// NavigateRoute no-ops when the ship is already at the destination and the
// RouteExecutor waits out a transit already in progress (the boot-time
// ShipStateScheduler.ScheduleAllPending re-arms the arrival timer).
func buildNavigateShipCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &shipNavCmd.NavigateRouteCommand{
		ShipSymbol:  cfg.RequiredString("ship_symbol"),
		Destination: cfg.RequiredString("destination"),
		PlayerID:    shared.MustNewPlayerID(playerID),
	}
}

// buildRouteShipCommand rebuilds a one-shot cross-system route from its persisted
// launch config so restart recovery re-adopts a RUNNING route instead of orphaning it
// (sp-6hjw, same invariant as buildNavigateShipCommand / sp-7yej invariant 4). Re-running
// is safe: travel() waits out any in-transit leg and re-plans the gate path from the
// hull's CURRENT position, so a mid-route restart resumes rather than strands.
func buildRouteShipCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &shipNavCmd.RouteShipCommand{
		ShipSymbol:  cfg.RequiredString("ship_symbol"),
		Destination: cfg.RequiredString("destination"),
		PlayerID:    shared.MustNewPlayerID(playerID),
	}
}

// buildWarpShipCommand rebuilds a one-shot off-gate warp from its persisted launch
// config so restart recovery re-adopts a RUNNING warp instead of orphaning the hull
// (sp-7yej invariant 4, twin of buildRouteShipCommand). Re-running is safe: the
// executor re-reads the hull's CURRENT position and re-runs its fuel-safety guard, so
// a hull that already arrived is refused or no-ops rather than warped twice blind.
func buildWarpShipCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &shipNavCmd.WarpShipCommand{
		ShipSymbol:  cfg.RequiredString("ship_symbol"),
		Destination: cfg.RequiredString("destination"),
		PlayerID:    shared.MustNewPlayerID(playerID),
	}
}

// buildDockShipCommand / buildOrbitShipCommand / buildRefuelShipCommand rebuild
// the remaining one-shot ship ops (sp-7yej invariant 4). All are idempotent to
// re-run after a restart: docking a docked ship, orbiting an orbiting ship and
// refueling a full tank are no-ops at the domain/API layer, so the recovered
// container simply finishes the op (or confirms it already happened) and
// releases the hull through the normal runner lifecycle.
func buildDockShipCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &shipTypesCmd.DockShipCommand{
		ShipSymbol: cfg.RequiredString("ship_symbol"),
		PlayerID:   shared.MustNewPlayerID(playerID),
	}
}

func buildOrbitShipCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &shipTypesCmd.OrbitShipCommand{
		ShipSymbol: cfg.RequiredString("ship_symbol"),
		PlayerID:   shared.MustNewPlayerID(playerID),
	}
}

func buildRefuelShipCommand(cfg *configReader, playerID int, containerID string) interface{} {
	cmd := &shipTypesCmd.RefuelShipCommand{
		ShipSymbol: cfg.RequiredString("ship_symbol"),
		PlayerID:   shared.MustNewPlayerID(playerID),
	}
	// "units" is persisted only when the caller requested a partial refuel
	// (RefuelShip's *int contract: nil = full tank). Absent key → nil stays.
	if units, ok := cfg.PresentInt("units"); ok {
		cmd.Units = &units
	}
	return cmd
}

// buildJettisonCargoCommand rebuilds a one-shot jettison (sp-7yej invariant 4).
// A re-run after a restart either performs the jettison (it never happened) or
// fails HONESTLY because the cargo is already gone — a visible FAILED container
// with the verbatim API cause, never a silently-orphaned hull.
func buildJettisonCargoCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &shipCargoCmd.JettisonCargoCommand{
		ShipSymbol: cfg.RequiredString("ship_symbol"),
		PlayerID:   shared.MustNewPlayerID(playerID),
		GoodSymbol: cfg.RequiredString("good_symbol"),
		Units:      cfg.RequiredInt("units"),
	}
}

// buildScoutFleetAssignmentCommand rebuilds the async VRP fleet-assignment pass
// (sp-7yej invariant 4). Re-running the assignment after a restart is safe —
// it recomputes routes from current fleet/market state and claims no hull.
func buildScoutFleetAssignmentCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &scoutingCmd.AssignScoutingFleetCommand{
		PlayerID:     shared.MustNewPlayerID(playerID),
		SystemSymbol: cfg.RequiredString("system_symbol"),
	}
}

func buildContractWorkflowCommand(cfg *configReader, playerID int, containerID string) interface{} {
	// sp-ehg9: a looping batch-contract container persists iterations=-1; rebuild
	// its RunWorkflowCommand with Loop=true so a daemon restart RE-ADOPTS the
	// frigate loop as a loop (recovery-safe). recoverContainer separately reads
	// the same config["iterations"] for the container's maxIterations. A
	// single-shot worker (iterations 1 or absent — every coordinator-spawned
	// worker) rebuilds Loop=false, byte-identical to today.
	return &contractCmd.RunWorkflowCommand{
		ShipSymbol:    cfg.RequiredString("ship_symbol"),
		PlayerID:      shared.MustNewPlayerID(playerID),
		ContainerID:   containerID,
		CoordinatorID: cfg.OptionalString("coordinator_id"),
		Loop:          cfg.OptionalInt("iterations", 1) == -1,
	}
}

func buildContractFleetCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &contractCmd.RunFleetCoordinatorCommand{
		PlayerID:        shared.MustNewPlayerID(playerID),
		ShipSymbols:     []string{},
		ContainerID:     cfg.RequiredString("container_id"),
		DedicatedShips:  cfg.OptionalStringSlice("dedicated_ships"),
		StandbyStations: cfg.OptionalStringSlice("standby_stations"),
		// First-boot seed marker (sp-86vb): absent on creation → false → the seed
		// is applied once and the marker persisted; true on every restart rebuild →
		// the seed is NOT replayed, so a live `fleet remove` survives the restart
		// (RULINGS #2). Written by DedicatedFleetSeedConfigPersister after first boot.
		DedicatedShipsSeeded: cfg.OptionalBool(dedicatedShipsSeededConfigKey),
		// Command-cargo baseline (RULINGS #5): absent key → 0 → the
		// contract package's documented default (80, the light-hauler
		// standard - see CommandCargoBaselineDefault).
		CommandCargoBaseline: cfg.OptionalInt("command_cargo_baseline", 0),
		// Auto-liquidation knobs: absent keys → default false/0 → feature ON
		// with jettison OFF. These are resolved LIVE from config.yaml by
		// resolveAutoLiquidationConfig on every build, so the persisted copies
		// are dead and a config edit + restart retunes a recovered coordinator.
		AutoLiquidationDisabled:     cfg.OptionalBool("auto_liquidation_disabled"),
		LiquidationMinJettisonValue: cfg.OptionalInt("liquidation_min_jettison_value", 0),
		// Idle-gap arb knobs: absent keys → 0 → the contract
		// package's documented defaults (IdleArbConfig.WithDefaults). These
		// keys are resolved LIVE from config.yaml by resolveIdleArbConfig on
		// every build — the persisted copies are dead — so a config
		// edit + daemon restart retunes the harvest, recovery included.
		IdleArbDisabled:     cfg.OptionalBool("idle_arb_disabled"),
		IdleArbReserveHulls: cfg.OptionalInt("idle_arb_reserve_hulls", 0),
		IdleArbHubRadius:    float64(cfg.OptionalInt("idle_arb_hub_radius", 0)),
		IdleArbMaxSpend:     cfg.OptionalInt("idle_arb_max_spend", 0),
		IdleArbMinMargin:    cfg.OptionalInt("idle_arb_min_margin", 0),
		IdleArbIntervalSecs: cfg.OptionalInt("idle_arb_interval_secs", 0),
		// Money guards. Absent → 0/nil → the contract package's
		// WithDefaults applies the documented defaults (leash 80, leg-cap 480s,
		// verify 80%, blacklist [ELECTRONICS]). An explicit empty blacklist ([])
		// is preserved by OptionalStringSlice (non-nil) so a config whitelist-flip
		// genuinely disables it without a code change.
		IdleArbLeashRadius:      float64(cfg.OptionalInt("idle_arb_leash_radius", 0)),
		IdleArbMaxLegSecs:       cfg.OptionalInt("idle_arb_max_leg_secs", 0),
		IdleArbMarginVerifyPct:  cfg.OptionalInt("idle_arb_margin_verify_pct", 0),
		IdleArbRecoveryHoldSecs: cfg.OptionalInt("idle_arb_recovery_hold_secs", 0),
		IdleArbBlacklist:        cfg.OptionalStringSlice("idle_arb_blacklist"),
		// Per-trip profitability floor (0 → WithDefaults: 100/u, 20%, 35/u fuel).
		IdleArbMinNetProfit:    cfg.OptionalInt("idle_arb_min_net_profit", 0),
		IdleArbNetProfitPct:    cfg.OptionalInt("idle_arb_net_profit_pct", 0),
		IdleArbFuelCostPerUnit: cfg.OptionalInt("idle_arb_fuel_cost_per_unit", 0),
	}
}

func buildPurchaseShipCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &shipyardCmd.PurchaseShipCommand{
		PurchasingShipSymbol: cfg.RequiredString("ship_symbol"),
		ShipType:             cfg.RequiredString("ship_type"),
		PlayerID:             shared.MustNewPlayerID(playerID),
		ShipyardWaypoint:     cfg.OptionalString("shipyard"),
	}
}

func buildBatchPurchaseShipsCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &shipyardCmd.BatchPurchaseShipsCommand{
		PurchasingShipSymbol: cfg.RequiredString("ship_symbol"),
		ShipType:             cfg.RequiredString("ship_type"),
		Quantity:             cfg.RequiredInt("quantity"),
		MaxBudget:            cfg.RequiredInt("max_budget"),
		PlayerID:             shared.MustNewPlayerID(playerID),
		ShipyardWaypoint:     cfg.OptionalString("shipyard"),
		// The optional operator-named fleet to dedicate each purchased hull
		// to atomically. Absent (plain purchase) -> "" -> byte-identical (hull lands
		// undedicated). Persisted in the container config so a daemon restart re-adopts
		// the same atomic buy+dedicate intent (RULINGS #2).
		DedicateFleet: cfg.OptionalString("dedicate_fleet"),
	}
}

// buildConstructionCoordinatorCommand rebuilds the standing construction-supply drain command
// from a persisted launch config so a daemon restart re-adopts it (RULINGS #2). The
// drain is queue-driven: it re-polls READY DELIVER_TO_CONSTRUCTION tasks from persistence every
// tick, so the only launch config it needs is the operating system + identity. max_iterations
// defaults to -1 (standing: loops forever inside Handle); a positive value bounds a CLI/test run.
func buildConstructionCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &goodsCmd.RunConstructionCoordinatorCommand{
		PlayerID: playerID,
		// Optional: an empty system lets the drain derive it per-tick from the construction
		// site (the bootstrap gate launches it with no system).
		SystemSymbol:  cfg.OptionalString("system_symbol"),
		ContainerID:   cfg.RequiredString("container_id"),
		MaxIterations: cfg.OptionalInt("max_iterations", -1),
		TickSeconds:   cfg.OptionalInt("tick_seconds", 0),
		// Prefer the drain's OWN dedicated gate-hauler fleet (e.g. TORWIND-C/-D) before
		// opportunistic idle hulls. Empty dedicated_fleet defaults (in-handler) to the shared
		// "manufacturing" identity that also authorizes the claim; exclusive_dedicated_fleet seals the
		// drain to that fleet (no opportunistic fallback). Both reload on restart (RULINGS #2).
		DedicatedFleet:          cfg.OptionalString("dedicated_fleet"),
		ExclusiveDedicatedFleet: cfg.OptionalBool("exclusive_dedicated_fleet"),
	}
}

func buildGasCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &gasCmd.RunGasCoordinatorCommand{
		GasOperationID: cfg.RequiredString("gas_operation_id"),
		PlayerID:       shared.MustNewPlayerID(playerID),
		GasGiant:       cfg.RequiredNonEmptyString("gas_giant"),
		SiphonShips:    cfg.RequiredStringSlice("siphon_ships"),
		StorageShips:   cfg.RequiredStringSlice("storage_ships", "transport_ships"),
		ContainerID:    cfg.RequiredString("container_id"),
		Force:          cfg.OptionalBool("force"),
		DryRun:         cfg.OptionalBool("dry_run"),
	}
}

// buildWarehouseCommand rebuilds the passive warehouse command from a persisted
// launch config so restart recovery re-adopts a RUNNING warehouse container
// (sp-dchv Lane B). Both ContainerID and OperationID are pinned to the
// recovery-supplied containerID (the persisted row's ID), mirroring
// trade_route/arb_run, so the operation row, the hull's ClaimShip, and the
// coordinator registration all stay pinned to the same identity across a
// restart. The hull's actual cargo is rebuilt separately and for free by the
// StorageRecoveryService from live ship state (RULINGS #2) — this only rebuilds
// the command that re-parks and re-registers the hull.
func buildWarehouseCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &storageCmd.RunWarehouseCommand{
		ShipSymbol:     cfg.RequiredNonEmptyString("ship_symbol"),
		WaypointSymbol: cfg.RequiredNonEmptyString("waypoint_symbol"),
		PlayerID:       shared.MustNewPlayerID(playerID),
		ContainerID:    containerID,
		OperationID:    containerID,
		SupportedGoods: cfg.RequiredStringSlice("supported_goods"),
	}
}

// buildTradeRouteCoordinatorCommand rebuilds the single-hull arbitrage circuit
// command from a persisted launch config so restart recovery can resume a RUNNING
// trade_route container. ContainerID is taken from the recovery-supplied
// containerID (the persisted row's ID), mirroring contract_workflow, so the operation
// context and the runner's ship claim stay pinned to the same container across a
// restart. MaxVisits defaults to 0 (the coordinator's own default-50 safety bound).
// WorkingCapitalReserve defaults to 0 (the coordinator's own defaultWorkingCapitalReserve
// floor, sp-bp6f) but is exposed as a launch-config knob so a captain can raise the
// reserve for a specific circuit without a redeploy. TargetDest defaults to "" (the
// undirected auto-scan) but is exposed as a launch-config knob so a captain can pin
// the circuit to a specific lane via --dest; an empty value preserves the
// original auto-selected behavior unchanged across a recovery rebuild.
func buildTradeRouteCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.RunTradeRouteCoordinatorCommand{
		ShipSymbol:            cfg.RequiredString("ship_symbol"),
		SystemSymbol:          cfg.RequiredString("system_symbol"),
		PlayerID:              playerID,
		ContainerID:           containerID,
		MaxVisits:             cfg.OptionalInt("max_visits", 0),
		WorkingCapitalReserve: cfg.OptionalInt("working_capital_reserve", 0),
		TargetDest:            cfg.OptionalString("dest_waypoint"),
	}
}

// buildArbCoordinatorCommand rebuilds the one-shot guarded arb command from a
// persisted launch config so restart recovery can resume a RUNNING arb_run container.
// ContainerID is taken from the recovery-supplied containerID (the persisted row's ID),
// mirroring trade_route so the operation context and the runner's ship claim stay pinned
// across a restart. good/buy_at/sell_at are required (the lane the captain directed);
// max_units/max_spend/min_margin/working_capital_reserve default to 0 (the coordinator's
// own "0 → unset/default" semantics for each guard), and are persisted as launch-config
// knobs so a recovery rebuild resumes the same directed run with the same caps.
//
// prior_attempt_cost is RUNTIME progress, not a launch knob: a fresh run
// persists it into this same config the moment its buy succeeds, so a daemon-restart
// rebuild reloads the already-incurred cost and the resumed run reports honest P&L
// (RULINGS #2) rather than starting its accounting at TotalCost=0. Absent (a run that
// crashed before buying, or never persisted) it defaults to 0 — the honest fail-open
// floor, never an over-count.
func buildArbCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.RunArbCoordinatorCommand{
		ShipSymbol:            cfg.RequiredString("ship_symbol"),
		Good:                  cfg.RequiredNonEmptyString("good"),
		BuyAt:                 cfg.RequiredNonEmptyString("buy_at"),
		SellAt:                cfg.RequiredNonEmptyString("sell_at"),
		PlayerID:              playerID,
		ContainerID:           containerID,
		MaxUnits:              cfg.OptionalInt("max_units", 0),
		MaxSpend:              cfg.OptionalInt("max_spend", 0),
		MinMargin:             cfg.OptionalInt("min_margin", 0),
		WorkingCapitalReserve: cfg.OptionalInt("working_capital_reserve", 0),
		PriorAttemptCost:      cfg.OptionalInt("prior_attempt_cost", 0),
		// Per-tranche sell floor fraction. Absent → 0 → the coordinator's
		// own defaultArbSellFloorFraction (0.80), so a captain arb-run with no knob
		// set is still floored; idle-arb writes the live 80% knob here.
		SellFloorFraction: cfg.OptionalFloat("sell_floor_fraction", 0),
	}
}

// buildTourCoordinatorCommand rebuilds the one-shot guarded tour command (sp-1ek0) from
// a persisted launch config so restart recovery can resume a RUNNING tour_run container.
// ContainerID comes from the recovery-supplied containerID (the persisted row's ID),
// mirroring arb_run/trade_route so the operation context and the runner's ship claim stay
// pinned across a restart. ship_symbol is required; the guard knobs default to 0 (the
// coordinator's own "0 → default" semantics: max_hops→6, max_spend→the capital budget,
// replan_limit→2, working_capital_reserve→150k, the non-contract floor per sp-q8bon).
// iterations drives the CONTINUOUS-tour
// loop: -1 = tour until margins die, N>0 = N tours, 0/absent → the one-tour
// default (unchanged one-shot behavior). The coordinator owns this loop
// (CoordinatorOwnsIterations); the container still runs Handle() once. A restart
// re-plans from current position/cargo — cargo-aware, never a blind re-buy — and a
// persisted iterations survives the rebuild so a -1 run resumes continuous.
func buildTourCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.RunTourCoordinatorCommand{
		ShipSymbol:  cfg.RequiredString("ship_symbol"),
		PlayerID:    playerID,
		ContainerID: containerID,
		AgentSymbol: cfg.OptionalString("agent_symbol"),
		MaxHops:     cfg.OptionalInt("max_hops", 0),
		// Reload the per-tour distinct-system cap StartTourRun stamped from
		// [trade_fleet].max_tour_systems (the read side of the launch/rebuild boundary,
		// mirroring reposition_jump_bound). Absent → 0 → the solver's MAX_TOUR_SYSTEMS
		// default (2), so a tour launched without the knob is byte-identical to today;
		// a positive value sweeps tour length.
		MaxTourSystems: cfg.OptionalInt("max_tour_systems", 0),
		// Config plumbing: reload the closed-circuit arming flag StartTourRun
		// stamped from [trade_fleet].closed_tours (the read side of the launch/rebuild
		// boundary, mirroring max_tour_systems). OptionalBool yields false for an absent
		// key → cmd.ClosedTours=false → im74's cons.Closed reads an OPEN tour,
		// byte-identical to today; true arms the return-to-anchor closed circuit.
		ClosedTours:               cfg.OptionalBool("closed_tours"),
		MaxSpend:                  int64(cfg.OptionalInt("max_spend", 0)),
		MinMargin:                 cfg.OptionalInt("min_margin", 0),
		LookbackMinMargin:         cfg.OptionalInt("lookback_min_margin", 0),
		LookbackSourceCallCredits: cfg.OptionalInt("lookback_source_call_credits", 0),
		LookbackItemCallCredits:   cfg.OptionalInt("lookback_item_call_credits", 0),
		ReplanLimit:               cfg.OptionalInt("replan_limit", 0),
		// sp-ggk2 RULINGS #4: the reserve is a money guard — a PRESENT-but-unparseable
		// value fails the build (fail closed), never a silent 0 → 50k floor. An absent key
		// still defers to the coordinator's own default (0 → defaultWorkingCapitalReserve),
		// so a captain CLI tour with no --reserve is unchanged.
		WorkingCapitalReserve: int64(cfg.PresentOrFailInt("working_capital_reserve", 0)),
		Iterations:            cfg.OptionalInt("iterations", 0),
		// Reposition-on-margins-death knobs (sp-zhii). reposition_disabled defaults to
		// false → the feature is ON for continuous runs (the captain filed sp-zhii to end
		// the whack-a-mole); the floor/K default to 0 → the coordinator's own
		// reposition{MinMargin,MaxCandidates}Default. reposition_in_progress / _target_*
		// are RUNTIME state the coordinator persists mid-jump (RULINGS #2), reloaded here
		// so a restart resumes the jump instead of re-planning at an intermediate hop.
		RepositionDisabled: cfg.OptionalBool("reposition_disabled"),
		// sp-e8d92 FIRST REFUSAL. The two deadlines are restart-durable: a run rebuilt mid-offer must
		// honour the SAME deadline rather than open a fresh one (an offer that renewed itself across
		// restarts is exactly the unexpiring hold that would strand a trade hull), and must remember that
		// this hull's last offer went unclaimed rather than immediately pay another window (RULINGS #2).
		// An absent or unparseable value reads as the zero time = no offer, which is today's behaviour.
		RelocationOfferWindowSeconds:  cfg.OptionalInt("relocation_offer_window_secs", 0),
		RelocationOfferMinHulls:       cfg.OptionalInt("relocation_offer_min_hulls", 0),
		RelocationOfferBackoffMinutes: cfg.OptionalInt("relocation_offer_backoff_minutes", 0),
		RelocationOfferUntil:          parseRelocationOfferInstant(cfg.OptionalString("relocation_offer_until")),
		RelocationOfferBackoffUntil:   parseRelocationOfferInstant(cfg.OptionalString("relocation_offer_backoff_until")),
		RepositionMinMargin:           cfg.OptionalInt("reposition_min_margin", 0),
		RepositionMaxCandidates:       cfg.OptionalInt("reposition_max_candidates", 0),
		// The stored-adjacency reposition jump bound (0/absent → the coordinator's own
		// default 12). This is the READ side, paired with the container_ops_tour.go WRITE side —
		// together they make the bound survive the launch-config → rebuild round-trip a recovery
		// restart runs.
		RepositionJumpBound:      cfg.OptionalInt("reposition_jump_bound", 0),
		RepositionInProgress:     cfg.OptionalBool("reposition_in_progress"),
		RepositionTargetSystem:   cfg.OptionalString("reposition_target_system"),
		RepositionTargetWaypoint: cfg.OptionalString("reposition_target_waypoint"),
		// The in-flight SELL leg: the READ side paired with the container_ops_tour.go WRITE side,
		// so the sink a hull was already carrying cargo toward survives the rebuild. Absent →
		// no leg in flight → the run plans from the hull's current position exactly as before.
		TourLegWaypoint: cfg.OptionalString(tourLegWaypointConfigKey),
		TourLegGoods:    cfg.OptionalString(tourLegGoodsConfigKey),
		// sp-686e: stranded-hull detector threshold from [trade_fleet]; 0/absent → the
		// coordinator's own default (3, resolveStrandedThreshold).
		StrandedConsecutiveThreshold: cfg.OptionalInt("stranded_consecutive_threshold", 0),
		// sp-z7ng placement/relocation scoring loop. OptionalBool/OptionalInt
		// yield zero values for absent keys — the exact default-OFF dormancy mechanism the
		// Reposition* knobs use: placement_disabled absent ⇒ false ⇒ ARMED (RULINGS #22); the legacy static-floor
		// reposition runs, byte-identical to today; the window/floor/shortlist default to 0 ⇒ the
		// coordinator's own placement*Default. Every existing container and recovery rebuild takes
		// branch runs only under an explicit placement_disabled: true or an unreadable β.
		PlacementDisabled:          cfg.OptionalBool("placement_disabled"),
		PlacementBetaWindowMinutes: cfg.OptionalInt("placement_beta_window_minutes", 0),
		PlacementParkFloorPct:      cfg.OptionalInt("placement_park_floor_pct", 0),
		PlacementShortlistTopN:     cfg.OptionalInt("placement_shortlist_top_n", 0),
		PlacementHorizonMinutes:    cfg.OptionalInt("placement_horizon_minutes", 0),
		// MVT trade loop: mvt_loop absent ⇒ false ⇒ legacy path; knobs default to spec values.
		MVTLoop:                  cfg.OptionalBool("mvt_loop"),
		YieldWindowSells:         cfg.OptionalInt("yield_window_sells", tradingCmd.DefaultYieldWindowSells),
		YieldMinSells:            cfg.OptionalInt("yield_min_sells", tradingCmd.DefaultYieldMinSells),
		ClaimReachHops:           cfg.OptionalInt("claim_reach_hops", tradingCmd.DefaultClaimReachHops),
		ClaimReachMaxHops:        cfg.OptionalInt("claim_reach_max_hops", tradingCmd.DefaultClaimReachMaxHops),
		SpecialistCadenceMinutes: cfg.OptionalInt("specialist_cadence_minutes", tradingCmd.DefaultSpecialistCadenceMinutes),
		// Below this span the fleet rate stands in for the hull's own; 0/absent ⇒ the spec default.
		YieldRateSpanFloorMinutes: cfg.OptionalInt("yield_rate_span_floor_minutes", tradingCmd.DefaultYieldRateSpanFloorMinutes),
		// sp-uf64 reposition reach (always-broaden discovery + deadhead-decay ranking + anti-herd).
		// OptionalBool/OptionalInt yield zero values for absent keys — the exact default-OFF dormancy
		// the Placement*/Reposition* knobs use: reposition_reach_enabled absent ⇒ false ⇒ the legacy
		// 1-hop-first reposition runs, byte-identical to today; the decay/cap default to 0 ⇒ the
		// coordinator's own repositionReach*Default (85, 5). Every existing container and recovery
		// rebuild takes the legacy branch until a captain explicitly sets reposition_reach_enabled: true.
		RepositionReachEnabled:           cfg.OptionalBool("reposition_reach_enabled"),
		RepositionReachHopDecayPct:       cfg.OptionalInt("reposition_reach_hop_decay_pct", 0),
		RepositionReachMaxHullsPerSystem: cfg.OptionalInt("reposition_reach_max_hulls_per_system", 0),
		// Own-trade recency de-ranking, ARMED (RULINGS #22): OptionalInt 0 ⇒ the coordinator's own
		// ownTrade*Default, and the kill switch absent ⇒ live, including on a recovery rebuild.
		OwnTradePenaltyPct:      cfg.OptionalInt("reposition_own_trade_penalty_pct", 0),
		OwnTradeColdMinutes:     cfg.OptionalInt("reposition_own_trade_cold_minutes", 0),
		OwnTradePenaltyDisabled: cfg.OptionalBool("reposition_own_trade_penalty_disabled"),
		// epic sp-fguo Part 2 rate-floor early-reposition. OptionalBool/OptionalInt yield zero values
		// for absent keys — the exact default-OFF dormancy the Reposition*/Placement* knobs use:
		// reposition_rate_floor_enabled absent ⇒ false ⇒ the trigger is dormant, byte-identical to
		// today; pct/improvement/dwell default to 0 ⇒ the coordinator's own repositionRateFloor*Default
		// (40, 200, 15). Every existing container and recovery rebuild takes the dormant branch until a
		// captain explicitly sets reposition_rate_floor_enabled: true.
		RepositionRateFloorEnabled:        cfg.OptionalBool("reposition_rate_floor_enabled"),
		RepositionRateFloorPct:            cfg.OptionalInt("reposition_rate_floor_pct", 0),
		RepositionRateFloorImprovementPct: cfg.OptionalInt("reposition_rate_floor_improvement_pct", 0),
		RepositionRateFloorDwellMinutes:   cfg.OptionalInt("reposition_rate_floor_dwell_minutes", 0),
		// Candidate widening (the #1 fleet-$/hr lever): reload the gate-hop radius +
		// shortlist bound StartTourRun stamped from [trade_fleet].candidate_hop_depth /
		// candidate_shortlist_top_n (the read side of the launch/rebuild boundary, mirroring
		// max_tour_systems). OptionalInt yields 0 for an absent key → the coordinator's resolveCandidate*
		// helpers floor candidate_hop_depth → 1 (the exact 1-hop set, byte-identical to today) and
		// resolve candidate_shortlist_top_n → 6; EFFECT is additionally arming-gated by MaxTourSystems > 2
		// (effectiveCandidateHopDepth), so a positive depth alone never widens. Every existing container
		// and recovery rebuild stays 1-hop until a captain explicitly sets candidate_hop_depth: 2 with the
		// solver clamp already lifted.
		CandidateHopDepth:      cfg.OptionalInt("candidate_hop_depth", 0),
		CandidateShortlistTopN: cfg.OptionalInt("candidate_shortlist_top_n", 0),
		// The read side of the recovery-externality weight's launch/rebuild
		// boundary. OptionalFloat yields 0 for an absent key → the solver charges nothing and
		// ranks on raw margin, byte-identical to today, so every container launched before
		// arming (and every recovery rebuild of one) stays unarmed.
		ExternalityWeight:         cfg.OptionalFloat("externality_weight", 0),
		TourNeighborsDurableFirst: cfg.OptionalBool("tour_neighbors_durable_first"),
	}
}

// buildStockerCoordinatorCommand rebuilds the stocker loop command from a
// persisted launch config so restart recovery can resume a RUNNING stocker container.
// ContainerID comes from the recovery-supplied containerID (the persisted row's ID),
// mirroring tour_run so the operation context and the runner's ship claim stay pinned
// across a restart. ship_symbol + warehouse_waypoint are required (the dedicated hull and
// the deposit anchor); the caps default to 0 (the coordinator's own "0 → default"
// semantics: budget_per_leg → no cap, working_capital_reserve → 150k (the non-contract
// floor, sp-q8bon), iterations → one round-trip, max_market_age_minutes → 75,
// target_per_good → the miner's demand). The
// coordinator owns the round-trip loop (CoordinatorOwnsIterations); the container runs
// Handle() once. A restart re-plans from the hull's current cargo — a laden hull resumes
// deposit-first, never a blind re-buy (RULINGS #2).
func buildStockerCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.RunStockerCoordinatorCommand{
		ShipSymbol:            cfg.RequiredNonEmptyString("ship_symbol"),
		WarehouseWaypoint:     cfg.RequiredNonEmptyString("warehouse_waypoint"),
		PlayerID:              playerID,
		ContainerID:           containerID,
		AgentSymbol:           cfg.OptionalString("agent_symbol"),
		BudgetPerLeg:          cfg.OptionalInt("budget_per_leg", 0),
		WorkingCapitalReserve: int64(cfg.OptionalInt("working_capital_reserve", 0)),
		Iterations:            cfg.OptionalInt("iterations", 0),
		MaxMarketAgeMinutes:   cfg.OptionalInt("max_market_age_minutes", 0),
		TargetPerGood:         cfg.OptionalInt("target_per_good", 0),
		// sp-k1ka standing intent + its cadence/hysteresis knobs round-trip through the
		// launch config so a restart RE-ADOPTS the stocker STANDING (RULINGS #2): recovery
		// rebuilds this exact command from the persisted config, so the resumed loop parks-
		// and-re-stages exactly as before — no manual relaunch.
		Standing:         cfg.OptionalBool("standing"),
		TickSeconds:      cfg.OptionalInt("tick_seconds", 0),
		RefillHysteresis: cfg.OptionalInt("refill_hysteresis", 0),
		// sp-k2xav / RULINGS #14: re-adopt the contract depot's INTRA-system sourcing across a
		// restart. Absent (every generic/pre-existing stocker container) => false => cross-system,
		// byte-identical.
		HomeSystemOnly: cfg.OptionalBool("home_system_only"),
	}
}

// parseRelocationOfferInstant reads a persisted sp-e8d92 offer deadline, or the zero time when the value
// is absent, empty, or unparseable. Unparseable reads as NO OFFER (fail-safe): ignoring a real offer
// costs one missed relocation, whereas honouring a corrupt one holds a hull out of touring.
func parseRelocationOfferInstant(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return at.UTC()
}

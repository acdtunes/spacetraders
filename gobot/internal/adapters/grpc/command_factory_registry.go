package grpc

import (
	"fmt"
	"time"
)

type ContainerSpec struct {
	CommandType string
	IsWorker    bool
	// CoordinatorOwnsIterations declares the type's iteration model (sp-7yej
	// invariant 3): true means the command's handler owns the WHOLE run
	// internally (trade-route's visit budget, scout_tour's tour count, arb's
	// one-shot leg) and the container wrapper must run exactly ONE iteration —
	// re-entering the handler would double-loop the budget (the scout N×N
	// defect) or re-run a non-resumable task. False is the runner-loop model:
	// the container's maxIterations drives repeated Handle() calls, each one
	// unit of work (goods_factory cycles). recoverContainer consults this so a
	// restart rebuild can never hand a coordinator-owned budget to the runner
	// loop. See containerSpecList for the full per-type semantics table.
	CoordinatorOwnsIterations bool
	build                     func(cfg *configReader, playerID int, containerID string) interface{}
}

func (spec ContainerSpec) BuildCommand(config map[string]interface{}, playerID int, containerID string) (interface{}, error) {
	if spec.build == nil {
		return nil, fmt.Errorf("no command builder for command type '%s'", spec.CommandType)
	}
	cfg := newConfigReader(config)
	cmd := spec.build(cfg, playerID, containerID)
	if err := cfg.Err(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// containerSpecList is the registry AND the container lifecycle contract's per-type semantics table.
// Every container type the daemon creates MUST appear here — a type absent from it is marked FAILED
// at restart recovery and its in-flight work abandoned. A type deliberately removed goes in
// retiredCommandTypes instead, which skips its persisted rows cleanly rather than alarming.
//
// ITERATION SEMANTICS (invariant 3) — one operator-facing meaning everywhere:
//
//	-1 = infinite (until stopped); N>0 = exactly N units of the type's own work unit;
//	 0 = the type's documented default, NEVER "zero work".
//
// Who loops is declared per type via CoordinatorOwnsIterations. A type whose coordinator owns the
// loop re-adopts itself across a restart; a WORKER (one carrying coordinator_id) is not recovered
// standalone — markWorkerInterrupted preserves its claim and the parent re-dispatches it.
//
// HONEST COMPLETION (invariant 2): any coordinator whose run can end holding cargo bought that run,
// or with its task incomplete, threads that through its response's common.CompletionReporter, and
// finishCleanExit refuses success=true. New cargo-leg coordinators must adopt that shape and funnel
// every laden exit through one epilogue (run_trade_route_coordinator.go's runCircuit is the
// reference).
func containerSpecList() []ContainerSpec {
	return []ContainerSpec{
		{CommandType: "scout_tour", build: buildScoutTourCommand, CoordinatorOwnsIterations: true},
		// probe_sensing_coordinator is the standing sensing engine. The retired freshness sizer,
		// frontier expansion, scout-post, shipyard-backfill and scout-reposition types are
		// deliberately ABSENT — see retiredCommandTypes.
		{CommandType: "probe_sensing_coordinator", build: buildProbeSensingCoordinatorCommand},
		{CommandType: "contract_workflow", build: buildContractWorkflowCommand},
		{CommandType: "contract_fleet_coordinator", build: buildContractFleetCoordinatorCommand},
		// trade_fleet_coordinator (sp-1278): a standing coordinator that loops forever
		// inside one Handle() call, so — like scout_post/contract_fleet — it is NOT a
		// CoordinatorOwnsIterations type; the container-level iteration budget (-1) is
		// irrelevant because Handle() never returns.
		{CommandType: "trade_fleet_coordinator", build: buildTradeFleetCoordinatorCommand},
		// worker_ferry: a one-shot cross-system relay worker (twin of scout_reposition)
		// that moves a hull to a destination waypoint. Its former managing coordinator (the
		// worker_rebalancer_coordinator) was retired with the factory ops; the ferry
		// primitive is retained for the daemon's persist/start dispatch + container recovery. It
		// wraps exactly ONE iteration (CoordinatorOwnsIterations).
		{CommandType: "worker_ferry", build: buildWorkerFerryCommand, CoordinatorOwnsIterations: true},
		// cargo_liquidation: the contract fleet coordinator's one-shot
		// self-clearing worker for a parked-with-cargo hull (twin of worker_ferry). The
		// coordinator owns re-dispatch, so the container wraps exactly ONE iteration
		// (CoordinatorOwnsIterations).
		{CommandType: "cargo_liquidation", build: buildCargoLiquidationCommand, CoordinatorOwnsIterations: true},
		{CommandType: "purchase_ship", build: buildPurchaseShipCommand},
		{CommandType: "batch_purchase_ships", build: buildBatchPurchaseShipsCommand},
		// construction_coordinator: the standing construction-supply drain. Like
		// trade_fleet/siting it loops forever inside one Handle(), so it is NOT a
		// CoordinatorOwnsIterations type; the container-level budget (-1) is irrelevant.
		// Registering it here is what makes a launched or restart-recovered drain runnable.
		{CommandType: "construction_coordinator", build: buildConstructionCoordinatorCommand},
		// fleet_growth: the standing fleet-growth coordinator — the fleet's ONLY heavy buyer, and
		// one of the two readers of the heavy/probe wave. Like trade_fleet/siting it loops
		// forever inside one Handle(), so it is NOT a CoordinatorOwnsIterations type.
		{CommandType: "fleet_growth", build: buildFleetGrowthCommand},
		// contract_scaler: the standing dedicated contract auto-scaler. Like fleet_growth/siting it
		// loops forever inside one Handle(), so it is NOT a CoordinatorOwnsIterations type; the
		// container-level budget (-1) is irrelevant. Registering it here is what makes an ARMED-launch or
		// restart-recovered scaler runnable — launch itself stays gated behind the bootstrap early-scaling
		// arm (default-off), never boot-standing.
		{CommandType: "contract_scaler", build: buildContractScalerCommand},
		// opportunity_relocator's restart matters more than most: its persisted relocation intents are
		// re-derived on the first tick, so a rebuild that never runs leaves one unfinished.
		{CommandType: "opportunity_relocator", build: buildOpportunityRelocatorCommand},
		// bootstrap is the one standing coordinator whose Handle() RETURNS — at the terminal EXPANSION
		// exit — so its container completes on the response's RunTerminal report, and a non-terminal
		// return is paced at the bootstrap tick rather than re-entered at loop speed.
		{CommandType: "bootstrap", build: buildBootstrapCommand},
		// auto_outfit_coordinator (sp-buyd): the standing guarded auto-outfit coordinator.
		// Like fleet_growth/siting it loops forever inside one Handle(), so it is NOT a
		// CoordinatorOwnsIterations type; the container-level budget (-1) is irrelevant.
		// Registering it here is what makes a launched or restart-recovered coordinator
		// runnable — launch itself stays EXPLICIT (never boot-standing, deploy-inert).
		{CommandType: "auto_outfit_coordinator", build: buildAutoOutfitCoordinatorCommand},
		{CommandType: "gas_coordinator", build: buildGasCoordinatorCommand},
		{CommandType: "warehouse", build: buildWarehouseCommand},
		{CommandType: "trade_route", build: buildTradeRouteCoordinatorCommand, CoordinatorOwnsIterations: true},
		{CommandType: "arb_run", build: buildArbCoordinatorCommand, CoordinatorOwnsIterations: true},
		// longhaul_arb_coordinator (sp-mepj): the standing long-haul arb fleet coordinator.
		// Like trade_fleet_coordinator it loops forever inside one Handle(), so it is NOT a
		// CoordinatorOwnsIterations type; the container-level budget (-1) is irrelevant.
		// Registering it here is what makes an armed-launch or restart-recovered coordinator
		// runnable — the launch itself is the operator's `workflow long-haul-coordinator` arm.
		{CommandType: "longhaul_arb_coordinator", build: buildLongHaulArbFleetCoordinatorCommand},
		// longhaul_arb (sp-mepj): the per-hull long-haul WORKER the coordinator spawns. Like
		// arb_run/trade_route its handler owns the whole run internally (its continuous
		// discover->buy->sell->backhaul episode loop), so the container wraps exactly ONE
		// iteration (CoordinatorOwnsIterations) — the runner must not re-enter and double-loop.
		{CommandType: "longhaul_arb", build: buildLongHaulArbWorkerCommand, CoordinatorOwnsIterations: true},
		{CommandType: "tour_run", build: buildTourCoordinatorCommand, CoordinatorOwnsIterations: true},
		{CommandType: "stocker", build: buildStockerCoordinatorCommand, CoordinatorOwnsIterations: true},
		// One-shot ship operations (sp-7yej invariant 4). Each rebuilds trivially
		// from its persisted config and is safe to re-run: navigate resumes/waits
		// out the live transit via RouteExecutor, dock/orbit/refuel no-op when
		// already done, and a re-run jettison of already-jettisoned cargo fails
		// honestly rather than silently.
		{CommandType: "navigate_ship", build: buildNavigateShipCommand, CoordinatorOwnsIterations: true},
		{CommandType: "route_ship", build: buildRouteShipCommand, CoordinatorOwnsIterations: true},
		{CommandType: "warp_ship", build: buildWarpShipCommand, CoordinatorOwnsIterations: true},
		{CommandType: "dock_ship", build: buildDockShipCommand, CoordinatorOwnsIterations: true},
		{CommandType: "orbit_ship", build: buildOrbitShipCommand, CoordinatorOwnsIterations: true},
		{CommandType: "refuel_ship", build: buildRefuelShipCommand, CoordinatorOwnsIterations: true},
		{CommandType: "jettison_cargo", build: buildJettisonCargoCommand, CoordinatorOwnsIterations: true},
		{CommandType: "scout_fleet_assignment", build: buildScoutFleetAssignmentCommand, CoordinatorOwnsIterations: true},
		{CommandType: "gas_siphon_worker", IsWorker: true},
		{CommandType: "storage_ship", IsWorker: true},
	}
}

func (s *DaemonServer) registerContainerSpecs() {
	for _, spec := range containerSpecList() {
		s.containerSpecs[spec.CommandType] = spec
	}
}

func (s *DaemonServer) buildCommandForType(commandType string, config map[string]interface{}, playerID int, containerID string) (interface{}, error) {
	spec, exists := s.containerSpecs[commandType]
	if !exists {
		return nil, fmt.Errorf("unknown command type '%s'", commandType)
	}
	// Creation and restart recovery both funnel through here, so clearing and re-injecting a
	// coordinator's knobs on EVERY build is what makes config.yaml the one source of truth: a
	// config edit + restart retunes even a recovered coordinator, and no persisted copy can
	// shadow the live value. Every resolve below follows that discipline.
	if commandType == "contract_fleet_coordinator" {
		s.resolveIdleArbConfig(config)
		// Same live-config discipline for the parked-hull auto-liquidation knobs
		// (enable/disable + min-jettison floor). Cleared and re-injected from config.yaml
		// on every build so a retune reaches a recovered coordinator.
		s.resolveAutoLiquidationConfig(config)
	}
	// sp-1278: same live-config discipline for the trade-fleet coordinator. Its
	// [trade_fleet] knobs (enabled/cooldown/max-concurrent/per-tour caps) are cleared
	// and re-injected from the boot-loaded config.yaml on every build — creation and
	// recovery alike — so a config edit + restart retunes a recovered coordinator and
	// no persisted copy can shadow the live value.
	if commandType == "trade_fleet_coordinator" {
		s.resolveTradeFleetConfig(config)
	}
	// The growth coordinator's launch keys are CLEARED on every build — creation and recovery
	// alike — so each build starts from the coordinator's own documented defaults and no persisted
	// copy can shadow them. Its operator surface is the three LIVE bare knobs, which this does not
	// touch.
	if commandType == "fleet_growth" {
		s.resolveFleetGrowthConfig(config)
	}
	// Same live-config discipline for the captain bootstrap coordinator. Its [bootstrap]
	// knobs are cleared and re-injected from the boot-loaded config.yaml on every build — creation
	// and recovery alike — so a config edit + restart retunes a recovered coordinator and no
	// persisted copy can shadow the live value (the sp-ts82 pattern).
	if commandType == "bootstrap" {
		s.resolveBootstrapConfig(config)
	}
	// Same live-config discipline for the scouting subsystem's tour-start
	// phase jitter ceiling. The [scouting] knob is cleared and re-injected from the
	// boot-loaded config.yaml on every build — creation and recovery alike — for both
	// scout_tour and scout_post_coordinator, so a config edit + restart retunes a
	// recovered scout and no persisted copy can shadow the live value.
	if commandType == "scout_tour" {
		s.resolveScoutingConfig(config)
	}
	// probe_sensing_coordinator resolves TWO things: the [sensing] knobs (the goods whitelist — a
	// string the int-only tune registry cannot carry), and a tuned pressure_half_life_secs pushed
	// into the API client's limiter-pressure EWMA so it survives a bounce. The half-life is
	// process-global client state, hence the narrow type assertion rather than widening the port.
	if commandType == "probe_sensing_coordinator" {
		s.resolveSensingConfig(config)
		if halfLife, ok := intValue(config["pressure_half_life_secs"]); ok && halfLife > 0 {
			if setter, ok := s.apiClient.(interface{ SetLimiterPressureHalfLife(time.Duration) }); ok {
				setter.SetLimiterPressureHalfLife(time.Duration(halfLife) * time.Second)
			}
		}
	}
	return spec.BuildCommand(config, playerID, containerID)
}

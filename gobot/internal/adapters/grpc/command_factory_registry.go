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

// containerSpecList is the registry AND the container lifecycle contract's
// per-type semantics table (sp-7yej invariants 3+4). Every container type the
// daemon creates MUST appear here — a type absent from this list is marked
// FAILED at restart recovery ("unknown command type") and its in-flight work
// is abandoned.
//
// ITERATION SEMANTICS (invariant 3) — one operator-facing meaning everywhere:
//
//	-1  = infinite: run until stopped/margin-death.
//	N>0 = exactly N units of the type's own work unit (see table).
//	 0  = the type's documented default — NEVER "zero work". (scout_tour: 1
//	      tour, normalized in buildScoutTourCommand; goods_factory: 1 cycle,
//	      cfg default; trade_route max_visits: the coordinator's default 50.)
//
// Who loops is declared per type via CoordinatorOwnsIterations:
//
//	type                        unit of work      loop owner    restart behavior
//	--------------------------  ----------------  ------------  ---------------------------------
//	scout_tour                  one full tour     coordinator   re-adopts; finite tour re-runs
//	                                                            from scratch (progress not
//	                                                            persisted), ∞ resumes; a
//	                                                            coordinator-spawned tour (has
//	                                                            coordinator_id) is skipped and
//	                                                            respawned by scout_post_coordinator
//	scout_post_coordinator      ∞ internal loop   coordinator   re-adopts; reloads posts +
//	                                                            assignments, respawns tours (cxpq),
//	                                                            re-dispatches interrupted relays (s232)
//	scout_reposition            one cross-gate    coordinator   worker (coordinator_id): skipped +
//	                            relay             (parent)      markWorkerInterrupted preserves the
//	                                                            claim; scout_post_coordinator re-
//	                                                            dispatches from current position —
//	                                                            travel() re-plans the hops (s232)
//	worker_rebalancer_          ∞ internal loop   coordinator   re-adopts; all state DB-derived
//	coordinator                                                 (ship + container rows), so a fresh
//	                                                            handler ferries identically (f5pr)
//	worker_ferry                one cross-system  coordinator   worker (coordinator_id): skipped +
//	                            relay             (parent)      markWorkerInterrupted preserves the
//	                                                            claim; worker_rebalancer_coordinator
//	                                                            reclaims it (arrival or interruption),
//	                                                            re-plans from current position (f5pr)
//	contract_workflow           one contract      coordinator   re-adopts standalone; worker
//	                                                            (coordinator_id) waits for parent
//	contract_fleet_coordinator  ∞ internal loop   coordinator   re-adopts
//	purchase_ship               one purchase      coordinator   re-adopts (idempotence at API)
//	batch_purchase_ships        one batch         coordinator   re-adopts
//	goods_factory_coordinator   one cycle         RUNNER        re-adopts with persisted budget
//	                                                            (sp-perx); -1 uses 2q2o backoff
//	manufacturing_coordinator   ∞ internal loop   coordinator   re-adopts
//	gas_coordinator             ∞ internal loop   coordinator   re-adopts
//	warehouse                   passive hold      coordinator   re-adopts; op row +
//	                            (blocks on                      hull cargo rebuilt by
//	                            shutdown)                       StorageRecoveryService
//	                                                            from live ship state (dchv)
//	trade_route                 visit budget      coordinator   re-adopts; laden exit is a
//	                            (max_visits)                    FAILURE (sp-1hj5, invariant 2)
//	tour_run                    tour count        coordinator   re-adopts; re-plans from current
//	                            (iterations:      (owns loop)   position/cargo. -1 = continuous
//	                            -1/N/0→1)                       (re-plan+fly until margins die/
//	                                                            starvation); laden exit is a
//	                                                            FAILURE (sp-m5kv, invariant 2)
//	arb_run                     one directed leg  coordinator   re-adopts; resumes past the buy
//	                                                            (sp-5nqx), strand = failure
//	stocker                     round-trip        coordinator   re-adopts; a laden hull resumes
//	                            (iterations:      (owns loop)   deposit-first. -1 = continuous
//	                            -1/N/0→1)                       (fill until nothing left to
//	                                                            stock/starvation); undeposited
//	                                                            exit is a FAILURE (sp-zdwg,
//	                                                            invariant 2)
//	navigate_ship               one route         coordinator   re-adopts; RouteExecutor waits
//	                                                            out / resumes the live transit
//	dock_ship / orbit_ship /    one ship op       coordinator   re-adopts; the op is idempotent
//	refuel_ship                                                 (already-done → no-op)
//	jettison_cargo              one jettison      coordinator   re-adopts; an already-jettisoned
//	                                                            load fails HONESTLY (no re-buy)
//	scout_fleet_assignment      one VRP pass      coordinator   re-adopts; re-runs the assignment
//	workers (manufacturing_     one task          coordinator   NOT recovered standalone —
//	task_worker, gas_siphon_                      (parent)      markWorkerInterrupted preserves
//	worker, storage_ship)                                       the claim; parent re-adopts (tgp5)
//
// HONEST COMPLETION (invariant 2): any coordinator whose run can end holding
// cargo bought that run, or with its task incomplete, threads that through its
// response's common.CompletionReporter — the runner's finishCleanExit refuses
// success=true (trade_route adopted; arb_run reports via non-nil error, valid
// because its fixed lane resumes across retries). New cargo-leg coordinators
// MUST adopt one of those two shapes and funnel every laden exit through a
// single epilogue (invariant 1's finish-current-leg rule; see
// run_trade_route_coordinator.go's runCircuit for the reference pattern).
func containerSpecList() []ContainerSpec {
	return []ContainerSpec{
		{CommandType: "scout_tour", build: buildScoutTourCommand, CoordinatorOwnsIterations: true},
		{CommandType: "scout_post_coordinator", build: buildScoutPostCoordinatorCommand},
		// probe_sensing_coordinator: the standing sensing engine (successor of the retired
		// market-freshness sizer + frontier expansion pair — their types are deliberately
		// ABSENT from this list so a still-RUNNING legacy container fails closed at restart
		// recovery). Like scout_post/contract_fleet it loops forever inside one Handle(), so
		// it is NOT a CoordinatorOwnsIterations type; the container-level budget (-1) is
		// irrelevant.
		{CommandType: "probe_sensing_coordinator", build: buildProbeSensingCoordinatorCommand},
		{CommandType: "shipyard_backfill_coordinator", build: buildShipyardBackfillCoordinatorCommand},
		{CommandType: "scout_reposition", build: buildScoutRepositionCommand, CoordinatorOwnsIterations: true},
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
		// fleet_autosizer (sp-1txd): the standing fleet capacity autosizer. Like
		// trade_fleet/siting it loops forever inside one Handle(), so it is NOT a
		// CoordinatorOwnsIterations type; the container-level budget (-1) is irrelevant.
		{CommandType: "fleet_autosizer", build: buildFleetAutosizerCommand},
		// contract_scaler: the standing dedicated contract auto-scaler. Like fleet_autosizer/siting it
		// loops forever inside one Handle(), so it is NOT a CoordinatorOwnsIterations type; the
		// container-level budget (-1) is irrelevant. Registering it here is what makes an ARMED-launch or
		// restart-recovered scaler runnable — launch itself stays gated behind the bootstrap early-scaling
		// arm (default-off), never boot-standing.
		{CommandType: "contract_scaler", build: buildContractScalerCommand},
		// opportunity_relocator (sp-zvywu Part 2): the standing opportunity relocator. Like
		// fleet_autosizer/contract_scaler it loops forever inside one Handle(), so it is NOT a
		// CoordinatorOwnsIterations type; the container-level budget (-1) is irrelevant. Registering it
		// here is what makes a launched or restart-recovered relocator runnable — and the restart matters
		// more than usual: its persisted relocation intents are re-derived on the first tick, so a
		// rebuild that never runs would leave an in-flight relocation unfinished (RULINGS #2).
		{CommandType: "opportunity_relocator", build: buildOpportunityRelocatorCommand},
		// bootstrap (sp-3nbe): the standing captain bootstrap coordinator. Like
		// fleet_autosizer/siting it owns its whole reconcile loop inside one Handle() (NOT a
		// CoordinatorOwnsIterations type) — but UNLIKE them Handle() RETURNS at the terminal
		// EXPANSION exit (gate built + handed off), so its container completes on the response's
		// RunTerminal report, and any non-terminal return is paced at the bootstrap tick
		// (standingIterationFloors) rather than re-entered at loop speed.
		{CommandType: "bootstrap", build: buildBootstrapCommand},
		// auto_outfit_coordinator (sp-buyd): the standing guarded auto-outfit coordinator.
		// Like fleet_autosizer/capacity it loops forever inside one Handle(), so it is NOT a
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
	// The contract coordinator's idle-arb harvest knobs are resolved LIVE
	// from the daemon's boot-loaded config.yaml on EVERY build. Both creation
	// (ContractFleetCoordinator) and restart recovery (recoverContainer) funnel
	// through here, so a config.yaml retune + daemon restart actually retunes a
	// recovered coordinator. The persisted idle_arb_* keys are dead:
	// resolveIdleArbConfig clears them and re-injects the live values, making
	// config.yaml the one source of truth. No coordinator recreate is ever
	// needed for these knobs.
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
	// sp-1txd: same live-config discipline for the fleet capacity autosizer. Its
	// [fleet_autosizer] knobs are cleared and re-injected from the boot-loaded config.yaml on
	// every build — creation and recovery alike — so a config edit + restart retunes a recovered
	// coordinator and no persisted copy can shadow the live value (the sp-ts82 pattern).
	if commandType == "fleet_autosizer" {
		s.resolveFleetAutosizerConfig(config)
	}
	// sp-3nbe: same live-config discipline for the captain bootstrap coordinator. Its [bootstrap]
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
	if commandType == "scout_tour" || commandType == "scout_post_coordinator" {
		s.resolveScoutingConfig(config)
	}
	// probe_sensing_coordinator: two live-config resolutions on every build — creation and
	// restart recovery alike. (1) The [sensing] config.yaml knobs (the goods whitelist — a
	// string the int-only tune registry cannot carry) are cleared and re-injected so a stale
	// persisted copy can never shadow the current config.yaml (the sp-ts82 discipline).
	// (2) A persisted/tuned pressure_half_life_secs is applied to the API client's
	// limiter-pressure EWMA, so a `tune` of it survives a bounce and takes effect at the next
	// rebuild. The boot default comes from config.yaml [daemon]
	// limiter_pressure_half_life_seconds (wired in main.go); a positive container value takes
	// precedence. The half-life is process-global client state, hence the narrow assertion
	// instead of widening the domain port.
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

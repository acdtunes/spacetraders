package grpc

import (
	"fmt"
	"strings"
	"time"

	autooutfitCmd "github.com/andrescamacho/spacetraders-go/internal/application/autooutfit"
	commonApp "github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	liquidationCmd "github.com/andrescamacho/spacetraders-go/internal/application/liquidation"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	shipCargoCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNavCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypesCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	storageCmd "github.com/andrescamacho/spacetraders-go/internal/application/storage/commands"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	manufacturingDomain "github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type configFieldError struct {
	Fields []string
}

func (e *configFieldError) Error() string {
	return "missing or invalid " + strings.Join(e.Fields, ", ")
}

type configReader struct {
	values  map[string]interface{}
	invalid []string
}

func newConfigReader(values map[string]interface{}) *configReader {
	return &configReader{values: values}
}

func (r *configReader) fail(key string) {
	r.invalid = append(r.invalid, key)
}

func (r *configReader) Err() error {
	if len(r.invalid) == 0 {
		return nil
	}
	return &configFieldError{Fields: r.invalid}
}

func (r *configReader) RequiredString(key string) string {
	value, ok := r.values[key].(string)
	if !ok {
		r.fail(key)
	}
	return value
}

func (r *configReader) RequiredNonEmptyString(key string) string {
	value, ok := r.values[key].(string)
	if !ok || value == "" {
		r.fail(key)
		return ""
	}
	return value
}

func (r *configReader) OptionalString(key string) string {
	value, _ := r.values[key].(string)
	return value
}

func (r *configReader) OptionalStringDefault(key, fallback string) string {
	if value, ok := r.values[key].(string); ok && value != "" {
		return value
	}
	return fallback
}

func (r *configReader) RequiredInt(key string) int {
	value, ok := intValue(r.values[key])
	if !ok {
		r.fail(key)
	}
	return value
}

// PresentInt reads an int value and reports whether the key was present and
// valid — for genuinely optional numeric knobs whose ABSENCE means something
// (RefuelShip's nil-units = full tank), where OptionalInt's fallback would
// erase the present-vs-absent distinction.
func (r *configReader) PresentInt(key string) (int, bool) {
	return intValue(r.values[key])
}

func (r *configReader) OptionalInt(key string, fallback int) int {
	value, ok := intValue(r.values[key])
	if !ok {
		return fallback
	}
	return value
}

// PresentOrFailInt reads a numeric knob that, WHEN THE KEY IS PRESENT, MUST parse — a
// present-but-unparseable value is a hard build failure (RULINGS #4 fail-closed) rather
// than a silent fallback to the caller's default. Absent → fallback (a genuinely omitted
// knob still defers to the coordinator's own default).
//
// It exists because OptionalInt collapses "key absent" and "key present but wrong type"
// to the SAME fallback — exactly how sp-ggk2 hid: a working_capital_reserve that was
// PRESENT and non-zero was indistinguishable from absent once its type failed to parse,
// so a corrupt reserve resolved to the 50k floor invisibly. For a money guard, a failed
// build (no tour, no buy — the hull is released cleanly) is the correct fail-closed; a
// tour must never spend beneath a floor it could not determine.
func (r *configReader) PresentOrFailInt(key string, fallback int) int {
	raw, present := r.values[key]
	if !present {
		return fallback
	}
	value, ok := intValue(raw)
	if !ok {
		r.fail(key)
		return fallback
	}
	return value
}

// OptionalFloat reads a float config value (e.g. sp-lbbm's sell_floor_fraction),
// returning fallback when the key is absent or non-numeric. JSON numbers
// round-trip through float64, and an int is accepted too.
func (r *configReader) OptionalFloat(key string, fallback float64) float64 {
	value, ok := floatValue(r.values[key])
	if !ok {
		return fallback
	}
	return value
}

func (r *configReader) OptionalBool(key string) bool {
	value, _ := r.values[key].(bool)
	return value
}

// PresentBool reads a bool knob and reports whether the key was present, for a
// genuinely optional bool whose ABSENCE means "defer to a default" that is not
// simply false (sp-1txd's prefer_demand_proximal_yard defaults TRUE — OptionalBool
// would collapse "unset" into false and silently disable the default-on behaviour).
func (r *configReader) PresentBool(key string) (bool, bool) {
	value, ok := r.values[key].(bool)
	return value, ok
}

func (r *configReader) RequiredStringSlice(key string, aliases ...string) []string {
	if value, ok := stringSliceValue(r.values[key]); ok {
		return value
	}
	for _, alias := range aliases {
		if value, ok := stringSliceValue(r.values[alias]); ok {
			return value
		}
	}
	r.fail(key)
	return nil
}

// OptionalStringSlice reads a string-slice config value with no required
// default (e.g. sp-snmb's --dedicated-ships/--standby-stations, which are
// opt-in and disabled entirely when absent). Unlike RequiredStringSlice, a
// missing or wrong-typed key is not a validation failure - it simply
// returns nil.
func (r *configReader) OptionalStringSlice(key string, aliases ...string) []string {
	if value, ok := stringSliceValue(r.values[key]); ok {
		return value
	}
	for _, alias := range aliases {
		if value, ok := stringSliceValue(r.values[alias]); ok {
			return value
		}
	}
	return nil
}

// OptionalGoodGatingOverrides reads the per-good buy-gating override map (sp-sdyo) from a launch
// config key holding the map's JSON encoding (GoodGatingOverrides.Encode). A missing, non-string,
// or malformed value yields nil (no overrides) — the guard-tightening default that keeps every
// good on the global gates, matching the lenient Optional* family. This is a PER-LAUNCH key (not a
// global config.yaml knob and not in manufacturingConfigKeys), so it persists in the container
// config as a JSON string and reloads on restart untouched (RULINGS #2).
func (r *configReader) OptionalGoodGatingOverrides(key string) manufacturingDomain.GoodGatingOverrides {
	raw, ok := r.values[key].(string)
	if !ok || raw == "" {
		return nil
	}
	overrides, err := manufacturingDomain.DecodeGoodGatingOverrides(raw)
	if err != nil {
		return nil
	}
	return overrides
}

// intValue coerces a config value to int. It MUST handle every numeric type a launch
// config can carry on EITHER build path: float64 (the JSON-recovery path — persisted
// numbers round-trip through float64) AND the native int/int64 the daemon stores on the
// fresh-start/coordinator-launch path (buildCommandForType is called directly on the
// in-memory map before any JSON round-trip). Omitting int64 was the sp-ggk2 money bug: a
// native int64 working_capital_reserve fell through to (0,false), OptionalInt returned its
// fallback 0, and the 1M reserve silently became the 50k floor on a live-launched tour —
// while the SAME config read back correctly after a restart (JSON→float64), which is why
// it was invisible. int is 64-bit on the daemon's target so the int64→int narrowing never
// overflows a credit value.
func intValue(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case int32:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

func floatValue(raw interface{}) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}

func stringSliceValue(raw interface{}) ([]string, bool) {
	switch v := raw.(type) {
	case []string:
		return v, true
	case []interface{}:
		out := make([]string, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

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
// FAILED at restart recovery ("unknown command type") and its in-flight work is
// abandoned, which is exactly how the TORWIND-18/12 navigates orphaned.
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
		{CommandType: "frontier_expansion_coordinator", build: buildFrontierExpansionCoordinatorCommand},
		{CommandType: "market_freshness_sizer_coordinator", build: buildMarketFreshnessSizerCoordinatorCommand},
		{CommandType: "shipyard_backfill_coordinator", build: buildShipyardBackfillCoordinatorCommand},
		{CommandType: "scout_reposition", build: buildScoutRepositionCommand, CoordinatorOwnsIterations: true},
		{CommandType: "contract_workflow", build: buildContractWorkflowCommand},
		{CommandType: "contract_fleet_coordinator", build: buildContractFleetCoordinatorCommand},
		// trade_fleet_coordinator (sp-1278): a standing coordinator that loops forever
		// inside one Handle() call, so — like scout_post/contract_fleet — it is NOT a
		// CoordinatorOwnsIterations type; the container-level iteration budget (-1) is
		// irrelevant because Handle() never returns.
		{CommandType: "trade_fleet_coordinator", build: buildTradeFleetCoordinatorCommand},
		// worker_rebalancer_coordinator (sp-f5pr): a standing coordinator that loops
		// forever inside one Handle() call, so — like trade_fleet/scout_post — it is NOT a
		// CoordinatorOwnsIterations type. worker_ferry is its one-shot cross-system relay
		// worker (twin of scout_reposition): the coordinator owns re-dispatch, so the
		// container wraps exactly ONE iteration (CoordinatorOwnsIterations).
		{CommandType: "worker_rebalancer_coordinator", build: buildWorkerRebalancerCoordinatorCommand},
		{CommandType: "worker_ferry", build: buildWorkerFerryCommand, CoordinatorOwnsIterations: true},
		// cargo_liquidation (sp-39oi): the contract fleet coordinator's one-shot
		// self-clearing worker for a parked-with-cargo hull (twin of worker_ferry). The
		// coordinator owns re-dispatch, so the container wraps exactly ONE iteration
		// (CoordinatorOwnsIterations).
		{CommandType: "cargo_liquidation", build: buildCargoLiquidationCommand, CoordinatorOwnsIterations: true},
		{CommandType: "purchase_ship", build: buildPurchaseShipCommand},
		{CommandType: "batch_purchase_ships", build: buildBatchPurchaseShipsCommand},
		{CommandType: "goods_factory_coordinator", build: buildGoodsFactoryCoordinatorCommand},
		// construction_coordinator (sp-382j): the standing construction-supply drain. Like
		// trade_fleet/siting it loops forever inside one Handle(), so it is NOT a
		// CoordinatorOwnsIterations type; the container-level budget (-1) is irrelevant.
		// Registering it here is what makes a launched or restart-recovered drain runnable.
		{CommandType: "construction_coordinator", build: buildConstructionCoordinatorCommand},
		// siting_coordinator (sp-vdld): the standing factory-siting brain. Like
		// trade_fleet/frontier it loops forever inside one Handle(), so it is NOT a
		// CoordinatorOwnsIterations type; the container-level budget (-1) is irrelevant.
		{CommandType: "siting_coordinator", build: buildSitingCoordinatorCommand},
		// fleet_autosizer (sp-1txd): the standing fleet capacity autosizer. Like
		// trade_fleet/siting it loops forever inside one Handle(), so it is NOT a
		// CoordinatorOwnsIterations type; the container-level budget (-1) is irrelevant.
		{CommandType: "fleet_autosizer", build: buildFleetAutosizerCommand},
		// bootstrap (sp-3nbe): the standing captain bootstrap coordinator. Like
		// fleet_autosizer/siting it loops forever inside one Handle(), so it is NOT a
		// CoordinatorOwnsIterations type; the container-level budget (-1) is irrelevant.
		{CommandType: "bootstrap", build: buildBootstrapCommand},
		// capacity_reconciler_coordinator (st-7zk): the standing capacity reconciler. Like
		// fleet_autosizer/siting it loops forever inside one Handle(), so it is NOT a
		// CoordinatorOwnsIterations type; the container-level budget (-1) is irrelevant.
		// Registering it here is what makes a launched or restart-recovered reconciler
		// runnable — launch itself stays EXPLICIT (never boot-standing, st-fyr).
		{CommandType: "capacity_reconciler_coordinator", build: buildCapacityReconcilerCoordinatorCommand},
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
		{CommandType: "tour_run", build: buildTourCoordinatorCommand, CoordinatorOwnsIterations: true},
		{CommandType: "stocker", build: buildStockerCoordinatorCommand, CoordinatorOwnsIterations: true},
		// One-shot ship operations (sp-7yej invariant 4): these were created by
		// container_ops_ship.go but never registered, so a daemon restart
		// mid-operation marked them FAILED ("unknown command type") and dropped
		// the work on the floor — the TORWIND-18/12 orphaned navigates. Each
		// rebuilds trivially from its persisted config and is safe to re-run:
		// navigate resumes/waits out the live transit via RouteExecutor,
		// dock/orbit/refuel no-op when already done, and a re-run jettison of
		// already-jettisoned cargo fails honestly rather than silently.
		{CommandType: "navigate_ship", build: buildNavigateShipCommand, CoordinatorOwnsIterations: true},
		{CommandType: "route_ship", build: buildRouteShipCommand, CoordinatorOwnsIterations: true},
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
	// sp-ts82: the contract coordinator's idle-arb harvest knobs are resolved LIVE
	// from the daemon's boot-loaded config.yaml on EVERY build. Both creation
	// (ContractFleetCoordinator) and restart recovery (recoverContainer) funnel
	// through here, so a config.yaml retune + daemon restart actually retunes a
	// recovered coordinator — the documented path (sp-uohe) finally true. The
	// persisted idle_arb_* keys are dead: resolveIdleArbConfig clears them and
	// re-injects the live values, making config.yaml the one source of truth. No
	// coordinator recreate is ever needed for these knobs.
	if commandType == "contract_fleet_coordinator" {
		s.resolveIdleArbConfig(config)
		// sp-39oi: same live-config discipline for the parked-hull auto-liquidation knobs
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
	// sp-f5pr: same live-config discipline for the worker-rebalancer coordinator. Its
	// [worker_rebalancer] knobs are cleared and re-injected from the boot-loaded
	// config.yaml on every build — creation and recovery alike — so a config edit +
	// restart retunes a recovered coordinator and no persisted copy can shadow the live
	// value.
	if commandType == "worker_rebalancer_coordinator" {
		s.resolveWorkerRebalancerConfig(config)
	}
	// sp-kk61: same live-config discipline for the goods_factory_coordinator's working-capital
	// reserve (singleton ProductionExecutor, sp-agzj's max(50000, configured) input-buy floor):
	// [manufacturing].working_capital_reserve is resolved fresh on every build — creation and
	// restart recovery alike — so a config.yaml retune reaches a recovered coordinator with no
	// redeploy. (sp-jav2 X2: the parallel manufacturing_coordinator that shared this branch is
	// retired.)
	if commandType == "goods_factory_coordinator" {
		s.resolveManufacturingConfig(config)
	}
	// sp-vh1s: the construction-supply drain gets the SAME [manufacturing] unified_gate_fill toggle,
	// resolved fresh on every build so a config edit + restart flips a recovered drain — but via a
	// surgical resolver that injects ONLY the toggle, leaving the drain's launch-config production_strategy
	// untouched (the full resolveManufacturingConfig would override it).
	if commandType == "construction_coordinator" {
		s.resolveConstructionUnifiedGateFill(config)
	}
	// sp-vdld: same live-config discipline for the siting coordinator. Its
	// [manufacturing.siting] knobs are cleared and re-injected from the boot-loaded
	// config.yaml on every build — creation and recovery alike — so a config edit +
	// restart retunes a recovered coordinator and no persisted copy can shadow the live
	// value.
	if commandType == "siting_coordinator" {
		s.resolveSitingConfig(config)
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
	// st-7zk: same live-config discipline for the capacity reconciler. Its
	// [capacity_reconciler] calibration is cleared and re-injected from the boot-loaded
	// config.yaml on every build — creation and recovery alike — so a config edit + restart
	// retunes a recovered coordinator and no persisted copy can shadow the live value (the
	// sp-ts82 pattern).
	if commandType == "capacity_reconciler_coordinator" {
		s.resolveCapacityReconcilerConfig(config)
	}
	// sp-x8i5: same live-config discipline for the scouting subsystem's tour-start
	// phase jitter ceiling. The [scouting] knob is cleared and re-injected from the
	// boot-loaded config.yaml on every build — creation and recovery alike — for both
	// scout_tour and scout_post_coordinator, so a config edit + restart retunes a
	// recovered scout and no persisted copy can shadow the live value.
	if commandType == "scout_tour" || commandType == "scout_post_coordinator" {
		s.resolveScoutingConfig(config)
	}
	return spec.BuildCommand(config, playerID, containerID)
}

func buildScoutTourCommand(cfg *configReader, playerID int, containerID string) interface{} {
	// Unified iteration semantics (sp-7yej invariant 3): 0 means "the type's
	// default" (one tour, matching the CLI flag's default), never "zero work".
	// Before this, iterations=0 completed the container instantly without
	// scouting anything — the "0 tours vanished" half of tonight's scout
	// divergence. Normalized here so creation and restart recovery (both build
	// through this factory) agree.
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

// buildScoutPostCoordinatorCommand rebuilds the standing scout-post coordinator
// from its persisted launch config so restart recovery re-adopts it (sp-cxpq): it
// reloads the posts table and respawns each post's tour. Like
// contract_fleet_coordinator it loops forever inside one Handle() call, so the
// container-level iteration budget is irrelevant and it is NOT a
// CoordinatorOwnsIterations type. tick_interval_secs, market_drift_threshold, and
// market_drift_max_age_secs are all optional (0 → the coordinator's own default) —
// the latter two bound the debounced market-set re-cut (sp-ykhl, RULINGS #5).
// budget_change_debounce_cycles (0 → default) bounds the debounced hull-budget
// re-partition that absorbs the freshness sizer's ±1 demand-noise swings (sp-itr5).
func buildScoutPostCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &scoutingCmd.RunScoutPostCoordinatorCommand{
		PlayerID:                        shared.MustNewPlayerID(playerID),
		ContainerID:                     cfg.RequiredNonEmptyString("container_id"),
		TickIntervalSecs:                cfg.OptionalInt("tick_interval_secs", 0),
		MarketDriftThreshold:            cfg.OptionalInt("market_drift_threshold", 0),
		MarketDriftMaxAgeSecs:           cfg.OptionalInt("market_drift_max_age_secs", 0),
		BudgetChangeDebounceCycles:      cfg.OptionalInt("budget_change_debounce_cycles", 0),
		UndersizedAvgHopSecs:            cfg.OptionalInt("undersized_avg_hop_secs", 0),
		UndersizedRewarnCooldownSecs:    cfg.OptionalInt("undersized_rewarn_cooldown_secs", 0),
		StartJitterMaxSecs:              cfg.OptionalInt("tour_start_jitter_max_seconds", 0),
		MaxRepositionJumps:              cfg.OptionalInt("max_reposition_jumps", 0),
		RepositionFailureCooldownSecs:   cfg.OptionalInt("reposition_failure_cooldown_secs", 0),
		CoverageSpreadDisabled:          cfg.OptionalBool("coverage_spread_disabled"),
		RespawnAttemptCap:               cfg.OptionalInt("respawn_attempt_cap", 0),
		RespawnCapDisabled:              cfg.OptionalBool("respawn_cap_disabled"),
		ManningStallCycles:              cfg.OptionalInt("manning_stall_cycles", 0),
		ManningStallCorrectionCap:       cfg.OptionalInt("manning_stall_correction_cap", 0),
		GateReconcileEnabled:            cfg.OptionalBool("gate_reconcile_enabled"),
		GateReconcileMaxDispatch:        cfg.OptionalInt("gate_reconcile_max_dispatch", 0),
		GateReconcileMarketlessDisabled: cfg.OptionalBool("gate_reconcile_marketless_disabled"),
		// sp-u8jc cross-system reuse relay: an int-mode flag (0=off, byte-identical) + a hop bound,
		// threaded config→command like sp-6vep's probe_reuse_enabled/edge_relay_max_hops. Absent from
		// config.yaml ⇒ OptionalInt returns 0 ⇒ the relay is off and byte-identical to today.
		ScoutCrossSystemRelayEnabled: cfg.OptionalInt("scout_cross_system_relay_enabled", 0),
		ScoutRelayMaxHops:            cfg.OptionalInt("scout_relay_max_hops", 0),
	}
}

// buildTradeFleetCoordinatorCommand rebuilds the standing trade-fleet coordinator
// command (sp-1278) from a persisted launch config so a daemon restart re-adopts it.
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
		// sp-yqx4: raw pass-through — the coordinator only relays this to StartTourRun; the
		// tour build resolves 0/absent → the 40% default at the point of enforcement.
		WorkingCapitalReserveTreasuryPct: cfg.OptionalInt("trade_fleet_reserve_treasury_pct", 0),
		// sp-1pli: minutes on config, seconds on the command (matches CooldownSecs) — converted
		// here, the one crossing point, so every downstream read is uniformly in seconds.
		RelaunchBackoffMaxSecs: cfg.OptionalInt("trade_fleet_relaunch_backoff_max_minutes", 0) * 60,
		// sp-nkci: the restart-mass-park exemption is live by default — an absent disable key
		// reads as false (exemption ON), like Enabled. Window/threshold defer to the
		// coordinator's own defaults (120s / 4 hulls) when unset.
		MassParkExemptDisabled: cfg.OptionalBool("trade_fleet_masspark_exempt_disabled"),
		MassParkWindowSecs:     cfg.OptionalInt("trade_fleet_masspark_window_seconds", 0),
		MassParkMinHulls:       cfg.OptionalInt("trade_fleet_masspark_min_hulls", 0),
	}
}

// buildWorkerRebalancerCoordinatorCommand rebuilds the standing worker-rebalancer
// coordinator command (sp-f5pr) from a persisted launch config so a daemon restart
// re-adopts it. The [worker_rebalancer] knobs are resolved LIVE from config.yaml just
// before this runs (resolveWorkerRebalancerConfig in buildCommandForType), so the persisted
// worker_rebalancer_* keys are transient — the reads below see the current config.yaml.
// Enabled is reconstructed as the negation of worker_rebalancer_disabled: an absent key
// reads as enabled, preserving the default-ON intent across a recovery from an old config
// that predates the key. dry_run is the launch-time flag (mirrors the frontier
// coordinator), read back so a dry-run coordinator resumes dry-run after a restart.
func buildWorkerRebalancerCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.RunWorkerRebalancerCoordinatorCommand{
		PlayerID:             shared.MustNewPlayerID(playerID),
		ContainerID:          cfg.RequiredNonEmptyString("container_id"),
		AgentSymbol:          cfg.OptionalString("agent_symbol"),
		DryRun:               cfg.OptionalBool("dry_run"),
		Enabled:              !cfg.OptionalBool("worker_rebalancer_disabled"),
		TickIntervalSecs:     cfg.OptionalInt("worker_rebalancer_tick_secs", 0),
		VacancyMinMinutes:    cfg.OptionalInt("worker_rebalancer_vacancy_min_minutes", 0),
		SourceMinIdle:        cfg.OptionalInt("worker_rebalancer_source_min_idle", 0),
		FerryCooldownSecs:    cfg.OptionalInt("worker_rebalancer_ferry_cooldown_secs", 0),
		MaxConcurrentFerries: cfg.OptionalInt("worker_rebalancer_max_concurrent_ferries", 0),
		MaxLightsPerSystem:   cfg.OptionalInt("worker_rebalancer_max_lights_per_system", 0),
		EffectSelfcheckTicks: cfg.OptionalInt("worker_rebalancer_effect_selfcheck_ticks", 0),
	}
}

// buildWorkerFerryCommand rebuilds a one-shot cross-system ferry from its persisted launch
// config so restart recovery re-adopts it (sp-f5pr, twin of buildScoutRepositionCommand). A
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
		// sp-fwxm: reload the ferry-reposition jump bound stamped at PersistWorkerFerryWorker (the
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

// buildFrontierExpansionCoordinatorCommand rebuilds the standing frontier expansion
// coordinator from its persisted launch config so restart recovery re-adopts it
// byte-identically (RULINGS #2, sp-8w89). It is a reconcile-loop coordinator (NOT a
// CoordinatorOwnsIterations type — it loops forever inside one Handle()). Every knob is
// optional (0/false → the coordinator's own default, RULINGS #5), so the creation op and
// recovery share one construction and can never drift.
func buildFrontierExpansionCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &expansionCmd.RunFrontierExpansionCoordinatorCommand{
		PlayerID:                 shared.MustNewPlayerID(playerID),
		ContainerID:              cfg.RequiredNonEmptyString("container_id"),
		TickIntervalSecs:         cfg.OptionalInt("tick_interval_secs", 0),
		DryRun:                   cfg.OptionalBool("dry_run"),
		MaxProbeFleet:            cfg.OptionalInt("max_probe_fleet", 0),
		MaxSpendPerCycle:         cfg.OptionalInt("max_spend_per_cycle", 0),
		PurchaseCooldownSecs:     cfg.OptionalInt("purchase_cooldown_secs", 0),
		SpendWindowSecs:          cfg.OptionalInt("spend_window_secs", 0),
		ExpansionMaxHops:         cfg.OptionalInt("expansion_max_hops", 0),
		MaxFrontierPostsInFlight: cfg.OptionalInt("max_frontier_posts_in_flight", 0),
		FrontierFreshnessSecs:    cfg.OptionalInt("frontier_freshness_secs", 0),
		WeightKnownMarket:        cfg.OptionalInt("weight_known_market", 0),
		WeightHopPenalty:         cfg.OptionalInt("weight_hop_penalty", 0),
		WeightVirginBonus:        cfg.OptionalInt("weight_virgin_bonus", 0),
		ProximalYardHopPenalty:   cfg.OptionalInt("proximal_yard_hop_penalty", 0),
		ProbeSiblingPriceMargin:  cfg.OptionalInt("probe_sibling_price_margin", 0),
		MaxProbePrice:            cfg.OptionalInt("max_probe_price", 0),
		// sp-rjgr depth-vs-breadth balance (all live-tunable; 0 → the coordinator's own default).
		BreadthFractionPercent: cfg.OptionalInt("breadth_fraction_percent", 0),
		MaxDepthPathfinders:    cfg.OptionalInt("max_depth_pathfinders", 0),
		MaxDepthHops:           cfg.OptionalInt("max_depth_hops", 0),
		ObjectiveBiasPercent:   cfg.OptionalInt("objective_bias_percent", 0),
		ReservedFreshnessFloor: cfg.OptionalInt("reserved_freshness_floor", 0), // sp-iopd symmetric floor
		// sp-6vep reuse-before-buy the deep frontier (all live-tunable; 0 ⇒ the coordinator's own
		// DEFAULT-SAFE value, so a merge is byte-identical to today until armed next era).
		ProbeReuseEnabled:       cfg.OptionalInt("probe_reuse_enabled", 0),
		EdgeRelayMaxHops:        cfg.OptionalInt("edge_relay_max_hops", 0),
		ReuseValueCeiling:       cfg.OptionalInt("reuse_value_ceiling", 0),
		SnowballNeighbors:       cfg.OptionalInt("snowball_neighbors", 0),
		PostInflightTimeoutSecs: cfg.OptionalInt("post_inflight_timeout_secs", 0),
	}
}

// buildMarketFreshnessSizerCoordinatorCommand rebuilds the standing market-freshness
// auto-sizer from its persisted launch config so restart recovery re-adopts it
// byte-identically (RULINGS #2, sp-orgp). Like the frontier coordinator it is a
// reconcile-loop coordinator (NOT a CoordinatorOwnsIterations type). Every knob is optional
// (0/false → the coordinator's own default, RULINGS #5), so the creation op and recovery
// share one construction and can never drift. Per-system SLA overrides are a command-level
// capability wired through a richer config path; the flat launch config carries the scalar
// knobs the common single-SLA case needs.
func buildMarketFreshnessSizerCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &scoutingCmd.RunMarketFreshnessSizerCoordinatorCommand{
		PlayerID:                shared.MustNewPlayerID(playerID),
		ContainerID:             cfg.RequiredNonEmptyString("container_id"),
		TickIntervalSecs:        cfg.OptionalInt("tick_interval_secs", 0),
		DryRun:                  cfg.OptionalBool("dry_run"),
		SLASeconds:              cfg.OptionalInt("sla_seconds", 0),
		SeedCycleSeconds:        cfg.OptionalInt("seed_cycle_seconds", 0),
		MinCycleSamples:         cfg.OptionalInt("min_cycle_samples", 0),
		WorstCycleSeconds:       cfg.OptionalInt("worst_cycle_seconds", 0),
		CycleDampeningPercent:   cfg.OptionalInt("cycle_dampening_percent", 0),
		MaxProbesPerSystem:      cfg.OptionalInt("max_probes_per_system", 0),
		BreachResponsePercent:   cfg.OptionalInt("breach_response_percent", 0),
		TargetPercentile:        cfg.OptionalInt("target_percentile", 0), // sp-r57g percentile-age target
		ValueWeightedMode:       cfg.OptionalInt("value_weighted", 0),    // sp-r57g value-weighting mode (2=on default, 1=off)
		ReleaseSlackPercent:     cfg.OptionalInt("release_slack_percent", 0),
		ReleaseStableWindowSecs: cfg.OptionalInt("release_stable_window_secs", 0),
		ReservedFrontierFloor:   cfg.OptionalInt("reserved_frontier_floor", 0), // sp-iopd reserved frontier floor
		MaxProbeFleet:           cfg.OptionalInt("max_probe_fleet", 0),
		MaxSpendPerCycle:        cfg.OptionalInt("max_spend_per_cycle", 0),
		PurchaseCooldownSecs:    cfg.OptionalInt("purchase_cooldown_secs", 0),
		SpendWindowSecs:         cfg.OptionalInt("spend_window_secs", 0),
	}
}

// buildShipyardBackfillCoordinatorCommand rebuilds the standing shipyard-backfill sweep from
// its persisted launch config so restart recovery re-adopts it byte-identically (RULINGS #2,
// sp-rhju). Like the frontier coordinator it is a reconcile-loop coordinator (NOT a
// CoordinatorOwnsIterations type — it loops forever inside one Handle()). Every knob is
// optional (0 → the coordinator's own default, RULINGS #5), so creation and recovery share one
// construction and can never drift.
func buildShipyardBackfillCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &scoutingCmd.RunShipyardBackfillCoordinatorCommand{
		PlayerID:              shared.MustNewPlayerID(playerID),
		ContainerID:           cfg.RequiredNonEmptyString("container_id"),
		TickIntervalSecs:      cfg.OptionalInt("tick_interval_secs", 0),
		MaxDispatchesPerCycle: cfg.OptionalInt("max_dispatches_per_cycle", 0),
		MaxHops:               cfg.OptionalInt("backfill_max_hops", 0),
	}
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
		TreasuryReserve:        cfg.OptionalInt("treasury_reserve", 0),
		MaxTreasuryFractionPct: cfg.OptionalInt("max_treasury_fraction_pct", 0),
		InstallFeeEstimate:     cfg.OptionalInt("install_fee_estimate", 0),
		HopCost:                cfg.OptionalInt("hop_cost", 0),
		TelemetryWindowSecs:    cfg.OptionalInt("telemetry_window_secs", 0),
	}
}

// buildScoutRepositionCommand rebuilds a one-shot cross-gate reposition relay from its
// persisted launch config so restart recovery re-adopts it (sp-s232). A coordinator-
// spawned relay (coordinator_id present) is skipped by recovery and re-dispatched by
// the scout_post_coordinator, but the command is still rebuilt here so the coordinator's
// StartScoutReposition path can reconstruct it. Re-running after a restart is safe:
// travel() waits out any in-transit leg (sp-8l3o) and re-plans the gate path from the
// hull's CURRENT position, so a mid-relay restart resumes rather than strands.
func buildScoutRepositionCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &scoutingCmd.ScoutRepositionCommand{
		PlayerID:            shared.MustNewPlayerID(playerID),
		ShipSymbol:          cfg.RequiredString("ship_symbol"),
		DestinationWaypoint: cfg.RequiredString("destination"),
		CoordinatorID:       cfg.OptionalString("coordinator_id"),
		// sp-o34q: reload the expendable-probe reposition bound the coordinator selected.
		// WITHOUT this the rebuilt relay ran at 0 -> travelWithJumpBound degraded to the
		// strict fetch-through resolver -> a >5-jump post produced the verbatim ErrUnroutable
		// "within N jumps" and crash-looped (8k9m's bound reached the in-memory command but was
		// dropped across the persist/rebuild boundary). Absent (0) is the strict-resolver
		// fallback, so a legacy/mis-wired config can never accidentally relax the sp-qxa4
		// unreadable-gate discipline; only an explicitly persisted positive bound routes past
		// unreadable gates. resolveScoutingConfig deliberately does NOT run for scout_reposition
		// (only scout_tour/scout_post_coordinator), so this per-relay value is never clobbered.
		MaxRepositionJumps: cfg.OptionalInt("max_reposition_jumps", 0),
		// sp-4yse: reload the 0-hop gate-charting intent. Absent (false) is the plain market
		// reposition — a legacy/manning relay never charts the gate; only a relay the sweep
		// explicitly flagged charts on arrival.
		ChartGateOnArrival: cfg.OptionalBool("chart_gate_on_arrival"),
	}
}

// buildNavigateShipCommand rebuilds a one-shot navigate from its persisted
// launch config so restart recovery re-adopts a RUNNING navigate instead of
// orphaning it (sp-7yej invariant 4 — the TORWIND-18/12 incident: daemon
// restarted mid-transit, recovery hit "unknown command type 'navigate_ship'",
// the hull was released and the flight abandoned). Re-running the command is
// safe: NavigateRoute no-ops when the ship is already at the destination and
// the RouteExecutor waits out a transit already in progress (the boot-time
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
	return &contractCmd.RunWorkflowCommand{
		ShipSymbol:    cfg.RequiredString("ship_symbol"),
		PlayerID:      shared.MustNewPlayerID(playerID),
		ContainerID:   containerID,
		CoordinatorID: cfg.OptionalString("coordinator_id"),
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
		// Command-cargo baseline (sp-uj6a, RULINGS #5): absent key → 0 → the
		// contract package's documented default (80, the light-hauler
		// standard - see CommandCargoBaselineDefault).
		CommandCargoBaseline: cfg.OptionalInt("command_cargo_baseline", 0),
		// Auto-liquidation knobs (sp-39oi): absent keys → default false/0 → feature ON
		// with jettison OFF. These are resolved LIVE from config.yaml by
		// resolveAutoLiquidationConfig on every build (sp-ts82), so the persisted copies
		// are dead and a config edit + restart retunes a recovered coordinator.
		AutoLiquidationDisabled:     cfg.OptionalBool("auto_liquidation_disabled"),
		LiquidationMinJettisonValue: cfg.OptionalInt("liquidation_min_jettison_value", 0),
		// Idle-gap arb knobs (sp-1z2h): absent keys → 0 → the contract
		// package's documented defaults (IdleArbConfig.WithDefaults). These
		// keys are resolved LIVE from config.yaml by resolveIdleArbConfig on
		// every build (sp-ts82) — the persisted copies are dead — so a config
		// edit + daemon restart retunes the harvest, recovery included.
		IdleArbDisabled:     cfg.OptionalBool("idle_arb_disabled"),
		IdleArbReserveHulls: cfg.OptionalInt("idle_arb_reserve_hulls", 0),
		IdleArbHubRadius:    float64(cfg.OptionalInt("idle_arb_hub_radius", 0)),
		IdleArbMaxSpend:     cfg.OptionalInt("idle_arb_max_spend", 0),
		IdleArbMinMargin:    cfg.OptionalInt("idle_arb_min_margin", 0),
		IdleArbIntervalSecs: cfg.OptionalInt("idle_arb_interval_secs", 0),
		// sp-uohe money guards. Absent → 0/nil → the contract package's
		// WithDefaults applies the documented defaults (leash 80, leg-cap 480s,
		// verify 80%, blacklist [ELECTRONICS]). An explicit empty blacklist ([])
		// is preserved by OptionalStringSlice (non-nil) so a config whitelist-flip
		// genuinely disables it without a code change.
		IdleArbLeashRadius:      float64(cfg.OptionalInt("idle_arb_leash_radius", 0)),
		IdleArbMaxLegSecs:       cfg.OptionalInt("idle_arb_max_leg_secs", 0),
		IdleArbMarginVerifyPct:  cfg.OptionalInt("idle_arb_margin_verify_pct", 0),
		IdleArbRecoveryHoldSecs: cfg.OptionalInt("idle_arb_recovery_hold_secs", 0),
		IdleArbBlacklist:        cfg.OptionalStringSlice("idle_arb_blacklist"),
		// sp-u4tv per-trip profitability floor (0 → WithDefaults: 100/u, 20%, 35/u fuel).
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
	}
}

// buildConstructionCoordinatorCommand rebuilds the standing construction-supply drain command
// (sp-382j) from a persisted launch config so a daemon restart re-adopts it (RULINGS #2). The
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
		// sp-yfzi: the production acquisition strategy the drain resolves a FABRICATE material's tree
		// on. Empty/absent → "smart" (resolveProductionStrategy), so construction produces scarce
		// intermediates recursively without the captain naming it; a per-launch production_strategy
		// override or the pipeline's per-good overrides dial it back (RULINGS #5).
		ProductionStrategy: resolveProductionStrategy(cfg.OptionalString("production_strategy")),
		// sp-vh1s (Admiral sign-off 2026-07-14): the unified gate-fill toggle, from [manufacturing] via
		// resolveConstructionUnifiedGateFill. absent/false → the drain honors the planner's frozen
		// buy-vs-fabricate decision per material (byte-identical to today); ON drives the resolver's full
		// scarcity-gated tree for every gate material and marks the run a gate node (the drain stamps
		// WithUnifiedGateFill + a construction-site DeliveryTarget derived from the task's own site).
		UnifiedGateFill: cfg.OptionalBool("unified_gate_fill"),
		// sp-to2v: the SAME feeding-efficiency policy the goods factory runs, threaded into the drain via
		// resolveConstructionUnifiedGateFill. absent/false/0 → greedy byte-identical feeding.
		FabricationEfficiency:  cfg.OptionalBool("fabrication_efficiency"),
		FeedSaturationMaxUnits: cfg.OptionalInt("feed_saturation_max_units", 0),
		FeedSaturationMinUnits: cfg.OptionalInt("feed_saturation_min_units", 0),
		FeedNonResponsiveGoods: cfg.OptionalStringSlice("feed_non_responsive_goods"),
		// sp-ubwi: the per-supplyTask timeout, from [manufacturing] via resolveConstructionUnifiedGateFill.
		// 0/absent → the drain's raised 30m default (the old hardcoded 10m abandoned legit long hauls).
		SupplyTaskTimeoutSeconds: cfg.OptionalInt("construction_supply_task_timeout_seconds", 0),
		// sp-e55b: prefer the drain's OWN dedicated gate-hauler fleet (e.g. TORWIND-C/-D) before
		// opportunistic idle hulls. Empty dedicated_fleet defaults (in-handler) to the shared
		// "manufacturing" identity that also authorizes the claim; exclusive_dedicated_fleet seals the
		// drain to that fleet (no opportunistic fallback). Both reload on restart (RULINGS #2).
		DedicatedFleet:          cfg.OptionalString("dedicated_fleet"),
		ExclusiveDedicatedFleet: cfg.OptionalBool("exclusive_dedicated_fleet"),
	}
}

func buildGoodsFactoryCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &goodsCmd.RunFactoryCoordinatorCommand{
		PlayerID:      playerID,
		TargetGood:    cfg.RequiredString("target_good"),
		SystemSymbol:  cfg.RequiredString("system_symbol"),
		ContainerID:   cfg.RequiredString("container_id"),
		MaxIterations: cfg.OptionalInt("max_iterations", 1),
		InputsOnly:    cfg.OptionalBool("inputs_only"),
		// sp-agzj: unify the factory input floor with the fleet reserve. 0/absent → the
		// coordinator's immutable 50k lower bound; a set value (the fleet's 1M) raises it.
		WorkingCapitalReserve: cfg.OptionalInt("working_capital_reserve", 0),
		// sp-yqx4: 0/absent → the 40% default so a factory below ~2.5M treasury is not
		// deadlocked by a reserve above the balance; a positive value is the captain's
		// [manufacturing] override.
		WorkingCapitalReserveTreasuryPct: resolveReserveTreasuryPct(cfg.OptionalInt("working_capital_reserve_treasury_pct", 0)),
		// sp-iv65: the ladder-chase input price ceiling. 0/absent → the executor resolves the
		// 1.5 default at the point of use (the guard runs ON in production without the captain
		// naming it); a set value is the captain's [manufacturing] override. The disable flag
		// is the emergency off-switch (RULINGS #5).
		InputPriceCeilingMultiplier: cfg.OptionalFloat("input_price_ceiling_multiplier", 0),
		InputPriceCeilingDisabled:   cfg.OptionalBool("input_price_ceiling_disabled"),
		// sp-sdyo: the per-good buy-gating override map (JSON string). A per-launch key that
		// persists in the container config and reloads on restart (RULINGS #2); absent → nil (every
		// good on the global gates). NOT added to manufacturingConfigKeys — it is per-launch, not a
		// global config.yaml knob, so resolveManufacturingConfig must not clear it.
		GoodGatingOverrides: cfg.OptionalGoodGatingOverrides("good_gating_overrides"),
		// sp-jav2 / FACTORY_DOCTRINE X1: the fabricate depth cap. 0/absent → the resolver resolves
		// the depth-1 default at the point of use (the cap runs ON in production without the captain
		// naming it — fabricate the output, buy its inputs); a set value is the captain's
		// [manufacturing] override. The disable flag is the RULINGS #5 emergency off-switch.
		FabricateMaxDepth:         cfg.OptionalInt("fabricate_max_depth", 0),
		FabricateDepthCapDisabled: cfg.OptionalBool("fabricate_depth_cap_disabled"),
		// sp-yfzi: the production acquisition strategy. Empty/absent → "smart" (resolveProductionStrategy),
		// so this factory resolves its tree with scarcity-gated recursion ON without the captain naming
		// it; a captain pins "prefer-buy" in [manufacturing] to dial back to the sp-jav2 posture.
		ProductionStrategy: resolveProductionStrategy(cfg.OptionalString("production_strategy")),
		// sp-a5j7 Phase 2: supply-first sourcing (the wedx restoration). Rescue multiplier 0 →
		// the executor resolves the 1.2 default; era-end flips to price-first < T-6h; the disable
		// flag is the RULINGS #5 escape hatch back to pure price-first.
		InputRescueMultiplier: cfg.OptionalFloat("input_rescue_multiplier", 0),
		InputEraEndPriceFirst: cfg.OptionalBool("input_era_end_price_first"),
		InputSourcingDisabled: cfg.OptionalBool("input_sourcing_disabled"),
		// sp-rh2z: the chain P&L kill-switch. 0/absent → the coordinator resolves the 30000/hr
		// threshold + 6h window defaults at the point of use (the switch runs ON in production
		// without the captain naming it); a set value is the captain's [manufacturing] override.
		// The disable flag is the RULINGS #5 emergency off-switch.
		ChainPnLKillThresholdPerHour: cfg.OptionalInt("chain_pnl_kill_threshold_per_hour", 0),
		ChainPnLWindowHours:          cfg.OptionalInt("chain_pnl_window_hours", 0),
		ChainPnLKillDisabled:         cfg.OptionalBool("chain_pnl_kill_disabled"),
		// sp-r5a6: the input-poison anti-cycle. 0/absent → the coordinator resolves the 194min
		// recovery half-life default at the point of use (the anti-cycle runs ON in production
		// without the captain naming it); a set value is the analyst's [manufacturing] override.
		// The disable flag is the RULINGS #5 emergency off-switch.
		InputRecoveryReattemptMinutes: cfg.OptionalInt("input_recovery_reattempt_minutes", 0),
		AntiCycleDisabled:             cfg.OptionalBool("anti_cycle_disabled"),
		// sp-xdk6: the export-ask-subsidy rest signal. 0/absent → the coordinator resolves the 90min
		// recovery-window default at the point of use (the signal runs ON in production without the
		// captain naming it); a set value is the analyst's [manufacturing] override. The disable flag
		// is the RULINGS #5 emergency off-switch.
		RestWindowMinutes:  cfg.OptionalInt("rest_window_minutes", 0),
		RestSignalDisabled: cfg.OptionalBool("rest_signal_disabled"),
		// sp-vh1s (Admiral sign-off 2026-07-14): the unified gate-fill toggle + gate output-buy
		// throughput-pacing, from [manufacturing] via injectManufacturingConfig. absent/false/0 → the
		// whole feature dark (IsUnifiedGateNode needs BOTH the toggle AND a construction-site target) and
		// the pacing coefficients resolve to their 2.0/1.0 defaults at the point of use — but the pacing
		// is only ever consulted for a gate node, so an OFF/profit factory is byte-identical. The disable
		// flag is the RULINGS #5 emergency off-switch. ConstructionSiteWaypoint is a PER-LAUNCH key (like
		// good_gating_overrides): a gate-fill factory launch names the jump-gate site the root output is
		// DELIVERED to; empty (every profit factory) leaves the run selling at a resale sink.
		UnifiedGateFill:           cfg.OptionalBool("unified_gate_fill"),
		ConstructionSiteWaypoint:  cfg.OptionalString("construction_site_waypoint"),
		GateOutputBuyRateMultiple: cfg.OptionalFloat("gate_output_buy_rate_multiple", 0),
		GateOutputPerLotMultiple:  cfg.OptionalFloat("gate_output_per_lot_multiple", 0),
		GateOutputPacingDisabled:  cfg.OptionalBool("gate_output_pacing_disabled"),
		// sp-to2v: the fabrication-efficiency feeding policy (toggle + saturation-window coefficients +
		// the non-responsive-goods override). absent/false/0 → the whole layer dark and the coefficients
		// resolve to their 200/25 defaults + the verified default non-responsive set at the point of use.
		FabricationEfficiency:  cfg.OptionalBool("fabrication_efficiency"),
		FeedSaturationMaxUnits: cfg.OptionalInt("feed_saturation_max_units", 0),
		FeedSaturationMinUnits: cfg.OptionalInt("feed_saturation_min_units", 0),
		FeedNonResponsiveGoods: cfg.OptionalStringSlice("feed_non_responsive_goods"),
		// sp-ev0n: the live-tunable concurrent-hull cap. worker_cap is the PER-OP override the
		// `goods factory workers` RPC writes live; it is NOT among the config.yaml-reinjected
		// manufacturingConfigKeys, so a live value persists verbatim across a restart (RULINGS
		// #2). factory_worker_cap_default carries the global [manufacturing.siting]
		// workers_per_chain (injected by injectManufacturingConfig, re-resolved from config.yaml
		// each build). resolveFactoryWorkerCap prefers the per-op override, else the global
		// default, else 0 = unbounded — so a fleet that never set workers_per_chain keeps the
		// pre-sp-ev0n emergent fan-out (RULINGS #5).
		WorkerCap: resolveFactoryWorkerCap(cfg.OptionalInt("worker_cap", 0), cfg.OptionalInt("factory_worker_cap_default", 0)),
	}
}

// resolveFactoryWorkerCap picks the concurrent-hull cap a goods_factory launches with
// (sp-ev0n): the per-op override (set live via `goods factory workers`) wins; absent that,
// the global [manufacturing.siting] workers_per_chain default; absent both, 0 = unbounded
// (the pre-sp-ev0n emergent fan-out, so a fleet that never configured workers_per_chain is
// unchanged). A negative override is treated as unset. The live provider re-reads the per-op
// value each pass, so this only sets the launch/restart baseline (RULINGS #2/#5).
func resolveFactoryWorkerCap(perOpOverride, globalDefault int) int {
	if perOpOverride > 0 {
		return perOpOverride
	}
	if globalDefault > 0 {
		return globalDefault
	}
	return 0
}

// buildSitingCoordinatorCommand rebuilds the standing siting coordinator command (sp-vdld)
// from a persisted launch config so a daemon restart re-adopts it. The [manufacturing.siting]
// knobs are resolved LIVE from config.yaml just before this runs (resolveSitingConfig in
// buildCommandForType), so the persisted siting_* keys are transient — the reads below see the
// current config.yaml. Disabled is reconstructed as siting_disabled directly (absent = false =
// ACTIVE), so an absent key boots the coordinator LIVE, preserving the default-ON intent across
// a recovery from an old config that predates the key (Admiral: no dark-shipping).
func buildSitingCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &goodsCmd.RunSitingCoordinatorCommand{
		PlayerID:                 playerID,
		ContainerID:              cfg.RequiredNonEmptyString("container_id"),
		AgentSymbol:              cfg.OptionalString("agent_symbol"),
		Disabled:                 cfg.OptionalBool("siting_disabled"),
		DryRun:                   cfg.OptionalBool("siting_dry_run"),
		TickIntervalSecs:         cfg.OptionalInt("siting_tick_secs", 0),
		TopK:                     cfg.OptionalInt("siting_top_k", 0),
		WorkersPerChain:          cfg.OptionalFloat("siting_workers_per_chain", 0),
		FreshnessMaxSecs:         cfg.OptionalInt("siting_freshness_max_secs", 0),
		EmitStalenessSecs:        cfg.OptionalInt("siting_emit_staleness_secs", 0),
		WeightTourAlignment:      cfg.OptionalFloat("siting_weight_tour_alignment", 0),
		WeightInputCompetition:   cfg.OptionalFloat("siting_weight_input_competition", 0),
		WeightStaleness:          cfg.OptionalFloat("siting_weight_staleness", 0),
		WeightWorkerReachability: cfg.OptionalFloat("siting_weight_worker_reachability", 0),
		MaxChainsPerSystem:       cfg.OptionalInt("siting_max_chains_per_system", 0),
		MaxChainsPerInputMarket:  cfg.OptionalInt("siting_max_chains_per_input_market", 0),
		RetireHysteresisTicks:    cfg.OptionalInt("siting_retire_hysteresis_ticks", 0),
		EffectSelfcheckTicks:     cfg.OptionalInt("siting_effect_selfcheck_ticks", 0),
		ScoutDemandCooldownSecs:  cfg.OptionalInt("siting_scout_demand_cooldown_secs", 0),
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
// trade_route container (sp-zewt). ContainerID is taken from the recovery-supplied
// containerID (the persisted row's ID), mirroring contract_workflow, so the operation
// context and the runner's ship claim stay pinned to the same container across a
// restart. MaxVisits defaults to 0 (the coordinator's own default-50 safety bound).
// WorkingCapitalReserve defaults to 0 (the coordinator's own defaultWorkingCapitalReserve
// floor, sp-bp6f) but is exposed as a launch-config knob so a captain can raise the
// reserve for a specific circuit without a redeploy. TargetDest defaults to "" (the
// undirected auto-scan) but is exposed as a launch-config knob so a captain can pin
// the circuit to a specific lane via --dest (sp-xwa1); an empty value preserves the
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

// buildArbCoordinatorCommand rebuilds the one-shot guarded arb command (sp-p4ua) from a
// persisted launch config so restart recovery can resume a RUNNING arb_run container.
// ContainerID is taken from the recovery-supplied containerID (the persisted row's ID),
// mirroring trade_route so the operation context and the runner's ship claim stay pinned
// across a restart. good/buy_at/sell_at are required (the lane the captain directed);
// max_units/max_spend/min_margin/working_capital_reserve default to 0 (the coordinator's
// own "0 → unset/default" semantics for each guard), and are persisted as launch-config
// knobs so a recovery rebuild resumes the same directed run with the same caps.
//
// prior_attempt_cost (sp-dkj7) is RUNTIME progress, not a launch knob: a fresh run
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
		// sp-lbbm per-tranche sell floor fraction. Absent → 0 → the coordinator's
		// own defaultArbSellFloorFraction (0.80), so a captain arb-run with no knob
		// set is still floored; idle-arb writes the live 80% knob here.
		SellFloorFraction: cfg.OptionalFloat("sell_floor_fraction", 0),
	}
}

// resolveReserveTreasuryPct maps a launch-config working_capital_reserve_treasury_pct to
// the value the coordinator enforces (sp-yqx4). 0/absent/negative → the deadlock-proof 40%
// default (common.DefaultReserveTreasuryPct), a positive value is the captain's override.
// Applied at the TERMINAL command builders (tour, goods_factory) whose commands stamp the
// pct onto ctx — so a config that never named the key still runs the counter-cyclical floor
// in production, while the value stays operator-tunable (RULINGS #5). The trade_fleet
// coordinator build forwards the RAW value (it only relays it to StartTourRun); the tour
// build below performs the resolution once, at the point of enforcement.
func resolveReserveTreasuryPct(configured int) int {
	if configured <= 0 {
		return commonApp.DefaultReserveTreasuryPct
	}
	return configured
}

// resolveProductionStrategy resolves the production acquisition strategy for the PRODUCTION command
// builders (goods_factory + construction), sp-yfzi. An empty/absent value → the scarcity-gated
// "smart" default (mfgServices.DefaultProductionStrategy): the resolver fabricates a SCARCE
// intermediate that has a factory and buys an abundant one, ON in production without the captain
// naming it. A configured value ("prefer-buy" to dial back to the sp-jav2 posture, or
// "prefer-fabricate") is passed through verbatim so the knob stays operator-tunable (RULINGS #5).
// Applied at the launch build so a directly-built command (tests) keeps the empty value — the
// resolver then falls back to its own prefer-buy default, byte-identical to today.
func resolveProductionStrategy(configured string) string {
	if configured == "" {
		return mfgServices.DefaultProductionStrategy
	}
	return configured
}

// buildTourCoordinatorCommand rebuilds the one-shot guarded tour command (sp-1ek0) from
// a persisted launch config so restart recovery can resume a RUNNING tour_run container.
// ContainerID comes from the recovery-supplied containerID (the persisted row's ID),
// mirroring arb_run/trade_route so the operation context and the runner's ship claim stay
// pinned across a restart. ship_symbol is required; the guard knobs default to 0 (the
// coordinator's own "0 → default" semantics: max_hops→6, max_spend→25% of treasury,
// replan_limit→2, working_capital_reserve→50k). iterations drives the CONTINUOUS-tour
// loop (sp-m5kv): -1 = tour until margins die, N>0 = N tours, 0/absent → the one-tour
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
		// sp-syaz: reload the per-tour distinct-system cap StartTourRun stamped from
		// [trade_fleet].max_tour_systems (the read side of the launch/rebuild boundary,
		// mirroring reposition_jump_bound). Absent → 0 → the solver's MAX_TOUR_SYSTEMS
		// default (2), so a tour launched without the knob is byte-identical to today;
		// a positive value sweeps tour length.
		MaxTourSystems: cfg.OptionalInt("max_tour_systems", 0),
		// sp-im74 config plumbing: reload the closed-circuit arming flag StartTourRun
		// stamped from [trade_fleet].closed_tours (the read side of the launch/rebuild
		// boundary, mirroring max_tour_systems). OptionalBool yields false for an absent
		// key → cmd.ClosedTours=false → im74's cons.Closed reads an OPEN tour,
		// byte-identical to today; true arms the return-to-anchor closed circuit.
		ClosedTours: cfg.OptionalBool("closed_tours"),
		MaxSpend:    int64(cfg.OptionalInt("max_spend", 0)),
		MinMargin:   cfg.OptionalInt("min_margin", 0),
		ReplanLimit: cfg.OptionalInt("replan_limit", 0),
		// sp-ggk2 RULINGS #4: the reserve is a money guard — a PRESENT-but-unparseable
		// value fails the build (fail closed), never a silent 0 → 50k floor. An absent key
		// still defers to the coordinator's own default (0 → defaultWorkingCapitalReserve),
		// so a captain CLI tour with no --reserve is unchanged.
		WorkingCapitalReserve: int64(cfg.PresentOrFailInt("working_capital_reserve", 0)),
		// sp-yqx4: 0/absent → the 40% default so every rebuilt/relaunched/recovered tour runs
		// the counter-cyclical floor; a positive value is the captain's [trade_fleet] override.
		WorkingCapitalReserveTreasuryPct: resolveReserveTreasuryPct(cfg.OptionalInt("working_capital_reserve_treasury_pct", 0)),
		Iterations:                       cfg.OptionalInt("iterations", 0),
		// Reposition-on-margins-death knobs (sp-zhii). reposition_disabled defaults to
		// false → the feature is ON for continuous runs (the captain filed sp-zhii to end
		// the whack-a-mole); the floor/K default to 0 → the coordinator's own
		// reposition{MinMargin,MaxCandidates}Default. reposition_in_progress / _target_*
		// are RUNTIME state the coordinator persists mid-jump (RULINGS #2), reloaded here
		// so a restart resumes the jump instead of re-planning at an intermediate hop.
		RepositionDisabled:      cfg.OptionalBool("reposition_disabled"),
		RepositionMinMargin:     cfg.OptionalInt("reposition_min_margin", 0),
		RepositionMaxCandidates: cfg.OptionalInt("reposition_max_candidates", 0),
		// sp-kl16: the stored-adjacency reposition jump bound (0/absent → the coordinator's own
		// default 12). This is the o34q READ side — the scout bug (sp-o34q) was buildScoutRepositionCommand
		// never reading the persisted bound back, degrading the live relay to the strict 5-jump
		// resolver; reading it here (paired with the container_ops_tour.go write) is what makes the
		// bound survive the launch-config → rebuild round-trip a recovery restart runs.
		RepositionJumpBound:      cfg.OptionalInt("reposition_jump_bound", 0),
		RepositionInProgress:     cfg.OptionalBool("reposition_in_progress"),
		RepositionTargetSystem:   cfg.OptionalString("reposition_target_system"),
		RepositionTargetWaypoint: cfg.OptionalString("reposition_target_waypoint"),
		// sp-686e: stranded-hull detector threshold from [trade_fleet]; 0/absent → the
		// coordinator's own default (3, resolveStrandedThreshold).
		StrandedConsecutiveThreshold: cfg.OptionalInt("stranded_consecutive_threshold", 0),
		// sp-z7ng placement/relocation scoring loop (epic sp-fguo). OptionalBool/OptionalInt
		// yield zero values for absent keys — the exact default-OFF dormancy mechanism the
		// Reposition* knobs use: placement_score_enabled absent ⇒ false ⇒ the legacy static-floor
		// reposition runs, byte-identical to today; the window/floor/shortlist default to 0 ⇒ the
		// coordinator's own placement*Default. Every existing container and recovery rebuild takes
		// the legacy branch until a captain explicitly sets placement_score_enabled: true.
		PlacementScoreEnabled:      cfg.OptionalBool("placement_score_enabled"),
		PlacementBetaWindowMinutes: cfg.OptionalInt("placement_beta_window_minutes", 0),
		PlacementParkFloorPct:      cfg.OptionalInt("placement_park_floor_pct", 0),
		PlacementShortlistTopN:     cfg.OptionalInt("placement_shortlist_top_n", 0),
		// sp-uf64 reposition reach (always-broaden discovery + deadhead-decay ranking + anti-herd).
		// OptionalBool/OptionalInt yield zero values for absent keys — the exact default-OFF dormancy
		// the Placement*/Reposition* knobs use: reposition_reach_enabled absent ⇒ false ⇒ the legacy
		// 1-hop-first reposition runs, byte-identical to today; the decay/cap default to 0 ⇒ the
		// coordinator's own repositionReach*Default (85, 5). Every existing container and recovery
		// rebuild takes the legacy branch until a captain explicitly sets reposition_reach_enabled: true.
		RepositionReachEnabled:           cfg.OptionalBool("reposition_reach_enabled"),
		RepositionReachHopDecayPct:       cfg.OptionalInt("reposition_reach_hop_decay_pct", 0),
		RepositionReachMaxHullsPerSystem: cfg.OptionalInt("reposition_reach_max_hulls_per_system", 0),
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
		// sp-jsng candidate widening (the #1 fleet-$/hr lever, sp-7q5t): reload the gate-hop radius +
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
	}
}

// buildStockerCoordinatorCommand rebuilds the stocker loop command (sp-zdwg) from a
// persisted launch config so restart recovery can resume a RUNNING stocker container.
// ContainerID comes from the recovery-supplied containerID (the persisted row's ID),
// mirroring tour_run so the operation context and the runner's ship claim stay pinned
// across a restart. ship_symbol + warehouse_waypoint are required (the dedicated hull and
// the deposit anchor); the caps default to 0 (the coordinator's own "0 → default"
// semantics: budget_per_leg → no cap, working_capital_reserve → 50k, iterations → one
// round-trip, max_market_age_minutes → 75, target_per_good → the miner's demand). The
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
	}
}

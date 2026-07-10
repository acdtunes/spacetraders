package grpc

import (
	"fmt"
	"strings"

	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	shipCargoCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNavCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypesCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
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

func (r *configReader) OptionalBool(key string) bool {
	value, _ := r.values[key].(bool)
	return value
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

func intValue(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
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
//	                                                            persisted), ∞ resumes
//	contract_workflow           one contract      coordinator   re-adopts standalone; worker
//	                                                            (coordinator_id) waits for parent
//	contract_fleet_coordinator  ∞ internal loop   coordinator   re-adopts
//	purchase_ship               one purchase      coordinator   re-adopts (idempotence at API)
//	batch_purchase_ships        one batch         coordinator   re-adopts
//	goods_factory_coordinator   one cycle         RUNNER        re-adopts with persisted budget
//	                                                            (sp-perx); -1 uses 2q2o backoff
//	manufacturing_coordinator   ∞ internal loop   coordinator   re-adopts
//	gas_coordinator             ∞ internal loop   coordinator   re-adopts
//	trade_route                 visit budget      coordinator   re-adopts; laden exit is a
//	                            (max_visits)                    FAILURE (sp-1hj5, invariant 2)
//	arb_run                     one directed leg  coordinator   re-adopts; resumes past the buy
//	                                                            (sp-5nqx), strand = failure
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
		{CommandType: "contract_workflow", build: buildContractWorkflowCommand},
		{CommandType: "contract_fleet_coordinator", build: buildContractFleetCoordinatorCommand},
		{CommandType: "purchase_ship", build: buildPurchaseShipCommand},
		{CommandType: "batch_purchase_ships", build: buildBatchPurchaseShipsCommand},
		{CommandType: "goods_factory_coordinator", build: buildGoodsFactoryCoordinatorCommand},
		{CommandType: "manufacturing_coordinator", build: buildManufacturingCoordinatorCommand},
		{CommandType: "gas_coordinator", build: buildGasCoordinatorCommand},
		{CommandType: "trade_route", build: buildTradeRouteCoordinatorCommand, CoordinatorOwnsIterations: true},
		{CommandType: "arb_run", build: buildArbCoordinatorCommand, CoordinatorOwnsIterations: true},
		{CommandType: "tour_run", build: buildTourCoordinatorCommand, CoordinatorOwnsIterations: true},
		// One-shot ship operations (sp-7yej invariant 4): these were created by
		// container_ops_ship.go but never registered, so a daemon restart
		// mid-operation marked them FAILED ("unknown command type") and dropped
		// the work on the floor — the TORWIND-18/12 orphaned navigates. Each
		// rebuilds trivially from its persisted config and is safe to re-run:
		// navigate resumes/waits out the live transit via RouteExecutor,
		// dock/orbit/refuel no-op when already done, and a re-run jettison of
		// already-jettisoned cargo fails honestly rather than silently.
		{CommandType: "navigate_ship", build: buildNavigateShipCommand, CoordinatorOwnsIterations: true},
		{CommandType: "dock_ship", build: buildDockShipCommand, CoordinatorOwnsIterations: true},
		{CommandType: "orbit_ship", build: buildOrbitShipCommand, CoordinatorOwnsIterations: true},
		{CommandType: "refuel_ship", build: buildRefuelShipCommand, CoordinatorOwnsIterations: true},
		{CommandType: "jettison_cargo", build: buildJettisonCargoCommand, CoordinatorOwnsIterations: true},
		{CommandType: "scout_fleet_assignment", build: buildScoutFleetAssignmentCommand, CoordinatorOwnsIterations: true},
		{CommandType: "manufacturing_task_worker", IsWorker: true},
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
		PlayerID:   shared.MustNewPlayerID(playerID),
		ShipSymbol: cfg.RequiredString("ship_symbol"),
		Markets:    cfg.RequiredStringSlice("markets"),
		Iterations: iterations,
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
		// Idle-gap arb knobs (sp-1z2h): absent keys → 0 → the contract
		// package's documented defaults (IdleArbConfig.WithDefaults). The
		// escape hatch and every parameter live in the persisted launch
		// config so a restart recovers the same harvest behavior.
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
		IdleArbLeashRadius:     float64(cfg.OptionalInt("idle_arb_leash_radius", 0)),
		IdleArbMaxLegSecs:      cfg.OptionalInt("idle_arb_max_leg_secs", 0),
		IdleArbMarginVerifyPct: cfg.OptionalInt("idle_arb_margin_verify_pct", 0),
		IdleArbBlacklist:       cfg.OptionalStringSlice("idle_arb_blacklist"),
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

func buildGoodsFactoryCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &goodsCmd.RunFactoryCoordinatorCommand{
		PlayerID:      playerID,
		TargetGood:    cfg.RequiredString("target_good"),
		SystemSymbol:  cfg.RequiredString("system_symbol"),
		ContainerID:   cfg.RequiredString("container_id"),
		MaxIterations: cfg.OptionalInt("max_iterations", 1),
		InputsOnly:    cfg.OptionalBool("inputs_only"),
	}
}

func buildManufacturingCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &goodsCmd.RunParallelManufacturingCoordinatorCommand{
		SystemSymbol:           cfg.RequiredString("system_symbol"),
		PlayerID:               playerID,
		ContainerID:            cfg.RequiredString("container_id"),
		MinPurchasePrice:       cfg.OptionalInt("min_price", 1000),
		MaxConcurrentTasks:     cfg.OptionalInt("max_workers", 3),
		MaxPipelines:           cfg.OptionalInt("max_pipelines", 3),
		MaxCollectionPipelines: cfg.OptionalInt("max_collection_pipelines", 0),
		Strategy:               cfg.OptionalStringDefault("strategy", "prefer-fabricate"),
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
	}
}

// buildTourCoordinatorCommand rebuilds the one-shot guarded tour command (sp-1ek0) from
// a persisted launch config so restart recovery can resume a RUNNING tour_run container.
// ContainerID comes from the recovery-supplied containerID (the persisted row's ID),
// mirroring arb_run/trade_route so the operation context and the runner's ship claim stay
// pinned across a restart. ship_symbol is required; the guard knobs default to 0 (the
// coordinator's own "0 → default" semantics: max_hops→6, max_spend→25% of treasury,
// replan_limit→2, working_capital_reserve→50k). A restart re-plans from current
// position/cargo, so recovery is cargo-aware, never a blind re-buy.
func buildTourCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.RunTourCoordinatorCommand{
		ShipSymbol:            cfg.RequiredString("ship_symbol"),
		PlayerID:              playerID,
		ContainerID:           containerID,
		AgentSymbol:           cfg.OptionalString("agent_symbol"),
		MaxHops:               cfg.OptionalInt("max_hops", 0),
		MaxSpend:              int64(cfg.OptionalInt("max_spend", 0)),
		MinMargin:             cfg.OptionalInt("min_margin", 0),
		ReplanLimit:           cfg.OptionalInt("replan_limit", 0),
		WorkingCapitalReserve: int64(cfg.OptionalInt("working_capital_reserve", 0)),
	}
}

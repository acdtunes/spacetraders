package grpc

import (
	"fmt"
	"strings"
	"time"

	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
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
		{CommandType: "scout_reposition", build: buildScoutRepositionCommand, CoordinatorOwnsIterations: true},
		{CommandType: "contract_workflow", build: buildContractWorkflowCommand},
		{CommandType: "contract_fleet_coordinator", build: buildContractFleetCoordinatorCommand},
		// trade_fleet_coordinator (sp-1278): a standing coordinator that loops forever
		// inside one Handle() call, so — like scout_post/contract_fleet — it is NOT a
		// CoordinatorOwnsIterations type; the container-level iteration budget (-1) is
		// irrelevant because Handle() never returns.
		{CommandType: "trade_fleet_coordinator", build: buildTradeFleetCoordinatorCommand},
		{CommandType: "purchase_ship", build: buildPurchaseShipCommand},
		{CommandType: "batch_purchase_ships", build: buildBatchPurchaseShipsCommand},
		{CommandType: "goods_factory_coordinator", build: buildGoodsFactoryCoordinatorCommand},
		{CommandType: "manufacturing_coordinator", build: buildManufacturingCoordinatorCommand},
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
	}
	// sp-1278: same live-config discipline for the trade-fleet coordinator. Its
	// [trade_fleet] knobs (enabled/cooldown/max-concurrent/per-tour caps) are cleared
	// and re-injected from the boot-loaded config.yaml on every build — creation and
	// recovery alike — so a config edit + restart retunes a recovered coordinator and
	// no persisted copy can shadow the live value.
	if commandType == "trade_fleet_coordinator" {
		s.resolveTradeFleetConfig(config)
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
		PlayerID:     shared.MustNewPlayerID(playerID),
		ShipSymbol:   cfg.RequiredString("ship_symbol"),
		Markets:      cfg.RequiredStringSlice("markets"),
		Iterations:   iterations,
		ScanInterval: time.Duration(cfg.OptionalInt("scan_interval_secs", 0)) * time.Second,
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
func buildScoutPostCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &scoutingCmd.RunScoutPostCoordinatorCommand{
		PlayerID:              shared.MustNewPlayerID(playerID),
		ContainerID:           cfg.RequiredNonEmptyString("container_id"),
		TickIntervalSecs:      cfg.OptionalInt("tick_interval_secs", 0),
		MarketDriftThreshold:  cfg.OptionalInt("market_drift_threshold", 0),
		MarketDriftMaxAgeSecs: cfg.OptionalInt("market_drift_max_age_secs", 0),
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
		// sp-agzj: unify the factory input floor with the fleet reserve. 0/absent → the
		// coordinator's immutable 50k lower bound; a set value (the fleet's 1M) raises it.
		WorkingCapitalReserve: cfg.OptionalInt("working_capital_reserve", 0),
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
		ShipSymbol:            cfg.RequiredString("ship_symbol"),
		PlayerID:              playerID,
		ContainerID:           containerID,
		AgentSymbol:           cfg.OptionalString("agent_symbol"),
		MaxHops:               cfg.OptionalInt("max_hops", 0),
		MaxSpend:              int64(cfg.OptionalInt("max_spend", 0)),
		MinMargin:             cfg.OptionalInt("min_margin", 0),
		ReplanLimit:           cfg.OptionalInt("replan_limit", 0),
		WorkingCapitalReserve: int64(cfg.OptionalInt("working_capital_reserve", 0)),
		Iterations:            cfg.OptionalInt("iterations", 0),
		// Reposition-on-margins-death knobs (sp-zhii). reposition_disabled defaults to
		// false → the feature is ON for continuous runs (the captain filed sp-zhii to end
		// the whack-a-mole); the floor/K default to 0 → the coordinator's own
		// reposition{MinMargin,MaxCandidates}Default. reposition_in_progress / _target_*
		// are RUNTIME state the coordinator persists mid-jump (RULINGS #2), reloaded here
		// so a restart resumes the jump instead of re-planning at an intermediate hop.
		RepositionDisabled:       cfg.OptionalBool("reposition_disabled"),
		RepositionMinMargin:      cfg.OptionalInt("reposition_min_margin", 0),
		RepositionMaxCandidates:  cfg.OptionalInt("reposition_max_candidates", 0),
		RepositionInProgress:     cfg.OptionalBool("reposition_in_progress"),
		RepositionTargetSystem:   cfg.OptionalString("reposition_target_system"),
		RepositionTargetWaypoint: cfg.OptionalString("reposition_target_waypoint"),
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
	}
}

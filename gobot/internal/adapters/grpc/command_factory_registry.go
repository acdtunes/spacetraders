package grpc

import (
	"fmt"
	"strings"

	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
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
	build       func(cfg *configReader, playerID int, containerID string) interface{}
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

func containerSpecList() []ContainerSpec {
	return []ContainerSpec{
		{CommandType: "scout_tour", build: buildScoutTourCommand},
		{CommandType: "contract_workflow", build: buildContractWorkflowCommand},
		{CommandType: "contract_fleet_coordinator", build: buildContractFleetCoordinatorCommand},
		{CommandType: "purchase_ship", build: buildPurchaseShipCommand},
		{CommandType: "batch_purchase_ships", build: buildBatchPurchaseShipsCommand},
		{CommandType: "goods_factory_coordinator", build: buildGoodsFactoryCoordinatorCommand},
		{CommandType: "manufacturing_coordinator", build: buildManufacturingCoordinatorCommand},
		{CommandType: "gas_coordinator", build: buildGasCoordinatorCommand},
		{CommandType: "trade_route", build: buildTradeRouteCoordinatorCommand},
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
	return &scoutingCmd.ScoutTourCommand{
		PlayerID:   shared.MustNewPlayerID(playerID),
		ShipSymbol: cfg.RequiredString("ship_symbol"),
		Markets:    cfg.RequiredStringSlice("markets"),
		Iterations: cfg.RequiredInt("iterations"),
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
// reserve for a specific circuit without a redeploy.
func buildTradeRouteCoordinatorCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &tradingCmd.RunTradeRouteCoordinatorCommand{
		ShipSymbol:            cfg.RequiredString("ship_symbol"),
		SystemSymbol:          cfg.RequiredString("system_symbol"),
		PlayerID:              playerID,
		ContainerID:           containerID,
		MaxVisits:             cfg.OptionalInt("max_visits", 0),
		WorkingCapitalReserve: cfg.OptionalInt("working_capital_reserve", 0),
	}
}

package grpc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func newFactoryTestServer() *DaemonServer {
	s := &DaemonServer{containerSpecs: make(map[string]ContainerSpec)}
	s.registerContainerSpecs()
	return s
}

func jsonRoundTrip(t *testing.T, launchConfig map[string]interface{}) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(launchConfig)
	require.NoError(t, err)
	var persisted map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &persisted))
	return persisted
}

func TestRecoveryFactoryRebuildsCommandFromLaunchConfig(t *testing.T) {
	s := newFactoryTestServer()
	playerID := 7
	pid := shared.MustNewPlayerID(playerID)

	cases := []struct {
		name         string
		commandType  string
		containerID  string
		launchConfig map[string]interface{}
		want         interface{}
	}{
		{
			name:        "scout_tour",
			commandType: "scout_tour",
			containerID: "scout-1",
			launchConfig: map[string]interface{}{
				"ship_symbol": "SHIP-A",
				"markets":     []string{"M1", "M2"},
				"iterations":  3,
			},
			want: &scoutingCmd.ScoutTourCommand{
				PlayerID:   pid,
				ShipSymbol: "SHIP-A",
				Markets:    []string{"M1", "M2"},
				Iterations: 3,
			},
		},
		{
			name:        "contract_workflow",
			commandType: "contract_workflow",
			containerID: "worker-1",
			launchConfig: map[string]interface{}{
				"ship_symbol":    "SHIP-A",
				"coordinator_id": "coord-1",
			},
			want: &contractCmd.RunWorkflowCommand{
				ShipSymbol:    "SHIP-A",
				PlayerID:      pid,
				ContainerID:   "worker-1",
				CoordinatorID: "coord-1",
			},
		},
		{
			name:        "contract_fleet_coordinator",
			commandType: "contract_fleet_coordinator",
			containerID: "fleet-1",
			launchConfig: map[string]interface{}{
				"ship_symbols": []interface{}{},
				"container_id": "fleet-1",
			},
			want: &contractCmd.RunFleetCoordinatorCommand{
				PlayerID:    pid,
				ShipSymbols: []string{},
				ContainerID: "fleet-1",
			},
		},
		{
			name:        "purchase_ship",
			commandType: "purchase_ship",
			containerID: "purchase-1",
			launchConfig: map[string]interface{}{
				"ship_symbol": "SHIP-A",
				"ship_type":   "SHIP_PROBE",
				"shipyard":    "WP-YARD",
			},
			want: &shipyardCmd.PurchaseShipCommand{
				PurchasingShipSymbol: "SHIP-A",
				ShipType:             "SHIP_PROBE",
				PlayerID:             pid,
				ShipyardWaypoint:     "WP-YARD",
			},
		},
		{
			name:        "batch_purchase_ships",
			commandType: "batch_purchase_ships",
			containerID: "batch-1",
			launchConfig: map[string]interface{}{
				"ship_symbol": "SHIP-A",
				"ship_type":   "SHIP_MINING_DRONE",
				"quantity":    4,
				"max_budget":  120000,
				"shipyard":    "WP-YARD",
			},
			want: &shipyardCmd.BatchPurchaseShipsCommand{
				PurchasingShipSymbol: "SHIP-A",
				ShipType:             "SHIP_MINING_DRONE",
				Quantity:             4,
				MaxBudget:            120000,
				PlayerID:             pid,
				ShipyardWaypoint:     "WP-YARD",
			},
		},
		{
			name:        "goods_factory_coordinator",
			commandType: "goods_factory_coordinator",
			containerID: "goods-1",
			launchConfig: map[string]interface{}{
				"target_good":    "MICROPROCESSORS",
				"system_symbol":  "X1-TEST",
				"container_id":   "goods-1",
				"max_iterations": 5,
			},
			want: &goodsCmd.RunFactoryCoordinatorCommand{
				PlayerID:      playerID,
				TargetGood:    "MICROPROCESSORS",
				SystemSymbol:  "X1-TEST",
				ContainerID:   "goods-1",
				MaxIterations: 5,
			},
		},
		{
			name:        "goods_factory_coordinator defaults max_iterations",
			commandType: "goods_factory_coordinator",
			containerID: "goods-2",
			launchConfig: map[string]interface{}{
				"target_good":   "IRON",
				"system_symbol": "X1-TEST",
				"container_id":  "goods-2",
			},
			want: &goodsCmd.RunFactoryCoordinatorCommand{
				PlayerID:      playerID,
				TargetGood:    "IRON",
				SystemSymbol:  "X1-TEST",
				ContainerID:   "goods-2",
				MaxIterations: 1,
			},
		},
		{
			name:        "manufacturing_coordinator",
			commandType: "manufacturing_coordinator",
			containerID: "mfg-1",
			launchConfig: map[string]interface{}{
				"system_symbol":            "X1-TEST",
				"min_price":                2000,
				"max_workers":              5,
				"max_pipelines":            4,
				"max_collection_pipelines": 2,
				"min_balance":              50000,
				"container_id":             "mfg-1",
				"mode":                     "parallel_task_based",
				"strategy":                 "smart",
			},
			want: &goodsCmd.RunParallelManufacturingCoordinatorCommand{
				SystemSymbol:           "X1-TEST",
				PlayerID:               playerID,
				ContainerID:            "mfg-1",
				MinPurchasePrice:       2000,
				MaxConcurrentTasks:     5,
				MaxPipelines:           4,
				MaxCollectionPipelines: 2,
				Strategy:               "smart",
			},
		},
		{
			name:        "manufacturing_coordinator defaults",
			commandType: "manufacturing_coordinator",
			containerID: "mfg-2",
			launchConfig: map[string]interface{}{
				"system_symbol": "X1-TEST",
				"container_id":  "mfg-2",
				"strategy":      "",
			},
			want: &goodsCmd.RunParallelManufacturingCoordinatorCommand{
				SystemSymbol:           "X1-TEST",
				PlayerID:               playerID,
				ContainerID:            "mfg-2",
				MinPurchasePrice:       1000,
				MaxConcurrentTasks:     3,
				MaxPipelines:           3,
				MaxCollectionPipelines: 0,
				Strategy:               "prefer-fabricate",
			},
		},
		{
			name:        "gas_coordinator",
			commandType: "gas_coordinator",
			containerID: "gas-1",
			launchConfig: map[string]interface{}{
				"gas_operation_id": "gas-1",
				"gas_giant":        "WP-GIANT",
				"siphon_ships":     []string{"SIPHON-1", "SIPHON-2"},
				"storage_ships":    []string{"STORE-1"},
				"container_id":     "gas-1",
				"force":            true,
				"dry_run":          true,
				"max_leg_time":     30,
			},
			want: &gasCmd.RunGasCoordinatorCommand{
				GasOperationID: "gas-1",
				PlayerID:       pid,
				GasGiant:       "WP-GIANT",
				SiphonShips:    []string{"SIPHON-1", "SIPHON-2"},
				StorageShips:   []string{"STORE-1"},
				ContainerID:    "gas-1",
				Force:          true,
				DryRun:         true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.buildCommandForType(tc.commandType, jsonRoundTrip(t, tc.launchConfig), playerID, tc.containerID)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestRecoveryFactoryRejectsMissingOrWrongTypedFields(t *testing.T) {
	s := newFactoryTestServer()
	playerID := 7

	cases := []struct {
		name        string
		commandType string
		config      map[string]interface{}
		wantErrPart string
	}{
		{"scout_tour missing ship_symbol", "scout_tour", map[string]interface{}{"markets": []interface{}{"M1"}, "iterations": 1.0}, "ship_symbol"},
		{"scout_tour markets wrong type", "scout_tour", map[string]interface{}{"ship_symbol": "S", "markets": "M1", "iterations": 1.0}, "markets"},
		{"scout_tour market entry wrong type", "scout_tour", map[string]interface{}{"ship_symbol": "S", "markets": []interface{}{1.0}, "iterations": 1.0}, "market"},
		{"scout_tour missing iterations", "scout_tour", map[string]interface{}{"ship_symbol": "S", "markets": []interface{}{"M1"}}, "iterations"},
		{"contract_workflow missing ship_symbol", "contract_workflow", map[string]interface{}{"coordinator_id": "c"}, "ship_symbol"},
		{"contract_fleet_coordinator missing container_id", "contract_fleet_coordinator", map[string]interface{}{}, "container_id"},
		{"purchase_ship missing ship_type", "purchase_ship", map[string]interface{}{"ship_symbol": "S"}, "ship_type"},
		{"purchase_ship missing ship_symbol", "purchase_ship", map[string]interface{}{"ship_type": "T"}, "ship_symbol"},
		{"batch_purchase missing quantity", "batch_purchase_ships", map[string]interface{}{"ship_symbol": "S", "ship_type": "T", "max_budget": 1.0}, "quantity"},
		{"batch_purchase wrong typed max_budget", "batch_purchase_ships", map[string]interface{}{"ship_symbol": "S", "ship_type": "T", "quantity": 1.0, "max_budget": "lots"}, "max_budget"},
		{"goods missing target_good", "goods_factory_coordinator", map[string]interface{}{"container_id": "c", "system_symbol": "X"}, "target_good"},
		{"goods missing system_symbol", "goods_factory_coordinator", map[string]interface{}{"container_id": "c", "target_good": "G"}, "system_symbol"},
		{"goods missing container_id", "goods_factory_coordinator", map[string]interface{}{"target_good": "G", "system_symbol": "X"}, "container_id"},
		{"manufacturing missing container_id", "manufacturing_coordinator", map[string]interface{}{"system_symbol": "X"}, "container_id"},
		{"manufacturing missing system_symbol", "manufacturing_coordinator", map[string]interface{}{"container_id": "c"}, "system_symbol"},
		{"gas missing gas_operation_id", "gas_coordinator", map[string]interface{}{"gas_giant": "G", "container_id": "c", "siphon_ships": []interface{}{"A"}, "storage_ships": []interface{}{"B"}}, "gas_operation_id"},
		{"gas empty gas_giant", "gas_coordinator", map[string]interface{}{"gas_operation_id": "o", "gas_giant": "", "container_id": "c", "siphon_ships": []interface{}{"A"}, "storage_ships": []interface{}{"B"}}, "gas_giant"},
		{"gas missing siphon_ships", "gas_coordinator", map[string]interface{}{"gas_operation_id": "o", "gas_giant": "G", "container_id": "c", "storage_ships": []interface{}{"B"}}, "siphon"},
		{"gas siphon entry wrong type", "gas_coordinator", map[string]interface{}{"gas_operation_id": "o", "gas_giant": "G", "container_id": "c", "siphon_ships": []interface{}{1.0}, "storage_ships": []interface{}{"B"}}, "siphon"},
		{"gas missing storage and transport ships", "gas_coordinator", map[string]interface{}{"gas_operation_id": "o", "gas_giant": "G", "container_id": "c", "siphon_ships": []interface{}{"A"}}, "storage_ships"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.buildCommandForType(tc.commandType, tc.config, playerID, "recover-1")
			require.Error(t, err)
			require.Nil(t, got)
			require.Contains(t, err.Error(), tc.wantErrPart)
		})
	}
}

func TestGasRecoveryFactoryFallsBackToLegacyTransportShipsKey(t *testing.T) {
	s := newFactoryTestServer()

	got, err := s.buildCommandForType("gas_coordinator", map[string]interface{}{
		"gas_operation_id": "op-1",
		"gas_giant":        "WP-GIANT",
		"container_id":     "gas-1",
		"siphon_ships":     []interface{}{"SIPHON-1"},
		"transport_ships":  []interface{}{"LEGACY-1", "LEGACY-2"},
	}, 7, "gas-1")

	require.NoError(t, err)
	cmd, ok := got.(*gasCmd.RunGasCoordinatorCommand)
	require.True(t, ok)
	require.Equal(t, []string{"LEGACY-1", "LEGACY-2"}, cmd.StorageShips)
}

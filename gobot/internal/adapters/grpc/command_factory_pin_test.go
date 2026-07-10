package grpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
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
			// sp-zixw: scan_interval_secs is the on-disk form of ScanInterval
			// (whole seconds — the coordinator persists
			// int(cmd.ScanInterval.Seconds()) via PersistScoutTourWorker). A restart
			// must rebuild the exact interval the coordinator derived from the
			// post's freshness target, not silently fall back to the direct-launch
			// 15m default (which only applies when the key is absent, as in the
			// "scout_tour" case above).
			name:        "scout_tour with scan_interval_secs",
			commandType: "scout_tour",
			containerID: "scout-2",
			launchConfig: map[string]interface{}{
				"ship_symbol":        "SHIP-B",
				"markets":            []string{"M1"},
				"iterations":         2,
				"scan_interval_secs": 600,
			},
			want: &scoutingCmd.ScoutTourCommand{
				PlayerID:     pid,
				ShipSymbol:   "SHIP-B",
				Markets:      []string{"M1"},
				Iterations:   2,
				ScanInterval: 600 * time.Second,
			},
		},
		{
			// sp-cxpq: a RUNNING scout_post_coordinator must rebuild from its launch
			// config on restart so it re-adopts its posts + assignments and respawns
			// each post's tour. Like contract_fleet_coordinator it loops internally, so
			// container_id comes from config; tick_interval_secs round-trips its knob.
			name:        "scout_post_coordinator",
			commandType: "scout_post_coordinator",
			containerID: "scoutpost-1",
			launchConfig: map[string]interface{}{
				"container_id":       "scoutpost-1",
				"tick_interval_secs": 45,
			},
			want: &scoutingCmd.RunScoutPostCoordinatorCommand{
				PlayerID:         pid,
				ContainerID:      "scoutpost-1",
				TickIntervalSecs: 45,
			},
		},
		{
			name:        "scout_post_coordinator defaults tick_interval_secs",
			commandType: "scout_post_coordinator",
			containerID: "scoutpost-2",
			launchConfig: map[string]interface{}{
				"container_id": "scoutpost-2",
			},
			want: &scoutingCmd.RunScoutPostCoordinatorCommand{
				PlayerID:         pid,
				ContainerID:      "scoutpost-2",
				TickIntervalSecs: 0,
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
			// sp-snmb: dedicated_ships/standby_stations are optional config keys
			// populated from the operator's --dedicated-ships/--standby-stations
			// CLI flags. The case above (neither key present) must still default
			// both fields to nil - covered implicitly since its `want` leaves
			// them unset.
			name:        "contract_fleet_coordinator with dedicated fleet params",
			commandType: "contract_fleet_coordinator",
			containerID: "fleet-2",
			launchConfig: map[string]interface{}{
				"ship_symbols":     []interface{}{},
				"container_id":     "fleet-2",
				"dedicated_ships":  []string{"TORWIND-4", "TORWIND-5"},
				"standby_stations": []string{"X1-TEST-J56", "X1-TEST-E42"},
			},
			want: &contractCmd.RunFleetCoordinatorCommand{
				PlayerID:        pid,
				ShipSymbols:     []string{},
				ContainerID:     "fleet-2",
				DedicatedShips:  []string{"TORWIND-4", "TORWIND-5"},
				StandbyStations: []string{"X1-TEST-J56", "X1-TEST-E42"},
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
		{
			// sp-zewt: a RUNNING trade_route container must rebuild from its launch
			// config on restart. Without this registry entry recovery fails with
			// "unknown command type" and the hull is force-released (the vjwb orphan);
			// with it, the circuit resumes. ContainerID comes from the recovery-supplied
			// containerID param (like contract_workflow), not the config map.
			name:        "trade_route",
			commandType: "trade_route",
			containerID: "trade-1",
			launchConfig: map[string]interface{}{
				"ship_symbol":   "SHIP-A",
				"system_symbol": "X1-TR",
				"container_id":  "trade-1",
				"max_visits":    20,
			},
			want: &tradingCmd.RunTradeRouteCoordinatorCommand{
				ShipSymbol:   "SHIP-A",
				SystemSymbol: "X1-TR",
				PlayerID:     playerID,
				ContainerID:  "trade-1",
				MaxVisits:    20,
			},
		},
		{
			name:        "trade_route defaults max_visits",
			commandType: "trade_route",
			containerID: "trade-2",
			launchConfig: map[string]interface{}{
				"ship_symbol":   "SHIP-B",
				"system_symbol": "X1-TR",
				"container_id":  "trade-2",
			},
			want: &tradingCmd.RunTradeRouteCoordinatorCommand{
				ShipSymbol:   "SHIP-B",
				SystemSymbol: "X1-TR",
				PlayerID:     playerID,
				ContainerID:  "trade-2",
				MaxVisits:    0,
			},
		},
		{
			// sp-1ek0: a RUNNING tour_run container must rebuild from its launch config
			// on restart (invariant 4) so the tour resumes (cargo-aware re-plan from
			// current state) instead of orphaning the hull. ContainerID comes from the
			// recovery-supplied param; the guard knobs round-trip through their config keys.
			name:        "tour_run",
			commandType: "tour_run",
			containerID: "tour-1",
			launchConfig: map[string]interface{}{
				"ship_symbol":             "SHIP-A",
				"container_id":            "tour-1",
				"agent_symbol":            "TORWIND",
				"max_hops":                4,
				"max_spend":               300000,
				"min_margin":              5,
				"replan_limit":            2,
				"working_capital_reserve": 60000,
			},
			want: &tradingCmd.RunTourCoordinatorCommand{
				ShipSymbol:            "SHIP-A",
				PlayerID:              playerID,
				ContainerID:           "tour-1",
				AgentSymbol:           "TORWIND",
				MaxHops:               4,
				MaxSpend:              300000,
				MinMargin:             5,
				ReplanLimit:           2,
				WorkingCapitalReserve: 60000,
			},
		},
		{
			// sp-m5kv: a CONTINUOUS tour (iterations=-1) must round-trip through the
			// launch config so a restart rebuilds the run STILL continuous (RULINGS #2)
			// — the coordinator re-plans from the recovered position/cargo and keeps
			// touring until margins die, rather than collapsing to a single tour.
			name:        "tour_run continuous",
			commandType: "tour_run",
			containerID: "tour-inf",
			launchConfig: map[string]interface{}{
				"ship_symbol":  "SHIP-C",
				"container_id": "tour-inf",
				"iterations":   -1,
			},
			want: &tradingCmd.RunTourCoordinatorCommand{
				ShipSymbol:  "SHIP-C",
				PlayerID:    playerID,
				ContainerID: "tour-inf",
				Iterations:  -1,
			},
		},
		{
			// Bare config → the coordinator's own "0 → default" semantics per knob
			// (iterations 0 → one tour, normalized inside the coordinator's loop).
			name:        "tour_run defaults",
			commandType: "tour_run",
			containerID: "tour-2",
			launchConfig: map[string]interface{}{
				"ship_symbol":  "SHIP-B",
				"container_id": "tour-2",
			},
			want: &tradingCmd.RunTourCoordinatorCommand{
				ShipSymbol:  "SHIP-B",
				PlayerID:    playerID,
				ContainerID: "tour-2",
			},
		},
		{
			// sp-dkj7: a restart-rebuilt arb_run must reload prior_attempt_cost — the
			// RUNTIME buy cost a fresh run persisted into this config the moment it bought —
			// so the resumed run reports honest P&L (RULINGS #2) instead of TotalCost=0.
			// Round-tripped through JSON (int → float64), so this also pins the float
			// coercion the persisted config forces.
			name:        "arb_run with persisted prior cost",
			commandType: "arb_run",
			containerID: "arb-1",
			launchConfig: map[string]interface{}{
				"ship_symbol":             "SHIP-A",
				"container_id":            "arb-1",
				"good":                    "WIDGET",
				"buy_at":                  "X1-TR-EXPORT",
				"sell_at":                 "X1-TR-IMPORT",
				"max_units":               40,
				"max_spend":               100000,
				"min_margin":              500,
				"working_capital_reserve": 50000,
				"prior_attempt_cost":      80000,
			},
			want: &tradingCmd.RunArbCoordinatorCommand{
				ShipSymbol:            "SHIP-A",
				PlayerID:              playerID,
				ContainerID:           "arb-1",
				Good:                  "WIDGET",
				BuyAt:                 "X1-TR-EXPORT",
				SellAt:                "X1-TR-IMPORT",
				MaxUnits:              40,
				MaxSpend:              100000,
				MinMargin:             500,
				WorkingCapitalReserve: 50000,
				PriorAttemptCost:      80000,
			},
		},
		{
			// A fresh arb_run (never persisted a cost) rebuilds with prior_attempt_cost=0 —
			// the honest fail-open floor, so a resume that beat the persist under-reports
			// rather than over-counts.
			name:        "arb_run without persisted cost defaults to zero",
			commandType: "arb_run",
			containerID: "arb-2",
			launchConfig: map[string]interface{}{
				"ship_symbol":  "SHIP-B",
				"container_id": "arb-2",
				"good":         "WIDGET",
				"buy_at":       "X1-TR-EXPORT",
				"sell_at":      "X1-TR-IMPORT",
			},
			want: &tradingCmd.RunArbCoordinatorCommand{
				ShipSymbol:       "SHIP-B",
				PlayerID:         playerID,
				ContainerID:      "arb-2",
				Good:             "WIDGET",
				BuyAt:            "X1-TR-EXPORT",
				SellAt:           "X1-TR-IMPORT",
				PriorAttemptCost: 0,
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
		{"scout_post_coordinator missing container_id", "scout_post_coordinator", map[string]interface{}{}, "container_id"},
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
		{"trade_route missing ship_symbol", "trade_route", map[string]interface{}{"system_symbol": "X1-TR"}, "ship_symbol"},
		{"trade_route missing system_symbol", "trade_route", map[string]interface{}{"ship_symbol": "SHIP-A"}, "system_symbol"},
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

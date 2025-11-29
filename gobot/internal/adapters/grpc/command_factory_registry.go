package grpc

import (
	"fmt"

	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/goods/commands"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// registerCommandFactories registers command factories for container recovery
// Adding a new container type only requires adding a factory here - no changes to recovery logic
func (s *DaemonServer) registerCommandFactories() {
	// Scout tour factory
	s.commandFactories["scout_tour"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		shipSymbol, ok := config["ship_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_symbol")
		}

		marketsRaw, ok := config["markets"].([]interface{})
		if !ok {
			return nil, fmt.Errorf("missing or invalid markets")
		}

		markets := make([]string, len(marketsRaw))
		for i, m := range marketsRaw {
			markets[i], ok = m.(string)
			if !ok {
				return nil, fmt.Errorf("invalid market entry at index %d", i)
			}
		}

		iterations, ok := config["iterations"].(float64)
		if !ok {
			return nil, fmt.Errorf("missing or invalid iterations")
		}

		return &scoutingCmd.ScoutTourCommand{
			PlayerID:   shared.MustNewPlayerID(int(playerID)),
			ShipSymbol: shipSymbol,
			Markets:    markets,
			Iterations: int(iterations),
		}, nil
	}

	// Contract workflow factory (single contract execution)
	s.commandFactories["contract_workflow"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		shipSymbol, ok := config["ship_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_symbol")
		}

		coordinatorID, _ := config["coordinator_id"].(string) // Optional

		return &contractCmd.RunWorkflowCommand{
			ShipSymbol:         shipSymbol,
			PlayerID:           shared.MustNewPlayerID(playerID),
			CoordinatorID:      coordinatorID,
			CompletionCallback: nil, // Will be set by container runner if needed
		}, nil
	}

	// Contract fleet coordinator factory (multi-ship coordination)
	s.commandFactories["contract_fleet_coordinator"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		containerID, ok := config["container_id"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid container_id")
		}

		// ship_symbols is deprecated and no longer required (dynamic discovery is used)
		// Pass empty array for backward compatibility
		return &contractCmd.RunFleetCoordinatorCommand{
			PlayerID:    shared.MustNewPlayerID(playerID),
			ShipSymbols: []string{}, // Deprecated field, no longer used
			ContainerID: containerID,
		}, nil
	}

	// Arbitrage coordinator factory (multi-ship trading coordination)
	s.commandFactories["arbitrage_coordinator"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		containerID, ok := config["container_id"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid container_id")
		}

		systemSymbol, ok := config["system_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid system_symbol")
		}

		minMargin, _ := config["min_margin"].(float64) // Optional, defaults in handler
		maxWorkers, _ := config["max_workers"].(int)   // Optional, defaults in handler
		minBalance, _ := config["min_balance"].(int)   // Optional, defaults to 0 (no limit)

		return &tradingCmd.RunArbitrageCoordinatorCommand{
			SystemSymbol: systemSymbol,
			PlayerID:     playerID,
			ContainerID:  containerID,
			MinMargin:    minMargin,
			MaxWorkers:   maxWorkers,
			MinBalance:   minBalance,
		}, nil
	}

	// Purchase ship factory
	s.commandFactories["purchase_ship"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		shipSymbol, ok := config["ship_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_symbol")
		}

		shipType, ok := config["ship_type"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_type")
		}

		shipyardWaypoint, _ := config["shipyard"].(string) // Optional

		return &shipyardCmd.PurchaseShipCommand{
			PurchasingShipSymbol: shipSymbol,
			ShipType:             shipType,
			PlayerID:             shared.MustNewPlayerID(playerID),
			ShipyardWaypoint:     shipyardWaypoint,
		}, nil
	}

	// Batch purchase ships factory
	s.commandFactories["batch_purchase_ships"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		shipSymbol, ok := config["ship_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_symbol")
		}

		shipType, ok := config["ship_type"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid ship_type")
		}

		quantity, ok := config["quantity"].(float64)
		if !ok {
			return nil, fmt.Errorf("missing or invalid quantity")
		}

		maxBudget, ok := config["max_budget"].(float64)
		if !ok {
			return nil, fmt.Errorf("missing or invalid max_budget")
		}

		shipyardWaypoint, _ := config["shipyard"].(string) // Optional

		return &shipyardCmd.BatchPurchaseShipsCommand{
			PurchasingShipSymbol: shipSymbol,
			ShipType:             shipType,
			Quantity:             int(quantity),
			MaxBudget:            int(maxBudget),
			PlayerID:             shared.MustNewPlayerID(playerID),
			ShipyardWaypoint:     shipyardWaypoint,
		}, nil
	}

	// Goods factory coordinator factory
	s.commandFactories["goods_factory_coordinator"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		containerID, ok := config["container_id"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid container_id")
		}

		targetGood, ok := config["target_good"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid target_good")
		}

		systemSymbol, ok := config["system_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid system_symbol")
		}

		// Extract max_iterations from config (default to 1 if not present for backward compatibility)
		maxIterations := 1
		if val, ok := config["max_iterations"]; ok {
			switch v := val.(type) {
			case int:
				maxIterations = v
			case float64:
				maxIterations = int(v)
			}
		}

		return &goodsCmd.RunFactoryCoordinatorCommand{
			PlayerID:      playerID,
			TargetGood:    targetGood,
			SystemSymbol:  systemSymbol,
			ContainerID:   containerID,
			MaxIterations: maxIterations,
		}, nil
	}

	// Manufacturing coordinator factory (task-based pipeline)
	s.commandFactories["manufacturing_coordinator"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		containerID, ok := config["container_id"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid container_id")
		}

		systemSymbol, ok := config["system_symbol"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid system_symbol")
		}

		// Extract numeric config with defaults
		minPrice := 1000
		if val, ok := config["min_price"]; ok {
			switch v := val.(type) {
			case int:
				minPrice = v
			case float64:
				minPrice = int(v)
			}
		}

		maxWorkers := 3
		if val, ok := config["max_workers"]; ok {
			switch v := val.(type) {
			case int:
				maxWorkers = v
			case float64:
				maxWorkers = int(v)
			}
		}

		maxPipelines := 3
		if val, ok := config["max_pipelines"]; ok {
			switch v := val.(type) {
			case int:
				maxPipelines = v
			case float64:
				maxPipelines = int(v)
			}
		}

		// Extract strategy (default: prefer-fabricate for recursive manufacturing)
		strategy := "prefer-fabricate"
		if val, ok := config["strategy"].(string); ok && val != "" {
			strategy = val
		}

		return &tradingCmd.RunParallelManufacturingCoordinatorCommand{
			SystemSymbol:       systemSymbol,
			PlayerID:           playerID,
			ContainerID:        containerID,
			MinPurchasePrice:   minPrice,
			MaxConcurrentTasks: maxWorkers,
			MaxPipelines:       maxPipelines,
			Strategy:           strategy,
		}, nil
	}

	// Gas extraction coordinator factory
	s.commandFactories["gas_coordinator"] = func(config map[string]interface{}, playerID int) (interface{}, error) {
		gasOperationID, ok := config["gas_operation_id"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid gas_operation_id")
		}

		gasGiant, ok := config["gas_giant"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid gas_giant")
		}

		containerID, ok := config["container_id"].(string)
		if !ok {
			return nil, fmt.Errorf("missing or invalid container_id")
		}

		// Parse siphon ships
		siphonShipsRaw, ok := config["siphon_ships"].([]interface{})
		if !ok {
			return nil, fmt.Errorf("missing or invalid siphon_ships")
		}

		siphonShips := make([]string, len(siphonShipsRaw))
		for i, s := range siphonShipsRaw {
			siphonShips[i], ok = s.(string)
			if !ok {
				return nil, fmt.Errorf("invalid siphon ship at index %d", i)
			}
		}

		// Parse transport ships
		transportShipsRaw, ok := config["transport_ships"].([]interface{})
		if !ok {
			return nil, fmt.Errorf("missing or invalid transport_ships")
		}

		transportShips := make([]string, len(transportShipsRaw))
		for i, t := range transportShipsRaw {
			transportShips[i], ok = t.(string)
			if !ok {
				return nil, fmt.Errorf("invalid transport ship at index %d", i)
			}
		}

		return &gasCmd.RunGasCoordinatorCommand{
			GasOperationID: gasOperationID,
			PlayerID:       shared.MustNewPlayerID(playerID),
			GasGiant:       gasGiant,
			SiphonShips:    siphonShips,
			TransportShips: transportShips,
			ContainerID:    containerID,
		}, nil
	}
}

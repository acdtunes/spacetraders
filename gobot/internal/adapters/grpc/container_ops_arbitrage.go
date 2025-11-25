package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ArbitrageCoordinator creates an arbitrage coordinator for automated trading operations
func (s *DaemonServer) ArbitrageCoordinator(ctx context.Context, systemSymbol string, playerID int, minMargin float64, maxWorkers int, minBalance int) (string, error) {
	// Create container ID
	containerID := utils.GenerateContainerID("arbitrage_coordinator", systemSymbol)

	// Create arbitrage coordinator command
	cmd := &tradingCmd.RunArbitrageCoordinatorCommand{
		SystemSymbol: systemSymbol,
		PlayerID:     playerID,
		ContainerID:  containerID,
		MinMargin:    minMargin,
		MaxWorkers:   maxWorkers,
		MinBalance:   minBalance,
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeArbitrageCoordinator,
		playerID,
		-1, // Infinite iterations
		nil, // No parent container
		map[string]interface{}{
			"system_symbol": systemSymbol,
			"min_margin":    minMargin,
			"max_workers":   maxWorkers,
			"min_balance":   minBalance,
			"container_id":  containerID,
		},
		nil, // Use default RealClock
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "arbitrage_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// PersistArbitrageWorkerContainer creates (but does NOT start) an arbitrage worker container in DB
func (s *DaemonServer) PersistArbitrageWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	// Type assert to RunArbitrageWorkerCommand
	cmd, ok := command.(*tradingCmd.RunArbitrageWorkerCommand)
	if !ok {
		return daemon.ErrInvalidCommandType
	}

	// Extract opportunity details for metadata
	opportunity := cmd.Opportunity
	var parentContainerID *string
	if cmd.CoordinatorID != "" {
		parentContainerID = &cmd.CoordinatorID
	}

	// Create container entity (single iteration for worker containers)
	// Store full opportunity data for reconstruction
	iterations := 1
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeArbitrageWorker,
		int(playerID),
		iterations,
		parentContainerID, // Link to parent coordinator container
		map[string]interface{}{
			"ship_symbol":     cmd.ShipSymbol,
			"player_id":       cmd.PlayerID,
			"min_balance":     cmd.MinBalance,
			"good":            opportunity.Good(),
			"buy_market":      opportunity.BuyMarket().Symbol,
			"buy_x":           opportunity.BuyMarket().X,
			"buy_y":           opportunity.BuyMarket().Y,
			"buy_system":      opportunity.BuyMarket().SystemSymbol,
			"sell_market":     opportunity.SellMarket().Symbol,
			"sell_x":          opportunity.SellMarket().X,
			"sell_y":          opportunity.SellMarket().Y,
			"sell_system":     opportunity.SellMarket().SystemSymbol,
			"buy_price":       opportunity.BuyPrice(),
			"sell_price":      opportunity.SellPrice(),
			"profit_per_unit": opportunity.ProfitPerUnit(),
			"profit_margin":   opportunity.ProfitMargin(),
			"cargo_capacity":  opportunity.CargoCapacity(),
			"profit":          opportunity.EstimatedProfit(),
			"buy_supply":      opportunity.BuySupply(),
			"sell_activity":   opportunity.SellActivity(),
		},
		nil, // Use default RealClock for production
	)

	// Persist container (no CreateIfNoActiveWorker check needed - arbitrage allows multiple workers)
	if err := s.containerRepo.Add(ctx, containerEntity, "arbitrage_worker"); err != nil {
		return fmt.Errorf("failed to persist arbitrage worker container: %w", err)
	}

	return nil
}

// StartArbitrageWorkerContainer starts a previously persisted arbitrage worker container
func (s *DaemonServer) StartArbitrageWorkerContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	// Load container from database
	allContainers, err := s.containerRepo.ListAll(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	var containerModel *persistence.ContainerModel
	for _, c := range allContainers {
		if c.ID == containerID {
			containerModel = c
			break
		}
	}

	if containerModel == nil {
		return fmt.Errorf("container %s not found", containerID)
	}

	// Parse config
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Extract fields and reconstruct opportunity
	shipSymbol := config["ship_symbol"].(string)
	playerID := int(config["player_id"].(float64))
	minBalance := int(config["min_balance"].(float64))

	// Reconstruct waypoints
	buyMarket, err := shared.NewWaypoint(
		config["buy_market"].(string),
		config["buy_x"].(float64),
		config["buy_y"].(float64),
	)
	if err != nil {
		return fmt.Errorf("failed to create buy market waypoint: %w", err)
	}
	buyMarket.SystemSymbol = config["buy_system"].(string)

	sellMarket, err := shared.NewWaypoint(
		config["sell_market"].(string),
		config["sell_x"].(float64),
		config["sell_y"].(float64),
	)
	if err != nil {
		return fmt.Errorf("failed to create sell market waypoint: %w", err)
	}
	sellMarket.SystemSymbol = config["sell_system"].(string)

	// Extract supply/activity values with defaults
	buySupply := "MODERATE"
	if val, ok := config["buy_supply"].(string); ok && val != "" {
		buySupply = val
	}
	sellActivity := "WEAK"
	if val, ok := config["sell_activity"].(string); ok && val != "" {
		sellActivity = val
	}

	// Reconstruct opportunity using NewArbitrageOpportunity
	opportunity, err := trading.NewArbitrageOpportunity(
		config["good"].(string),
		buyMarket,
		sellMarket,
		int(config["buy_price"].(float64)),
		int(config["sell_price"].(float64)),
		int(config["cargo_capacity"].(float64)),
		buySupply,
		sellActivity,
		0.0, // minMargin not needed for reconstruction
	)
	if err != nil {
		return fmt.Errorf("failed to reconstruct opportunity: %w", err)
	}

	// Create command
	cmd := &tradingCmd.RunArbitrageWorkerCommand{
		ShipSymbol:  shipSymbol,
		Opportunity: opportunity,
		PlayerID:    playerID,
		ContainerID: containerID,
		MinBalance:  minBalance,
	}

	// Create container entity from model
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		1, // Worker containers are single iteration
		nil, // No parent container needed for reconstruction
		config,
		nil,
	)

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	if completionCallback != nil {
		runner.SetCompletionCallback(completionCallback)
	}
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return nil
}

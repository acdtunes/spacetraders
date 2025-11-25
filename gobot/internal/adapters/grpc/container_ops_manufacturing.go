package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/goods/services"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// PersistManufacturingWorkerContainer creates (but does NOT start) a manufacturing worker container in DB
func (s *DaemonServer) PersistManufacturingWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	// Type assert to RunManufacturingWorkerCommand
	cmd, ok := command.(*tradingCmd.RunManufacturingWorkerCommand)
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
	// Store opportunity data for reconstruction (simpler than arbitrage - no waypoint reconstruction needed)
	iterations := 1
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeManufacturingWorker,
		int(playerID),
		iterations,
		parentContainerID, // Link to parent coordinator container
		map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"player_id":      cmd.PlayerID,
			"system_symbol":  cmd.SystemSymbol,
			"good":           opportunity.Good(),
			"sell_market":    opportunity.SellMarket().Symbol,
			"sell_x":         opportunity.SellMarket().X,
			"sell_y":         opportunity.SellMarket().Y,
			"sell_system":    opportunity.SellMarket().SystemSymbol,
			"purchase_price": opportunity.PurchasePrice(),
			"activity":       opportunity.Activity(),
			"supply":         opportunity.Supply(),
			"tree_depth":     opportunity.TreeDepth(),
			"input_count":    opportunity.InputCount(),
			"score":          opportunity.Score(),
		},
		nil, // Use default RealClock for production
	)

	// Persist container
	if err := s.containerRepo.Add(ctx, containerEntity, "manufacturing_worker"); err != nil {
		return fmt.Errorf("failed to persist manufacturing worker container: %w", err)
	}

	return nil
}

// StartManufacturingWorkerContainer starts a previously persisted manufacturing worker container
func (s *DaemonServer) StartManufacturingWorkerContainer(
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

	// Extract fields
	shipSymbol := config["ship_symbol"].(string)
	playerID := int(config["player_id"].(float64))
	systemSymbol := config["system_symbol"].(string)

	// Reconstruct sell market waypoint
	sellMarket, err := shared.NewWaypoint(
		config["sell_market"].(string),
		config["sell_x"].(float64),
		config["sell_y"].(float64),
	)
	if err != nil {
		return fmt.Errorf("failed to create sell market waypoint: %w", err)
	}
	sellMarket.SystemSymbol = config["sell_system"].(string)

	// Extract optional fields with defaults
	activity := ""
	if val, ok := config["activity"].(string); ok {
		activity = val
	}
	supply := ""
	if val, ok := config["supply"].(string); ok {
		supply = val
	}

	// Rebuild dependency tree using supply chain resolver
	resolver := s.getSupplyChainResolver(playerID)
	dependencyTree, err := resolver.BuildDependencyTree(ctx, config["good"].(string), systemSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to rebuild dependency tree: %w", err)
	}

	// Reconstruct opportunity
	opportunity, err := trading.NewManufacturingOpportunity(
		config["good"].(string),
		sellMarket,
		int(config["purchase_price"].(float64)),
		activity,
		supply,
		dependencyTree,
	)
	if err != nil {
		return fmt.Errorf("failed to reconstruct manufacturing opportunity: %w", err)
	}

	// Extract coordinator ID if present
	coordinatorID := ""
	if containerModel.ParentContainerID != nil {
		coordinatorID = *containerModel.ParentContainerID
	}

	// Create command
	cmd := &tradingCmd.RunManufacturingWorkerCommand{
		ShipSymbol:    shipSymbol,
		Opportunity:   opportunity,
		PlayerID:      playerID,
		ContainerID:   containerID,
		CoordinatorID: coordinatorID,
		SystemSymbol:  systemSymbol,
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

// ManufacturingCoordinator creates a manufacturing coordinator for automated manufacturing operations
func (s *DaemonServer) ManufacturingCoordinator(ctx context.Context, systemSymbol string, playerID int, minPrice int, maxWorkers int, minBalance int) (string, error) {
	// Create container ID
	containerID := utils.GenerateContainerID("manufacturing_coordinator", systemSymbol)

	// Create manufacturing coordinator command
	cmd := &tradingCmd.RunManufacturingCoordinatorCommand{
		SystemSymbol:     systemSymbol,
		PlayerID:         playerID,
		ContainerID:      containerID,
		MinPurchasePrice: minPrice,
		MaxWorkers:       maxWorkers,
		MinBalance:       minBalance,
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeManufacturingCoordinator,
		playerID,
		-1, // Infinite iterations
		nil, // No parent container
		map[string]interface{}{
			"system_symbol": systemSymbol,
			"min_price":     minPrice,
			"max_workers":   maxWorkers,
			"min_balance":   minBalance,
			"container_id":  containerID,
		},
		nil, // Use default RealClock
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "manufacturing_coordinator"); err != nil {
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

// ParallelManufacturingCoordinator creates a parallel task-based manufacturing coordinator
func (s *DaemonServer) ParallelManufacturingCoordinator(ctx context.Context, systemSymbol string, playerID int, minPrice int, maxWorkers int, minBalance int) (string, error) {
	// Create container ID
	containerID := utils.GenerateContainerID("parallel_manufacturing", systemSymbol)

	// Create parallel manufacturing coordinator command
	cmd := &tradingCmd.RunParallelManufacturingCoordinatorCommand{
		SystemSymbol:       systemSymbol,
		PlayerID:           playerID,
		ContainerID:        containerID,
		MinPurchasePrice:   minPrice,
		MaxConcurrentTasks: maxWorkers, // Map maxWorkers to MaxConcurrentTasks
		MaxPipelines:       3,          // Default to 3 pipelines
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeManufacturingCoordinator, // Use standard coordinator type
		playerID,
		-1, // Infinite iterations
		nil, // No parent container
		map[string]interface{}{
			"system_symbol": systemSymbol,
			"min_price":     minPrice,
			"max_workers":   maxWorkers,
			"min_balance":   minBalance,
			"container_id":  containerID,
			"mode":          "parallel_task_based",
		},
		nil, // Use default RealClock
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "manufacturing_coordinator"); err != nil {
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

// getSupplyChainResolver creates a supply chain resolver for rebuilding dependency trees
func (s *DaemonServer) getSupplyChainResolver(playerID int) *goodsServices.SupplyChainResolver {
	marketRepo := persistence.NewMarketRepository(s.db)
	return goodsServices.NewSupplyChainResolver(goods.ExportToImportMap, marketRepo)
}

// PersistManufacturingTaskWorkerContainer creates (but does NOT start) a manufacturing task worker container in DB
// This is for task-based parallel manufacturing where tasks are already persisted in manufacturing_tasks table
func (s *DaemonServer) PersistManufacturingTaskWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	// Type assert to RunManufacturingTaskWorkerCommand
	cmd, ok := command.(*tradingCmd.RunManufacturingTaskWorkerCommand)
	if !ok {
		return daemon.ErrInvalidCommandType
	}

	// Get task details
	task := cmd.Task
	var parentContainerID *string
	if cmd.CoordinatorID != "" {
		parentContainerID = &cmd.CoordinatorID
	}

	// Create container entity - store task reference (task is already persisted separately)
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeManufacturingTaskWorker,
		int(playerID),
		1, // Single iteration for worker containers
		parentContainerID,
		map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"player_id":      cmd.PlayerID,
			"task_id":        task.ID(),
			"task_type":      string(task.TaskType()),
			"good":           task.Good(),
			"pipeline_id":    task.PipelineID(),
			"coordinator_id": cmd.CoordinatorID,
		},
		nil, // Use default RealClock
	)

	// Persist container
	if err := s.containerRepo.Add(ctx, containerEntity, "manufacturing_task_worker"); err != nil {
		return fmt.Errorf("failed to persist manufacturing task worker container: %w", err)
	}

	return nil
}

// StartManufacturingTaskWorkerContainer starts a previously persisted manufacturing task worker container
func (s *DaemonServer) StartManufacturingTaskWorkerContainer(
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

	// Extract fields
	shipSymbol := config["ship_symbol"].(string)
	playerID := int(config["player_id"].(float64))
	taskID := config["task_id"].(string)
	coordinatorID := ""
	if val, ok := config["coordinator_id"].(string); ok {
		coordinatorID = val
	}

	// Load task from task repository
	taskRepo := persistence.NewGormManufacturingTaskRepository(s.db)
	task, err := taskRepo.FindByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("failed to load task %s: %w", taskID, err)
	}
	if task == nil {
		return fmt.Errorf("task %s not found", taskID)
	}

	// Create command
	cmd := &tradingCmd.RunManufacturingTaskWorkerCommand{
		ShipSymbol:    shipSymbol,
		Task:          task,
		PlayerID:      playerID,
		ContainerID:   containerID,
		CoordinatorID: coordinatorID,
	}

	// Create container entity from model
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		1, // Worker containers are single iteration
		nil,
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

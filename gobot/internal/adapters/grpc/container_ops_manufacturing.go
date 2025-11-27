package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ParallelManufacturingCoordinator creates a parallel task-based manufacturing coordinator
// SINGLETON: Only one coordinator per system is allowed. If one already exists, returns its ID.
func (s *DaemonServer) ParallelManufacturingCoordinator(ctx context.Context, systemSymbol string, playerID int, minPrice int, maxWorkers int, maxPipelines int, minBalance int, strategy string) (string, error) {
	// SINGLETON CHECK: Only one coordinator per system
	existingCoordinator, err := s.containerRepo.FindActiveCoordinatorByTypeAndSystem(
		ctx,
		string(container.ContainerTypeManufacturingCoordinator),
		systemSymbol,
		playerID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to check for existing coordinator: %w", err)
	}
	if existingCoordinator != nil {
		fmt.Printf("SINGLETON: Coordinator already exists for system %s: %s (status: %s)\n",
			systemSymbol, existingCoordinator.ID, existingCoordinator.Status)
		return existingCoordinator.ID, nil
	}

	// Create container ID
	containerID := utils.GenerateContainerID("parallel_manufacturing", systemSymbol)

	// CLEANUP: Stop any orphaned workers from previous crashed coordinators
	// This can happen if a coordinator crashed and left workers running
	// Find ALL manufacturing worker containers for this player and mark them as stopped
	orphanedCount, err := s.containerRepo.StopAllOrphanedManufacturingWorkers(ctx, playerID)
	if err != nil {
		fmt.Printf("Warning: failed to clean orphaned workers: %v\n", err)
	} else if orphanedCount > 0 {
		fmt.Printf("CLEANUP: Stopped %d orphaned worker containers\n", orphanedCount)
	}

	// Create parallel manufacturing coordinator command
	cmd := &tradingCmd.RunParallelManufacturingCoordinatorCommand{
		SystemSymbol:       systemSymbol,
		PlayerID:           playerID,
		ContainerID:        containerID,
		MinPurchasePrice:   minPrice,
		MaxConcurrentTasks: maxWorkers,   // Map maxWorkers to MaxConcurrentTasks
		MaxPipelines:       maxPipelines, // Use provided maxPipelines
		Strategy:           strategy,     // Acquisition strategy (prefer-buy, prefer-fabricate, smart)
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
			"max_pipelines": maxPipelines,
			"min_balance":   minBalance,
			"container_id":  containerID,
			"mode":          "parallel_task_based",
			"strategy":      strategy,
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
			"ship_symbol":     cmd.ShipSymbol,
			"player_id":       cmd.PlayerID,
			"task_id":         task.ID(),
			"task_type":       string(task.TaskType()),
			"good":            task.Good(),
			"pipeline_id":     task.PipelineID(),
			"coordinator_id":  cmd.CoordinatorID,
			"pipeline_number": cmd.PipelineNumber,
			"product_good":    cmd.ProductGood,
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

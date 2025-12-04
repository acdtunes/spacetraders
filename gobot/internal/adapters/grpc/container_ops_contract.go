package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// BatchContractWorkflow handles batch contract workflow requests
func (s *DaemonServer) BatchContractWorkflow(ctx context.Context, shipSymbol string, iterations, playerID int) (string, error) {
	// Create container ID
	containerID := utils.GenerateContainerID("batch_contract_workflow", shipSymbol)

	// Delegate to ContractWorkflow
	// Note: iterations parameter is ignored for now - ContractWorkflow always does 1 iteration
	// TODO: Support multiple iterations by updating container metadata
	return s.ContractWorkflow(ctx, containerID, shipSymbol, playerID, "")
}

// ContractWorkflow creates and starts a contract workflow container
func (s *DaemonServer) ContractWorkflow(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	playerID int,
	coordinatorID string,
) (string, error) {
	// Persist container to DB
	if err := s.PersistContractWorkflow(ctx, containerID, shipSymbol, playerID, coordinatorID); err != nil {
		return "", err
	}

	// Start the container
	if err := s.StartContractWorkflow(ctx, containerID); err != nil {
		return "", err
	}

	return containerID, nil
}

// PersistContractWorkflow creates a contract workflow container in DB (does NOT start it)
func (s *DaemonServer) PersistContractWorkflow(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	playerID int,
	coordinatorID string,
) error {
	// Create container entity (single iteration for worker containers)
	iterations := 1
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeContractWorkflow,
		playerID,
		iterations,
		&coordinatorID, // Link to parent coordinator container
		map[string]interface{}{
			"ship_symbol":    shipSymbol,
			"coordinator_id": coordinatorID,
		},
		nil, // Use default RealClock for production
	)

	// Atomically check for existing worker and create new one
	// This prevents multiple workers from running simultaneously
	created, err := s.containerRepo.CreateIfNoActiveWorker(ctx, containerEntity, "contract_workflow")
	if err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	if !created {
		return fmt.Errorf("CONTRACT_WORKFLOW container already running for player %d", playerID)
	}

	return nil
}

// StartContractWorkflow starts a previously persisted contract workflow container
func (s *DaemonServer) StartContractWorkflow(
	ctx context.Context,
	containerID string,
) error {
	// We need playerID to load the container, but we don't have it here
	// Solution: Load from all players or add playerID parameter
	// For now, use a workaround: query by ID only (add new repository method)
	// Temporary: Use ListAll and filter
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
	coordinatorID, _ := config["coordinator_id"].(string)

	// Create command
	cmd := &contractCmd.RunWorkflowCommand{
		ShipSymbol:    shipSymbol,
		PlayerID:      shared.MustNewPlayerID(containerModel.PlayerID),
		ContainerID:   containerModel.ID,
		CoordinatorID: coordinatorID,
	}

	// Create container entity from model
	// Worker containers always have 1 iteration
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		1, // Worker containers are single iteration
		nil, // No parent container
		config,
		nil,
	)

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return nil
}

// ContractFleetCoordinator creates a fleet coordinator for multi-ship contract operations
// Ships are discovered dynamically - no pre-assignment needed
func (s *DaemonServer) ContractFleetCoordinator(ctx context.Context, shipSymbols []string, playerID int) (string, error) {
	// Create container ID using player ID instead of ship symbol (no ships pre-assigned)
	containerID := utils.GenerateContainerID("contract_fleet_coordinator", fmt.Sprintf("player-%d", playerID))

	// Create contract fleet coordinator command (no ship symbols - uses dynamic discovery)
	cmd := &contractCmd.RunFleetCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(playerID),
		ShipSymbols: nil, // Dynamic discovery - no pre-assignment
		ContainerID: containerID,
	}

	// No ship symbols metadata needed
	var shipSymbolsInterface []interface{}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeContractFleetCoordinator,
		playerID,
		-1, // Infinite iterations
		nil, // No parent container
		map[string]interface{}{
			"ship_symbols": shipSymbolsInterface,
			"container_id": containerID,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "contract_fleet_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

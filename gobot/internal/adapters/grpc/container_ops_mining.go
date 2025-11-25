package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	miningCmd "github.com/andrescamacho/spacetraders-go/internal/application/mining/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// MiningOperationResult contains the result of a mining operation
type MiningOperationResult struct {
	ContainerID   string
	AsteroidField string
	MarketSymbol  string
	ShipRoutes    []common.ShipRouteDTO
	Errors        []string
}

// MiningOperation starts a mining operation with Transport-as-Sink pattern
func (s *DaemonServer) MiningOperation(
	ctx context.Context,
	asteroidField string,
	minerShips []string,
	transportShips []string,
	topNOres int,
	miningType string,
	force bool,
	dryRun bool,
	maxLegTime int,
	playerID int,
) (*MiningOperationResult, error) {
	// Validate we have either an asteroid field or mining type
	if asteroidField == "" && miningType == "" {
		return nil, fmt.Errorf("no asteroid field specified and no mining type provided")
	}

	// Create container ID
	var containerID string
	if dryRun {
		containerID = utils.GenerateContainerID("mining_dry_run", minerShips[0])
	} else {
		containerID = utils.GenerateContainerID("mining_coordinator", minerShips[0])
	}

	// Create mining coordinator command
	cmd := &miningCmd.RunCoordinatorCommand{
		MiningOperationID: containerID,
		PlayerID:          shared.MustNewPlayerID(playerID),
		AsteroidField:     asteroidField,
		MinerShips:        minerShips,
		TransportShips:    transportShips,
		TopNOres:          topNOres,
		ContainerID:       containerID,
		MiningType:        miningType,
		Force:             force,
		DryRun:            dryRun,
		MaxLegTime:        maxLegTime,
	}

	// Convert ship arrays to interface{} for metadata
	minerShipsInterface := make([]interface{}, len(minerShips))
	for i, s := range minerShips {
		minerShipsInterface[i] = s
	}

	transportShipsInterface := make([]interface{}, len(transportShips))
	for i, s := range transportShips {
		transportShipsInterface[i] = s
	}

	// Set iterations: 1 for dry-run (single execution), -1 for normal (infinite)
	iterations := -1
	if dryRun {
		iterations = 1
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeMiningCoordinator,
		playerID,
		iterations,
		nil, // No parent container
		map[string]interface{}{
			"mining_operation_id": containerID,
			"asteroid_field":      asteroidField,
			"miner_ships":         minerShipsInterface,
			"transport_ships":     transportShipsInterface,
			"top_n_ores":          topNOres,
			"container_id":        containerID,
			"mining_type":         miningType,
			"force":               force,
			"dry_run":             dryRun,
			"max_leg_time":        maxLegTime,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "mining_coordinator"); err != nil {
		return nil, fmt.Errorf("failed to persist container: %w", err)
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

	return &MiningOperationResult{
		ContainerID:   containerID,
		AsteroidField: asteroidField,
	}, nil
}

// extractSystemSymbol extracts system symbol from a waypoint symbol
func extractSystemSymbol(waypointSymbol string) string {
	// Waypoint format: X1-GZ7-B12 -> System: X1-GZ7
	parts := make([]string, 0)
	current := ""
	for _, c := range waypointSymbol {
		if c == '-' {
			parts = append(parts, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return waypointSymbol
}

// PersistMiningWorkerContainer creates a mining worker container in DB (does NOT start it)
func (s *DaemonServer) PersistMiningWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	cmd, ok := command.(*miningCmd.RunWorkerCommand)
	if !ok {
		return fmt.Errorf("invalid command type for mining worker")
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeMiningWorker,
		int(playerID),
		1, // Worker containers are single iteration
		&cmd.CoordinatorID, // Link to parent coordinator container
		map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"asteroid_field": cmd.AsteroidField,
			"top_n_ores":     cmd.TopNOres,
			"coordinator_id": cmd.CoordinatorID,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "mining_worker"); err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	// Cache the command with channels for StartMiningWorkerContainer
	s.pendingWorkerCommandsMu.Lock()
	s.pendingWorkerCommands[containerID] = cmd
	s.pendingWorkerCommandsMu.Unlock()

	return nil
}

// StartMiningWorkerContainer starts a previously persisted mining worker container
func (s *DaemonServer) StartMiningWorkerContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	// Try to get cached command with channels first
	s.pendingWorkerCommandsMu.Lock()
	cachedCmd, hasCached := s.pendingWorkerCommands[containerID]
	if hasCached {
		delete(s.pendingWorkerCommands, containerID)
	}
	s.pendingWorkerCommandsMu.Unlock()

	var cmd *miningCmd.RunWorkerCommand
	var config map[string]interface{}
	var playerID int

	if hasCached {
		// Use cached command with channels
		cmd = cachedCmd.(*miningCmd.RunWorkerCommand)
		playerID = cmd.PlayerID.Value()
		config = map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"asteroid_field": cmd.AsteroidField,
			"top_n_ores":     cmd.TopNOres,
			"coordinator_id": cmd.CoordinatorID,
		}
	} else {
		// Fallback: Load from database (for recovery - channels will be nil)
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
		if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
			return fmt.Errorf("failed to parse config: %w", err)
		}

		// Extract fields
		shipSymbol := config["ship_symbol"].(string)
		asteroidField := config["asteroid_field"].(string)
		topNOres := 3
		if val, ok := config["top_n_ores"].(float64); ok {
			topNOres = int(val)
		}
		coordinatorID, _ := config["coordinator_id"].(string)

		playerID = containerModel.PlayerID
		cmd = &miningCmd.RunWorkerCommand{
			ShipSymbol:    shipSymbol,
			PlayerID:      shared.MustNewPlayerID(playerID),
			AsteroidField: asteroidField,
			TopNOres:      topNOres,
			CoordinatorID: coordinatorID,
			Coordinator:   nil, // Not available from DB recovery - worker must reconnect
		}
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeMiningWorker,
		playerID,
		1, // Worker containers are single iteration
		nil, // No parent container
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

// PersistTransportWorkerContainer creates a transport worker container in DB (does NOT start it)
func (s *DaemonServer) PersistTransportWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	cmd, ok := command.(*miningCmd.RunTransportWorkerCommand)
	if !ok {
		return fmt.Errorf("invalid command type for transport worker")
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTransportWorker,
		int(playerID),
		1, // Worker containers are single iteration
		&cmd.CoordinatorID, // Link to parent coordinator container
		map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"asteroid_field": cmd.AsteroidField,
			"coordinator_id": cmd.CoordinatorID,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "transport_worker"); err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	// Cache the command with channels for StartTransportWorkerContainer
	s.pendingWorkerCommandsMu.Lock()
	s.pendingWorkerCommands[containerID] = cmd
	s.pendingWorkerCommandsMu.Unlock()

	return nil
}

// StartTransportWorkerContainer starts a previously persisted transport worker container
func (s *DaemonServer) StartTransportWorkerContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	// Try to get cached command with channels first
	s.pendingWorkerCommandsMu.Lock()
	cachedCmd, hasCached := s.pendingWorkerCommands[containerID]
	if hasCached {
		delete(s.pendingWorkerCommands, containerID)
	}
	s.pendingWorkerCommandsMu.Unlock()

	var cmd *miningCmd.RunTransportWorkerCommand
	var config map[string]interface{}
	var playerID int

	if hasCached {
		// Use cached command with channels
		cmd = cachedCmd.(*miningCmd.RunTransportWorkerCommand)
		playerID = cmd.PlayerID.Value()
		config = map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"asteroid_field": cmd.AsteroidField,
			"coordinator_id": cmd.CoordinatorID,
		}
	} else {
		// Fallback: Load from database (for recovery - channels will be nil)
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
		if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
			return fmt.Errorf("failed to parse config: %w", err)
		}

		// Extract fields
		shipSymbol := config["ship_symbol"].(string)
		asteroidField := config["asteroid_field"].(string)
		coordinatorID, _ := config["coordinator_id"].(string)

		playerID = containerModel.PlayerID
		marketSymbol, _ := config["market_symbol"].(string)
		cmd = &miningCmd.RunTransportWorkerCommand{
			ShipSymbol:    shipSymbol,
			PlayerID:      shared.MustNewPlayerID(playerID),
			AsteroidField: asteroidField,
			MarketSymbol:  marketSymbol,
			CoordinatorID: coordinatorID,
			Coordinator:   nil, // Not available from DB recovery - worker must reconnect
		}
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTransportWorker,
		playerID,
		1, // Worker containers are single iteration
		nil, // No parent container
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

// PersistMiningCoordinatorContainer creates a mining coordinator container in DB (does NOT start it)
func (s *DaemonServer) PersistMiningCoordinatorContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	cmd, ok := command.(*miningCmd.RunCoordinatorCommand)
	if !ok {
		return fmt.Errorf("invalid command type for mining coordinator")
	}

	// Convert ship arrays to interface{} for JSON
	minerShipsInterface := make([]interface{}, len(cmd.MinerShips))
	for i, s := range cmd.MinerShips {
		minerShipsInterface[i] = s
	}

	transportShipsInterface := make([]interface{}, len(cmd.TransportShips))
	for i, s := range cmd.TransportShips {
		transportShipsInterface[i] = s
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeMiningCoordinator,
		int(playerID),
		-1, // Infinite iterations for coordinator
		nil, // No parent container
		map[string]interface{}{
			"mining_operation_id": cmd.MiningOperationID,
			"asteroid_field":      cmd.AsteroidField,
			"miner_ships":         minerShipsInterface,
			"transport_ships":     transportShipsInterface,
			"top_n_ores":          cmd.TopNOres,
			"container_id":        cmd.ContainerID,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "mining_coordinator"); err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	return nil
}

// StartMiningCoordinatorContainer starts a previously persisted mining coordinator container
func (s *DaemonServer) StartMiningCoordinatorContainer(
	ctx context.Context,
	containerID string,
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

	// Use factory to create command
	factory := s.commandFactories["mining_coordinator"]
	cmd, err := factory(config, containerModel.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to create command: %w", err)
	}

	// Create container entity from model
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		-1, // Coordinator runs indefinitely
		nil, // No parent container
		config,
		nil,
	)

	// Create and start container runner
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipAssignmentRepo)
	s.registerContainer(containerID, runner)

	// Start container in background
	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return nil
}

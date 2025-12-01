package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// GasExtractionOperationResult contains the result of a gas extraction operation
type GasExtractionOperationResult struct {
	ContainerID    string
	GasGiant       string
	ShipRoutes     []common.ShipRouteDTO
	Errors         []string
}

// GasExtractionOperation starts a gas extraction operation with siphon and storage ships.
// Storage ships stay at the gas giant and buffer cargo; delivery is handled by manufacturing pool via STORAGE_ACQUIRE_DELIVER tasks.
// SINGLETON: Only one coordinator per gas giant is allowed. If one already exists, returns its ID.
func (s *DaemonServer) GasExtractionOperation(
	ctx context.Context,
	gasGiant string,
	siphonShips []string,
	storageShips []string,
	force bool,
	dryRun bool,
	maxLegTime int,
	playerID int,
) (*GasExtractionOperationResult, error) {
	// Validate we have at least one siphon ship
	if len(siphonShips) == 0 {
		return nil, fmt.Errorf("at least one siphon ship is required")
	}

	// Auto-select gas giant if not specified
	if gasGiant == "" {
		selectedGasGiant, err := s.selectGasGiantForShip(ctx, siphonShips[0], playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to auto-select gas giant: %w", err)
		}
		gasGiant = selectedGasGiant
		fmt.Printf("Auto-selected gas giant: %s\n", gasGiant)
	}

	// SINGLETON CHECK: Only one coordinator per gas giant (skip for dry runs)
	if !dryRun {
		existingCoordinator, err := s.containerRepo.FindActiveGasCoordinator(ctx, gasGiant, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to check for existing gas coordinator: %w", err)
		}
		if existingCoordinator != nil {
			fmt.Printf("SINGLETON: Gas coordinator already exists for %s: %s (status: %s)\n",
				gasGiant, existingCoordinator.ID, existingCoordinator.Status)
			return &GasExtractionOperationResult{
				ContainerID: existingCoordinator.ID,
				GasGiant:    gasGiant,
			}, nil
		}
	}

	// Create container ID
	var containerID string
	if dryRun {
		containerID = utils.GenerateContainerID("gas_dry_run", siphonShips[0])
	} else {
		containerID = utils.GenerateContainerID("gas_coordinator", siphonShips[0])
	}

	// Create gas coordinator command
	cmd := &gasCmd.RunGasCoordinatorCommand{
		GasOperationID: containerID,
		PlayerID:       shared.MustNewPlayerID(playerID),
		GasGiant:       gasGiant,
		SiphonShips:    siphonShips,
		StorageShips:   storageShips,
		ContainerID:    containerID,
		Force:          force,
		DryRun:         dryRun,
	}

	// Convert ship arrays to interface{} for metadata
	siphonShipsInterface := make([]interface{}, len(siphonShips))
	for i, s := range siphonShips {
		siphonShipsInterface[i] = s
	}

	storageShipsInterface := make([]interface{}, len(storageShips))
	for i, s := range storageShips {
		storageShipsInterface[i] = s
	}

	// Set iterations: 1 for dry-run (single execution), -1 for normal (infinite)
	iterations := -1
	if dryRun {
		iterations = 1
	}

	// Create container for this operation
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeGasCoordinator,
		playerID,
		iterations,
		nil, // No parent container
		map[string]interface{}{
			"gas_operation_id": containerID,
			"gas_giant":        gasGiant,
			"siphon_ships":     siphonShipsInterface,
			"storage_ships":    storageShipsInterface,
			"container_id":     containerID,
			"force":            force,
			"dry_run":          dryRun,
			"max_leg_time":     maxLegTime,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "gas_coordinator"); err != nil {
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

	return &GasExtractionOperationResult{
		ContainerID: containerID,
		GasGiant:    gasGiant,
	}, nil
}

// PersistGasSiphonWorkerContainer creates a gas siphon worker container in DB (does NOT start it)
func (s *DaemonServer) PersistGasSiphonWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	cmd, ok := command.(*gasCmd.RunSiphonWorkerCommand)
	if !ok {
		return fmt.Errorf("invalid command type for gas siphon worker")
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeGasSiphonWorker,
		int(playerID),
		1, // Worker containers are single iteration
		&cmd.CoordinatorID, // Link to parent coordinator container
		map[string]interface{}{
			"ship_symbol":          cmd.ShipSymbol,
			"gas_giant":            cmd.GasGiant,
			"coordinator_id":       cmd.CoordinatorID,
			"storage_operation_id": cmd.StorageOperationID,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "gas_siphon_worker"); err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	// Cache the command with channels for StartGasSiphonWorkerContainer
	s.pendingWorkerCommandsMu.Lock()
	s.pendingWorkerCommands[containerID] = cmd
	s.pendingWorkerCommandsMu.Unlock()

	return nil
}

// StartGasSiphonWorkerContainer starts a previously persisted gas siphon worker container
func (s *DaemonServer) StartGasSiphonWorkerContainer(
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

	var cmd *gasCmd.RunSiphonWorkerCommand
	var config map[string]interface{}
	var playerID int

	if hasCached {
		// Use cached command with channels
		cmd = cachedCmd.(*gasCmd.RunSiphonWorkerCommand)
		playerID = cmd.PlayerID.Value()
		config = map[string]interface{}{
			"ship_symbol":          cmd.ShipSymbol,
			"gas_giant":            cmd.GasGiant,
			"coordinator_id":       cmd.CoordinatorID,
			"storage_operation_id": cmd.StorageOperationID,
		}
	} else {
		// Fallback: Load from database (for recovery)
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
		gasGiant := config["gas_giant"].(string)
		coordinatorID, _ := config["coordinator_id"].(string)
		storageOperationID, _ := config["storage_operation_id"].(string)

		playerID = containerModel.PlayerID
		cmd = &gasCmd.RunSiphonWorkerCommand{
			ShipSymbol:         shipSymbol,
			PlayerID:           shared.MustNewPlayerID(playerID),
			GasGiant:           gasGiant,
			CoordinatorID:      coordinatorID,
			StorageOperationID: storageOperationID,
		}
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeGasSiphonWorker,
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

// PersistGasTransportWorkerContainer creates a gas transport worker container in DB (does NOT start it)
func (s *DaemonServer) PersistGasTransportWorkerContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	cmd, ok := command.(*gasCmd.RunGasTransportWorkerCommand)
	if !ok {
		return fmt.Errorf("invalid command type for gas transport worker")
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeGasTransportWorker,
		int(playerID),
		1, // Worker containers are single iteration
		&cmd.CoordinatorID, // Link to parent coordinator container
		map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"gas_giant":      cmd.GasGiant,
			"coordinator_id": cmd.CoordinatorID,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "gas_transport_worker"); err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	// Cache the command with channels for StartGasTransportWorkerContainer
	s.pendingWorkerCommandsMu.Lock()
	s.pendingWorkerCommands[containerID] = cmd
	s.pendingWorkerCommandsMu.Unlock()

	return nil
}

// StartGasTransportWorkerContainer starts a previously persisted gas transport worker container
func (s *DaemonServer) StartGasTransportWorkerContainer(
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

	var cmd *gasCmd.RunGasTransportWorkerCommand
	var config map[string]interface{}
	var playerID int

	if hasCached {
		// Use cached command with channels
		cmd = cachedCmd.(*gasCmd.RunGasTransportWorkerCommand)
		playerID = cmd.PlayerID.Value()
		config = map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"gas_giant":      cmd.GasGiant,
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
		gasGiant := config["gas_giant"].(string)
		coordinatorID, _ := config["coordinator_id"].(string)

		playerID = containerModel.PlayerID
		cmd = &gasCmd.RunGasTransportWorkerCommand{
			ShipSymbol:    shipSymbol,
			PlayerID:      shared.MustNewPlayerID(playerID),
			GasGiant:      gasGiant,
			CoordinatorID: coordinatorID,
			Coordinator:   nil, // Not available from DB recovery - worker must reconnect
		}
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeGasTransportWorker,
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

// PersistStorageShipContainer creates (but does NOT start) a storage ship worker container in DB.
// The container will navigate the ship to the gas giant and register with storage coordinator.
func (s *DaemonServer) PersistStorageShipContainer(
	ctx context.Context,
	containerID string,
	playerID uint,
	command interface{},
) error {
	cmd, ok := command.(*gasCmd.RunStorageShipWorkerCommand)
	if !ok {
		return fmt.Errorf("invalid command type for storage ship worker")
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeStorageShip,
		int(playerID),
		-1, // Infinite - stays running until stopped
		&cmd.CoordinatorID, // Link to parent coordinator container
		map[string]interface{}{
			"ship_symbol":          cmd.ShipSymbol,
			"gas_giant":            cmd.GasGiant,
			"coordinator_id":       cmd.CoordinatorID,
			"storage_operation_id": cmd.StorageOperationID,
		},
		nil, // Use default RealClock for production
	)

	// Persist container to database
	if err := s.containerRepo.Add(ctx, containerEntity, "storage_ship"); err != nil {
		return fmt.Errorf("failed to persist container: %w", err)
	}

	// Cache the command for StartStorageShipContainer
	s.pendingWorkerCommandsMu.Lock()
	s.pendingWorkerCommands[containerID] = cmd
	s.pendingWorkerCommandsMu.Unlock()

	return nil
}

// StartStorageShipContainer starts a previously persisted storage ship worker container.
func (s *DaemonServer) StartStorageShipContainer(
	ctx context.Context,
	containerID string,
	completionCallback chan<- string,
) error {
	// Try to get cached command first
	s.pendingWorkerCommandsMu.Lock()
	cachedCmd, hasCached := s.pendingWorkerCommands[containerID]
	if hasCached {
		delete(s.pendingWorkerCommands, containerID)
	}
	s.pendingWorkerCommandsMu.Unlock()

	var cmd *gasCmd.RunStorageShipWorkerCommand
	var config map[string]interface{}
	var playerID int

	if hasCached {
		// Use cached command
		cmd = cachedCmd.(*gasCmd.RunStorageShipWorkerCommand)
		playerID = cmd.PlayerID.Value()
		config = map[string]interface{}{
			"ship_symbol":          cmd.ShipSymbol,
			"gas_giant":            cmd.GasGiant,
			"coordinator_id":       cmd.CoordinatorID,
			"storage_operation_id": cmd.StorageOperationID,
		}
	} else {
		// Fallback: Load from database (for recovery)
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
		gasGiant := config["gas_giant"].(string)
		coordinatorID, _ := config["coordinator_id"].(string)
		storageOperationID, _ := config["storage_operation_id"].(string)

		playerID = containerModel.PlayerID
		cmd = &gasCmd.RunStorageShipWorkerCommand{
			ShipSymbol:         shipSymbol,
			PlayerID:           shared.MustNewPlayerID(playerID),
			GasGiant:           gasGiant,
			CoordinatorID:      coordinatorID,
			StorageOperationID: storageOperationID,
		}
	}

	// Create container entity
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeStorageShip,
		playerID,
		-1, // Infinite - stays running until stopped
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

// Note: DaemonServer implements daemon.DaemonClient interface including gas methods
// The full interface check is done elsewhere to avoid circular dependencies

// selectGasGiantForShip auto-selects a gas giant based on ship location.
// Uses the ship's current system and finds the closest gas giant waypoint.
func (s *DaemonServer) selectGasGiantForShip(ctx context.Context, shipSymbol string, playerID int) (string, error) {
	// Get ship to determine system
	ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return "", fmt.Errorf("failed to get ship %s: %w", shipSymbol, err)
	}

	// Extract system from ship's current location
	systemSymbol := ship.CurrentLocation().SystemSymbol

	// List all waypoints in the system
	waypoints, err := s.waypointRepo.ListBySystem(ctx, systemSymbol)
	if err != nil {
		return "", fmt.Errorf("failed to list waypoints in system %s: %w", systemSymbol, err)
	}

	// Find gas giants
	var gasGiants []*shared.Waypoint
	for _, wp := range waypoints {
		if wp.Type == "GAS_GIANT" {
			gasGiants = append(gasGiants, wp)
		}
	}

	if len(gasGiants) == 0 {
		return "", fmt.Errorf("no gas giant found in system %s", systemSymbol)
	}

	// If there's only one, use it
	if len(gasGiants) == 1 {
		return gasGiants[0].Symbol, nil
	}

	// If multiple gas giants, select the closest one to the ship
	closestGasGiant := gasGiants[0]
	shipWaypoint := ship.CurrentLocation()
	minDistance := shipWaypoint.DistanceTo(closestGasGiant)

	for _, gg := range gasGiants[1:] {
		distance := shipWaypoint.DistanceTo(gg)
		if distance < minDistance {
			minDistance = distance
			closestGasGiant = gg
		}
	}

	return closestGasGiant.Symbol, nil
}

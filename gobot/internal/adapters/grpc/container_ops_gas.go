package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	gasCmd "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// GasExtractionOperationResult contains the result of a gas extraction operation
type GasExtractionOperationResult struct {
	ContainerID string
	GasGiant    string
	ShipRoutes  []common.ShipRouteDTO
	Errors      []string
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
	if len(siphonShips) == 0 {
		return nil, fmt.Errorf("at least one siphon ship is required")
	}

	if gasGiant == "" {
		selected, err := s.selectGasGiantForShip(ctx, siphonShips[0], playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to auto-select gas giant: %w", err)
		}
		gasGiant = selected
		fmt.Printf("Auto-selected gas giant: %s\n", gasGiant)
	}

	if !dryRun {
		existing, err := s.adoptExistingGasCoordinator(ctx, gasGiant, playerID)
		if err != nil || existing != nil {
			return existing, err
		}
	}

	containerIDPrefix := "gas_coordinator"
	if dryRun {
		containerIDPrefix = "gas_dry_run"
	}
	containerID := utils.GenerateContainerID(containerIDPrefix, siphonShips[0])

	config := map[string]interface{}{
		"gas_operation_id": containerID,
		"gas_giant":        gasGiant,
		"siphon_ships":     toInterfaceSlice(siphonShips),
		"storage_ships":    toInterfaceSlice(storageShips),
		"container_id":     containerID,
		"force":            force,
		"dry_run":          dryRun,
		"max_leg_time":     maxLegTime,
	}

	cmd, err := s.buildCommandForType("gas_coordinator", config, playerID, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create command: %w", err)
	}

	iterations := -1 // infinite; a dry run executes once
	if dryRun {
		iterations = 1
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeGasCoordinator,
		playerID,
		iterations,
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "gas_coordinator"); err != nil {
		return nil, fmt.Errorf("failed to persist container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Container")

	return &GasExtractionOperationResult{
		ContainerID: containerID,
		GasGiant:    gasGiant,
	}, nil
}

// adoptExistingGasCoordinator enforces one coordinator per gas giant: a live one is
// returned as the operation's result rather than starting a second. nil means none exists.
func (s *DaemonServer) adoptExistingGasCoordinator(ctx context.Context, gasGiant string, playerID int) (*GasExtractionOperationResult, error) {
	existing, err := s.containerRepo.FindActiveGasCoordinator(ctx, gasGiant, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to check for existing gas coordinator: %w", err)
	}
	if existing == nil {
		return nil, nil
	}
	fmt.Printf("SINGLETON: Gas coordinator already exists for %s: %s (status: %s)\n",
		gasGiant, existing.ID, existing.Status)
	return &GasExtractionOperationResult{ContainerID: existing.ID, GasGiant: gasGiant}, nil
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

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeGasSiphonWorker,
		int(playerID),
		1,                  // Worker containers are single iteration
		&cmd.CoordinatorID, // Link to parent coordinator container
		map[string]interface{}{
			"ship_symbol":          cmd.ShipSymbol,
			"gas_giant":            cmd.GasGiant,
			"coordinator_id":       cmd.CoordinatorID,
			"storage_operation_id": cmd.StorageOperationID,
		},
		nil, // Use default RealClock for production
	)

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
		containerModel, err := s.findContainerModelByID(ctx, containerID)
		if err != nil {
			return err
		}

		if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
			return fmt.Errorf("failed to parse config: %w", err)
		}

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

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeGasSiphonWorker,
		playerID,
		1,   // Worker containers are single iteration
		nil, // No parent container
		config,
		nil,
	)

	s.startContainerRunner(containerEntity, cmd, containerID, "Container")

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

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeStorageShip,
		int(playerID),
		-1,                 // Infinite - stays running until stopped
		&cmd.CoordinatorID, // Link to parent coordinator container
		map[string]interface{}{
			"ship_symbol":          cmd.ShipSymbol,
			"gas_giant":            cmd.GasGiant,
			"coordinator_id":       cmd.CoordinatorID,
			"storage_operation_id": cmd.StorageOperationID,
		},
		nil, // Use default RealClock for production
	)

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
) error {
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
		containerModel, err := s.findContainerModelByID(ctx, containerID)
		if err != nil {
			return err
		}

		if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
			return fmt.Errorf("failed to parse config: %w", err)
		}

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

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeStorageShip,
		playerID,
		-1, // Infinite - stays running until stopped
		nil,
		config,
		nil,
	)

	s.startContainerRunner(containerEntity, cmd, containerID, "Container")

	return nil
}

// Note: DaemonServer implements daemon.DaemonClient interface including gas methods
// The full interface check is done elsewhere to avoid circular dependencies

// selectGasGiantForShip auto-selects a gas giant based on ship location.
// Uses the ship's current system and finds the closest gas giant waypoint.
func (s *DaemonServer) selectGasGiantForShip(ctx context.Context, shipSymbol string, playerID int) (string, error) {
	ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return "", fmt.Errorf("failed to get ship %s: %w", shipSymbol, err)
	}

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

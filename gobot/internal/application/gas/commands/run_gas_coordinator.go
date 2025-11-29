package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainShared "github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// RunGasCoordinatorCommand manages a fleet of siphon ships with storage ship buffering.
// Transport/delivery is handled by the manufacturing pool via STORAGE_ACQUIRE_DELIVER tasks.
type RunGasCoordinatorCommand struct {
	GasOperationID string
	PlayerID       domainShared.PlayerID
	GasGiant       string   // Waypoint symbol of the gas giant
	SiphonShips    []string // Ships for siphoning (need siphon mounts + gas processor)
	StorageShips   []string // Ships that buffer cargo in orbit (stay at gas giant)
	ContainerID    string   // Coordinator's own container ID
	Force          bool     // Override fuel validation warnings
	DryRun         bool     // If true, only plan routes without starting workers
}

// RunGasCoordinatorResponse contains the coordinator execution results
type RunGasCoordinatorResponse struct {
	TotalTransfers      int
	TotalUnitsDelivered int
	Errors              []string
	// Dry-run results
	GasGiant   string                // Selected gas giant (dry-run)
	ShipRoutes []common.ShipRouteDTO // Planned routes for all ships (dry-run)
}

// RunGasCoordinatorHandler implements the gas fleet coordinator logic
type RunGasCoordinatorHandler struct {
	mediator            common.Mediator
	shipRepo            navigation.ShipRepository
	storageOpRepo       storage.StorageOperationRepository
	shipAssignmentRepo  container.ShipAssignmentRepository
	daemonClient        daemon.DaemonClient
	waypointRepo        system.WaypointRepository
	storageCoordinator  storage.StorageCoordinator
}

// NewRunGasCoordinatorHandler creates a new gas coordinator handler
func NewRunGasCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	storageOpRepo storage.StorageOperationRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	daemonClient daemon.DaemonClient,
	waypointRepo system.WaypointRepository,
	storageCoordinator storage.StorageCoordinator,
) *RunGasCoordinatorHandler {
	return &RunGasCoordinatorHandler{
		mediator:            mediator,
		shipRepo:            shipRepo,
		storageOpRepo:       storageOpRepo,
		shipAssignmentRepo:  shipAssignmentRepo,
		daemonClient:        daemonClient,
		waypointRepo:        waypointRepo,
		storageCoordinator:  storageCoordinator,
	}
}

// Handle executes the gas coordinator command
func (h *RunGasCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunGasCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &RunGasCoordinatorResponse{
		TotalTransfers:      0,
		TotalUnitsDelivered: 0,
		Errors:              []string{},
	}

	// Validate we have storage ships
	if len(cmd.StorageShips) == 0 {
		return nil, fmt.Errorf("at least one storage ship is required for gas extraction")
	}

	// Auto-select gas giant if not provided
	if cmd.GasGiant == "" {
		gasGiant, err := h.autoSelectGasGiant(ctx, cmd, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to auto-select gas giant: %w", err)
		}
		cmd.GasGiant = gasGiant
		logger.Log("INFO", "Gas giant auto-selected", map[string]interface{}{
			"action":    "auto_select_gas_giant",
			"gas_giant": gasGiant,
		})
	}

	// Dry-run mode: plan routes and return without starting workers
	if cmd.DryRun {
		return h.planDryRunRoutes(ctx, cmd, logger)
	}

	// Step 1: Create or load storage operation
	storageOp, err := h.getOrCreateStorageOperation(ctx, cmd, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to get/create storage operation: %w", err)
	}

	// Step 2: Register storage ships with coordinator
	logger.Log("INFO", "Registering storage ships with coordinator", map[string]interface{}{
		"action":        "register_storage_ships",
		"storage_count": len(cmd.StorageShips),
		"operation_id":  cmd.GasOperationID,
	})

	if err := h.registerStorageShips(ctx, cmd, logger); err != nil {
		return nil, fmt.Errorf("failed to register storage ships: %w", err)
	}

	// Step 3: Create ship pool assignments (only siphon ships now)
	logger.Log("INFO", "Ship pool creation initiated", map[string]interface{}{
		"action":        "create_ship_pool",
		"siphon_count":  len(cmd.SiphonShips),
		"storage_count": len(cmd.StorageShips),
		"container_id":  cmd.ContainerID,
	})

	// Assign both siphon and storage ships to this container
	allShips := append(cmd.SiphonShips, cmd.StorageShips...)
	if err := h.createPoolAssignments(ctx, allShips, cmd.ContainerID, cmd.PlayerID.Value()); err != nil {
		return nil, fmt.Errorf("failed to create pool assignments: %w", err)
	}

	// Track all spawned worker container IDs for cleanup on shutdown
	var workerContainerIDs []string

	// Step 4: Spawn siphon workers (they deposit to storage ships)
	logger.Log("INFO", "Siphon workers spawning", map[string]interface{}{
		"action":       "spawn_siphons",
		"siphon_count": len(cmd.SiphonShips),
	})
	for _, siphonShip := range cmd.SiphonShips {
		containerID, err := h.spawnSiphonWorker(ctx, cmd, siphonShip)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to spawn siphon worker for %s: %v", siphonShip, err)
			logger.Log("ERROR", "Siphon worker spawn failed", map[string]interface{}{
				"action":      "spawn_siphon",
				"ship_symbol": siphonShip,
				"error":       err.Error(),
			})
			result.Errors = append(result.Errors, errMsg)
		} else {
			workerContainerIDs = append(workerContainerIDs, containerID)
		}
	}

	// Step 5: Main monitoring loop (simple - no transport coordination needed)
	// Transport/delivery is handled by manufacturing pool via STORAGE_ACQUIRE_DELIVER tasks
	logger.Log("INFO", "Gas extraction operation started with storage system", map[string]interface{}{
		"action":        "start_operation",
		"container_id":  cmd.ContainerID,
		"gas_giant":     cmd.GasGiant,
		"siphon_ships":  len(cmd.SiphonShips),
		"storage_ships": len(cmd.StorageShips),
	})

	// Simple wait loop - just monitor for shutdown
	<-ctx.Done()

	// Context cancelled, cleanup
	logger.Log("INFO", "Gas coordinator shutdown requested", map[string]interface{}{
		"action":       "shutdown_coordinator",
		"container_id": cmd.ContainerID,
		"worker_count": len(workerContainerIDs),
	})

	// Stop all worker containers
	for _, containerID := range workerContainerIDs {
		logger.Log("INFO", "Worker container stopping", map[string]interface{}{
			"action":              "stop_worker",
			"worker_container_id": containerID,
		})
		_ = h.daemonClient.StopContainer(ctx, containerID)
	}

	// Unregister storage ships from coordinator
	for _, shipSymbol := range cmd.StorageShips {
		h.storageCoordinator.UnregisterStorageShip(shipSymbol)
	}

	// Release pool assignments
	h.releasePoolAssignments(ctx, cmd.ContainerID, cmd.PlayerID.Value())

	// Update storage operation status
	storageOp.Stop()
	h.storageOpRepo.Update(ctx, storageOp)

	return result, ctx.Err()
}

// getOrCreateStorageOperation loads or creates the storage operation using domain/storage
func (h *RunGasCoordinatorHandler) getOrCreateStorageOperation(
	ctx context.Context,
	cmd *RunGasCoordinatorCommand,
	logger common.ContainerLogger,
) (*storage.StorageOperation, error) {
	// Try to load existing operation
	operation, err := h.storageOpRepo.FindByID(ctx, cmd.GasOperationID)
	if err == nil && operation != nil {
		// Resume existing operation
		logger.Log("INFO", "Resuming existing storage operation", map[string]interface{}{
			"action":       "resume_operation",
			"operation_id": cmd.GasOperationID,
			"status":       operation.Status(),
		})
		if operation.IsPending() {
			operation.Start()
			h.storageOpRepo.Update(ctx, operation)
		}
		return operation, nil
	}

	// Determine supported goods for gas extraction
	// For gas giants, common gases are HYDROCARBON, LIQUID_NITROGEN, LIQUID_HYDROGEN
	supportedGoods := []string{"HYDROCARBON", "LIQUID_NITROGEN", "LIQUID_HYDROGEN"}

	// Create new storage operation
	operation, err = storage.NewStorageOperation(
		cmd.GasOperationID,
		cmd.PlayerID.Value(),
		cmd.GasGiant,
		storage.OperationTypeGasSiphon,
		cmd.SiphonShips,
		cmd.StorageShips,
		supportedGoods,
		nil, // Use default clock
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage operation: %w", err)
	}

	if err := h.storageOpRepo.Create(ctx, operation); err != nil {
		return nil, fmt.Errorf("failed to persist operation: %w", err)
	}

	operation.Start()
	h.storageOpRepo.Update(ctx, operation)

	logger.Log("INFO", "Created new storage operation", map[string]interface{}{
		"action":          "create_operation",
		"operation_id":    cmd.GasOperationID,
		"supported_goods": supportedGoods,
	})

	return operation, nil
}

// registerStorageShips creates StorageShip entities and registers them with the coordinator
func (h *RunGasCoordinatorHandler) registerStorageShips(
	ctx context.Context,
	cmd *RunGasCoordinatorCommand,
	logger common.ContainerLogger,
) error {
	for _, shipSymbol := range cmd.StorageShips {
		// Load ship from API to get current cargo state
		ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to load storage ship %s: %w", shipSymbol, err)
		}

		// Convert current cargo to initial cargo map
		initialCargo := make(map[string]int)
		for _, item := range ship.Cargo().Inventory {
			initialCargo[item.Symbol] = item.Units
		}

		// Create storage ship entity
		storageShip, err := storage.NewStorageShip(
			shipSymbol,
			cmd.GasGiant, // Will be positioned at gas giant
			cmd.GasOperationID,
			ship.Cargo().Capacity,
			initialCargo,
		)
		if err != nil {
			return fmt.Errorf("failed to create storage ship entity %s: %w", shipSymbol, err)
		}

		// Register with coordinator
		if err := h.storageCoordinator.RegisterStorageShip(storageShip); err != nil {
			logger.Log("WARNING", "Storage ship may already be registered", map[string]interface{}{
				"action":      "register_storage_ship",
				"ship_symbol": shipSymbol,
				"error":       err.Error(),
			})
			// Continue - ship might already be registered from recovery
		}

		logger.Log("INFO", "Storage ship registered", map[string]interface{}{
			"action":         "register_storage_ship",
			"ship_symbol":    shipSymbol,
			"cargo_capacity": ship.Cargo().Capacity,
			"current_cargo":  ship.Cargo().Units,
		})
	}

	return nil
}

// createPoolAssignments creates ship assignments for all ships
func (h *RunGasCoordinatorHandler) createPoolAssignments(
	ctx context.Context,
	ships []string,
	containerID string,
	playerID int,
) error {
	for _, ship := range ships {
		assignment := container.NewShipAssignment(ship, playerID, containerID, nil)
		if err := h.shipAssignmentRepo.Assign(ctx, assignment); err != nil {
			return fmt.Errorf("failed to assign %s: %w", ship, err)
		}
	}
	return nil
}

// releasePoolAssignments releases all ship assignments
func (h *RunGasCoordinatorHandler) releasePoolAssignments(
	ctx context.Context,
	containerID string,
	playerID int,
) error {
	assignments, err := h.shipAssignmentRepo.FindByContainer(ctx, containerID, playerID)
	if err != nil {
		return err
	}

	for _, assignment := range assignments {
		h.shipAssignmentRepo.Release(ctx, assignment.ShipSymbol(), playerID, "coordinator shutdown")
	}

	return nil
}

// spawnSiphonWorker creates and starts a siphon worker for a ship
// The siphon worker will deposit cargo to storage ships via the StorageCoordinator
func (h *RunGasCoordinatorHandler) spawnSiphonWorker(
	ctx context.Context,
	cmd *RunGasCoordinatorCommand,
	shipSymbol string,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	workerContainerID := utils.GenerateContainerID("siphon-worker", shipSymbol)

	workerCmd := &RunSiphonWorkerCommand{
		ShipSymbol:         shipSymbol,
		PlayerID:           cmd.PlayerID,
		GasGiant:           cmd.GasGiant,
		CoordinatorID:      cmd.ContainerID,
		StorageOperationID: cmd.GasOperationID,
	}

	// Step 1: Persist worker container
	logger.Log("INFO", "Siphon worker container persisting", map[string]interface{}{
		"action":              "persist_siphon_worker",
		"ship_symbol":         shipSymbol,
		"worker_container_id": workerContainerID,
	})
	if err := h.daemonClient.PersistGasSiphonWorkerContainer(ctx, workerContainerID, uint(cmd.PlayerID.Value()), workerCmd); err != nil {
		return "", fmt.Errorf("failed to persist worker: %w", err)
	}

	// Step 2: Transfer ship to worker
	if err := h.shipAssignmentRepo.Transfer(ctx, shipSymbol, cmd.ContainerID, workerContainerID); err != nil {
		_ = h.daemonClient.StopContainer(ctx, workerContainerID)
		return "", fmt.Errorf("failed to transfer ship: %w", err)
	}

	// Step 3: Start worker (no completion callback needed - workers run continuously)
	if err := h.daemonClient.StartGasSiphonWorkerContainer(ctx, workerContainerID, nil); err != nil {
		_ = h.shipAssignmentRepo.Transfer(ctx, shipSymbol, workerContainerID, cmd.ContainerID)
		return "", fmt.Errorf("failed to start worker: %w", err)
	}

	logger.Log("INFO", "Siphon worker started successfully", map[string]interface{}{
		"action":              "start_siphon_worker",
		"ship_symbol":         shipSymbol,
		"worker_container_id": workerContainerID,
	})
	return workerContainerID, nil
}

// NOTE: spawnGasTransportWorker removed - transport is now handled by
// manufacturing pool via STORAGE_ACQUIRE_DELIVER tasks

// planDryRunRoutes plans routes for all ships without starting workers
func (h *RunGasCoordinatorHandler) planDryRunRoutes(
	ctx context.Context,
	cmd *RunGasCoordinatorCommand,
	logger common.ContainerLogger,
) (*RunGasCoordinatorResponse, error) {
	logger.Log("INFO", "Dry-run mode initiated for gas extraction with storage system", map[string]interface{}{
		"action":        "dry_run_start",
		"siphon_count":  len(cmd.SiphonShips),
		"storage_count": len(cmd.StorageShips),
		"gas_giant":     cmd.GasGiant,
	})

	result := &RunGasCoordinatorResponse{
		GasGiant:   cmd.GasGiant,
		ShipRoutes: []common.ShipRouteDTO{},
		Errors:     []string{},
	}

	// Extract system symbol from gas giant waypoint (e.g., "X1-AU21-J63" -> "X1-AU21")
	parts := strings.Split(cmd.GasGiant, "-")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid gas giant waypoint format: %s", cmd.GasGiant)
	}
	systemSymbol := parts[0] + "-" + parts[1]

	// Log planned routes for siphon ships
	for _, siphonSymbol := range cmd.SiphonShips {
		ship, err := h.shipRepo.FindBySymbol(ctx, siphonSymbol, cmd.PlayerID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to fetch siphon %s: %v", siphonSymbol, err))
			continue
		}

		// Calculate simple route info
		shipRoute := common.ShipRouteDTO{
			ShipSymbol: siphonSymbol,
			ShipType:   "siphon",
			Segments: []common.RouteSegmentDTO{
				{
					From:       ship.CurrentLocation().Symbol,
					To:         cmd.GasGiant,
					FlightMode: "CRUISE",
				},
			},
		}
		result.ShipRoutes = append(result.ShipRoutes, shipRoute)
	}

	// Log planned routes for storage ships (they stay in orbit at gas giant)
	for _, storageSymbol := range cmd.StorageShips {
		ship, err := h.shipRepo.FindBySymbol(ctx, storageSymbol, cmd.PlayerID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to fetch storage %s: %v", storageSymbol, err))
			continue
		}

		// Storage ships navigate to gas giant and stay in orbit
		shipRoute := common.ShipRouteDTO{
			ShipSymbol: storageSymbol,
			ShipType:   "storage",
			Segments: []common.RouteSegmentDTO{
				{
					From:       ship.CurrentLocation().Symbol,
					To:         cmd.GasGiant,
					FlightMode: "CRUISE",
				},
			},
		}
		result.ShipRoutes = append(result.ShipRoutes, shipRoute)
	}

	logger.Log("INFO", "Dry-run planning complete for gas extraction", map[string]interface{}{
		"action":        "dry_run_complete",
		"ships_planned": len(result.ShipRoutes),
		"gas_giant":     result.GasGiant,
		"system":        systemSymbol,
		"note":          "Transport handled by manufacturing pool via STORAGE_ACQUIRE_DELIVER tasks",
	})

	return result, nil
}

// autoSelectGasGiant finds a gas giant in the system based on the first siphon ship's location
func (h *RunGasCoordinatorHandler) autoSelectGasGiant(
	ctx context.Context,
	cmd *RunGasCoordinatorCommand,
	logger common.ContainerLogger,
) (string, error) {
	// We need at least one siphon ship to determine the system
	if len(cmd.SiphonShips) == 0 {
		return "", fmt.Errorf("at least one siphon ship required to auto-select gas giant")
	}

	// Get the first siphon ship to determine the system
	siphonShip, err := h.shipRepo.FindBySymbol(ctx, cmd.SiphonShips[0], cmd.PlayerID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch siphon ship %s: %w", cmd.SiphonShips[0], err)
	}

	// Extract system symbol from ship's location
	shipLocation := siphonShip.CurrentLocation().Symbol
	parts := strings.Split(shipLocation, "-")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid ship location format: %s", shipLocation)
	}
	systemSymbol := parts[0] + "-" + parts[1]

	logger.Log("INFO", "Searching for gas giant in system", map[string]interface{}{
		"action":        "search_gas_giant",
		"system":        systemSymbol,
		"ship_location": shipLocation,
	})

	// List all waypoints in the system
	waypoints, err := h.waypointRepo.ListBySystem(ctx, systemSymbol)
	if err != nil {
		return "", fmt.Errorf("failed to list waypoints in system %s: %w", systemSymbol, err)
	}

	// Find gas giants
	var gasGiants []*domainShared.Waypoint
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

	// If multiple gas giants, select the closest one to the siphon ship
	closestGasGiant := gasGiants[0]
	shipWaypoint := siphonShip.CurrentLocation()
	minDistance := shipWaypoint.DistanceTo(closestGasGiant)

	for _, gg := range gasGiants[1:] {
		distance := shipWaypoint.DistanceTo(gg)
		if distance < minDistance {
			minDistance = distance
			closestGasGiant = gg
		}
	}

	logger.Log("INFO", "Gas giant selected", map[string]interface{}{
		"action":     "select_gas_giant",
		"gas_giant":  closestGasGiant.Symbol,
		"distance":   minDistance,
		"candidates": len(gasGiants),
	})

	return closestGasGiant.Symbol, nil
}

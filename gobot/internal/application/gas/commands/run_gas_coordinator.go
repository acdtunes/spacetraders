package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainShared "github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

const flightModeCruise = "CRUISE"

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
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	storageOpRepo      storage.StorageOperationRepository
	daemonClient       daemon.DaemonClient
	waypointRepo       system.WaypointRepository
	storageCoordinator storage.StorageCoordinator
	clock              domainShared.Clock
}

// NewRunGasCoordinatorHandler creates a new gas coordinator handler
func NewRunGasCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	storageOpRepo storage.StorageOperationRepository,
	daemonClient daemon.DaemonClient,
	waypointRepo system.WaypointRepository,
	storageCoordinator storage.StorageCoordinator,
	clock domainShared.Clock,
) *RunGasCoordinatorHandler {
	if clock == nil {
		clock = domainShared.NewRealClock()
	}
	return &RunGasCoordinatorHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		storageOpRepo:      storageOpRepo,
		daemonClient:       daemonClient,
		waypointRepo:       waypointRepo,
		storageCoordinator: storageCoordinator,
		clock:              clock,
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

	// Step 2: Create ship pool assignments for siphon ships
	logger.Log("INFO", "Ship pool creation initiated", map[string]interface{}{
		"action":        "create_ship_pool",
		"siphon_count":  len(cmd.SiphonShips),
		"storage_count": len(cmd.StorageShips),
		"container_id":  cmd.ContainerID,
	})

	if err := h.createPoolAssignments(ctx, cmd.SiphonShips, cmd.ContainerID, cmd.PlayerID); err != nil {
		return nil, fmt.Errorf("failed to create pool assignments: %w", err)
	}

	// Track all spawned worker container IDs for cleanup on shutdown
	var workerContainerIDs []string
	var storageContainerIDs []string

	// Step 3: Spawn storage ship workers (they navigate themselves and register with coordinator)
	// This is non-blocking - storage ships navigate in parallel via their own containers
	logger.Log("INFO", "Storage ship workers spawning", map[string]interface{}{
		"action":        "spawn_storage_ships",
		"storage_count": len(cmd.StorageShips),
	})
	for _, storageShip := range cmd.StorageShips {
		containerID, err := h.spawnStorageShipWorker(ctx, cmd, storageShip)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to spawn storage ship worker for %s: %v", storageShip, err)
			logger.Log("ERROR", "Storage ship worker spawn failed", map[string]interface{}{
				"action":      "spawn_storage_ship",
				"ship_symbol": storageShip,
				"error":       err.Error(),
			})
			result.Errors = append(result.Errors, errMsg)
		} else {
			storageContainerIDs = append(storageContainerIDs, containerID)
		}
	}

	// Step 4: Wait for at least one storage ship to be registered before spawning siphon workers
	// This prevents race conditions where siphons try to deposit before storage is ready
	logger.Log("INFO", "Waiting for storage ship registration", map[string]interface{}{
		"action":        "wait_storage_registration",
		"storage_count": len(cmd.StorageShips),
		"operation_id":  cmd.GasOperationID,
	})

	if err := h.waitForStorageShipRegistration(ctx, cmd.GasOperationID, len(cmd.StorageShips), logger); err != nil {
		// If we timeout waiting for storage ships, still spawn siphons - they have their own retry logic
		logger.Log("WARNING", "Storage ship registration wait timeout, spawning siphons anyway", map[string]interface{}{
			"action": "storage_wait_timeout",
			"error":  err.Error(),
		})
	}

	// Step 5: Spawn siphon workers now that storage ships are registered
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
		"action":        "shutdown_coordinator",
		"container_id":  cmd.ContainerID,
		"siphon_count":  len(workerContainerIDs),
		"storage_count": len(storageContainerIDs),
	})

	// Stop all siphon worker containers
	for _, containerID := range workerContainerIDs {
		logger.Log("INFO", "Siphon worker container stopping", map[string]interface{}{
			"action":              "stop_siphon_worker",
			"worker_container_id": containerID,
		})
		_ = h.daemonClient.StopContainer(ctx, containerID)
	}

	// Stop all storage ship containers (they handle their own unregistration)
	for _, containerID := range storageContainerIDs {
		logger.Log("INFO", "Storage ship container stopping", map[string]interface{}{
			"action":       "stop_storage_ship",
			"container_id": containerID,
		})
		_ = h.daemonClient.StopContainer(ctx, containerID)
	}

	// Release pool assignments for siphon ships
	h.releasePoolAssignments(ctx, cmd.ContainerID, cmd.PlayerID)

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
	// Try to load existing operation by ID
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

	// Check for ALL existing RUNNING operations on the same gas giant
	// This prevents multiple concurrent operations for the same waypoint
	// CRITICAL: Must use FindAllRunningByWaypoint to stop ALL duplicates, not just the first one
	existingOps, err := h.storageOpRepo.FindAllRunningByWaypoint(ctx, cmd.PlayerID.Value(), cmd.GasGiant)
	if err != nil {
		logger.Log("WARN", "Failed to check for existing operations on gas giant", map[string]interface{}{
			"gas_giant": cmd.GasGiant,
			"error":     err.Error(),
		})
		// Continue anyway - better to create duplicate than fail completely
	} else if len(existingOps) > 0 {
		// Stop ALL old operations before creating a new one
		logger.Log("WARN", "Stopping existing storage operations for gas giant (replaced by new operation)", map[string]interface{}{
			"old_operation_count": len(existingOps),
			"new_operation_id":    cmd.GasOperationID,
			"gas_giant":           cmd.GasGiant,
		})
		for _, existingOp := range existingOps {
			existingOp.Stop()
			if err := h.storageOpRepo.Update(ctx, existingOp); err != nil {
				logger.Log("ERROR", "Failed to stop old storage operation", map[string]interface{}{
					"operation_id": existingOp.ID(),
					"error":        err.Error(),
				})
			} else {
				logger.Log("INFO", "Stopped old storage operation", map[string]interface{}{
					"operation_id": existingOp.ID(),
				})
			}
		}
	}

	// Determine supported goods for gas extraction
	// For gas giants, common gases are HYDROCARBON, LIQUID_NITROGEN, LIQUID_HYDROGEN
	supportedGoods := []string{goodHydrocarbon, "LIQUID_NITROGEN", "LIQUID_HYDROGEN"}

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

// operationGas is the gas coordinator's fleet identity for the atomic
// ClaimShip dedication check (sp-l7h2 Phase 2): a hull the captain pins to
// another fleet is rejected inside ClaimShip's locked transaction, while a
// hull pinned "gas" (or unpinned) claims normally.
const operationGas = "gas"

// createPoolAssignments creates ship assignments for all ships.
// It first releases any existing assignment from previous runs, then claims
// each hull through ShipRepository.ClaimShip (sp-l7h2 Phase 2) — the atomic,
// operation-checked write — instead of the old read-modify-write
// AssignToContainer+Save, so a hull pinned to a foreign fleet can never be
// pulled into the gas pool, even by a racing claim.
func (h *RunGasCoordinatorHandler) createPoolAssignments(
	ctx context.Context,
	ships []string,
	containerID string,
	playerID domainShared.PlayerID,
) error {
	for _, shipSymbol := range ships {
		// Release any existing assignment from a previous run's container under
		// CAS-retry (sp-wa7c) — gas ships are explicitly configured, so a stale claim
		// held by ANOTHER container is force-taken (recovery semantics, unchanged).
		// The closure re-applies ForceRelease on the FRESH row so a concurrent writer's
		// cargo/nav update survives instead of being last-write-wins clobbered; it
		// skips when the hull is idle or already on this container (changed=false), so
		// no gratuitous release write happens before the claim verdict below.
		if _, _, err := h.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() == containerID {
					return false, nil
				}
				sh.ForceRelease("reassigning to new coordinator", h.clock)
				return true, nil
			}); err != nil {
			return fmt.Errorf("failed to save release for %s: %w", shipSymbol, err)
		}

		// Atomic claim: assignment + fleet dedication checked in one locked
		// transaction. Idempotent when the hull already belongs to this
		// coordinator container (recovery re-run).
		if err := h.shipRepo.ClaimShip(ctx, shipSymbol, containerID, playerID, operationGas); err != nil {
			return fmt.Errorf("failed to claim %s for gas pool: %w", shipSymbol, err)
		}
	}
	return nil
}

// releasePoolAssignments releases all ship assignments
func (h *RunGasCoordinatorHandler) releasePoolAssignments(
	ctx context.Context,
	containerID string,
	playerID domainShared.PlayerID,
) error {
	// Find all ships assigned to this container
	ships, err := h.shipRepo.FindByContainer(ctx, containerID, playerID)
	if err != nil {
		return err
	}

	// Release each ship under CAS-retry (sp-wa7c): re-apply ForceRelease on the FRESH
	// row so a concurrent writer's cargo/nav update survives instead of being
	// last-write-wins clobbered by the FindByContainer snapshot. Skip unless the hull
	// is still on THIS container (a concurrent release or re-claim -> changed=false).
	for _, ship := range ships {
		shipSymbol := ship.ShipSymbol()
		if _, _, err := h.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != containerID {
					return false, nil
				}
				sh.ForceRelease("coordinator shutdown", h.clock)
				return true, nil
			}); err != nil {
			// Log but continue with other ships
			continue
		}
	}

	return nil
}

type gasWorkerSpawnSpec struct {
	idPrefix         string
	acquire          bool // hull ENTERS the gas operation here (not handed off from the pool)
	preReleaseReason string
	persistLogMsg    string
	persistLogAction string
	successLogMsg    string
	successLogAction string
	saveErrContext   string
	persist          func(ctx context.Context, containerID string) error
	start            func(ctx context.Context, containerID string) error
	attach           func(ship *navigation.Ship, containerID string) error
	rollback         func(ship *navigation.Ship)
}

func (h *RunGasCoordinatorHandler) spawnWorker(
	ctx context.Context,
	playerID domainShared.PlayerID,
	shipSymbol string,
	spec gasWorkerSpawnSpec,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	workerContainerID := utils.GenerateContainerID(spec.idPrefix, shipSymbol)

	var ship *navigation.Ship
	if spec.acquire {
		loaded, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return "", fmt.Errorf("failed to load ship: %w", err)
		}
		ship = loaded
	}

	logger.Log("INFO", spec.persistLogMsg, map[string]interface{}{
		"action":              spec.persistLogAction,
		"ship_symbol":         shipSymbol,
		"worker_container_id": workerContainerID,
	})
	if err := spec.persist(ctx, workerContainerID); err != nil {
		return "", fmt.Errorf("failed to persist worker: %w", err)
	}

	if spec.acquire {
		// Acquisition boundary (sp-l7h2 Phase 2): this hull enters the gas
		// operation here (it was never pooled by createPoolAssignments), so it
		// is claimed through the atomic operation-checked ClaimShip instead of
		// the old ForceRelease+AssignToContainer+Save read-modify-write — a
		// hull pinned to a foreign fleet is rejected inside the locked
		// transaction, not clobbered.
		//
		// A stale claim from a previous run's container is still force-taken first
		// (config-listed hull, recovery semantics unchanged) under CAS-retry (sp-wa7c):
		// the closure re-applies ForceRelease on the FRESH row so a concurrent writer's
		// cargo/nav update survives instead of being last-write-wins clobbered, and
		// skips when the hull is idle or already on this worker container (changed=
		// false), so an idle hull gets no gratuitous release write before the claim
		// verdict.
		if _, _, err := h.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() == workerContainerID {
					return false, nil
				}
				sh.ForceRelease(spec.preReleaseReason, h.clock)
				return true, nil
			}); err != nil {
			_ = h.daemonClient.StopContainer(ctx, workerContainerID)
			return "", fmt.Errorf("failed to save pre-claim release: %w", err)
		}
		if err := h.shipRepo.ClaimShip(ctx, shipSymbol, workerContainerID, playerID, operationGas); err != nil {
			_ = h.daemonClient.StopContainer(ctx, workerContainerID)
			return "", fmt.Errorf("failed to claim %s for worker: %w", shipSymbol, err)
		}
	} else {
		// Intra-operation handoff: the hull already belongs to this gas
		// operation (claimed at the boundary by createPoolAssignments); moving
		// it pool→worker is a transfer within the same owner, not a new
		// acquisition, so it stays on the attach+Save path.
		loaded, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			_ = h.daemonClient.StopContainer(ctx, workerContainerID)
			return "", fmt.Errorf("failed to load ship: %w", err)
		}
		ship = loaded

		if err := spec.attach(ship, workerContainerID); err != nil {
			_ = h.daemonClient.StopContainer(ctx, workerContainerID)
			return "", err
		}
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			_ = h.daemonClient.StopContainer(ctx, workerContainerID)
			return "", fmt.Errorf("%s: %w", spec.saveErrContext, err)
		}
	}

	if err := spec.start(ctx, workerContainerID); err != nil {
		spec.rollback(ship)
		_ = h.shipRepo.Save(ctx, ship)
		_ = h.daemonClient.StopContainer(ctx, workerContainerID)
		return "", fmt.Errorf("failed to start worker: %w", err)
	}

	logger.Log("INFO", spec.successLogMsg, map[string]interface{}{
		"action":              spec.successLogAction,
		"ship_symbol":         shipSymbol,
		"worker_container_id": workerContainerID,
	})
	return workerContainerID, nil
}

// spawnSiphonWorker creates and starts a siphon worker for a ship
// The siphon worker will deposit cargo to storage ships via the StorageCoordinator
func (h *RunGasCoordinatorHandler) spawnSiphonWorker(
	ctx context.Context,
	cmd *RunGasCoordinatorCommand,
	shipSymbol string,
) (string, error) {
	workerCmd := &RunSiphonWorkerCommand{
		ShipSymbol:         shipSymbol,
		PlayerID:           cmd.PlayerID,
		GasGiant:           cmd.GasGiant,
		CoordinatorID:      cmd.ContainerID,
		StorageOperationID: cmd.GasOperationID,
	}

	spec := gasWorkerSpawnSpec{
		idPrefix:         "siphon-worker",
		acquire:          false, // pool→worker handoff: the pool claimed this hull at the operation boundary
		persistLogMsg:    "Siphon worker container persisting",
		persistLogAction: "persist_siphon_worker",
		successLogMsg:    "Siphon worker started successfully",
		successLogAction: "start_siphon_worker",
		saveErrContext:   "failed to save ship transfer",
		persist: func(ctx context.Context, containerID string) error {
			return h.daemonClient.PersistContainer(ctx, daemon.ContainerKindGasSiphonWorker, containerID, uint(cmd.PlayerID.Value()), workerCmd)
		},
		start: func(ctx context.Context, containerID string) error {
			return h.daemonClient.StartContainer(ctx, daemon.ContainerKindGasSiphonWorker, containerID)
		},
		attach: func(ship *navigation.Ship, containerID string) error {
			if err := ship.TransferToContainer(containerID, h.clock); err != nil {
				return fmt.Errorf("failed to transfer ship: %w", err)
			}
			return nil
		},
		rollback: func(ship *navigation.Ship) {
			_ = ship.TransferToContainer(cmd.ContainerID, h.clock)
		},
	}

	return h.spawnWorker(ctx, cmd.PlayerID, shipSymbol, spec)
}

// spawnStorageShipWorker creates and starts a storage ship worker for a ship.
// The storage ship worker navigates to the gas giant and registers with the storage coordinator.
func (h *RunGasCoordinatorHandler) spawnStorageShipWorker(
	ctx context.Context,
	cmd *RunGasCoordinatorCommand,
	shipSymbol string,
) (string, error) {
	workerCmd := &RunStorageShipWorkerCommand{
		ShipSymbol:         shipSymbol,
		PlayerID:           cmd.PlayerID,
		GasGiant:           cmd.GasGiant,
		CoordinatorID:      cmd.ContainerID,
		StorageOperationID: cmd.GasOperationID,
	}

	spec := gasWorkerSpawnSpec{
		idPrefix:         "storage-ship",
		acquire:          true, // storage ships are never pooled: they enter the operation at this spawn
		preReleaseReason: "reassigning to storage ship container",
		persistLogMsg:    "Storage ship worker container persisting",
		persistLogAction: "persist_storage_ship_worker",
		successLogMsg:    "Storage ship worker started successfully",
		successLogAction: "start_storage_ship_worker",
		persist: func(ctx context.Context, containerID string) error {
			return h.daemonClient.PersistContainer(ctx, daemon.ContainerKindStorageShip, containerID, uint(cmd.PlayerID.Value()), workerCmd)
		},
		start: func(ctx context.Context, containerID string) error {
			return h.daemonClient.StartContainer(ctx, daemon.ContainerKindStorageShip, containerID)
		},
		rollback: func(ship *navigation.Ship) {
			ship.ForceRelease("failed to start container", h.clock)
		},
	}

	return h.spawnWorker(ctx, cmd.PlayerID, shipSymbol, spec)
}

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
					FlightMode: flightModeCruise,
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
					FlightMode: flightModeCruise,
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

// waitForStorageShipRegistration polls the coordinator until at least one storage ship is registered.
// This ensures siphon workers don't start before storage ships are ready to receive cargo.
// Returns nil when at least one storage ship is registered, or error after timeout.
func (h *RunGasCoordinatorHandler) waitForStorageShipRegistration(
	ctx context.Context,
	operationID string,
	expectedCount int,
	logger common.ContainerLogger,
) error {
	const (
		pollInterval = 2  // seconds
		maxWaitTime  = 60 // seconds
	)

	ticker := time.NewTicker(pollInterval * time.Second)
	defer ticker.Stop()

	waited := 0
	for waited < maxWaitTime {
		// Check how many storage ships are registered
		ships := h.storageCoordinator.GetStorageShipsForOperation(operationID)
		registeredCount := len(ships)

		if registeredCount > 0 {
			logger.Log("INFO", "Storage ship(s) registered, proceeding with siphon workers", map[string]interface{}{
				"action":           "storage_registered",
				"registered_count": registeredCount,
				"expected_count":   expectedCount,
				"operation_id":     operationID,
			})
			return nil
		}

		// Log progress every 10 seconds
		if waited%10 == 0 {
			logger.Log("INFO", "Waiting for storage ship registration", map[string]interface{}{
				"action":         "waiting_storage",
				"waited_seconds": waited,
				"operation_id":   operationID,
			})
		}

		// Wait for next poll or context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			waited += pollInterval
		}
	}

	return fmt.Errorf("timeout waiting for storage ship registration after %d seconds", maxWaitTime)
}

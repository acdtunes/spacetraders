package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/gas/coordination"
	"github.com/andrescamacho/spacetraders-go/internal/application/gas/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	domainGas "github.com/andrescamacho/spacetraders-go/internal/domain/gas"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainShared "github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// RunGasCoordinatorCommand manages a fleet of siphon and transport ships
// for gas extraction operations
type RunGasCoordinatorCommand struct {
	GasOperationID string
	PlayerID       domainShared.PlayerID
	GasGiant       string   // Waypoint symbol of the gas giant
	SiphonShips    []string // Ships for siphoning (need siphon mounts + gas processor)
	TransportShips []string // Ships for transport to factories
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
	operationRepo      domainGas.OperationRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	daemonClient       daemon.DaemonClient
	waypointRepo       system.WaypointRepository
}

// NewRunGasCoordinatorHandler creates a new gas coordinator handler
func NewRunGasCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	operationRepo domainGas.OperationRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	daemonClient daemon.DaemonClient,
	waypointRepo system.WaypointRepository,
) *RunGasCoordinatorHandler {
	return &RunGasCoordinatorHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		operationRepo:      operationRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		daemonClient:       daemonClient,
		waypointRepo:       waypointRepo,
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

	// Validate gas giant is provided
	if cmd.GasGiant == "" {
		return nil, fmt.Errorf("gas giant waypoint must be specified")
	}

	// Dry-run mode: plan routes and return without starting workers
	if cmd.DryRun {
		return h.planDryRunRoutes(ctx, cmd, logger)
	}

	// Step 1: Create or load gas operation
	operation, err := h.getOrCreateOperation(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to get/create operation: %w", err)
	}

	// Step 2: Create ship pool assignments
	logger.Log("INFO", "Ship pool creation initiated", map[string]interface{}{
		"action":          "create_ship_pool",
		"siphon_count":    len(cmd.SiphonShips),
		"transport_count": len(cmd.TransportShips),
		"container_id":    cmd.ContainerID,
	})

	allShips := append(cmd.SiphonShips, cmd.TransportShips...)
	if err := h.createPoolAssignments(ctx, allShips, cmd.ContainerID, cmd.PlayerID.Value()); err != nil {
		return nil, fmt.Errorf("failed to create pool assignments: %w", err)
	}

	// Step 3: Create transport coordinator (abstracts channel-based coordination)
	coordinator := coordination.NewChannelTransportCoordinator(cmd.SiphonShips, cmd.TransportShips)
	defer coordinator.Shutdown() // Ensure channels are closed on exit

	// Get raw channels for coordinator's main loop
	siphonRequestChan, transportAvailabilityChan, transferCompleteChan, siphonAssignChans := coordinator.GetChannels()

	// Track all spawned worker container IDs for cleanup on shutdown
	var workerContainerIDs []string

	// Step 4: Spawn transport workers (they run continuously)
	logger.Log("INFO", "Gas transport workers spawning", map[string]interface{}{
		"action":          "spawn_transports",
		"transport_count": len(cmd.TransportShips),
	})
	for _, transportShip := range cmd.TransportShips {
		containerID, err := h.spawnGasTransportWorker(ctx, cmd, transportShip, coordinator)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to spawn gas transport worker for %s: %v", transportShip, err)
			logger.Log("ERROR", "Gas transport worker spawn failed", map[string]interface{}{
				"action":      "spawn_transport",
				"ship_symbol": transportShip,
				"error":       err.Error(),
			})
			result.Errors = append(result.Errors, errMsg)
		} else {
			workerContainerIDs = append(workerContainerIDs, containerID)
		}
	}

	// Step 5: Spawn siphon workers (they run continuously)
	logger.Log("INFO", "Siphon workers spawning", map[string]interface{}{
		"action":       "spawn_siphons",
		"siphon_count": len(cmd.SiphonShips),
	})
	for _, siphonShip := range cmd.SiphonShips {
		containerID, err := h.spawnSiphonWorker(ctx, cmd, siphonShip, coordinator)
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

	// Step 6: Main coordination loop
	logger.Log("INFO", "Gas coordination loop started", map[string]interface{}{
		"action":       "start_coordination",
		"container_id": cmd.ContainerID,
		"gas_giant":    cmd.GasGiant,
	})

	// Available transport pool
	availableTransports := []string{}

	// Track cargo levels of each transport (to fill one completely before next)
	transportCargoLevels := make(map[string]int)

	// Queue of siphons waiting for transport
	waitingSiphons := []string{}

	for {
		select {
		case <-ctx.Done():
			// Context cancelled, stop all workers and cleanup
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
			h.releasePoolAssignments(ctx, cmd.ContainerID, cmd.PlayerID.Value())
			operation.Stop()
			h.operationRepo.Save(ctx, operation)
			return result, ctx.Err()

		case transportSymbol := <-transportAvailabilityChan:
			// Transport signaled availability - fetch its current cargo level
			if transportShip, err := h.shipRepo.FindBySymbol(ctx, transportSymbol, cmd.PlayerID); err == nil {
				transportCargoLevels[transportSymbol] = transportShip.Cargo().Units
			}

			// Check if there are waiting siphons
			if len(waitingSiphons) > 0 {
				// Match immediately with first waiting siphon
				siphonSymbol := waitingSiphons[0]
				waitingSiphons = waitingSiphons[1:]

				// Send transport to siphon
				select {
				case siphonAssignChans[siphonSymbol] <- transportSymbol:
					logger.Log("INFO", "Transport assigned to waiting siphon", map[string]interface{}{
						"action":      "assign_transport",
						"transport":   transportSymbol,
						"siphon":      siphonSymbol,
						"cargo_units": transportCargoLevels[transportSymbol],
					})
				case <-ctx.Done():
					return result, ctx.Err()
				}
			} else {
				// Add to available pool
				availableTransports = append(availableTransports, transportSymbol)
			}

		case siphonSymbol := <-siphonRequestChan:
			// Siphon requesting transport

			if len(availableTransports) > 0 {
				// Select transport with most cargo to fill it first
				bestIdx := 0
				bestCargo := transportCargoLevels[availableTransports[0]]
				for i, ts := range availableTransports {
					if transportCargoLevels[ts] > bestCargo {
						bestIdx = i
						bestCargo = transportCargoLevels[ts]
					}
				}
				transportSymbol := availableTransports[bestIdx]
				availableTransports = append(availableTransports[:bestIdx], availableTransports[bestIdx+1:]...)

				// Send transport to siphon
				select {
				case siphonAssignChans[siphonSymbol] <- transportSymbol:
					logger.Log("INFO", "Transport assigned to siphon", map[string]interface{}{
						"action":      "assign_transport",
						"transport":   transportSymbol,
						"siphon":      siphonSymbol,
						"cargo_units": bestCargo,
					})
				case <-ctx.Done():
					return result, ctx.Err()
				}
			} else {
				// No transport available, add to waiting queue
				waitingSiphons = append(waitingSiphons, siphonSymbol)
				logger.Log("INFO", "Siphon waiting for transport", map[string]interface{}{
					"action":      "siphon_waiting",
					"siphon":      siphonSymbol,
					"queue_depth": len(waitingSiphons),
				})
			}

		case transfer := <-transferCompleteChan:
			// Siphon completed transferring cargo to transport
			result.TotalTransfers++
			logger.Log("INFO", "Cargo transfer complete", map[string]interface{}{
				"action":         "transfer_complete",
				"siphon":         transfer.SiphonSymbol,
				"transport":      transfer.TransportSymbol,
				"transfer_count": result.TotalTransfers,
			})

			// Notify the transport that cargo was received
			if err := coordinator.NotifyCargoReceived(ctx, transfer.TransportSymbol); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to notify transport %s of cargo received: %v", transfer.TransportSymbol, err), nil)
			}
		}
	}
}

// getOrCreateOperation loads or creates the gas operation
func (h *RunGasCoordinatorHandler) getOrCreateOperation(
	ctx context.Context,
	cmd *RunGasCoordinatorCommand,
) (*domainGas.Operation, error) {
	// Try to load existing operation
	operation, err := h.operationRepo.FindByID(ctx, cmd.GasOperationID, cmd.PlayerID.Value())
	if err == nil && operation != nil {
		// Resume existing operation
		if operation.IsPending() {
			operation.Start()
			h.operationRepo.Save(ctx, operation)
		}
		return operation, nil
	}

	// Create new operation
	operation = domainGas.NewOperation(
		cmd.GasOperationID,
		cmd.PlayerID.Value(),
		cmd.GasGiant,
		cmd.SiphonShips,
		cmd.TransportShips,
		-1, // Infinite iterations
		nil,
	)

	if err := h.operationRepo.Add(ctx, operation); err != nil {
		return nil, fmt.Errorf("failed to insert operation: %w", err)
	}

	operation.Start()
	h.operationRepo.Save(ctx, operation)

	return operation, nil
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
func (h *RunGasCoordinatorHandler) spawnSiphonWorker(
	ctx context.Context,
	cmd *RunGasCoordinatorCommand,
	shipSymbol string,
	coordinator ports.TransportCoordinator,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	workerContainerID := utils.GenerateContainerID("siphon-worker", shipSymbol)

	workerCmd := &RunSiphonWorkerCommand{
		ShipSymbol:    shipSymbol,
		PlayerID:      cmd.PlayerID,
		GasGiant:      cmd.GasGiant,
		CoordinatorID: cmd.ContainerID,
		Coordinator:   coordinator,
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

// spawnGasTransportWorker creates and starts a gas transport worker for a ship
func (h *RunGasCoordinatorHandler) spawnGasTransportWorker(
	ctx context.Context,
	cmd *RunGasCoordinatorCommand,
	shipSymbol string,
	coordinator ports.TransportCoordinator,
) (string, error) {
	logger := common.LoggerFromContext(ctx)

	workerContainerID := utils.GenerateContainerID("gas-transport-worker", shipSymbol)

	workerCmd := &RunGasTransportWorkerCommand{
		ShipSymbol:    shipSymbol,
		PlayerID:      cmd.PlayerID,
		GasGiant:      cmd.GasGiant,
		CoordinatorID: cmd.ContainerID,
		Coordinator:   coordinator,
	}

	// Step 1: Persist worker container
	logger.Log("INFO", "Gas transport worker container persisting", map[string]interface{}{
		"action":              "persist_gas_transport_worker",
		"ship_symbol":         shipSymbol,
		"worker_container_id": workerContainerID,
	})
	if err := h.daemonClient.PersistGasTransportWorkerContainer(ctx, workerContainerID, uint(cmd.PlayerID.Value()), workerCmd); err != nil {
		return "", fmt.Errorf("failed to persist worker: %w", err)
	}

	// Step 2: Transfer ship to worker
	if err := h.shipAssignmentRepo.Transfer(ctx, shipSymbol, cmd.ContainerID, workerContainerID); err != nil {
		_ = h.daemonClient.StopContainer(ctx, workerContainerID)
		return "", fmt.Errorf("failed to transfer ship: %w", err)
	}

	// Step 3: Start worker (no completion callback needed - workers run continuously)
	if err := h.daemonClient.StartGasTransportWorkerContainer(ctx, workerContainerID, nil); err != nil {
		_ = h.shipAssignmentRepo.Transfer(ctx, shipSymbol, workerContainerID, cmd.ContainerID)
		return "", fmt.Errorf("failed to start worker: %w", err)
	}

	logger.Log("INFO", "Gas transport worker started successfully", map[string]interface{}{
		"action":              "start_gas_transport_worker",
		"ship_symbol":         shipSymbol,
		"worker_container_id": workerContainerID,
	})
	return workerContainerID, nil
}

// planDryRunRoutes plans routes for all ships without starting workers
func (h *RunGasCoordinatorHandler) planDryRunRoutes(
	ctx context.Context,
	cmd *RunGasCoordinatorCommand,
	logger common.ContainerLogger,
) (*RunGasCoordinatorResponse, error) {
	logger.Log("INFO", "Dry-run mode initiated for gas extraction", map[string]interface{}{
		"action":     "dry_run_start",
		"ship_count": len(cmd.SiphonShips) + len(cmd.TransportShips),
		"gas_giant":  cmd.GasGiant,
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

	// Log planned routes for transport ships
	for _, transportSymbol := range cmd.TransportShips {
		ship, err := h.shipRepo.FindBySymbol(ctx, transportSymbol, cmd.PlayerID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to fetch transport %s: %v", transportSymbol, err))
			continue
		}

		// Calculate simple route info
		shipRoute := common.ShipRouteDTO{
			ShipSymbol: transportSymbol,
			ShipType:   "transport",
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
	})

	return result, nil
}

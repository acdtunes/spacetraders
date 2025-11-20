package contract

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// ContractFleetCoordinatorCommand manages a pool of ships for continuous contract execution.
// It assigns contracts to the ship closest to the purchase market.
type ContractFleetCoordinatorCommand struct {
	PlayerID    int
	ShipSymbols []string // Pool of ships to use for contracts
	ContainerID string   // Coordinator's own container ID
}

// CoordinatorMetadata stores state for the coordinator's infinite loop
type CoordinatorMetadata struct {
	LastRebalanceTime time.Time
	RebalanceInterval time.Duration
}

// ContractFleetCoordinatorResponse contains the coordinator execution results
type ContractFleetCoordinatorResponse struct {
	ContractsCompleted int
	Errors             []string
}

// ContainerRepository interface for querying container state
type ContainerRepository interface {
	ListByStatusSimple(ctx context.Context, status string, playerID *int) ([]persistence.ContainerSummary, error)
}

// ContractFleetCoordinatorHandler implements the fleet coordinator logic
type ContractFleetCoordinatorHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	contractRepo       domainContract.ContractRepository
	marketRepo         market.MarketRepository
	shipAssignmentRepo daemon.ShipAssignmentRepository
	daemonClient       daemon.DaemonClient // For creating worker containers
	graphProvider      system.ISystemGraphProvider // For distance calculations
	containerRepo      ContainerRepository // For checking existing workers
}

// NewContractFleetCoordinatorHandler creates a new fleet coordinator handler
func NewContractFleetCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	contractRepo domainContract.ContractRepository,
	marketRepo market.MarketRepository,
	shipAssignmentRepo daemon.ShipAssignmentRepository,
	daemonClient daemon.DaemonClient,
	graphProvider system.ISystemGraphProvider,
	containerRepo ContainerRepository,
) *ContractFleetCoordinatorHandler {
	return &ContractFleetCoordinatorHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		contractRepo:       contractRepo,
		marketRepo:         marketRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		daemonClient:       daemonClient,
		graphProvider:      graphProvider,
		containerRepo:      containerRepo,
	}
}

// Handle executes the fleet coordinator command
func (h *ContractFleetCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*ContractFleetCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &ContractFleetCoordinatorResponse{
		ContractsCompleted: 0,
		Errors:             []string{},
	}

	// Initialize coordinator metadata for rebalancing
	metadata := &CoordinatorMetadata{
		LastRebalanceTime: time.Time{}, // Zero time = never rebalanced
		RebalanceInterval: 10 * time.Minute,
	}

	// Step 1: Validate that ships are not already assigned to active containers
	logger.Log("INFO", "Validating ship availability...", nil)
	for _, shipSymbol := range cmd.ShipSymbols {
		assignment, err := h.shipAssignmentRepo.FindByShip(ctx, shipSymbol, cmd.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to check assignment for %s: %w", shipSymbol, err)
		}

		if assignment != nil && assignment.Status() == "active" {
			return nil, fmt.Errorf("ship %s is already assigned to container %s - cannot create overlapping coordinator",
				shipSymbol, assignment.ContainerID())
		}
	}

	// Step 2: Create ship assignments for all pooled ships
	logger.Log("INFO", fmt.Sprintf("Initializing ship pool with %d ships", len(cmd.ShipSymbols)), nil)
	if err := CreatePoolAssignments(
		ctx,
		cmd.ShipSymbols,
		cmd.ContainerID,
		cmd.PlayerID,
		h.shipAssignmentRepo,
	); err != nil {
		return nil, fmt.Errorf("failed to create pool assignments: %w", err)
	}

	// Step 2: Create unbuffered completion channel for worker notifications
	// IMPORTANT: Unbuffered so signals are only received when actively waiting
	completionChan := make(chan string)

	// Step 3: Check for existing worker containers (recovery scenario)
	existingWorkers, err := h.findExistingWorkers(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to check for existing workers: %v", err), nil)
	} else if len(existingWorkers) > 0 {
		logger.Log("WARNING", fmt.Sprintf("Found %d existing CONTRACT_WORKFLOW workers from previous session - stopping them to prevent conflicts", len(existingWorkers)), nil)
		// Stop all existing workers to prevent multiple workers running simultaneously
		for _, worker := range existingWorkers {
			logger.Log("INFO", fmt.Sprintf("Stopping existing worker container: %s", worker.ID), nil)
			if err := h.daemonClient.StopContainer(ctx, worker.ID); err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to stop existing worker %s: %v", worker.ID, err), nil)
			}
		}
		logger.Log("INFO", "All existing workers stopped, coordinator will create new workers as needed", nil)
	}

	// Track current active worker container ID for cleanup on shutdown
	var activeWorkerContainerID string

	// Step 4: Main coordinator loop (infinite)
	// Execute one contract at a time (game constraint: one active contract per player)
	for {
		select {
		case <-ctx.Done():
			// Context cancelled, release pool and exit
			if activeWorkerContainerID != "" {
				logger.Log("INFO", fmt.Sprintf("Stopping active worker container: %s", activeWorkerContainerID), nil)
				_ = h.daemonClient.StopContainer(ctx, activeWorkerContainerID)
			}
			_ = ReleasePoolAssignments(ctx, cmd.ContainerID, cmd.PlayerID, h.shipAssignmentRepo, "coordinator_stopped")
			return result, ctx.Err()
		default:
			// Continue with contract assignment
		}

		// Find ships currently owned by coordinator
		availableShips, err := FindCoordinatorShips(ctx, cmd.ContainerID, cmd.PlayerID, h.shipAssignmentRepo)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to find coordinator ships: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			time.Sleep(10 * time.Second)
			continue
		}

		// If no ships available, wait for completion signal
		if len(availableShips) == 0 {
			logger.Log("INFO", "No ships available, waiting for completion...", nil)
			select {
			case shipSymbol := <-completionChan:
				logger.Log("INFO", fmt.Sprintf("Ship %s completed, back in pool", shipSymbol), nil)
				activeWorkerContainerID = "" // Worker completed
				// Loop immediately to assign next contract
			case <-time.After(30 * time.Second):
				// Timeout, check again
			case <-ctx.Done():
				if activeWorkerContainerID != "" {
					logger.Log("INFO", fmt.Sprintf("Stopping active worker container: %s", activeWorkerContainerID), nil)
					_ = h.daemonClient.StopContainer(ctx, activeWorkerContainerID)
				}
				_ = ReleasePoolAssignments(ctx, cmd.ContainerID, cmd.PlayerID, h.shipAssignmentRepo, "coordinator_stopped")
				return result, ctx.Err()
			}
			continue // Loop back to check for available ships
		}

		// CRITICAL CHECK: Prevent multiple workers by checking if any worker is already running
		// This prevents race conditions when negotiation fails early in the loop
		existingActiveWorkers, err := h.findExistingWorkers(ctx, cmd.PlayerID)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to check for active workers: %v", err), nil)
		} else if len(existingActiveWorkers) > 0 {
			logger.Log("WARNING", fmt.Sprintf("Found %d active CONTRACT_WORKFLOW workers - waiting instead of creating new worker", len(existingActiveWorkers)), nil)
			select {
			case shipSymbol := <-completionChan:
				logger.Log("INFO", fmt.Sprintf("Active worker completed for ship %s", shipSymbol), nil)
				activeWorkerContainerID = "" // Worker completed
				// Loop back to create new worker
			case <-time.After(1 * time.Minute):
				logger.Log("WARNING", "Timeout waiting for active worker, will check again", nil)
			case <-ctx.Done():
				if activeWorkerContainerID != "" {
					logger.Log("INFO", fmt.Sprintf("Stopping active worker container: %s", activeWorkerContainerID), nil)
					_ = h.daemonClient.StopContainer(ctx, activeWorkerContainerID)
				}
				_ = ReleasePoolAssignments(ctx, cmd.ContainerID, cmd.PlayerID, h.shipAssignmentRepo, "coordinator_stopped")
				return result, ctx.Err()
			}
			continue
		}

		// Negotiate contract (use any ship from pool for negotiation)
		logger.Log("INFO", "Negotiating new contract...", nil)
		contract, err := h.negotiateContract(ctx, availableShips[0], cmd.PlayerID)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to negotiate contract: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			time.Sleep(30 * time.Second)
			continue
		}

		// Find purchase market for contract
		logger.Log("INFO", "Finding purchase market...", nil)
		purchaseMarket, err := FindPurchaseMarket(ctx, contract, h.marketRepo, cmd.PlayerID)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to find purchase market: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			time.Sleep(30 * time.Second)
			continue
		}

		// Extract required cargo for delivery (for ship selection prioritization)
		var requiredCargo string
		for _, delivery := range contract.Terms().Deliveries {
			if delivery.UnitsRequired > delivery.UnitsFulfilled {
				requiredCargo = delivery.TradeSymbol
				break
			}
		}

		// Select closest ship to purchase market (prioritizes ships with required cargo)
		logger.Log("INFO", fmt.Sprintf("Selecting closest ship (required cargo: %s)...", requiredCargo), nil)
		selectedShip, distance, err := SelectClosestShip(
			ctx,
			availableShips,
			h.shipRepo,
			h.graphProvider,
			purchaseMarket,
			requiredCargo,
			cmd.PlayerID,
		)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to select ship: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			time.Sleep(10 * time.Second)
			continue
		}

		logger.Log("INFO", fmt.Sprintf("Selected %s (distance: %.2f units)", selectedShip, distance), nil)

		// Create worker container ID
		workerContainerID := fmt.Sprintf("contract-work-%s-%d", selectedShip, time.Now().Unix())

		// Create worker command
		workerCmd := &ContractWorkflowCommand{
			ShipSymbol:         selectedShip,
			PlayerID:           cmd.PlayerID,
			CoordinatorID:      cmd.ContainerID,
			CompletionCallback: completionChan,
		}

		// Step 1: Persist worker container to DB (synchronous, no start)
		logger.Log("INFO", fmt.Sprintf("Persisting worker container %s for %s", workerContainerID, selectedShip), nil)
		if err := h.daemonClient.PersistContractWorkflowContainer(ctx, workerContainerID, uint(cmd.PlayerID), workerCmd); err != nil {
			errMsg := fmt.Sprintf("Failed to persist worker container: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			time.Sleep(10 * time.Second)
			continue
		}

		// Step 2: Transfer ship from coordinator to worker (atomic, no race condition)
		logger.Log("INFO", fmt.Sprintf("Transferring %s to worker container", selectedShip), nil)
		if err := h.shipAssignmentRepo.Transfer(ctx, selectedShip, cmd.ContainerID, workerContainerID); err != nil {
			errMsg := fmt.Sprintf("Failed to transfer ship %s: %v", selectedShip, err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			// Clean up: stop worker container on transfer failure
			_ = h.daemonClient.StopContainer(ctx, workerContainerID)
			time.Sleep(10 * time.Second)
			continue
		}

		// Step 3: Start the worker container (ship is safely transferred)
		logger.Log("INFO", fmt.Sprintf("Starting worker container for %s", selectedShip), nil)
		if err := h.daemonClient.StartContractWorkflowContainer(ctx, workerContainerID, completionChan); err != nil {
			errMsg := fmt.Sprintf("Failed to start worker container: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			// Transfer ship back to coordinator on failure
			_ = h.shipAssignmentRepo.Transfer(ctx, selectedShip, workerContainerID, cmd.ContainerID)
			time.Sleep(10 * time.Second)
			continue
		}

		activeWorkerContainerID = workerContainerID

		// Block waiting for worker completion
		logger.Log("INFO", fmt.Sprintf("Waiting for %s to complete contract...", selectedShip), nil)
		logger.Log("DEBUG", "Entering select block for completion signal", nil)
		select {
		case completedShip := <-completionChan:
			logger.Log("INFO", fmt.Sprintf("Contract completed by %s", completedShip), nil)
			logger.Log("DEBUG", "Received completion signal, about to continue loop", nil)
			result.ContractsCompleted++
			activeWorkerContainerID = "" // Worker completed

			// Transfer ship back from worker to coordinator (atomic, prevents race conditions)
			// Worker container still owns the ship at this point
			if err := h.shipAssignmentRepo.Transfer(ctx, completedShip, workerContainerID, cmd.ContainerID); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Failed to transfer ship %s back to coordinator: %v", completedShip, err), nil)
				// Fallback: try inserting new assignment if transfer fails
				assignment := daemon.NewShipAssignment(completedShip, cmd.PlayerID, cmd.ContainerID, nil)
				_ = h.shipAssignmentRepo.Assign(ctx, assignment)
			}

			// Check if rebalancing is needed (time-gated)
			if time.Since(metadata.LastRebalanceTime) >= metadata.RebalanceInterval {
				logger.Log("INFO", "Rebalance interval reached, checking fleet distribution...", nil)

				// Extract system symbol from first ship
				var systemSymbol string
				if len(availableShips) > 0 {
					firstShip, err := h.shipRepo.FindBySymbol(ctx, availableShips[0], cmd.PlayerID)
					if err == nil {
						// Extract system from waypoint (e.g., X1-ABC123-XY456Z -> X1-ABC123)
						currentLocation := firstShip.CurrentLocation().Symbol
						for i := len(currentLocation) - 1; i >= 0; i-- {
							if currentLocation[i] == '-' {
								systemSymbol = currentLocation[:i]
								break
							}
						}
					}
				}

				if systemSymbol != "" {
					rebalanceCmd := &RebalanceContractFleetCommand{
						CoordinatorID: cmd.ContainerID,
						PlayerID:      cmd.PlayerID,
						SystemSymbol:  systemSymbol,
					}

					rebalanceResp, err := h.mediator.Send(ctx, rebalanceCmd)
					if err != nil {
						logger.Log("WARNING", fmt.Sprintf("Rebalancing failed: %v", err), nil)
					} else {
						result := rebalanceResp.(*RebalanceContractFleetResponse)
						if result.RebalancingSkipped {
							logger.Log("INFO", fmt.Sprintf("Rebalancing skipped: %s", result.SkipReason), nil)
						} else {
							logger.Log("INFO", fmt.Sprintf("Rebalancing complete: %d ships repositioned", result.ShipsMoved), nil)
						}
					}
				} else {
					logger.Log("WARNING", "Could not determine system symbol for rebalancing", nil)
				}

				metadata.LastRebalanceTime = time.Now()
			}

			// Loop back to negotiate next contract
			continue

		case <-time.After(30 * time.Minute):
			// Timeout waiting for worker
			logger.Log("ERROR", fmt.Sprintf("Timeout waiting for worker %s", selectedShip), nil)
			logger.Log("DEBUG", "Select timed out after 30 minutes, continuing loop", nil)
			errMsg := fmt.Sprintf("Worker timeout for ship %s", selectedShip)
			result.Errors = append(result.Errors, errMsg)
			// Loop back to try again
			continue

		case <-ctx.Done():
			logger.Log("INFO", "Context cancelled, exiting coordinator", nil)
			if activeWorkerContainerID != "" {
				logger.Log("INFO", fmt.Sprintf("Stopping active worker container: %s", activeWorkerContainerID), nil)
				_ = h.daemonClient.StopContainer(ctx, activeWorkerContainerID)
			}
			_ = ReleasePoolAssignments(ctx, cmd.ContainerID, cmd.PlayerID, h.shipAssignmentRepo, "coordinator_stopped")
			return result, ctx.Err()
		}

		// This line should NEVER be reached (all cases have continue/return)
		logger.Log("ERROR", "CRITICAL: Code execution fell through select statement!", nil)
	}
}

// negotiateContract negotiates a new contract
func (h *ContractFleetCoordinatorHandler) negotiateContract(
	ctx context.Context,
	shipSymbol string,
	playerID int,
) (*domainContract.Contract, error) {
	// Check for existing active contracts first
	activeContracts, err := h.contractRepo.FindActiveContracts(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to check active contracts: %w", err)
	}

	logger := common.LoggerFromContext(ctx)

	if len(activeContracts) > 0 {
		// Resume existing contract
		logger.Log("INFO", fmt.Sprintf("Resuming existing active contract: %s", activeContracts[0].ContractID()), nil)
		return activeContracts[0], nil
	}

	// Negotiate new contract
	negotiateCmd := &NegotiateContractCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
	}

	negotiateResp, err := h.mediator.Send(ctx, negotiateCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to negotiate: %w", err)
	}

	negotiateResult := negotiateResp.(*NegotiateContractResponse)
	logger.Log("INFO", fmt.Sprintf("Negotiated contract: %s", negotiateResult.Contract.ContractID()), nil)

	return negotiateResult.Contract, nil
}

// findExistingWorkers finds any existing ContractWorkflow containers that might still be running
func (h *ContractFleetCoordinatorHandler) findExistingWorkers(
	ctx context.Context,
	playerID int,
) ([]persistence.ContainerSummary, error) {
	// Query for RUNNING contract workflow containers
	runningWorkers, err := h.containerRepo.ListByStatusSimple(ctx, "RUNNING", &playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to query running workers: %w", err)
	}

	// Filter for CONTRACT_WORKFLOW type only
	var workers []persistence.ContainerSummary
	for _, container := range runningWorkers {
		if container.ContainerType == "CONTRACT_WORKFLOW" {
			workers = append(workers, container)
		}
	}

	return workers, nil
}

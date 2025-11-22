package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// Type aliases for convenience
type RunFleetCoordinatorCommand = contractTypes.RunFleetCoordinatorCommand
type RunFleetCoordinatorResponse = contractTypes.RunFleetCoordinatorResponse

// RunFleetCoordinatorHandler implements the fleet coordinator logic
type RunFleetCoordinatorHandler struct {
	fleetPoolManager       *contractServices.FleetPoolManager
	workerLifecycleManager *contractServices.WorkerLifecycleManager
	contractMarketService  *contractServices.ContractMarketService
	marketRepo             market.MarketRepository
	shipRepo               navigation.ShipRepository
	shipAssignmentRepo     domainContainer.ShipAssignmentRepository
	daemonClient           daemon.DaemonClient
	graphProvider          system.ISystemGraphProvider
	converter              system.IWaypointConverter
	clock                  shared.Clock
}

// NewRunFleetCoordinatorHandler creates a new fleet coordinator handler
// The clock parameter is optional - if nil, defaults to RealClock for production use
func NewRunFleetCoordinatorHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	contractRepo domainContract.ContractRepository,
	marketRepo market.MarketRepository,
	shipAssignmentRepo domainContainer.ShipAssignmentRepository,
	daemonClient daemon.DaemonClient,
	graphProvider system.ISystemGraphProvider,
	converter system.IWaypointConverter,
	containerRepo contractServices.ContainerRepository,
	clock shared.Clock,
) *RunFleetCoordinatorHandler {
	// Default to RealClock if not provided
	if clock == nil {
		clock = shared.NewRealClock()
	}

	fleetPoolManager := contractServices.NewFleetPoolManager(mediator, shipRepo, shipAssignmentRepo)
	workerLifecycleManager := contractServices.NewWorkerLifecycleManager(daemonClient, containerRepo)
	contractMarketService := contractServices.NewContractMarketService(mediator, contractRepo)

	return &RunFleetCoordinatorHandler{
		fleetPoolManager:       fleetPoolManager,
		workerLifecycleManager: workerLifecycleManager,
		contractMarketService:  contractMarketService,
		marketRepo:             marketRepo,
		shipRepo:               shipRepo,
		shipAssignmentRepo:     shipAssignmentRepo,
		daemonClient:           daemonClient,
		graphProvider:          graphProvider,
		converter:              converter,
		clock:                  clock,
	}
}

// Handle executes the fleet coordinator command
func (h *RunFleetCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	cmd, ok := request.(*RunFleetCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &RunFleetCoordinatorResponse{
		ContractsCompleted: 0,
		Errors:             []string{},
	}

	// No pool initialization - ships are discovered dynamically

	// Create unbuffered completion channel for worker notifications
	// IMPORTANT: Unbuffered so signals are only received when actively waiting
	completionChan := make(chan string)

	if err := h.workerLifecycleManager.StopExistingWorkers(ctx, cmd.PlayerID.Value()); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed during existing worker cleanup: %v", err), nil)
	}

	// Track current active worker container ID for cleanup on shutdown
	var activeWorkerContainerID string

	// Track previous ship for balancing logic
	var previousShipSymbol string

	// Step 4: Main coordinator loop (infinite)
	// Execute one contract at a time (game constraint: one active contract per player)
	for {
		select {
		case <-ctx.Done():
			// Context cancelled, exit
			if activeWorkerContainerID != "" {
				logger.Log("INFO", fmt.Sprintf("Stopping active worker container: %s", activeWorkerContainerID), nil)
				_ = h.workerLifecycleManager.StopWorkerContainer(ctx, activeWorkerContainerID)
			}
			return result, ctx.Err()
		default:
			// Continue with contract assignment
		}

		// Dynamically discover all idle light hauler ships
		_, availableShips, err := appContract.FindIdleLightHaulers(ctx, cmd.PlayerID, h.shipRepo, h.shipAssignmentRepo)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to find idle haulers: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			h.clock.Sleep(10 * time.Second)
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
					_ = h.workerLifecycleManager.StopWorkerContainer(ctx, activeWorkerContainerID)
				}
				return result, ctx.Err()
			}
			continue // Loop back to check for available ships
		}

		// CRITICAL CHECK: Prevent multiple workers by checking if any worker is already running
		// This prevents race conditions when negotiation fails early in the loop
		existingActiveWorkers, err := h.workerLifecycleManager.FindExistingWorkers(ctx, cmd.PlayerID.Value())
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
					_ = h.workerLifecycleManager.StopWorkerContainer(ctx, activeWorkerContainerID)
				}
				return result, ctx.Err()
			}
			continue
		}

		// Negotiate contract (use any ship from pool for negotiation)
		logger.Log("INFO", "Negotiating new contract...", nil)
		contract, err := h.contractMarketService.NegotiateContract(ctx, availableShips[0], cmd.PlayerID.Value())
		if err != nil {
			errMsg := fmt.Sprintf("Failed to negotiate contract: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			h.clock.Sleep(30 * time.Second)
			continue
		}

		// Find purchase market for contract
		logger.Log("INFO", "Finding purchase market...", nil)
		purchaseMarket, err := appContract.FindPurchaseMarket(ctx, contract, h.marketRepo, cmd.PlayerID.Value())
		if err != nil {
			errMsg := fmt.Sprintf("Failed to find purchase market: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			h.clock.Sleep(30 * time.Second)
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
		selectedShip, distance, err := appContract.SelectClosestShip(
			ctx,
			availableShips,
			h.shipRepo,
			h.graphProvider,
			h.converter,
			purchaseMarket,
			requiredCargo,
			cmd.PlayerID.Value(),
		)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to select ship: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			h.clock.Sleep(10 * time.Second)
			continue
		}

		logger.Log("INFO", fmt.Sprintf("Selected %s (distance: %.2f units)", selectedShip, distance), nil)

		// If selected ship is different from previous ship, balance previous ship's position
		if previousShipSymbol != "" && previousShipSymbol != selectedShip {
			logger.Log("INFO", fmt.Sprintf("Selected ship changed from %s to %s - balancing previous ship position", previousShipSymbol, selectedShip), nil)

			// Launch balancing command asynchronously (fire-and-forget)
			go func(shipSymbol string, playerID shared.PlayerID) {
				balanceCmd := &BalanceShipPositionCommand{
					ShipSymbol: shipSymbol,
					PlayerID:   playerID,
				}
				// Create background context since parent context may be cancelled
				balanceCtx := context.Background()
				balanceCtx = common.WithLogger(balanceCtx, common.LoggerFromContext(ctx))

				_, err := h.fleetPoolManager.GetMediator().Send(balanceCtx, balanceCmd)
				if err != nil {
					logger.Log("WARNING", fmt.Sprintf("Failed to balance ship %s position: %v", shipSymbol, err), nil)
				}
			}(previousShipSymbol, cmd.PlayerID)
		}

		// Create worker container ID
		workerContainerID := utils.GenerateContainerID("contract-work", selectedShip)

		// Create worker command
		workerCmd := &RunWorkflowCommand{
			ShipSymbol:         selectedShip,
			PlayerID:           cmd.PlayerID,
			ContainerID:        workerContainerID,
			CoordinatorID:      cmd.ContainerID,
			CompletionCallback: completionChan,
		}

		// Step 1: Persist worker container to DB (synchronous, no start)
		logger.Log("INFO", fmt.Sprintf("Persisting worker container %s for %s", workerContainerID, selectedShip), nil)
		if err := h.daemonClient.PersistContractWorkflowContainer(ctx, workerContainerID, uint(cmd.PlayerID.Value()), workerCmd); err != nil {
			errMsg := fmt.Sprintf("Failed to persist worker container: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			h.clock.Sleep(10 * time.Second)
			continue
		}

		// Step 2: Transfer ship from coordinator to worker (atomic, no race condition)
		logger.Log("INFO", fmt.Sprintf("Transferring %s to worker container", selectedShip), nil)
		if err := h.shipAssignmentRepo.Transfer(ctx, selectedShip, cmd.ContainerID, workerContainerID); err != nil {
			errMsg := fmt.Sprintf("Failed to transfer ship %s: %v", selectedShip, err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			// Clean up: stop worker container on transfer failure
			_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
			h.clock.Sleep(10 * time.Second)
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
			h.clock.Sleep(10 * time.Second)
			continue
		}

		activeWorkerContainerID = workerContainerID

		// Block waiting for worker completion
		logger.Log("INFO", fmt.Sprintf("Waiting for %s to complete contract...", selectedShip), nil)
		select {
		case completedShip := <-completionChan:
			logger.Log("INFO", fmt.Sprintf("Contract completed by %s", completedShip), nil)
			result.ContractsCompleted++
			activeWorkerContainerID = ""

			// Ship will no longer be transferred back to coordinator - it's automatically available
			// since we're using dynamic discovery instead of pool assignments

			// Store completed ship as previous ship for potential balancing in next iteration
			previousShipSymbol = completedShip

			continue

		case <-time.After(30 * time.Minute):
			// Timeout waiting for worker
			logger.Log("ERROR", fmt.Sprintf("Timeout waiting for worker %s", selectedShip), nil)
			errMsg := fmt.Sprintf("Worker timeout for ship %s", selectedShip)
			result.Errors = append(result.Errors, errMsg)
			// Loop back to try again
			continue

		case <-ctx.Done():
			logger.Log("INFO", "Context cancelled, exiting coordinator", nil)
			if activeWorkerContainerID != "" {
				logger.Log("INFO", fmt.Sprintf("Stopping active worker container: %s", activeWorkerContainerID), nil)
				_ = h.workerLifecycleManager.StopWorkerContainer(ctx, activeWorkerContainerID)
			}
			return result, ctx.Err()
		}

		// This line should NEVER be reached (all cases have continue/return)
		logger.Log("ERROR", "CRITICAL: Code execution fell through select statement!", nil)
	}
}


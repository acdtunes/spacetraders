package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
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

	fleetPoolManager := contractServices.NewFleetPoolManager(mediator, shipRepo)
	workerLifecycleManager := contractServices.NewWorkerLifecycleManager(daemonClient, containerRepo, shipRepo)
	contractMarketService := contractServices.NewContractMarketService(mediator, contractRepo)

	return &RunFleetCoordinatorHandler{
		fleetPoolManager:       fleetPoolManager,
		workerLifecycleManager: workerLifecycleManager,
		contractMarketService:  contractMarketService,
		marketRepo:             marketRepo,
		shipRepo:               shipRepo,
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
		// Use CommandShipFallback for contracts - allow command ship if no haulers available
		_, availableShips, err := appContract.FindIdleLightHaulers(ctx, cmd.PlayerID, h.shipRepo, appContract.CommandShipFallback)
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

		// Check if contract is already complete (all deliveries fulfilled)
		allDeliveriesFulfilled := true
		for _, delivery := range contract.Terms().Deliveries {
			if delivery.UnitsRequired > delivery.UnitsFulfilled {
				allDeliveriesFulfilled = false
				break
			}
		}
		if allDeliveriesFulfilled {
			logger.Log("INFO", "Contract deliveries complete - fulfilling contract to get reward", map[string]interface{}{
				"contract_id": contract.ContractID(),
			})
			// Try to fulfill the contract via API to claim rewards
			err := h.contractMarketService.FulfillContract(ctx, contract, cmd.PlayerID)
			if err != nil {
				logger.Log("ERROR", fmt.Sprintf("Failed to fulfill contract: %v", err), nil)
			} else {
				logger.Log("INFO", "Contract fulfilled successfully - will negotiate new contract", nil)
				result.ContractsCompleted++
			}
			h.clock.Sleep(5 * time.Second) // Brief pause before negotiating new contract
			continue
		}

		// Find purchase market for contract
		logger.Log("INFO", "Finding purchase market...", nil)
		purchaseMarket, err := appContract.FindPurchaseMarket(ctx, contract, h.marketRepo, cmd.PlayerID.Value())
		if err != nil {
			// Market data not yet available - this is expected while scouts are scanning
			logger.Log("INFO", "Purchase market not yet available - waiting for scouts to scan market data", map[string]interface{}{
				"contract_id": contract.ContractID(),
				"error":       err.Error(),
			})
			// Sleep and retry - scouts will eventually scan the required market
			h.clock.Sleep(30 * time.Second)
			continue
		}

		logger.Log("INFO", "Cheapest market found", nil)

		// Extract required cargo for delivery (for ship selection prioritization)
		var requiredCargo string
		var unitsNeeded int
		for _, delivery := range contract.Terms().Deliveries {
			if delivery.UnitsRequired > delivery.UnitsFulfilled {
				requiredCargo = delivery.TradeSymbol
				unitsNeeded = delivery.UnitsRequired - delivery.UnitsFulfilled
				break
			}
		}

		// Check for in-flight cargo from active workers (prevent duplicate purchases on restart)
		inFlightCargo, err := h.calculateInFlightCargo(ctx, requiredCargo, cmd.PlayerID.Value())
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to calculate in-flight cargo: %v", err), nil)
			// Continue anyway - better to risk duplication than block indefinitely
		}

		// If there's already enough in-flight cargo, wait for delivery instead of assigning new work
		if inFlightCargo >= unitsNeeded {
			logger.Log("INFO", fmt.Sprintf("Contract already has %d units of %s in-flight (needed: %d) - waiting for delivery instead of assigning new ship",
				inFlightCargo, requiredCargo, unitsNeeded), nil)
			// Wait for worker completion
			select {
			case shipSymbol := <-completionChan:
				logger.Log("INFO", fmt.Sprintf("Active worker completed for ship %s", shipSymbol), nil)
				activeWorkerContainerID = "" // Worker completed
				// Loop back to check contract status
			case <-time.After(1 * time.Minute):
				logger.Log("INFO", "Timeout waiting for delivery, will re-check", nil)
			case <-ctx.Done():
				if activeWorkerContainerID != "" {
					logger.Log("INFO", fmt.Sprintf("Stopping active worker container: %s", activeWorkerContainerID), nil)
					_ = h.workerLifecycleManager.StopWorkerContainer(ctx, activeWorkerContainerID)
				}
				return result, ctx.Err()
			}
			continue
		}

		// Log remaining units needed after accounting for in-flight cargo
		if inFlightCargo > 0 {
			logger.Log("INFO", fmt.Sprintf("Contract needs %d more units of %s (%d in-flight, %d required, %d fulfilled)",
				unitsNeeded-inFlightCargo, requiredCargo, inFlightCargo, unitsNeeded+contract.Terms().Deliveries[0].UnitsFulfilled, contract.Terms().Deliveries[0].UnitsFulfilled), nil)
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
			go func(shipSymbol string, playerID shared.PlayerID, coordinatorID string) {
				balanceCmd := &BalanceShipPositionCommand{
					ShipSymbol:    shipSymbol,
					PlayerID:      playerID,
					CoordinatorID: coordinatorID,
				}
				// Create background context since parent context may be cancelled
				balanceCtx := context.Background()
				balanceCtx = common.WithLogger(balanceCtx, common.LoggerFromContext(ctx))

				_, err := h.fleetPoolManager.GetMediator().Send(balanceCtx, balanceCmd)
				if err != nil {
					logger.Log("WARNING", fmt.Sprintf("Failed to balance ship %s position: %v", shipSymbol, err), nil)
				}
			}(previousShipSymbol, cmd.PlayerID, cmd.ContainerID)
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

		// Step 2: Assign ship to worker (dynamic discovery - no pre-assignment needed)
		logger.Log("INFO", fmt.Sprintf("Assigning %s to worker container", selectedShip), nil)
		ship, err := h.shipRepo.FindBySymbol(ctx, selectedShip, cmd.PlayerID)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to load ship %s: %v", selectedShip, err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
			h.clock.Sleep(10 * time.Second)
			continue
		}
		if err := ship.AssignToContainer(workerContainerID, h.clock); err != nil {
			errMsg := fmt.Sprintf("Failed to assign ship %s: %v", selectedShip, err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			// Clean up: stop worker container on assignment failure
			_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
			h.clock.Sleep(10 * time.Second)
			continue
		}
		if err := h.shipRepo.Save(ctx, ship); err != nil {
			errMsg := fmt.Sprintf("Failed to save ship assignment %s: %v", selectedShip, err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			_ = h.workerLifecycleManager.StopWorkerContainer(ctx, workerContainerID)
			h.clock.Sleep(10 * time.Second)
			continue
		}

		// Step 3: Start the worker container (ship is safely assigned)
		logger.Log("INFO", fmt.Sprintf("Starting worker container for %s", selectedShip), nil)
		if err := h.daemonClient.StartContractWorkflowContainer(ctx, workerContainerID, completionChan); err != nil {
			errMsg := fmt.Sprintf("Failed to start worker container: %v", err)
			logger.Log("ERROR", errMsg, nil)
			result.Errors = append(result.Errors, errMsg)
			// Clean up: release assignment on failure (ship returns to idle pool)
			ship.ForceRelease("worker_start_failed", h.clock)
			_ = h.shipRepo.Save(ctx, ship)
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

// calculateInFlightCargo calculates the total cargo of a specific trade symbol
// that is currently held by ships working on active contract workflows.
// This is used during restart recovery to prevent duplicate cargo purchases.
func (h *RunFleetCoordinatorHandler) calculateInFlightCargo(
	ctx context.Context,
	tradeSymbol string,
	playerID int,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Find all active CONTRACT_WORKFLOW containers
	activeWorkers, err := h.workerLifecycleManager.FindExistingWorkers(ctx, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to find existing workers: %w", err)
	}

	if len(activeWorkers) == 0 {
		return 0, nil
	}

	totalInFlight := 0

	// For each active worker, find its assigned ships and check their cargo
	for _, worker := range activeWorkers {
		ships, err := h.shipRepo.FindByContainer(ctx, worker.ID, shared.MustNewPlayerID(playerID))
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to get ships for container %s: %v", worker.ID, err), nil)
			continue
		}

		for _, ship := range ships {
			// Count cargo of the required trade symbol
			for _, item := range ship.Cargo().Inventory {
				if item.Symbol == tradeSymbol {
					totalInFlight += item.Units
					logger.Log("INFO", fmt.Sprintf("Found %d units of %s in ship %s cargo (worker %s)",
						item.Units, tradeSymbol, ship.ShipSymbol(), worker.ID), nil)
				}
			}
		}
	}

	if totalInFlight > 0 {
		logger.Log("INFO", fmt.Sprintf("Total in-flight cargo: %d units of %s", totalInFlight, tradeSymbol), nil)
	}

	return totalInFlight, nil
}


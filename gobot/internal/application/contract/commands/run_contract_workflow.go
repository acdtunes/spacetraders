package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// Type aliases for convenience
type RunWorkflowCommand = contractTypes.RunWorkflowCommand
type RunWorkflowResponse = contractTypes.RunWorkflowResponse

// RunWorkflowHandler implements the complete contract workflow
// following the exact Python implementation pattern:
//
// 1. Check for existing active contracts (idempotency)
// 2. Negotiate new contract or resume existing (handle error 4511)
// 3. Evaluate profitability (log only, always accept)
// 4. Accept contract (skip if already accepted)
// 5. For each delivery:
//   - Reload ship state
//   - Jettison wrong cargo if needed
//   - Calculate purchase needs
//   - Execute multi-trip loop if units > cargo capacity
//   - For each trip:
//   - Navigate to seller
//   - Dock
//   - Purchase with transaction splitting (handled by PurchaseCargoHandler)
//   - Navigate to delivery
//   - Dock
//   - Deliver cargo
//
// 6. Fulfill contract
// 7. Calculate profit
// 8. Transfer ship back to coordinator (if applicable)
// 9. Signal completion via channel (if applicable)
type RunWorkflowHandler struct {
	lifecycleService   *contractServices.ContractLifecycleService
	deliveryExecutor   *contractServices.DeliveryExecutor
	shipAssignmentRepo domainContainer.ShipAssignmentRepository
}

// NewRunWorkflowHandler creates a new contract workflow handler
func NewRunWorkflowHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	contractRepo domainContract.ContractRepository,
	shipAssignmentRepo domainContainer.ShipAssignmentRepository,
) *RunWorkflowHandler {
	cargoManager := contractServices.NewCargoManager(mediator, shipRepo)
	lifecycleService := contractServices.NewContractLifecycleService(mediator, contractRepo)
	deliveryExecutor := contractServices.NewDeliveryExecutor(mediator, shipRepo, cargoManager)

	return &RunWorkflowHandler{
		lifecycleService:   lifecycleService,
		deliveryExecutor:   deliveryExecutor,
		shipAssignmentRepo: shipAssignmentRepo,
	}
}

// Handle executes the contract workflow command
func (h *RunWorkflowHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunWorkflowCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &RunWorkflowResponse{
		Negotiated:  false,
		Accepted:    false,
		Fulfilled:   false,
		TotalProfit: 0,
		TotalTrips:  0,
		Error:       "",
	}

	// Execute workflow
	if err := h.executeWorkflow(ctx, cmd, result); err != nil {
		result.Error = err.Error()
		return result, err
	}

	logger := common.LoggerFromContext(ctx)

	// NOTE: With dynamic discovery, ships are NOT transferred back to coordinator
	// They are released by ContainerRunner and discovered dynamically in the next iteration
	// The ContainerRunner releases ship assignments on completion/failure

	// Signal completion if callback provided
	if cmd.CompletionCallback != nil {
		select {
		case cmd.CompletionCallback <- cmd.ShipSymbol:
			logger.Log("INFO", "Contract workflow completion signaled", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "signal_completion",
			})
		default:
			// Channel full or closed, log but don't error
			logger.Log("WARNING", "Contract workflow completion signal failed", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "signal_completion",
				"reason":      "channel_full_or_closed",
			})
		}
	}

	return result, nil
}

// executeWorkflow handles the contract workflow execution
func (h *RunWorkflowHandler) executeWorkflow(
	ctx context.Context,
	cmd *RunWorkflowCommand,
	result *RunWorkflowResponse,
) error {
	contract, wasNegotiated, err := h.lifecycleService.FindOrNegotiateContract(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return err
	}

	if wasNegotiated {
		result.Negotiated = true
	}

	profitabilityResp, err := h.lifecycleService.EvaluateContractProfitability(ctx, cmd.ShipSymbol, cmd.PlayerID, contract)
	if err != nil {
		// Non-fatal - logged in method
	}

	var wasAccepted bool
	contract, wasAccepted, err = h.lifecycleService.AcceptContractIfNeeded(ctx, contract, cmd.PlayerID)
	if err != nil {
		return err
	}

	if wasAccepted {
		result.Accepted = true
	}

	contract, err = h.deliveryExecutor.ProcessAllDeliveries(ctx, cmd.ShipSymbol, cmd.PlayerID, contract, profitabilityResp, result, cmd.ContainerID)
	if err != nil {
		return err
	}

	if err := h.lifecycleService.FulfillContract(ctx, contract, cmd.PlayerID); err != nil {
		return err
	}

	result.Fulfilled = true

	result.TotalProfit += h.lifecycleService.CalculateTotalProfit(contract)

	return nil
}

// transferShipBackToCoordinator transfers ship assignment from worker back to coordinator
func (h *RunWorkflowHandler) transferShipBackToCoordinator(
	ctx context.Context,
	cmd *RunWorkflowCommand,
) error {
	logger := common.LoggerFromContext(ctx)

	// If no container ID provided, cannot transfer
	if cmd.ContainerID == "" {
		logger.Log("WARNING", "Cannot transfer ship: worker container ID not provided", map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"action":         "transfer_to_coordinator",
			"coordinator_id": cmd.CoordinatorID,
		})
		return nil
	}

	if err := h.shipAssignmentRepo.Transfer(ctx, cmd.ShipSymbol, cmd.ContainerID, cmd.CoordinatorID); err != nil {
		logger.Log("WARNING", "Failed to transfer ship back to coordinator", map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"action":         "transfer_to_coordinator",
			"coordinator_id": cmd.CoordinatorID,
			"worker_id":      cmd.ContainerID,
			"error":          err.Error(),
		})
		// Fallback: try inserting new assignment if transfer fails
		assignment := domainContainer.NewShipAssignment(cmd.ShipSymbol, cmd.PlayerID.Value(), cmd.CoordinatorID, nil)
		_ = h.shipAssignmentRepo.Assign(ctx, assignment)
		return err
	}

	logger.Log("INFO", "Ship successfully transferred back to coordinator", map[string]interface{}{
		"ship_symbol":    cmd.ShipSymbol,
		"action":         "transfer_to_coordinator",
		"coordinator_id": cmd.CoordinatorID,
		"worker_id":      cmd.ContainerID,
	})

	return nil
}

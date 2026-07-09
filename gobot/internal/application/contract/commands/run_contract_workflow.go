package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
	lifecycleService *contractServices.ContractLifecycleService
	deliveryExecutor *contractServices.DeliveryExecutor
}

// NewRunWorkflowHandler creates a new contract workflow handler
func NewRunWorkflowHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	contractRepo domainContract.ContractRepository,
	clock shared.Clock,
) *RunWorkflowHandler {
	cargoManager := contractServices.NewCargoManager(mediator, shipRepo)
	lifecycleService := contractServices.NewContractLifecycleService(mediator, contractRepo)
	deliveryExecutor := contractServices.NewDeliveryExecutor(mediator, shipRepo, cargoManager)

	return &RunWorkflowHandler{
		lifecycleService: lifecycleService,
		deliveryExecutor: deliveryExecutor,
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
		// PARK, don't crash (sp-vwhi): insufficient-credits during purchase
		// is a clean recoverable exit, not a container crash. A nil Go
		// error here means the container runner does NOT count this as a
		// failure/restart - the dynamic-discovery fleet coordinator simply
		// re-picks-up this ship's unfulfilled contract on its next pass,
		// once the treasury recovers. Every other executeWorkflow error
		// keeps the existing crash-and-restart behavior unchanged.
		var insufficientErr *contractServices.ErrInsufficientCredits
		if errors.As(err, &insufficientErr) {
			result.Error = insufficientErr.Error()
			return result, nil
		}

		result.Error = err.Error()
		return result, err
	}

	// NOTE: With dynamic discovery, ships are NOT transferred back to coordinator
	// They are released by ContainerRunner and discovered dynamically in the next iteration
	// The ContainerRunner releases ship assignments on completion/failure
	// Completion is signaled via event bus (WorkerCompletedEvent published by ContainerRunner)

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

	// Claim this ship's NEXT contract immediately, at whatever waypoint the
	// last delivery already left it docked at - no deadhead trip back to
	// base first. Before this, a fulfilled ship had no path to claim its own
	// next contract: it released back to the fleet coordinator and waited to
	// be rediscovered, which measured fleet-wide as 74 ship-hours/day of idle
	// time between fulfillment and next acceptance (sp-qpmi). This is a
	// latency optimization on top of an already-successful fulfillment, so
	// failure here is non-fatal and never turns this result into an error -
	// it just falls back to the coordinator's normal discovery pass.
	h.negotiateNextContractBestEffort(ctx, cmd)

	return nil
}

// negotiateNextContractBestEffort reuses the same idempotent lifecycle calls
// FindOrNegotiateContract makes for a fresh worker (FindActiveContracts
// first, so it never re-negotiates a contract another path already claimed)
// to negotiate and accept this ship's next contract right after fulfillment.
// Neither negotiate nor accept require any particular ship location - only
// DOCKED state for negotiate, which already holds because DeliverCargo always
// navigates-and-docks the ship at the delivery waypoint first. Any failure is
// logged and swallowed: the coordinator's normal discovery pass remains the
// fallback path, so a transient error here cannot regress contract success
// rate.
func (h *RunWorkflowHandler) negotiateNextContractBestEffort(ctx context.Context, cmd *RunWorkflowCommand) {
	logger := common.LoggerFromContext(ctx)

	nextContract, wasNegotiated, err := h.lifecycleService.FindOrNegotiateContract(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", "Best-effort next-contract negotiation failed; falling back to coordinator discovery", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "negotiate_next_contract",
			"error":       err.Error(),
		})
		return
	}

	if _, _, err := h.lifecycleService.AcceptContractIfNeeded(ctx, nextContract, cmd.PlayerID); err != nil {
		logger.Log("WARNING", "Best-effort next-contract acceptance failed; falling back to coordinator discovery", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "accept_next_contract",
			"contract_id": nextContract.ContractID(),
			"error":       err.Error(),
		})
		return
	}

	logger.Log("INFO", "Claimed next contract immediately after fulfillment, without returning to base", map[string]interface{}{
		"ship_symbol":    cmd.ShipSymbol,
		"action":         "negotiate_on_delivery",
		"contract_id":    nextContract.ContractID(),
		"was_negotiated": wasNegotiated,
	})
}

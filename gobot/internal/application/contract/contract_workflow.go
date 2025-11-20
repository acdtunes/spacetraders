package contract

import (
	"context"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// ContractWorkflowCommand orchestrates complete contract workflow execution
type ContractWorkflowCommand struct {
	ShipSymbol         string
	PlayerID           int
	CoordinatorID      string           // Parent coordinator container ID (optional)
	CompletionCallback chan<- string    // Signal completion to coordinator (optional)
}

// ContractWorkflowResponse contains workflow execution results
type ContractWorkflowResponse struct {
	Negotiated  bool
	Accepted    bool
	Fulfilled   bool
	TotalProfit int
	TotalTrips  int
	Error       string
}

// ContractWorkflowHandler implements the complete contract workflow
// following the exact Python implementation pattern:
//
// 1. Check for existing active contracts (idempotency)
// 2. Negotiate new contract or resume existing (handle error 4511)
// 3. Evaluate profitability (log only, always accept)
// 4. Accept contract (skip if already accepted)
// 5. For each delivery:
//    - Reload ship state
//    - Jettison wrong cargo if needed
//    - Calculate purchase needs
//    - Execute multi-trip loop if units > cargo capacity
//    - For each trip:
//      * Navigate to seller
//      * Dock
//      * Purchase with transaction splitting (handled by PurchaseCargoHandler)
//      * Navigate to delivery
//      * Dock
//      * Deliver cargo
// 6. Fulfill contract
// 7. Calculate profit
// 8. Transfer ship back to coordinator (if applicable)
// 9. Signal completion via channel (if applicable)
type ContractWorkflowHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	contractRepo       domainContract.ContractRepository
	shipAssignmentRepo daemon.ShipAssignmentRepository
}

// NewContractWorkflowHandler creates a new contract workflow handler
func NewContractWorkflowHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	contractRepo domainContract.ContractRepository,
	shipAssignmentRepo daemon.ShipAssignmentRepository,
) *ContractWorkflowHandler {
	return &ContractWorkflowHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		contractRepo:       contractRepo,
		shipAssignmentRepo: shipAssignmentRepo,
	}
}

// Handle executes the contract workflow command
func (h *ContractWorkflowHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*ContractWorkflowCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &ContractWorkflowResponse{
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

	// Transfer ship back to coordinator if applicable
	if cmd.CoordinatorID != "" {
		if err := h.transferShipBackToCoordinator(ctx, cmd); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to transfer ship back to coordinator: %v", err), nil)
		}
	}

	// Signal completion if callback provided
	if cmd.CompletionCallback != nil {
		select {
		case cmd.CompletionCallback <- cmd.ShipSymbol:
			logger.Log("INFO", fmt.Sprintf("Signaled completion for ship %s", cmd.ShipSymbol), nil)
		default:
			// Channel full or closed, log but don't error
			logger.Log("WARNING", fmt.Sprintf("Could not signal completion for ship %s (channel full/closed)", cmd.ShipSymbol), nil)
		}
	}

	return result, nil
}

// executeWorkflow handles the contract workflow execution
func (h *ContractWorkflowHandler) executeWorkflow(
	ctx context.Context,
	cmd *ContractWorkflowCommand,
	result *ContractWorkflowResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	// Step 1: Check for existing active contracts (idempotency)
	activeContracts, err := h.contractRepo.FindActiveContracts(ctx, cmd.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to check active contracts: %w", err)
	}

	var contract *domainContract.Contract
	var wasNegotiated bool

	if len(activeContracts) > 0 {
		// Resume existing active contract
		contract = activeContracts[0]
		wasNegotiated = false
		logger.Log("INFO", fmt.Sprintf("Resuming existing contract: %s", contract.ContractID()), nil)
	} else {
		// Step 2: Negotiate new contract
		logger.Log("INFO", "Negotiating new contract", nil)
		negotiateCmd := &NegotiateContractCommand{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   cmd.PlayerID,
		}

		negotiateResp, err := h.mediator.Send(ctx, negotiateCmd)
		if err != nil {
			return fmt.Errorf("failed to negotiate contract: %w", err)
		}

		negotiateResult := negotiateResp.(*NegotiateContractResponse)

		contract = negotiateResult.Contract
		wasNegotiated = negotiateResult.WasNegotiated

		if wasNegotiated {
			result.Negotiated = true
			logger.Log("INFO", fmt.Sprintf("Contract negotiated: %s", contract.ContractID()), nil)
		}
	}

	// Step 3: Evaluate profitability (log only - always accept)
	logger.Log("INFO", fmt.Sprintf("Evaluating contract profitability for contract %s", contract.ContractID()), nil)
	profitabilityQuery := &EvaluateContractProfitabilityQuery{
		Contract:        contract,
		ShipSymbol:      cmd.ShipSymbol,
		PlayerID:        cmd.PlayerID,
		FuelCostPerTrip: 0, // Simplified for now
	}

	profitabilityResp, err := h.mediator.Send(ctx, profitabilityQuery)
	if err != nil {
		// Log warning but continue (non-fatal)
		logger.Log("WARNING", fmt.Sprintf("Failed to evaluate profitability: %v", err), nil)
	} else {
		profitResult := profitabilityResp.(*ProfitabilityResult)
		if !profitResult.IsProfitable {
			logger.Log("WARNING", fmt.Sprintf("Contract unprofitable (%s) but accepting anyway", profitResult.Reason), nil)
		} else {
			logger.Log("INFO", "Contract is profitable", nil)
		}
	}

	// Step 4: Accept contract (skip if already accepted)
	if !contract.Accepted() {
		acceptCmd := &AcceptContractCommand{
			ContractID: contract.ContractID(),
			PlayerID:   cmd.PlayerID,
		}

		acceptResp, err := h.mediator.Send(ctx, acceptCmd)
		if err != nil {
			return fmt.Errorf("failed to accept contract: %w", err)
		}

		acceptResult := acceptResp.(*AcceptContractResponse)

		contract = acceptResult.Contract
		result.Accepted = true
	}

	// Step 5: Process each delivery
	logger.Log("INFO", fmt.Sprintf("Processing %d deliveries", len(contract.Terms().Deliveries)), nil)
	for _, delivery := range contract.Terms().Deliveries {
		unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
		logger.Log("INFO", fmt.Sprintf("Delivery: %s, required=%d, fulfilled=%d, remaining=%d",
			delivery.TradeSymbol, delivery.UnitsRequired, delivery.UnitsFulfilled, unitsRemaining), nil)
		if unitsRemaining == 0 {
			logger.Log("INFO", "Delivery already fulfilled, skipping", nil)
			continue // Already fulfilled
		}
		logger.Log("INFO", "Processing delivery...", nil)

		// Step 6: Reload ship state (critical for fresh cargo data)
		logger.Log("INFO", "Reloading ship state...", nil)
		ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("Failed to reload ship: %v", err), nil)
			return fmt.Errorf("failed to reload ship: %w", err)
		}
		logger.Log("INFO", fmt.Sprintf("Ship loaded: cargo=%d/%d", ship.Cargo().Units, ship.Cargo().Capacity), nil)

		currentUnits := ship.Cargo().GetItemUnits(delivery.TradeSymbol)
		logger.Log("INFO", fmt.Sprintf("Current %s units in cargo: %d", delivery.TradeSymbol, currentUnits), nil)

		// Step 7: Jettison wrong cargo if needed
		hasWrongCargo := ship.Cargo().HasItemsOtherThan(delivery.TradeSymbol)
		needsSpace := currentUnits < unitsRemaining || ship.Cargo().IsFull()

		logger.Log("DEBUG", fmt.Sprintf("Jettison check: hasWrongCargo=%v, needsSpace=%v, currentUnits=%d, unitsRemaining=%d, isFull=%v",
			hasWrongCargo, needsSpace, currentUnits, unitsRemaining, ship.Cargo().IsFull()), nil)

		if hasWrongCargo && needsSpace {
			logger.Log("INFO", "Jettisoning wrong cargo...", nil)
			if err := h.jettisonWrongCargo(ctx, ship, delivery.TradeSymbol, cmd.PlayerID); err != nil {
				return fmt.Errorf("failed to jettison cargo: %w", err)
			}

			// Re-sync ship after jettison
			ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to reload ship after jettison: %w", err)
			}
			currentUnits = ship.Cargo().GetItemUnits(delivery.TradeSymbol)
			logger.Log("INFO", fmt.Sprintf("Jettison complete, cargo now: %d/%d", ship.Cargo().Units, ship.Cargo().Capacity), nil)
		}

		// Step 8: Calculate purchase needs
		unitsToPurchase := unitsRemaining - currentUnits
		logger.Log("INFO", fmt.Sprintf("Units to purchase: %d (remaining=%d - current=%d)", unitsToPurchase, unitsRemaining, currentUnits), nil)

		// Step 9: Purchase cargo if needed
		if unitsToPurchase > 0 {
			// Get profitability result for cheapest market
			profitResult := profitabilityResp.(*ProfitabilityResult)
			cheapestMarket := profitResult.CheapestMarketWaypoint
			logger.Log("INFO", fmt.Sprintf("Cheapest market: %s", cheapestMarket), nil)

			// Multi-trip purchase loop
			trips := int(math.Ceil(float64(unitsToPurchase) / float64(ship.Cargo().Capacity)))
			result.TotalTrips += trips
			logger.Log("INFO", fmt.Sprintf("Starting multi-trip purchase: %d trips needed", trips), nil)

			for trip := 0; trip < trips; trip++ {
				// Calculate available cargo space (capacity - current load)
				availableSpace := ship.Cargo().Capacity - ship.Cargo().Units

				// Use available space, not total capacity
				unitsThisTrip := utils.Min(availableSpace, unitsToPurchase)

				// Skip if no space available
				if unitsThisTrip <= 0 {
					logger.Log("WARNING", "No cargo space available, ending purchase loop", nil)
					break
				}

				// Navigate to seller (returns updated ship)
				ship, err = h.navigateToWaypoint(ctx, cmd.ShipSymbol, cheapestMarket, cmd.PlayerID)
				if err != nil {
					return fmt.Errorf("failed to navigate to market: %w", err)
				}

				// Dock at market
				if err := h.dockShip(ctx, ship, cmd.PlayerID); err != nil {
					return fmt.Errorf("failed to dock at market: %w", err)
				}

				// Purchase cargo (transaction splitting handled by PurchaseCargoHandler)
				purchaseCmd := &appShip.PurchaseCargoCommand{
					ShipSymbol: ship.ShipSymbol(),
					GoodSymbol: delivery.TradeSymbol,
					Units:      unitsThisTrip,
					PlayerID:   cmd.PlayerID,
				}

				purchaseResp, err := h.mediator.Send(ctx, purchaseCmd)
				if err != nil {
					return fmt.Errorf("failed to purchase cargo: %w", err)
				}

				_ = purchaseResp // Response unused after error check removed

				unitsToPurchase -= unitsThisTrip

				// Reload ship after purchase
				ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
				if err != nil {
					return fmt.Errorf("failed to reload ship after purchase: %w", err)
				}
			}
		}

		// Step 10: Deliver all cargo for this delivery item
		// This handles both fresh purchases and recovered cargo from interruptions
		ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return fmt.Errorf("failed to reload ship before delivery: %w", err)
		}

		unitsToDeliver := ship.Cargo().GetItemUnits(delivery.TradeSymbol)
		logger.Log("INFO", fmt.Sprintf("Delivering %d units of %s", unitsToDeliver, delivery.TradeSymbol), nil)

		if unitsToDeliver > 0 {
			// Navigate to delivery destination (returns updated ship)
			ship, err = h.navigateToWaypoint(ctx, cmd.ShipSymbol, delivery.DestinationSymbol, cmd.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to navigate to delivery: %w", err)
			}

			// Dock at delivery
			if err := h.dockShip(ctx, ship, cmd.PlayerID); err != nil {
				return fmt.Errorf("failed to dock at delivery: %w", err)
			}

			// Deliver cargo
			deliverCmd := &DeliverContractCommand{
				ContractID:  contract.ContractID(),
				ShipSymbol:  cmd.ShipSymbol,
				TradeSymbol: delivery.TradeSymbol,
				Units:       unitsToDeliver,
				PlayerID:    cmd.PlayerID,
			}

			deliverResp, err := h.mediator.Send(ctx, deliverCmd)
			if err != nil {
				return fmt.Errorf("failed to deliver cargo: %w", err)
			}

			deliverResult := deliverResp.(*DeliverContractResponse)
			contract = deliverResult.Contract
		}
	}

	// Step 11: Fulfill contract
	fulfillCmd := &FulfillContractCommand{
		ContractID: contract.ContractID(),
		PlayerID:   cmd.PlayerID,
	}

	fulfillResp, err := h.mediator.Send(ctx, fulfillCmd)
	if err != nil {
		return fmt.Errorf("failed to fulfill contract: %w", err)
	}

	_ = fulfillResp // Response unused after error check removed

	result.Fulfilled = true

	// Calculate profit (simplified - use actual payment from contract)
	totalPayment := contract.Terms().Payment.OnAccepted + contract.Terms().Payment.OnFulfilled
	result.TotalProfit += totalPayment

	return nil
}

// transferShipBackToCoordinator transfers ship assignment from worker back to coordinator
func (h *ContractWorkflowHandler) transferShipBackToCoordinator(
	ctx context.Context,
	cmd *ContractWorkflowCommand,
) error {
	logger := common.LoggerFromContext(ctx)

	// Note: Container ID will need to be passed via metadata when this handler is invoked
	// For now, we'll need to get it from somewhere. This is a placeholder.
	// The actual container ID should come from the container runner context.

	// TODO: Get actual worker container ID from context
	// For now, we'll skip the transfer and let the coordinator handle it
	logger.Log("INFO", fmt.Sprintf("Ship transfer back to coordinator: %s -> %s", cmd.ShipSymbol, cmd.CoordinatorID), nil)

	return nil
}

// jettisonWrongCargo jettisons all cargo items except the specified symbol
func (h *ContractWorkflowHandler) jettisonWrongCargo(
	ctx context.Context,
	ship *navigation.Ship,
	keepSymbol string,
	playerID int,
) error {
	wrongItems := ship.Cargo().GetOtherItems(keepSymbol)

	for _, item := range wrongItems {
		jettisonCmd := &appShip.JettisonCargoCommand{
			ShipSymbol: ship.ShipSymbol(),
			PlayerID:   playerID,
			GoodSymbol: item.Symbol,
			Units:      item.Units,
		}

		jettisonResp, err := h.mediator.Send(ctx, jettisonCmd)
		if err != nil {
			return fmt.Errorf("failed to jettison %s: %w", item.Symbol, err)
		}

		_ = jettisonResp // Response unused after error check removed
	}

	return nil
}

// navigateToWaypoint navigates ship to destination and returns updated ship state
func (h *ContractWorkflowHandler) navigateToWaypoint(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID int,
) (*navigation.Ship, error) {
	// Use HIGH-LEVEL NavigateShipCommand (handles route planning, refueling, multi-hop, idempotency)
	navigateCmd := &appShip.NavigateShipCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    playerID,
	}

	resp, err := h.mediator.Send(ctx, navigateCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate: %w", err)
	}

	navResp := resp.(*appShip.NavigateShipResponse)
	return navResp.Ship, nil
}

// dockShip docks ship (idempotent)
func (h *ContractWorkflowHandler) dockShip(
	ctx context.Context,
	ship *navigation.Ship,
	playerID int,
) error {
	dockCmd := &appShip.DockShipCommand{
		ShipSymbol: ship.ShipSymbol(),
		PlayerID:   playerID,
	}

	dockResp, err := h.mediator.Send(ctx, dockCmd)
	if err != nil {
		return fmt.Errorf("failed to dock: %w", err)
	}

	_ = dockResp // Response unused after error check removed

	return nil
}


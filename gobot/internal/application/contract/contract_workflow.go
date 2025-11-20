package contract

import (
	"context"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// RunWorkflowCommand orchestrates complete contract workflow execution
type RunWorkflowCommand struct {
	ShipSymbol         string
	PlayerID           int
	CoordinatorID      string           // Parent coordinator container ID (optional)
	CompletionCallback chan<- string    // Signal completion to coordinator (optional)
}

// RunWorkflowResponse contains workflow execution results
type RunWorkflowResponse struct {
	Negotiated  bool
	Accepted    bool
	Fulfilled   bool
	TotalProfit int
	TotalTrips  int
	Error       string
}

// RunWorkflowHandler implements the complete contract workflow
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
type RunWorkflowHandler struct {
	mediator           common.Mediator
	shipRepo           navigation.ShipRepository
	contractRepo       domainContract.ContractRepository
	shipAssignmentRepo domainContainer.ShipAssignmentRepository
}

// NewRunWorkflowHandler creates a new contract workflow handler
func NewRunWorkflowHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	contractRepo domainContract.ContractRepository,
	shipAssignmentRepo domainContainer.ShipAssignmentRepository,
) *RunWorkflowHandler {
	return &RunWorkflowHandler{
		mediator:           mediator,
		shipRepo:           shipRepo,
		contractRepo:       contractRepo,
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

	// Transfer ship back to coordinator if applicable
	if cmd.CoordinatorID != "" {
		if err := h.transferShipBackToCoordinator(ctx, cmd); err != nil {
			logger.Log("WARNING", "Ship transfer back to coordinator failed", map[string]interface{}{
				"ship_symbol":    cmd.ShipSymbol,
				"action":         "transfer_to_coordinator",
				"coordinator_id": cmd.CoordinatorID,
				"error":          err.Error(),
			})
		}
	}

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
		logger.Log("INFO", "Resuming existing active contract", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "resume_contract",
			"contract_id": contract.ContractID(),
		})
	} else {
		// Step 2: Negotiate new contract
		logger.Log("INFO", "Contract negotiation initiated", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "negotiate_contract",
		})
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
			logger.Log("INFO", "Contract negotiation successful", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "contract_negotiated",
				"contract_id": contract.ContractID(),
			})
		}
	}

	// Step 3: Evaluate profitability (log only - always accept)
	logger.Log("INFO", "Contract profitability evaluation initiated", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "evaluate_profitability",
		"contract_id": contract.ContractID(),
	})
	profitabilityQuery := &EvaluateContractProfitabilityQuery{
		Contract:        contract,
		ShipSymbol:      cmd.ShipSymbol,
		PlayerID:        cmd.PlayerID,
		FuelCostPerTrip: 0, // Simplified for now
	}

	profitabilityResp, err := h.mediator.Send(ctx, profitabilityQuery)
	if err != nil {
		// Log warning but continue (non-fatal)
		logger.Log("WARNING", "Contract profitability evaluation failed", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "evaluate_profitability",
			"contract_id": contract.ContractID(),
			"error":       err.Error(),
		})
	} else {
		profitResult := profitabilityResp.(*ProfitabilityResult)
		if !profitResult.IsProfitable {
			logger.Log("WARNING", "Contract unprofitable but accepting anyway", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "accept_unprofitable",
				"contract_id": contract.ContractID(),
				"reason":      profitResult.Reason,
			})
		} else {
			logger.Log("INFO", "Contract profitability confirmed", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "profitability_check",
				"contract_id": contract.ContractID(),
			})
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
	logger.Log("INFO", "Contract deliveries processing started", map[string]interface{}{
		"ship_symbol":    cmd.ShipSymbol,
		"action":         "process_deliveries",
		"contract_id":    contract.ContractID(),
		"delivery_count": len(contract.Terms().Deliveries),
	})
	for _, delivery := range contract.Terms().Deliveries {
		unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
		logger.Log("INFO", "Contract delivery status", map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"action":         "check_delivery",
			"trade_symbol":   delivery.TradeSymbol,
			"units_required": delivery.UnitsRequired,
			"units_fulfilled": delivery.UnitsFulfilled,
			"units_remaining": unitsRemaining,
		})
		if unitsRemaining == 0 {
			logger.Log("INFO", "Contract delivery already fulfilled", map[string]interface{}{
				"ship_symbol":  cmd.ShipSymbol,
				"action":       "skip_delivery",
				"trade_symbol": delivery.TradeSymbol,
			})
			continue // Already fulfilled
		}
		logger.Log("INFO", "Contract delivery processing initiated", map[string]interface{}{
			"ship_symbol":  cmd.ShipSymbol,
			"action":       "process_delivery",
			"trade_symbol": delivery.TradeSymbol,
		})

		// Step 6: Reload ship state (critical for fresh cargo data)
		logger.Log("INFO", "Ship state reload initiated", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "reload_ship_state",
		})
		ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			logger.Log("ERROR", "Ship state reload failed", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "reload_ship_state",
				"error":       err.Error(),
			})
			return fmt.Errorf("failed to reload ship: %w", err)
		}
		logger.Log("INFO", "Ship state loaded successfully", map[string]interface{}{
			"ship_symbol":    cmd.ShipSymbol,
			"action":         "ship_state_loaded",
			"cargo_units":    ship.Cargo().Units,
			"cargo_capacity": ship.Cargo().Capacity,
		})

		currentUnits := ship.Cargo().GetItemUnits(delivery.TradeSymbol)
		logger.Log("INFO", "Current cargo units checked", map[string]interface{}{
			"ship_symbol":  cmd.ShipSymbol,
			"action":       "check_cargo_units",
			"trade_symbol": delivery.TradeSymbol,
			"units":        currentUnits,
		})

		// Step 7: Jettison wrong cargo if needed
		hasWrongCargo := ship.Cargo().HasItemsOtherThan(delivery.TradeSymbol)
		needsSpace := currentUnits < unitsRemaining || ship.Cargo().IsFull()

		if hasWrongCargo && needsSpace {
			logger.Log("INFO", "Jettisoning wrong cargo", map[string]interface{}{
				"ship_symbol":  cmd.ShipSymbol,
				"action":       "jettison_cargo",
				"keep_symbol":  delivery.TradeSymbol,
			})
			if err := h.jettisonWrongCargo(ctx, ship, delivery.TradeSymbol, cmd.PlayerID); err != nil {
				return fmt.Errorf("failed to jettison cargo: %w", err)
			}

			// Re-sync ship after jettison
			ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to reload ship after jettison: %w", err)
			}
			currentUnits = ship.Cargo().GetItemUnits(delivery.TradeSymbol)
			logger.Log("INFO", "Cargo jettison completed", map[string]interface{}{
				"ship_symbol":    cmd.ShipSymbol,
				"action":         "jettison_complete",
				"cargo_units":    ship.Cargo().Units,
				"cargo_capacity": ship.Cargo().Capacity,
			})
		}

		// Step 8: Calculate purchase needs
		unitsToPurchase := unitsRemaining - currentUnits
		logger.Log("INFO", "Purchase needs calculated", map[string]interface{}{
			"ship_symbol":      cmd.ShipSymbol,
			"action":           "calculate_purchase",
			"trade_symbol":     delivery.TradeSymbol,
			"units_to_purchase": unitsToPurchase,
			"units_remaining":  unitsRemaining,
			"units_current":    currentUnits,
		})

		// Step 9: Purchase cargo if needed
		if unitsToPurchase > 0 {
			// Get profitability result for cheapest market
			profitResult := profitabilityResp.(*ProfitabilityResult)
			cheapestMarket := profitResult.CheapestMarketWaypoint
			logger.Log("INFO", "Cheapest market identified", map[string]interface{}{
				"ship_symbol":    cmd.ShipSymbol,
				"action":         "identify_market",
				"market_symbol":  cheapestMarket,
				"trade_symbol":   delivery.TradeSymbol,
			})

			// Multi-trip purchase loop
			trips := int(math.Ceil(float64(unitsToPurchase) / float64(ship.Cargo().Capacity)))
			result.TotalTrips += trips
			logger.Log("INFO", "Multi-trip purchase initiated", map[string]interface{}{
				"ship_symbol":  cmd.ShipSymbol,
				"action":       "start_multi_trip",
				"trips_needed": trips,
				"trade_symbol": delivery.TradeSymbol,
			})

			for trip := 0; trip < trips; trip++ {
				// Calculate available cargo space (capacity - current load)
				availableSpace := ship.Cargo().Capacity - ship.Cargo().Units

				// Use available space, not total capacity
				unitsThisTrip := utils.Min(availableSpace, unitsToPurchase)

				// Skip if no space available
				if unitsThisTrip <= 0 {
					logger.Log("WARNING", "Purchase loop terminated due to no cargo space", map[string]interface{}{
						"ship_symbol":  cmd.ShipSymbol,
						"action":       "purchase_loop_ended",
						"reason":       "no_cargo_space",
					})
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
		logger.Log("INFO", "Contract cargo delivery initiated", map[string]interface{}{
			"ship_symbol":  cmd.ShipSymbol,
			"action":       "deliver_cargo",
			"trade_symbol": delivery.TradeSymbol,
			"units":        unitsToDeliver,
		})

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
func (h *RunWorkflowHandler) transferShipBackToCoordinator(
	ctx context.Context,
	cmd *RunWorkflowCommand,
) error {
	logger := common.LoggerFromContext(ctx)

	// Note: Container ID will need to be passed via metadata when this handler is invoked
	// For now, we'll need to get it from somewhere. This is a placeholder.
	// The actual container ID should come from the container runner context.

	// TODO: Get actual worker container ID from context
	// For now, we'll skip the transfer and let the coordinator handle it
	logger.Log("INFO", "Ship transfer to coordinator initiated", map[string]interface{}{
		"ship_symbol":    cmd.ShipSymbol,
		"action":         "transfer_to_coordinator",
		"coordinator_id": cmd.CoordinatorID,
	})

	return nil
}

// jettisonWrongCargo jettisons all cargo items except the specified symbol
func (h *RunWorkflowHandler) jettisonWrongCargo(
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
func (h *RunWorkflowHandler) navigateToWaypoint(
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
func (h *RunWorkflowHandler) dockShip(
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


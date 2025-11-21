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
	contract, wasNegotiated, err := h.findOrNegotiateContract(ctx, cmd)
	if err != nil {
		return err
	}

	if wasNegotiated {
		result.Negotiated = true
	}

	profitabilityResp, err := h.evaluateContractProfitability(ctx, cmd, contract)
	if err != nil {
		// Non-fatal - logged in method
	}

	contract, err = h.acceptContractIfNeeded(ctx, contract, cmd.PlayerID, result)
	if err != nil {
		return err
	}

	contract, err = h.processAllDeliveries(ctx, cmd, contract, profitabilityResp, result)
	if err != nil {
		return err
	}

	if err := h.fulfillContract(ctx, contract, cmd.PlayerID); err != nil {
		return err
	}

	result.Fulfilled = true

	h.calculateTotalProfit(contract, result)

	return nil
}

func (h *RunWorkflowHandler) findOrNegotiateContract(
	ctx context.Context,
	cmd *RunWorkflowCommand,
) (*domainContract.Contract, bool, error) {
	logger := common.LoggerFromContext(ctx)

	activeContracts, err := h.contractRepo.FindActiveContracts(ctx, cmd.PlayerID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to check active contracts: %w", err)
	}

	if len(activeContracts) > 0 {
		contract := activeContracts[0]
		logger.Log("INFO", "Resuming existing active contract", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "resume_contract",
			"contract_id": contract.ContractID(),
		})
		return contract, false, nil
	}

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
		return nil, false, fmt.Errorf("failed to negotiate contract: %w", err)
	}

	negotiateResult := negotiateResp.(*NegotiateContractResponse)

	if negotiateResult.WasNegotiated {
		logger.Log("INFO", "Contract negotiation successful", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "contract_negotiated",
			"contract_id": negotiateResult.Contract.ContractID(),
		})
	}

	return negotiateResult.Contract, negotiateResult.WasNegotiated, nil
}

func (h *RunWorkflowHandler) evaluateContractProfitability(
	ctx context.Context,
	cmd *RunWorkflowCommand,
	contract *domainContract.Contract,
) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Contract profitability evaluation initiated", map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol,
		"action":      "evaluate_profitability",
		"contract_id": contract.ContractID(),
	})

	profitabilityQuery := &EvaluateContractProfitabilityQuery{
		Contract:        contract,
		ShipSymbol:      cmd.ShipSymbol,
		PlayerID:        cmd.PlayerID,
		FuelCostPerTrip: 0,
	}

	profitabilityResp, err := h.mediator.Send(ctx, profitabilityQuery)
	if err != nil {
		logger.Log("WARNING", "Contract profitability evaluation failed", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "evaluate_profitability",
			"contract_id": contract.ContractID(),
			"error":       err.Error(),
		})
		return nil, err
	}

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

	return profitabilityResp, nil
}

func (h *RunWorkflowHandler) acceptContractIfNeeded(
	ctx context.Context,
	contract *domainContract.Contract,
	playerID int,
	result *RunWorkflowResponse,
) (*domainContract.Contract, error) {
	if contract.Accepted() {
		return contract, nil
	}

	acceptCmd := &AcceptContractCommand{
		ContractID: contract.ContractID(),
		PlayerID:   playerID,
	}

	acceptResp, err := h.mediator.Send(ctx, acceptCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to accept contract: %w", err)
	}

	acceptResult := acceptResp.(*AcceptContractResponse)
	result.Accepted = true

	return acceptResult.Contract, nil
}

func (h *RunWorkflowHandler) processAllDeliveries(
	ctx context.Context,
	cmd *RunWorkflowCommand,
	contract *domainContract.Contract,
	profitabilityResp common.Response,
	result *RunWorkflowResponse,
) (*domainContract.Contract, error) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Contract deliveries processing started", map[string]interface{}{
		"ship_symbol":    cmd.ShipSymbol,
		"action":         "process_deliveries",
		"contract_id":    contract.ContractID(),
		"delivery_count": len(contract.Terms().Deliveries),
	})

	for _, delivery := range contract.Terms().Deliveries {
		unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
		logger.Log("INFO", "Contract delivery status", map[string]interface{}{
			"ship_symbol":     cmd.ShipSymbol,
			"action":          "check_delivery",
			"trade_symbol":    delivery.TradeSymbol,
			"units_required":  delivery.UnitsRequired,
			"units_fulfilled": delivery.UnitsFulfilled,
			"units_remaining": unitsRemaining,
		})

		if unitsRemaining == 0 {
			logger.Log("INFO", "Contract delivery already fulfilled", map[string]interface{}{
				"ship_symbol":  cmd.ShipSymbol,
				"action":       "skip_delivery",
				"trade_symbol": delivery.TradeSymbol,
			})
			continue
		}

		logger.Log("INFO", "Contract delivery processing initiated", map[string]interface{}{
			"ship_symbol":  cmd.ShipSymbol,
			"action":       "process_delivery",
			"trade_symbol": delivery.TradeSymbol,
		})

		var err error
		contract, err = h.processSingleDelivery(ctx, cmd, contract, delivery, profitabilityResp, result)
		if err != nil {
			return nil, err
		}
	}

	return contract, nil
}

func (h *RunWorkflowHandler) processSingleDelivery(
	ctx context.Context,
	cmd *RunWorkflowCommand,
	contract *domainContract.Contract,
	delivery domainContract.Delivery,
	profitabilityResp common.Response,
	result *RunWorkflowResponse,
) (*domainContract.Contract, error) {
	ship, currentUnits, err := h.reloadShipState(ctx, cmd.ShipSymbol, cmd.PlayerID, delivery.TradeSymbol)
	if err != nil {
		return nil, err
	}

	unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
	ship, currentUnits, err = h.jettisonWrongCargoIfNeeded(ctx, ship, delivery.TradeSymbol, currentUnits, unitsRemaining, cmd.PlayerID)
	if err != nil {
		return nil, err
	}

	unitsToPurchase := h.calculatePurchaseNeeds(ctx, cmd.ShipSymbol, delivery.TradeSymbol, unitsRemaining, currentUnits)

	if unitsToPurchase > 0 {
		profitResult := profitabilityResp.(*ProfitabilityResult)
		ship, err = h.executePurchaseLoop(ctx, cmd, ship, delivery.TradeSymbol, unitsToPurchase, profitResult.CheapestMarketWaypoint, result)
		if err != nil {
			return nil, err
		}
	}

	contract, err = h.deliverContractCargo(ctx, cmd, contract, ship, delivery)
	if err != nil {
		return nil, err
	}

	return contract, nil
}

func (h *RunWorkflowHandler) reloadShipState(
	ctx context.Context,
	shipSymbol string,
	playerID int,
	tradeSymbol string,
) (*navigation.Ship, int, error) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Ship state reload initiated", map[string]interface{}{
		"ship_symbol": shipSymbol,
		"action":      "reload_ship_state",
	})

	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		logger.Log("ERROR", "Ship state reload failed", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "reload_ship_state",
			"error":       err.Error(),
		})
		return nil, 0, fmt.Errorf("failed to reload ship: %w", err)
	}

	logger.Log("INFO", "Ship state loaded successfully", map[string]interface{}{
		"ship_symbol":    shipSymbol,
		"action":         "ship_state_loaded",
		"cargo_units":    ship.Cargo().Units,
		"cargo_capacity": ship.Cargo().Capacity,
	})

	currentUnits := ship.Cargo().GetItemUnits(tradeSymbol)
	logger.Log("INFO", "Current cargo units checked", map[string]interface{}{
		"ship_symbol":  shipSymbol,
		"action":       "check_cargo_units",
		"trade_symbol": tradeSymbol,
		"units":        currentUnits,
	})

	return ship, currentUnits, nil
}

func (h *RunWorkflowHandler) jettisonWrongCargoIfNeeded(
	ctx context.Context,
	ship *navigation.Ship,
	tradeSymbol string,
	currentUnits int,
	unitsRemaining int,
	playerID int,
) (*navigation.Ship, int, error) {
	logger := common.LoggerFromContext(ctx)

	hasWrongCargo := ship.Cargo().HasItemsOtherThan(tradeSymbol)
	needsSpace := currentUnits < unitsRemaining || ship.Cargo().IsFull()

	if !hasWrongCargo || !needsSpace {
		return ship, currentUnits, nil
	}

	logger.Log("INFO", "Jettisoning wrong cargo", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "jettison_cargo",
		"keep_symbol": tradeSymbol,
	})

	if err := h.jettisonWrongCargo(ctx, ship, tradeSymbol, playerID); err != nil {
		return nil, 0, fmt.Errorf("failed to jettison cargo: %w", err)
	}

	ship, err := h.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to reload ship after jettison: %w", err)
	}

	currentUnits = ship.Cargo().GetItemUnits(tradeSymbol)
	logger.Log("INFO", "Cargo jettison completed", map[string]interface{}{
		"ship_symbol":    ship.ShipSymbol(),
		"action":         "jettison_complete",
		"cargo_units":    ship.Cargo().Units,
		"cargo_capacity": ship.Cargo().Capacity,
	})

	return ship, currentUnits, nil
}

func (h *RunWorkflowHandler) calculatePurchaseNeeds(
	ctx context.Context,
	shipSymbol string,
	tradeSymbol string,
	unitsRemaining int,
	currentUnits int,
) int {
	logger := common.LoggerFromContext(ctx)

	unitsToPurchase := unitsRemaining - currentUnits
	logger.Log("INFO", "Purchase needs calculated", map[string]interface{}{
		"ship_symbol":       shipSymbol,
		"action":            "calculate_purchase",
		"trade_symbol":      tradeSymbol,
		"units_to_purchase": unitsToPurchase,
		"units_remaining":   unitsRemaining,
		"units_current":     currentUnits,
	})

	return unitsToPurchase
}

func (h *RunWorkflowHandler) executePurchaseLoop(
	ctx context.Context,
	cmd *RunWorkflowCommand,
	ship *navigation.Ship,
	tradeSymbol string,
	unitsToPurchase int,
	cheapestMarket string,
	result *RunWorkflowResponse,
) (*navigation.Ship, error) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Cheapest market identified", map[string]interface{}{
		"ship_symbol":   cmd.ShipSymbol,
		"action":        "identify_market",
		"market_symbol": cheapestMarket,
		"trade_symbol":  tradeSymbol,
	})

	trips := int(math.Ceil(float64(unitsToPurchase) / float64(ship.Cargo().Capacity)))
	result.TotalTrips += trips
	logger.Log("INFO", "Multi-trip purchase initiated", map[string]interface{}{
		"ship_symbol":  cmd.ShipSymbol,
		"action":       "start_multi_trip",
		"trips_needed": trips,
		"trade_symbol": tradeSymbol,
	})

	for trip := 0; trip < trips; trip++ {
		availableSpace := ship.Cargo().Capacity - ship.Cargo().Units
		unitsThisTrip := utils.Min(availableSpace, unitsToPurchase)

		if unitsThisTrip <= 0 {
			logger.Log("WARNING", "Purchase loop terminated due to no cargo space", map[string]interface{}{
				"ship_symbol": cmd.ShipSymbol,
				"action":      "purchase_loop_ended",
				"reason":      "no_cargo_space",
			})
			break
		}

		var err error
		ship, err = h.navigateToWaypoint(ctx, cmd.ShipSymbol, cheapestMarket, cmd.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to navigate to market: %w", err)
		}

		if err := h.dockShip(ctx, ship, cmd.PlayerID); err != nil {
			return nil, fmt.Errorf("failed to dock at market: %w", err)
		}

		purchaseCmd := &appShip.PurchaseCargoCommand{
			ShipSymbol: ship.ShipSymbol(),
			GoodSymbol: tradeSymbol,
			Units:      unitsThisTrip,
			PlayerID:   cmd.PlayerID,
		}

		_, err = h.mediator.Send(ctx, purchaseCmd)
		if err != nil {
			return nil, fmt.Errorf("failed to purchase cargo: %w", err)
		}

		unitsToPurchase -= unitsThisTrip

		ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to reload ship after purchase: %w", err)
		}
	}

	return ship, nil
}

func (h *RunWorkflowHandler) deliverContractCargo(
	ctx context.Context,
	cmd *RunWorkflowCommand,
	contract *domainContract.Contract,
	ship *navigation.Ship,
	delivery domainContract.Delivery,
) (*domainContract.Contract, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship before delivery: %w", err)
	}

	unitsToDeliver := ship.Cargo().GetItemUnits(delivery.TradeSymbol)
	logger.Log("INFO", "Contract cargo delivery initiated", map[string]interface{}{
		"ship_symbol":  cmd.ShipSymbol,
		"action":       "deliver_cargo",
		"trade_symbol": delivery.TradeSymbol,
		"units":        unitsToDeliver,
	})

	if unitsToDeliver == 0 {
		return contract, nil
	}

	ship, err = h.navigateToWaypoint(ctx, cmd.ShipSymbol, delivery.DestinationSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to delivery: %w", err)
	}

	if err := h.dockShip(ctx, ship, cmd.PlayerID); err != nil {
		return nil, fmt.Errorf("failed to dock at delivery: %w", err)
	}

	deliverCmd := &DeliverContractCommand{
		ContractID:  contract.ContractID(),
		ShipSymbol:  cmd.ShipSymbol,
		TradeSymbol: delivery.TradeSymbol,
		Units:       unitsToDeliver,
		PlayerID:    cmd.PlayerID,
	}

	deliverResp, err := h.mediator.Send(ctx, deliverCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to deliver cargo: %w", err)
	}

	deliverResult := deliverResp.(*DeliverContractResponse)
	return deliverResult.Contract, nil
}

func (h *RunWorkflowHandler) fulfillContract(
	ctx context.Context,
	contract *domainContract.Contract,
	playerID int,
) error {
	fulfillCmd := &FulfillContractCommand{
		ContractID: contract.ContractID(),
		PlayerID:   playerID,
	}

	_, err := h.mediator.Send(ctx, fulfillCmd)
	if err != nil {
		return fmt.Errorf("failed to fulfill contract: %w", err)
	}

	return nil
}

func (h *RunWorkflowHandler) calculateTotalProfit(
	contract *domainContract.Contract,
	result *RunWorkflowResponse,
) {
	totalPayment := contract.Terms().Payment.OnAccepted + contract.Terms().Payment.OnFulfilled
	result.TotalProfit += totalPayment
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


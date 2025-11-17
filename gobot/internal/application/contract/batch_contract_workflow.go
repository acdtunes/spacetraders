package contract

import (
	"context"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appShip "github.com/andrescamacho/spacetraders-go/internal/application/ship"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// min returns the minimum of two integers.
// Used to calculate units per trip when cargo capacity limits the purchase amount.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// BatchContractWorkflowCommand orchestrates complete contract workflows in batches
type BatchContractWorkflowCommand struct {
	ShipSymbol string
	Iterations int
	PlayerID   int
}

// BatchContractWorkflowResponse contains workflow execution results
type BatchContractWorkflowResponse struct {
	Negotiated  int
	Accepted    int
	Fulfilled   int
	Failed      int
	TotalProfit int
	TotalTrips  int
	Errors      []string
}

// BatchContractWorkflowHandler implements the complete contract workflow
// following the exact Python implementation pattern:
//
// For each iteration:
//  1. Check for existing active contracts (idempotency)
//  2. Negotiate new contract or resume existing (handle error 4511)
//  3. Evaluate profitability (log only, always accept)
//  4. Accept contract (skip if already accepted)
//  5. For each delivery:
//     - Reload ship state
//     - Jettison wrong cargo if needed
//     - Calculate purchase needs
//     - Execute multi-trip loop if units > cargo capacity
//     - For each trip:
//       * Navigate to seller
//       * Dock
//       * Purchase with transaction splitting (handled by PurchaseCargoHandler)
//       * Navigate to delivery
//       * Dock
//       * Deliver cargo
//  6. Fulfill contract
//  7. Calculate profit
type BatchContractWorkflowHandler struct {
	mediator     common.Mediator
	shipRepo     navigation.ShipRepository
	contractRepo domainContract.ContractRepository
}

// NewBatchContractWorkflowHandler creates a new batch contract workflow handler
func NewBatchContractWorkflowHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	contractRepo domainContract.ContractRepository,
) *BatchContractWorkflowHandler {
	return &BatchContractWorkflowHandler{
		mediator:     mediator,
		shipRepo:     shipRepo,
		contractRepo: contractRepo,
	}
}

// Handle executes the batch contract workflow command
func (h *BatchContractWorkflowHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*BatchContractWorkflowCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &BatchContractWorkflowResponse{
		Negotiated:  0,
		Accepted:    0,
		Fulfilled:   0,
		Failed:      0,
		TotalProfit: 0,
		TotalTrips:  0,
		Errors:      []string{},
	}

	// Execute iterations
	for iteration := 0; iteration < cmd.Iterations; iteration++ {
		if err := h.processIteration(ctx, cmd, result, iteration); err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("Iteration %d: %s", iteration+1, err.Error()))
			// Continue to next iteration (graceful error handling)
			continue
		}
	}

	return result, nil
}

// processIteration handles a single contract workflow iteration
func (h *BatchContractWorkflowHandler) processIteration(
	ctx context.Context,
	cmd *BatchContractWorkflowCommand,
	result *BatchContractWorkflowResponse,
	iteration int,
) error {
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
	} else {
		// Step 2: Negotiate new contract
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
			result.Negotiated++
		}
	}

	// Step 3: Evaluate profitability (log only - always accept)
	fmt.Printf("[WORKFLOW] Evaluating contract profitability for contract %s\n", contract.ContractID())
	profitabilityQuery := &EvaluateContractProfitabilityQuery{
		Contract:        contract,
		ShipSymbol:      cmd.ShipSymbol,
		PlayerID:        cmd.PlayerID,
		FuelCostPerTrip: 0, // Simplified for now
	}

	fmt.Println("[WORKFLOW] Calling profitability query...")
	profitabilityResp, err := h.mediator.Send(ctx, profitabilityQuery)
	fmt.Printf("[WORKFLOW] Profitability query returned: err=%v\n", err)
	if err != nil {
		// Log warning but continue (non-fatal)
		fmt.Printf("WARNING: Failed to evaluate profitability: %v\n", err)
	} else {
		profitResult := profitabilityResp.(*ProfitabilityResult)
		if !profitResult.IsProfitable {
			fmt.Printf("WARNING: Contract unprofitable (%s) but accepting anyway\n", profitResult.Reason)
		}
	}
	fmt.Println("[WORKFLOW] Profitability evaluation complete")

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
		result.Accepted++
	}

	// Step 5: Process each delivery
	fmt.Printf("[WORKFLOW] Processing %d deliveries\n", len(contract.Terms().Deliveries))
	for _, delivery := range contract.Terms().Deliveries {
		unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
		fmt.Printf("[WORKFLOW] Delivery: %s, required=%d, fulfilled=%d, remaining=%d\n",
			delivery.TradeSymbol, delivery.UnitsRequired, delivery.UnitsFulfilled, unitsRemaining)
		if unitsRemaining == 0 {
			fmt.Println("[WORKFLOW] Delivery already fulfilled, skipping")
			continue // Already fulfilled
		}
		fmt.Println("[WORKFLOW] Processing delivery...")

		// Step 6: Reload ship state (critical for fresh cargo data)
		fmt.Println("[WORKFLOW] Reloading ship state...")
		ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
		if err != nil {
			fmt.Printf("[WORKFLOW] ERROR: Failed to reload ship: %v\n", err)
			return fmt.Errorf("failed to reload ship: %w", err)
		}
		fmt.Printf("[WORKFLOW] Ship loaded: cargo=%d/%d\n", ship.Cargo().Units, ship.Cargo().Capacity)

		currentUnits := ship.Cargo().GetItemUnits(delivery.TradeSymbol)
		fmt.Printf("[WORKFLOW] Current %s units in cargo: %d\n", delivery.TradeSymbol, currentUnits)

		// Step 7: Jettison wrong cargo if needed
		hasWrongCargo := ship.Cargo().HasItemsOtherThan(delivery.TradeSymbol)
		needsSpace := currentUnits < unitsRemaining || ship.Cargo().IsFull()

		if hasWrongCargo && needsSpace {
			if err := h.jettisonWrongCargo(ctx, ship, delivery.TradeSymbol, cmd.PlayerID); err != nil {
				return fmt.Errorf("failed to jettison cargo: %w", err)
			}

			// Re-sync ship after jettison
			ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
			if err != nil {
				return fmt.Errorf("failed to reload ship after jettison: %w", err)
			}
			currentUnits = ship.Cargo().GetItemUnits(delivery.TradeSymbol)
		}

		// Step 8: Calculate purchase needs
		unitsToPurchase := unitsRemaining - currentUnits
		fmt.Printf("[WORKFLOW] Units to purchase: %d (remaining=%d - current=%d)\n", unitsToPurchase, unitsRemaining, currentUnits)

		if unitsToPurchase > 0 {
			// Get profitability result for cheapest market
			fmt.Println("[WORKFLOW] Getting cheapest market from profitability result...")
			profitResult := profitabilityResp.(*ProfitabilityResult)
			cheapestMarket := profitResult.CheapestMarketWaypoint
			fmt.Printf("[WORKFLOW] Cheapest market: %s\n", cheapestMarket)

			// Step 9: Multi-trip loop
			trips := int(math.Ceil(float64(unitsToPurchase) / float64(ship.Cargo().Capacity)))
			result.TotalTrips += trips
			fmt.Printf("[WORKFLOW] Starting multi-trip delivery: %d trips needed\n", trips)

			for trip := 0; trip < trips; trip++ {
				unitsThisTrip := min(ship.Cargo().Capacity, unitsToPurchase)

				// Navigate to seller
				if err := h.navigateToWaypoint(ctx, ship, cheapestMarket, cmd.PlayerID); err != nil {
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

				// Navigate to delivery destination
				if err := h.navigateToWaypoint(ctx, ship, delivery.DestinationSymbol, cmd.PlayerID); err != nil {
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
					Units:       unitsThisTrip,
					PlayerID:    cmd.PlayerID,
				}

				deliverResp, err := h.mediator.Send(ctx, deliverCmd)
				if err != nil {
					return fmt.Errorf("failed to deliver cargo: %w", err)
				}

				deliverResult := deliverResp.(*DeliverContractResponse)

				contract = deliverResult.Contract
				unitsToPurchase -= unitsThisTrip

				// Reload ship for next trip
				ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
				if err != nil {
					return fmt.Errorf("failed to reload ship: %w", err)
				}
			}
		}
	}

	// Step 10: Fulfill contract
	fulfillCmd := &FulfillContractCommand{
		ContractID: contract.ContractID(),
		PlayerID:   cmd.PlayerID,
	}

	fulfillResp, err := h.mediator.Send(ctx, fulfillCmd)
	if err != nil {
		return fmt.Errorf("failed to fulfill contract: %w", err)
	}

	_ = fulfillResp // Response unused after error check removed

	result.Fulfilled++

	// Calculate profit (simplified - use actual payment from contract)
	totalPayment := contract.Terms().Payment.OnAccepted + contract.Terms().Payment.OnFulfilled
	result.TotalProfit += totalPayment

	return nil
}

// jettisonWrongCargo jettisons all cargo items except the specified symbol
func (h *BatchContractWorkflowHandler) jettisonWrongCargo(
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

// navigateToWaypoint navigates ship to destination (idempotent)
func (h *BatchContractWorkflowHandler) navigateToWaypoint(
	ctx context.Context,
	ship *navigation.Ship,
	destination string,
	playerID int,
) error {
	// Check if already at destination
	if ship.CurrentLocation().Symbol == destination {
		return nil
	}

	// Use HIGH-LEVEL NavigateShipCommand (handles route planning, refueling, multi-hop)
	navigateCmd := &appShip.NavigateShipCommand{
		ShipSymbol:  ship.ShipSymbol(),
		Destination: destination,
		PlayerID:    playerID,
	}

	_, err := h.mediator.Send(ctx, navigateCmd)
	if err != nil {
		return fmt.Errorf("failed to navigate: %w", err)
	}

	return nil
}

// dockShip docks ship (idempotent)
func (h *BatchContractWorkflowHandler) dockShip(
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

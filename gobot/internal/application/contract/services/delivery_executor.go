package services

import (
	"context"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	appShipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// Type aliases for convenience
type DeliverContractCommand = contractTypes.DeliverContractCommand
type DeliverContractResponse = contractTypes.DeliverContractResponse
type RunWorkflowResponse = contractTypes.RunWorkflowResponse

// DeliveryExecutor handles contract delivery execution including purchasing and delivering cargo
type DeliveryExecutor struct {
	mediator     common.Mediator
	shipRepo     navigation.ShipRepository
	cargoManager *CargoManager
}

// NewDeliveryExecutor creates a new delivery executor service
func NewDeliveryExecutor(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	cargoManager *CargoManager,
) *DeliveryExecutor {
	return &DeliveryExecutor{
		mediator:     mediator,
		shipRepo:     shipRepo,
		cargoManager: cargoManager,
	}
}

// ProcessAllDeliveries processes all deliveries in a contract
func (e *DeliveryExecutor) ProcessAllDeliveries(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	contract *domainContract.Contract,
	profitabilityResp common.Response,
	result *RunWorkflowResponse,
	containerID string, // Container ID for operation context linking
) (*domainContract.Contract, error) {
	logger := common.LoggerFromContext(ctx)

	// Create operation context for transaction linking and add to context
	if containerID != "" {
		opContext := shared.NewOperationContext(containerID, "contract_workflow")
		ctx = shared.WithOperationContext(ctx, opContext)
	}

	logger.Log("INFO", "Contract deliveries processing started", map[string]interface{}{
		"ship_symbol":    shipSymbol,
		"action":         "process_deliveries",
		"contract_id":    contract.ContractID(),
		"delivery_count": len(contract.Terms().Deliveries),
	})

	for _, delivery := range contract.Terms().Deliveries {
		unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
		logger.Log("INFO", "Contract delivery status", map[string]interface{}{
			"ship_symbol":     shipSymbol,
			"action":          "check_delivery",
			"trade_symbol":    delivery.TradeSymbol,
			"units_required":  delivery.UnitsRequired,
			"units_fulfilled": delivery.UnitsFulfilled,
			"units_remaining": unitsRemaining,
		})

		if unitsRemaining == 0 {
			logger.Log("INFO", "Contract delivery already fulfilled", map[string]interface{}{
				"ship_symbol":  shipSymbol,
				"action":       "skip_delivery",
				"trade_symbol": delivery.TradeSymbol,
			})
			continue
		}

		logger.Log("INFO", "Contract delivery processing initiated", map[string]interface{}{
			"ship_symbol":  shipSymbol,
			"action":       "process_delivery",
			"trade_symbol": delivery.TradeSymbol,
		})

		var err error
		contract, err = e.ProcessSingleDelivery(ctx, shipSymbol, playerID, contract, delivery, profitabilityResp, result, nil)
		if err != nil {
			return nil, err
		}
	}

	return contract, nil
}

// ProcessSingleDelivery processes a single delivery item
func (e *DeliveryExecutor) ProcessSingleDelivery(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	contract *domainContract.Contract,
	delivery domainContract.Delivery,
	profitabilityResp common.Response,
	result *RunWorkflowResponse,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*domainContract.Contract, error) {
	ship, currentUnits, err := e.cargoManager.ReloadShipState(ctx, shipSymbol, playerID, delivery.TradeSymbol)
	if err != nil {
		return nil, err
	}

	unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
	ship, currentUnits, err = e.cargoManager.JettisonWrongCargoIfNeeded(ctx, ship, delivery.TradeSymbol, currentUnits, unitsRemaining, playerID)
	if err != nil {
		return nil, err
	}

	unitsToPurchase := e.cargoManager.CalculatePurchaseNeeds(ctx, shipSymbol, delivery.TradeSymbol, unitsRemaining, currentUnits)

	if unitsToPurchase > 0 {
		profitResult := profitabilityResp.(*contractQueries.ProfitabilityResult)
		ship, err = e.ExecutePurchaseLoop(ctx, shipSymbol, playerID, ship, delivery.TradeSymbol, unitsToPurchase, profitResult.CheapestMarketWaypoint, result, opContext)
		if err != nil {
			return nil, err
		}
	}

	contract, err = e.DeliverContractCargo(ctx, shipSymbol, playerID, contract, ship, delivery)
	if err != nil {
		return nil, err
	}

	return contract, nil
}

// ExecutePurchaseLoop executes the multi-trip purchase loop
func (e *DeliveryExecutor) ExecutePurchaseLoop(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	ship *navigation.Ship,
	tradeSymbol string,
	unitsToPurchase int,
	cheapestMarket string,
	result *RunWorkflowResponse,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*navigation.Ship, error) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Cheapest market identified", map[string]interface{}{
		"ship_symbol":   shipSymbol,
		"action":        "identify_market",
		"market_symbol": cheapestMarket,
		"trade_symbol":  tradeSymbol,
	})

	trips := int(math.Ceil(float64(unitsToPurchase) / float64(ship.Cargo().Capacity)))
	result.TotalTrips += trips
	logger.Log("INFO", "Multi-trip purchase initiated", map[string]interface{}{
		"ship_symbol":  shipSymbol,
		"action":       "start_multi_trip",
		"trips_needed": trips,
		"trade_symbol": tradeSymbol,
	})

	for trip := 0; trip < trips; trip++ {
		var shouldBreak bool
		var err error
		ship, unitsToPurchase, shouldBreak, err = e.executeSinglePurchaseTrip(ctx, shipSymbol, playerID, ship, tradeSymbol, cheapestMarket, unitsToPurchase, opContext)
		if err != nil {
			return nil, err
		}
		if shouldBreak {
			break
		}
	}

	return ship, nil
}

// executeSinglePurchaseTrip executes a single purchase trip
func (e *DeliveryExecutor) executeSinglePurchaseTrip(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	ship *navigation.Ship,
	tradeSymbol string,
	cheapestMarket string,
	unitsToPurchase int,
	opContext *shared.OperationContext, // Operation context for transaction linking
) (*navigation.Ship, int, bool, error) {
	logger := common.LoggerFromContext(ctx)

	availableSpace := ship.Cargo().Capacity - ship.Cargo().Units
	unitsThisTrip := utils.Min(availableSpace, unitsToPurchase)

	if unitsThisTrip <= 0 {
		logger.Log("WARNING", "Purchase loop terminated due to no cargo space", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"action":      "purchase_loop_ended",
			"reason":      "no_cargo_space",
		})
		return ship, unitsToPurchase, true, nil
	}

	var err error
	ship, err = e.navigateAndDock(ctx, shipSymbol, cheapestMarket, playerID)
	if err != nil {
		return nil, 0, false, fmt.Errorf("failed to navigate to market: %w", err)
	}

	purchaseCmd := &appShipCmd.PurchaseCargoCommand{
		ShipSymbol: ship.ShipSymbol(),
		GoodSymbol: tradeSymbol,
		Units:      unitsThisTrip,
		PlayerID:   playerID,
	}

	_, err = e.mediator.Send(ctx, purchaseCmd)
	if err != nil {
		return nil, 0, false, fmt.Errorf("failed to purchase cargo: %w", err)
	}

	unitsToPurchase -= unitsThisTrip

	ship, err = e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, 0, false, fmt.Errorf("failed to reload ship after purchase: %w", err)
	}

	return ship, unitsToPurchase, false, nil
}

// DeliverContractCargo delivers cargo to the contract destination
func (e *DeliveryExecutor) DeliverContractCargo(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	contract *domainContract.Contract,
	ship *navigation.Ship,
	delivery domainContract.Delivery,
) (*domainContract.Contract, error) {
	logger := common.LoggerFromContext(ctx)

	ship, err := e.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship before delivery: %w", err)
	}

	// Calculate how many units to deliver - cap at remaining units needed
	unitsInCargo := ship.Cargo().GetItemUnits(delivery.TradeSymbol)
	unitsRemaining := delivery.UnitsRequired - delivery.UnitsFulfilled
	unitsToDeliver := unitsInCargo
	if unitsToDeliver > unitsRemaining {
		unitsToDeliver = unitsRemaining
	}

	logger.Log("INFO", "Contract cargo delivery initiated", map[string]interface{}{
		"ship_symbol":     shipSymbol,
		"action":          "deliver_cargo",
		"trade_symbol":    delivery.TradeSymbol,
		"units_in_cargo":  unitsInCargo,
		"units_remaining": unitsRemaining,
		"units_to_deliver": unitsToDeliver,
	})

	if unitsToDeliver == 0 {
		return contract, nil
	}

	ship, err = e.navigateAndDock(ctx, shipSymbol, delivery.DestinationSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to delivery: %w", err)
	}

	deliverCmd := &DeliverContractCommand{
		ContractID:  contract.ContractID(),
		ShipSymbol:  shipSymbol,
		TradeSymbol: delivery.TradeSymbol,
		Units:       unitsToDeliver,
		PlayerID:    playerID,
	}

	deliverResp, err := e.mediator.Send(ctx, deliverCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to deliver cargo: %w", err)
	}

	deliverResult := deliverResp.(*DeliverContractResponse)
	return deliverResult.Contract, nil
}

// navigateAndDock navigates to destination and docks in one operation
func (e *DeliveryExecutor) navigateAndDock(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	ship, err := e.navigateToWaypoint(ctx, shipSymbol, destination, playerID)
	if err != nil {
		return nil, err
	}

	if err := e.dockShip(ctx, ship, playerID); err != nil {
		return nil, err
	}

	return ship, nil
}

// navigateToWaypoint navigates ship to destination and returns updated ship state
func (e *DeliveryExecutor) navigateToWaypoint(
	ctx context.Context,
	shipSymbol string,
	destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	// Use HIGH-LEVEL NavigateRouteCommand (handles route planning, refueling, multi-hop, idempotency)
	navigateCmd := &appShipCmd.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    playerID,
	}

	resp, err := e.mediator.Send(ctx, navigateCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate: %w", err)
	}

	navResp := resp.(*appShipCmd.NavigateRouteResponse)
	return navResp.Ship, nil
}

// dockShip docks ship (idempotent)
func (e *DeliveryExecutor) dockShip(
	ctx context.Context,
	ship *navigation.Ship,
	playerID shared.PlayerID,
) error {
	dockCmd := &shipTypes.DockShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	}

	dockResp, err := e.mediator.Send(ctx, dockCmd)
	if err != nil {
		return fmt.Errorf("failed to dock: %w", err)
	}

	_ = dockResp // Response unused after error check removed

	return nil
}

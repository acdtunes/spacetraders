package services

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
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
		profitResult, err := profitabilityResultOrErr(profitabilityResp, delivery.TradeSymbol)
		if err != nil {
			return nil, err
		}
		ship, err = e.ExecutePurchaseLoop(ctx, shipSymbol, playerID, ship, delivery.TradeSymbol, unitsToPurchase, profitResult.CheapestMarketWaypoint, result, opContext)
		if err != nil {
			// PARK, don't crash (sp-vwhi): a 4600 mid-purchase is a treasury
			// state, not a bug. Enrich the sentinel with the numbers an
			// operator needs and log ONCE at WARNING before returning it
			// unchanged - RunWorkflowHandler.Handle converts this into a
			// clean (nil-error) exit so the container doesn't crashloop.
			// The dynamic-discovery fleet coordinator re-picks-up this
			// contract on its next pass, which is the resume mechanism.
			var insufficientErr *ErrInsufficientCredits
			if errors.As(err, &insufficientErr) {
				insufficientErr.CreditsNeeded = profitResult.PurchaseCost
				insufficientErr.CreditsAvailable = e.lookupLiveCredits(ctx, playerID)
				logger := common.LoggerFromContext(ctx)
				logger.Log("WARNING", insufficientErr.Error(), map[string]interface{}{
					"ship_symbol":       shipSymbol,
					"action":            "parked",
					"reason":            "insufficient_credits",
					"trade_symbol":      delivery.TradeSymbol,
					"units_attempted":   insufficientErr.UnitsAttempted,
					"credits_needed":    insufficientErr.CreditsNeeded,
					"credits_available": insufficientErr.CreditsAvailable,
				})
			}
			return nil, err
		}
	}

	contract, err = e.DeliverContractCargo(ctx, shipSymbol, playerID, contract, ship, delivery)
	if err != nil {
		return nil, err
	}

	return contract, nil
}

// lookupLiveCredits fetches a fresh treasury snapshot for the WARNING log
// enrichment above. Returns -1 if the live lookup itself fails, so the log
// message still emits (with an explicit sentinel value) rather than being
// lost to a second error.
func (e *DeliveryExecutor) lookupLiveCredits(ctx context.Context, playerID shared.PlayerID) int {
	pid := playerID.Value()
	resp, err := e.mediator.Send(ctx, &playerQueries.GetPlayerQuery{PlayerID: &pid})
	if err != nil {
		return -1
	}
	playerResp, ok := resp.(*playerQueries.GetPlayerResponse)
	if !ok || playerResp.Player == nil {
		return -1
	}
	return playerResp.Player.Credits
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

	purchaseCmd := &shipCargo.PurchaseCargoCommand{
		ShipSymbol: ship.ShipSymbol(),
		GoodSymbol: tradeSymbol,
		Units:      unitsThisTrip,
		PlayerID:   playerID,
	}

	_, err = e.mediator.Send(ctx, purchaseCmd)
	if err != nil {
		if IsInsufficientCreditsError(err) {
			return nil, 0, false, &ErrInsufficientCredits{
				ShipSymbol:     shipSymbol,
				TradeSymbol:    tradeSymbol,
				UnitsAttempted: unitsThisTrip,
				Cause:          err,
			}
		}
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
		"ship_symbol":      shipSymbol,
		"action":           "deliver_cargo",
		"trade_symbol":     delivery.TradeSymbol,
		"units_in_cargo":   unitsInCargo,
		"units_remaining":  unitsRemaining,
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
	navigateCmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    playerID,
	}

	resp, err := e.mediator.Send(ctx, navigateCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate: %w", err)
	}

	navResp := resp.(*shipNav.NavigateRouteResponse)
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

	if _, err := e.mediator.Send(ctx, dockCmd); err != nil {
		return fmt.Errorf("failed to dock: %w", err)
	}

	return nil
}

// profitabilityResultOrErr validates the profitability response before use.
// The workflow handler treats profitability-evaluation failures as non-fatal,
// so a nil response reaches purchasing when no market data exists yet; the
// old unchecked assertion panicked the whole daemon (see captain incident
// 2026-07-02). A purchase without market data must fail the container, not
// the process.
func profitabilityResultOrErr(resp common.Response, good string) (*contractQueries.ProfitabilityResult, error) {
	result, ok := resp.(*contractQueries.ProfitabilityResult)
	if !ok || result == nil {
		return nil, fmt.Errorf("cannot plan purchase of %s: no profitability/market data available (scout markets first)", good)
	}
	return result, nil
}

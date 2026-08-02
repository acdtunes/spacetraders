package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

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

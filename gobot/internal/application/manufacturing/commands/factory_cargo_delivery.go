package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func firstCargoUnits(ship *navigation.Ship, good string) int {
	for _, item := range ship.Cargo().Inventory {
		if item.Symbol == good {
			return item.Units
		}
	}
	return 0
}

// deliverCargo navigates to destination, docks, and sells the specified good
func (h *RunFactoryCoordinatorHandler) deliverCargo(
	ctx context.Context,
	shipSymbol string,
	good string,
	destination string,
	playerID int,
) (*shipCargo.SellCargoResponse, error) {
	playerIDValue := shared.MustNewPlayerID(playerID)

	ship, err := h.productionExecutor.NavigateAndDock(ctx, shipSymbol, destination, playerIDValue)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to delivery destination: %w", err)
	}

	unitsToSell := firstCargoUnits(ship, good)
	if unitsToSell == 0 {
		return nil, fmt.Errorf("ship %s has no %s in cargo to deliver", shipSymbol, good)
	}

	sellCmd := &shipCargo.SellCargoCommand{
		ShipSymbol: shipSymbol,
		GoodSymbol: good,
		Units:      unitsToSell,
		PlayerID:   playerIDValue,
	}

	sellResp, err := h.mediator.Send(ctx, sellCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to sell cargo: %w", err)
	}

	response, ok := sellResp.(*shipCargo.SellCargoResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type from sell command")
	}

	return response, nil
}

// deliverExistingCargo delivers cargo that's already on the ship (skips buying)
func (h *RunFactoryCoordinatorHandler) deliverExistingCargo(
	ctx context.Context,
	ship *navigation.Ship,
	good string,
	destination string,
	playerID int,
) (*mfgServices.ProductionResult, error) {
	logger := common.LoggerFromContext(ctx)

	quantity := firstCargoUnits(ship, good)
	if quantity == 0 {
		return nil, fmt.Errorf("ship %s has no %s in cargo", ship.ShipSymbol(), good)
	}

	logger.Log("INFO", fmt.Sprintf("Delivering existing cargo: %d units of %s to %s", quantity, good, destination), map[string]interface{}{
		"ship":        ship.ShipSymbol(),
		"good":        good,
		"quantity":    quantity,
		"destination": destination,
	})

	deliveryResult, err := h.deliverCargo(ctx, ship.ShipSymbol(), good, destination, playerID)
	if err != nil {
		return nil, err
	}

	logger.Log("INFO", fmt.Sprintf("Existing cargo delivered: %d units of %s for %d credits", deliveryResult.UnitsSold, good, deliveryResult.TotalRevenue), map[string]interface{}{
		"good":     good,
		"units":    deliveryResult.UnitsSold,
		"revenue":  deliveryResult.TotalRevenue,
		"location": destination,
	})

	return &mfgServices.ProductionResult{
		QuantityAcquired: deliveryResult.UnitsSold,
		TotalCost:        -deliveryResult.TotalRevenue, // Negative because we earned money
		WaypointSymbol:   destination,
	}, nil
}

package services

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// CargoManager handles ship cargo operations including jettisoning and inventory management
type CargoManager struct {
	mediator common.Mediator
	shipRepo navigation.ShipRepository
}

// NewCargoManager creates a new cargo manager service
func NewCargoManager(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
) *CargoManager {
	return &CargoManager{
		mediator: mediator,
		shipRepo: shipRepo,
	}
}

// ReloadShipState reloads ship state from repository and returns current units of target cargo
func (m *CargoManager) ReloadShipState(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
	tradeSymbol string,
) (*navigation.Ship, int, error) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Ship state reload initiated", map[string]interface{}{
		"ship_symbol": shipSymbol,
		"action":      "reload_ship_state",
	})

	ship, err := m.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
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

// JettisonWrongCargoIfNeeded jettisons all cargo except target item if ship needs space
func (m *CargoManager) JettisonWrongCargoIfNeeded(
	ctx context.Context,
	ship *navigation.Ship,
	tradeSymbol string,
	currentUnits int,
	unitsRemaining int,
	playerID shared.PlayerID,
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

	if err := m.jettisonWrongCargo(ctx, ship, tradeSymbol, playerID.Value()); err != nil {
		return nil, 0, fmt.Errorf("failed to jettison cargo: %w", err)
	}

	ship, err := m.shipRepo.FindBySymbol(ctx, ship.ShipSymbol(), playerID)
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

// CalculatePurchaseNeeds calculates how many units need to be purchased
func (m *CargoManager) CalculatePurchaseNeeds(
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

// jettisonWrongCargo jettisons all cargo items except the specified symbol
func (m *CargoManager) jettisonWrongCargo(
	ctx context.Context,
	ship *navigation.Ship,
	keepSymbol string,
	playerID int,
) error {
	wrongItems := ship.Cargo().GetOtherItems(keepSymbol)

	for _, item := range wrongItems {
		jettisonCmd := &shipCargo.JettisonCargoCommand{
			ShipSymbol: ship.ShipSymbol(),
			PlayerID:   shared.MustNewPlayerID(playerID),
			GoodSymbol: item.Symbol,
			Units:      item.Units,
		}

		jettisonResp, err := m.mediator.Send(ctx, jettisonCmd)
		if err != nil {
			return fmt.Errorf("failed to jettison %s: %w", item.Symbol, err)
		}

		_ = jettisonResp // Response unused after error check removed
	}

	return nil
}

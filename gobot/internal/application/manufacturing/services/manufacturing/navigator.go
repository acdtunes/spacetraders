package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Navigator handles ship navigation and docking for manufacturing operations.
// Consolidates the repeated navigate+dock pattern found throughout the task worker.
type Navigator interface {
	// NavigateAndDock navigates to destination and docks in one operation
	NavigateAndDock(ctx context.Context, shipSymbol, destination string, playerID shared.PlayerID) (*navigation.Ship, error)

	// NavigateTo navigates without docking
	NavigateTo(ctx context.Context, shipSymbol, destination string, playerID shared.PlayerID) error

	// Dock docks the ship at current location
	Dock(ctx context.Context, shipSymbol string, playerID shared.PlayerID) error

	// ReloadShip fetches fresh ship state from repository
	ReloadShip(ctx context.Context, shipSymbol string, playerID shared.PlayerID) (*navigation.Ship, error)
}

// ManufacturingNavigator implements Navigator for manufacturing operations.
type ManufacturingNavigator struct {
	mediator common.Mediator
	shipRepo navigation.ShipRepository
}

// NewManufacturingNavigator creates a new navigator service.
func NewManufacturingNavigator(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
) *ManufacturingNavigator {
	return &ManufacturingNavigator{
		mediator: mediator,
		shipRepo: shipRepo,
	}
}

// NavigateAndDock navigates to destination and docks in one operation.
// This consolidates the repeated pattern of:
//  1. Check if already at destination (idempotent)
//  2. Navigate to destination
//  3. Dock at destination
func (n *ManufacturingNavigator) NavigateAndDock(
	ctx context.Context,
	shipSymbol, destination string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	logger := common.LoggerFromContext(ctx)

	// Load current ship state
	ship, err := n.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load ship: %w", err)
	}

	// Idempotent: Check if already at destination
	if ship.CurrentLocation().Symbol != destination {
		logger.Log("DEBUG", "Navigating to destination", map[string]interface{}{
			"ship": shipSymbol,
			"from": ship.CurrentLocation().Symbol,
			"to":   destination,
		})

		navResp, err := n.mediator.Send(ctx, &shipNav.NavigateRouteCommand{
			ShipSymbol:   shipSymbol,
			Destination:  destination,
			PlayerID:     playerID,
			PreferCruise: false,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to navigate to %s: %w", destination, err)
		}

		// Use ship from navigation response (already up-to-date)
		ship = navResp.(*shipNav.NavigateRouteResponse).Ship
	}

	// Dock at destination - pass ship directly so it's updated in place
	_, err = n.mediator.Send(ctx, &shipTypes.DockShipCommand{
		Ship:     ship,
		PlayerID: playerID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to dock: %w", err)
	}

	// OPTIMIZATION: Ship is updated in place by DockShipHandler (no reload needed)
	// The shipRepo.Dock() call updates the ship pointer with API response
	return ship, nil
}

// NavigateTo navigates to destination without docking.
func (n *ManufacturingNavigator) NavigateTo(
	ctx context.Context,
	shipSymbol, destination string,
	playerID shared.PlayerID,
) error {
	// Load current ship state
	ship, err := n.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return fmt.Errorf("failed to load ship: %w", err)
	}

	// Idempotent: Check if already at destination
	if ship.CurrentLocation().Symbol == destination {
		return nil
	}

	_, err = n.mediator.Send(ctx, &shipNav.NavigateRouteCommand{
		ShipSymbol:   shipSymbol,
		Destination:  destination,
		PlayerID:     playerID,
		PreferCruise: false,
	})
	if err != nil {
		return fmt.Errorf("failed to navigate to %s: %w", destination, err)
	}

	return nil
}

// Dock docks the ship at its current location.
func (n *ManufacturingNavigator) Dock(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
) error {
	_, err := n.mediator.Send(ctx, &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
	})
	if err != nil {
		return fmt.Errorf("failed to dock: %w", err)
	}
	return nil
}

// ReloadShip fetches fresh ship state from repository.
func (n *ManufacturingNavigator) ReloadShip(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
) (*navigation.Ship, error) {
	ship, err := n.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship: %w", err)
	}
	return ship, nil
}

package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RefuelShipCommand - Command to refuel a ship at its current waypoint
type RefuelShipCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
	Units      *int // nil = full refuel
}

// RefuelShipResponse - Response from refuel ship command
type RefuelShipResponse struct {
	FuelAdded   int
	CurrentFuel int
	CreditsCost int
}

// RefuelShipHandler - Handles refuel ship commands
type RefuelShipHandler struct {
	shipRepo navigation.ShipRepository
}

// NewRefuelShipHandler creates a new refuel ship handler
func NewRefuelShipHandler(
	shipRepo navigation.ShipRepository,
) *RefuelShipHandler {
	return &RefuelShipHandler{
		shipRepo: shipRepo,
	}
}

// Handle executes the refuel ship command
func (h *RefuelShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RefuelShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	ship, err := h.loadShip(ctx, cmd)
	if err != nil {
		return nil, err
	}

	if err := h.validateAtFuelStation(ship); err != nil {
		return nil, err
	}

	if err := h.ensureShipDockedForRefuel(ctx, ship, cmd.PlayerID); err != nil {
		return nil, err
	}

	fuelBefore := ship.Fuel().Current

	if err := h.refuelShipViaAPI(ctx, ship, cmd); err != nil {
		return nil, err
	}

	return h.buildRefuelResponse(ship, fuelBefore), nil
}

func (h *RefuelShipHandler) loadShip(ctx context.Context, cmd *RefuelShipCommand) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

func (h *RefuelShipHandler) validateAtFuelStation(ship *navigation.Ship) error {
	if !ship.CurrentLocation().HasFuel {
		return fmt.Errorf("waypoint does not have fuel station")
	}
	return nil
}

func (h *RefuelShipHandler) ensureShipDockedForRefuel(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	stateChanged, err := ship.EnsureDocked()
	if err != nil {
		return err
	}

	if stateChanged {
		if err := h.shipRepo.Dock(ctx, ship, playerID); err != nil {
			return fmt.Errorf("failed to dock ship: %w", err)
		}
	}
	return nil
}

func (h *RefuelShipHandler) refuelShipViaAPI(ctx context.Context, ship *navigation.Ship, cmd *RefuelShipCommand) error {
	if err := h.shipRepo.Refuel(ctx, ship, cmd.PlayerID, cmd.Units); err != nil {
		return fmt.Errorf("failed to refuel ship: %w", err)
	}
	return nil
}

func (h *RefuelShipHandler) buildRefuelResponse(ship *navigation.Ship, fuelBefore int) *RefuelShipResponse {
	fuelAdded := ship.Fuel().Current - fuelBefore
	creditsCost := fuelAdded * 100

	return &RefuelShipResponse{
		FuelAdded:   fuelAdded,
		CurrentFuel: ship.Fuel().Current,
		CreditsCost: creditsCost,
	}
}

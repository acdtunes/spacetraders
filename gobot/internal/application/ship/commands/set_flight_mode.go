package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// SetFlightModeHandler - Handles set flight mode commands
type SetFlightModeHandler struct {
	shipRepo navigation.ShipRepository
}

// NewSetFlightModeHandler creates a new set flight mode handler
func NewSetFlightModeHandler(
	shipRepo navigation.ShipRepository,
) *SetFlightModeHandler {
	return &SetFlightModeHandler{
		shipRepo: shipRepo,
	}
}

// Handle executes the set flight mode command
func (h *SetFlightModeHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*types.SetFlightModeCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	ship, err := h.loadShip(ctx, cmd)
	if err != nil {
		return nil, err
	}

	modeName, err := h.validateFlightMode(cmd.Mode)
	if err != nil {
		return nil, err
	}

	if err := h.setFlightModeViaAPI(ctx, ship, cmd.PlayerID, modeName); err != nil {
		return nil, err
	}

	return &types.SetFlightModeResponse{
		Status: "updated",
		Mode:   cmd.Mode,
	}, nil
}

func (h *SetFlightModeHandler) loadShip(ctx context.Context, cmd *types.SetFlightModeCommand) (*navigation.Ship, error) {
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

func (h *SetFlightModeHandler) validateFlightMode(mode shared.FlightMode) (string, error) {
	modeName := mode.Name()
	if !shared.IsValidFlightModeName(modeName) {
		return "", fmt.Errorf("invalid flight mode: %s", modeName)
	}
	return modeName, nil
}

func (h *SetFlightModeHandler) setFlightModeViaAPI(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID, modeName string) error {
	if err := h.shipRepo.SetFlightMode(ctx, ship, playerID, modeName); err != nil {
		return fmt.Errorf("failed to set flight mode: %w", err)
	}
	return nil
}

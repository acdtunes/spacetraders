package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// DockShipHandler - Handles dock ship commands
type DockShipHandler struct {
	shipRepo navigation.ShipRepository
}

// NewDockShipHandler creates a new dock ship handler
func NewDockShipHandler(
	shipRepo navigation.ShipRepository,
) *DockShipHandler {
	return &DockShipHandler{
		shipRepo: shipRepo,
	}
}

// Handle executes the dock ship command
func (h *DockShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*types.DockShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	ship, err := h.loadShip(ctx, cmd)
	if err != nil {
		return nil, err
	}

	stateChanged, err := h.ensureShipDocked(ship)
	if err != nil {
		return nil, err
	}

	if stateChanged {
		return h.dockShipViaAPI(ctx, ship, cmd.PlayerID)
	}

	return &types.DockShipResponse{
		Status: "already_docked",
	}, nil
}

func (h *DockShipHandler) loadShip(ctx context.Context, cmd *types.DockShipCommand) (*navigation.Ship, error) {
	// OPTIMIZATION: Use ship if provided (avoids API call)
	if cmd.Ship != nil {
		return cmd.Ship, nil
	}
	// Fall back to API fetch (backward compatibility)
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
}

func (h *DockShipHandler) ensureShipDocked(ship *navigation.Ship) (bool, error) {
	return ship.EnsureDocked()
}

func (h *DockShipHandler) dockShipViaAPI(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) (*types.DockShipResponse, error) {
	if err := h.shipRepo.Dock(ctx, ship, playerID); err != nil {
		return nil, fmt.Errorf("failed to dock ship: %w", err)
	}
	return &types.DockShipResponse{Status: "docked"}, nil
}

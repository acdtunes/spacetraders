package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// DockShipCommand - Command to dock a ship at its current waypoint
type DockShipCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
}

// DockShipResponse - Response from dock ship command
type DockShipResponse struct {
	Status string // "docked" or "already_docked"
}

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
	cmd, ok := request.(*DockShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Load ship from repository
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	// 2. Use domain method to ensure ship is docked (idempotent)
	stateChanged, err := ship.EnsureDocked()
	if err != nil {
		return nil, err
	}

	// 3. If state was changed, call repository to dock via API
	if stateChanged {
		if err := h.shipRepo.Dock(ctx, ship, cmd.PlayerID); err != nil {
			return nil, fmt.Errorf("failed to dock ship: %w", err)
		}

		return &DockShipResponse{
			Status: "docked",
		}, nil
	}

	// Ship was already docked
	return &DockShipResponse{
		Status: "already_docked",
	}, nil
}

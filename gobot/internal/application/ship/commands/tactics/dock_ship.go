package tactics

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

	status, err := runStateTransition(ctx, h.shipRepo, cmd, stateTransition{
		ensure: func(ship *navigation.Ship) (bool, error) {
			return ship.EnsureDocked()
		},
		callAPI: func(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
			if err := h.shipRepo.Dock(ctx, ship, playerID); err != nil {
				return fmt.Errorf("failed to dock ship: %w", err)
			}
			return nil
		},
		doneStatus:    "docked",
		alreadyStatus: "already_docked",
	})
	if err != nil {
		return nil, err
	}

	return &types.DockShipResponse{Status: status}, nil
}

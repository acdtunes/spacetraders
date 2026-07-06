package tactics

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// OrbitShipHandler - Handles orbit ship commands
type OrbitShipHandler struct {
	shipRepo navigation.ShipRepository
}

// NewOrbitShipHandler creates a new orbit ship handler
func NewOrbitShipHandler(
	shipRepo navigation.ShipRepository,
) *OrbitShipHandler {
	return &OrbitShipHandler{
		shipRepo: shipRepo,
	}
}

// Handle executes the orbit ship command
func (h *OrbitShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*types.OrbitShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	status, err := runStateTransition(ctx, h.shipRepo, cmd, stateTransition{
		ensure: func(ship *navigation.Ship) (bool, error) {
			return ship.EnsureInOrbit()
		},
		callAPI: func(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
			if err := h.shipRepo.Orbit(ctx, ship, playerID); err != nil {
				return fmt.Errorf("failed to orbit ship: %w", err)
			}
			return nil
		},
		doneStatus:    "in_orbit",
		alreadyStatus: "already_in_orbit",
	})
	if err != nil {
		return nil, err
	}

	return &types.OrbitShipResponse{Status: status}, nil
}

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

	ship, err := h.loadShip(ctx, cmd)
	if err != nil {
		return nil, err
	}

	stateChanged, err := h.ensureShipInOrbit(ship)
	if err != nil {
		return nil, err
	}

	if stateChanged {
		return h.orbitShipViaAPI(ctx, ship, cmd.PlayerID)
	}

	return &types.OrbitShipResponse{
		Status: "already_in_orbit",
	}, nil
}

func (h *OrbitShipHandler) loadShip(ctx context.Context, cmd *types.OrbitShipCommand) (*navigation.Ship, error) {
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

func (h *OrbitShipHandler) ensureShipInOrbit(ship *navigation.Ship) (bool, error) {
	return ship.EnsureInOrbit()
}

func (h *OrbitShipHandler) orbitShipViaAPI(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) (*types.OrbitShipResponse, error) {
	if err := h.shipRepo.Orbit(ctx, ship, playerID); err != nil {
		return nil, fmt.Errorf("failed to orbit ship: %w", err)
	}
	return &types.OrbitShipResponse{Status: "in_orbit"}, nil
}

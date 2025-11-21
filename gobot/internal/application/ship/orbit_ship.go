package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// OrbitShipCommand - Command to put a ship into orbit at its current waypoint
type OrbitShipCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
}

// OrbitShipResponse - Response from orbit ship command
type OrbitShipResponse struct {
	Status string // "in_orbit" or "already_in_orbit"
}

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
	cmd, ok := request.(*OrbitShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Load ship from repository
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	// 2. Use domain method to ensure ship is in orbit (idempotent)
	stateChanged, err := ship.EnsureInOrbit()
	if err != nil {
		return nil, err
	}

	// 3. If state was changed, call repository to orbit via API
	if stateChanged {
		if err := h.shipRepo.Orbit(ctx, ship, cmd.PlayerID); err != nil {
			return nil, fmt.Errorf("failed to orbit ship: %w", err)
		}

		return &OrbitShipResponse{
			Status: "in_orbit",
		}, nil
	}

	// Ship was already in orbit
	return &OrbitShipResponse{
		Status: "already_in_orbit",
	}, nil
}

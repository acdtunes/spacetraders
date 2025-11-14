package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// RefuelShipCommand - Command to refuel a ship at its current waypoint
type RefuelShipCommand struct {
	ShipSymbol string
	PlayerID   int
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

	// 1. Load ship from repository
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	// 2. Validate ship is at a fuel station
	if !ship.CurrentLocation().HasFuel {
		return nil, fmt.Errorf("waypoint does not have fuel station")
	}

	// 3. Ensure ship is docked (auto-dock if needed)
	stateChanged, err := ship.EnsureDocked()
	if err != nil {
		return nil, err
	}

	// 4. If state was changed, call repository to dock via API
	if stateChanged {
		if err := h.shipRepo.Dock(ctx, ship, cmd.PlayerID); err != nil {
			return nil, fmt.Errorf("failed to dock ship: %w", err)
		}
	}

	// 5. Calculate fuel to add before refueling
	var fuelAdded int

	if cmd.Units == nil {
		// Full refuel
		added, err := ship.RefuelToFull()
		if err != nil {
			return nil, err
		}
		fuelAdded = added
	} else {
		// Partial refuel
		if err := ship.Refuel(*cmd.Units); err != nil {
			return nil, err
		}
		fuelAdded = *cmd.Units
	}

	// 6. Call repository to refuel via API (only if fuel was added)
	if fuelAdded > 0 {
		if err := h.shipRepo.Refuel(ctx, ship, cmd.PlayerID, cmd.Units); err != nil {
			return nil, fmt.Errorf("failed to refuel ship: %w", err)
		}
	}

	// 7. Calculate cost (100 credits per unit is standard)
	creditsCost := fuelAdded * 100

	return &RefuelShipResponse{
		FuelAdded:   fuelAdded,
		CurrentFuel: ship.Fuel().Current,
		CreditsCost: creditsCost,
	}, nil
}

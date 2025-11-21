package ship

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// SetFlightModeCommand - Command to set ship's flight mode
type SetFlightModeCommand struct {
	ShipSymbol string
	PlayerID   shared.PlayerID
	Mode       shared.FlightMode
}

// SetFlightModeResponse - Response from set flight mode command
type SetFlightModeResponse struct {
	CurrentMode shared.FlightMode
}

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
	cmd, ok := request.(*SetFlightModeCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// 1. Load ship from repository
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	// 2. Validate flight mode is one of the valid modes
	modeName := cmd.Mode.Name()
	if !shared.IsValidFlightModeName(modeName) {
		return nil, fmt.Errorf("invalid flight mode: %s", modeName)
	}

	// 3. Call repository to set flight mode via API
	if err := h.shipRepo.SetFlightMode(ctx, ship, cmd.PlayerID, modeName); err != nil {
		return nil, fmt.Errorf("failed to set flight mode: %w", err)
	}

	// 4. Return success response
	return &SetFlightModeResponse{
		CurrentMode: cmd.Mode,
	}, nil
}

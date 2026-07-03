package reservation

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// SetShipReservationCommand marks a ship as reserved (or clears the reservation).
// A reserved ship is held out of every coordinator's dynamically-discovered
// idle-hauler pool, so a hauler dedicated to the jump-gate or manufacturing
// stream is never auto-claimed by the contract coordinator.
type SetShipReservationCommand struct {
	ShipSymbol  string // Required: ship symbol to (un)reserve
	Reserved    bool   // true to reserve, false to clear the reservation
	Reason      string // Optional: informational reason (only meaningful when Reserved)
	PlayerID    *int   // Optional: resolve by player ID
	AgentSymbol string // Optional: resolve by agent symbol
}

// SetShipReservationResponse reports the ship's reservation state after the update.
type SetShipReservationResponse struct {
	ShipSymbol string
	Reserved   bool
	Reason     string
}

// SetShipReservationHandler handles the SetShipReservation command.
type SetShipReservationHandler struct {
	shipRepo       navigation.ShipRepository
	playerResolver *common.PlayerResolver
}

// NewSetShipReservationHandler creates a new SetShipReservationHandler.
func NewSetShipReservationHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *SetShipReservationHandler {
	return &SetShipReservationHandler{
		shipRepo:       shipRepo,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the SetShipReservation command.
func (h *SetShipReservationHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*SetShipReservationCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *SetShipReservationCommand")
	}

	if cmd.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, cmd.PlayerID, cmd.AgentSymbol)
	if err != nil {
		return nil, err
	}

	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}

	if cmd.Reserved {
		ship.Reserve(cmd.Reason)
	} else {
		ship.ClearReservation()
	}

	if err := h.shipRepo.Save(ctx, ship); err != nil {
		return nil, fmt.Errorf("failed to persist ship reservation: %w", err)
	}

	return &SetShipReservationResponse{
		ShipSymbol: ship.ShipSymbol(),
		Reserved:   ship.IsReserved(),
		Reason:     ship.ReservationReason(),
	}, nil
}

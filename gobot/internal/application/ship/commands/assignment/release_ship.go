package assignment

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// ReleaseShipCommand clears a captain reservation, returning the ship to
// idle so normal coordinator discovery can claim it again.
type ReleaseShipCommand struct {
	ShipSymbol  string // Required: ship symbol to release
	Reason      string // Optional: free-text release reason, recorded in the audit trail
	PlayerID    *int   // Resolve by numeric player ID (takes precedence)
	AgentSymbol string // Resolve by agent symbol if PlayerID is nil
}

// ReleaseShipResponse confirms the reservation was cleared.
type ReleaseShipResponse struct {
	ShipSymbol string
}

// defaultReleaseReason is recorded when the caller gives no explicit reason,
// so the persisted audit trail never has a blank release reason.
const defaultReleaseReason = "captain_released"

// ReleaseShipHandler handles the ReleaseShip command.
type ReleaseShipHandler struct {
	shipRepo       navigation.ShipRepository
	playerResolver *common.PlayerResolver
}

// NewReleaseShipHandler creates a new ReleaseShipHandler.
func NewReleaseShipHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *ReleaseShipHandler {
	return &ReleaseShipHandler{
		shipRepo:       shipRepo,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the ReleaseShip command.
func (h *ReleaseShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*ReleaseShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *ReleaseShipCommand, got %T", request)
	}

	if cmd.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, cmd.PlayerID, cmd.AgentSymbol)
	if err != nil {
		return nil, err
	}

	reason := cmd.Reason
	if reason == "" {
		reason = defaultReleaseReason
	}

	if err := h.shipRepo.ReleaseCaptainReservation(ctx, cmd.ShipSymbol, reason, playerID); err != nil {
		return nil, fmt.Errorf("failed to release ship: %w", err)
	}

	return &ReleaseShipResponse{
		ShipSymbol: cmd.ShipSymbol,
	}, nil
}

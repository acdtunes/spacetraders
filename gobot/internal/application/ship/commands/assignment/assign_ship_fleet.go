package assignment

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// AssignShipFleetCommand dedicates a ship to a named fleet — the SINGLE write
// path for the DedicatedFleet tag (sp-l7h2). Fleet == "" clears the
// dedication, returning the ship to the general pool (the CLI's `fleet
// unassign` sends exactly that). Dedication is permanent ownership, distinct
// from a container claim ("who holds it right now"): assigning a busy ship
// succeeds and takes effect when its current claim is released — it never
// evicts the holder. Enforcement is two-layered: the FindIdleLightHaulers
// exclude filter (discovery pre-check) plus the atomic dedication guard
// inside ClaimShip's row-locked transaction (the correctness guarantee).
type AssignShipFleetCommand struct {
	ShipSymbol  string // Required: ship symbol to (un)dedicate
	Fleet       string // Fleet name; "" clears the dedication
	PlayerID    *int   // Resolve by numeric player ID (takes precedence)
	AgentSymbol string // Resolve by agent symbol if PlayerID is nil
}

// AssignShipFleetResponse confirms the dedication write.
type AssignShipFleetResponse struct {
	ShipSymbol string
	Fleet      string // The fleet now persisted; "" means undedicated
}

// AssignShipFleetHandler handles the AssignShipFleet command.
type AssignShipFleetHandler struct {
	shipRepo       navigation.ShipRepository
	playerResolver *common.PlayerResolver
}

// NewAssignShipFleetHandler creates a new AssignShipFleetHandler.
func NewAssignShipFleetHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *AssignShipFleetHandler {
	return &AssignShipFleetHandler{
		shipRepo:       shipRepo,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the AssignShipFleet command.
func (h *AssignShipFleetHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*AssignShipFleetCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *AssignShipFleetCommand, got %T", request)
	}

	if cmd.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, cmd.PlayerID, cmd.AgentSymbol)
	if err != nil {
		return nil, err
	}

	if err := h.shipRepo.AssignFleet(ctx, cmd.ShipSymbol, cmd.Fleet, playerID); err != nil {
		return nil, fmt.Errorf("failed to assign ship fleet: %w", err)
	}

	return &AssignShipFleetResponse{
		ShipSymbol: cmd.ShipSymbol,
		Fleet:      cmd.Fleet,
	}, nil
}

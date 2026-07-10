package outfitting

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// ListShipModulesQuery lists the modules currently installed on a ship. This is
// a read-only operation — it takes no claim.
type ListShipModulesQuery struct {
	ShipSymbol  string // Required
	PlayerID    *int   // Optional
	AgentSymbol string // Optional
}

// ListShipModulesResponse carries the ship's installed modules.
type ListShipModulesResponse struct {
	ShipSymbol string
	Modules    []ports.ModuleInfo
}

func (h *OutfittingHandler) handleList(ctx context.Context, cmd *ListShipModulesQuery) (*ListShipModulesResponse, error) {
	if cmd.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, cmd.PlayerID, cmd.AgentSymbol)
	if err != nil {
		return nil, err
	}

	player, err := h.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %w", err)
	}

	modules, err := h.apiClient.GetShipModules(ctx, cmd.ShipSymbol, player.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to list modules for %s: %w", cmd.ShipSymbol, err)
	}

	return &ListShipModulesResponse{
		ShipSymbol: cmd.ShipSymbol,
		Modules:    modules,
	}, nil
}

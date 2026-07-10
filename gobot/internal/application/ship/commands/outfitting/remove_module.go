package outfitting

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// RemoveModuleCommand removes an installed module from the ship back into its
// cargo. Mirror image of InstallModuleCommand.
type RemoveModuleCommand struct {
	ShipSymbol   string // Required: ship to remove from
	ModuleSymbol string // Required: module symbol to remove
	PlayerID     *int   // Optional: player ID
	AgentSymbol  string // Optional: agent symbol
}

// RemoveModuleResponse is the result of a remove.
type RemoveModuleResponse struct {
	Success       bool
	ShipSymbol    string
	ModuleSymbol  string
	CargoCapacity int // ship's cargo capacity AFTER the removal
	Fee           int // shipyard modification fee charged
	Modules       []ports.ModuleInfo
	Message       string
}

func (h *OutfittingHandler) handleRemove(ctx context.Context, cmd *RemoveModuleCommand) (*RemoveModuleResponse, error) {
	if cmd.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}
	if cmd.ModuleSymbol == "" {
		return nil, fmt.Errorf("module_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, cmd.PlayerID, cmd.AgentSymbol)
	if err != nil {
		return nil, err
	}

	outcome, err := h.modifyModule(
		ctx,
		"remove",
		cmd.ShipSymbol,
		cmd.ModuleSymbol,
		playerID,
		func(ship *navigation.Ship) error {
			// The module must currently be installed on the ship.
			for _, m := range ship.Modules() {
				if m.Symbol() == cmd.ModuleSymbol {
					return nil
				}
			}
			return fmt.Errorf("module %s not installed on %s", cmd.ModuleSymbol, cmd.ShipSymbol)
		},
		func(ctx context.Context, token string) (*ports.ModuleModificationResult, error) {
			return h.apiClient.RemoveShipModule(ctx, cmd.ShipSymbol, cmd.ModuleSymbol, token)
		},
	)
	if err != nil {
		return nil, err
	}

	return &RemoveModuleResponse{
		Success:       true,
		ShipSymbol:    cmd.ShipSymbol,
		ModuleSymbol:  cmd.ModuleSymbol,
		CargoCapacity: outcome.CargoCapacity,
		Fee:           outcome.Fee,
		Modules:       outcome.Modules,
		Message:       fmt.Sprintf("Removed %s from %s (fee %d, cargo capacity now %d)", cmd.ModuleSymbol, cmd.ShipSymbol, outcome.Fee, outcome.CargoCapacity),
	}, nil
}

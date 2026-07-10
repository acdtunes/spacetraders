package outfitting

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// InstallModuleCommand installs a module (which must be in the ship's cargo)
// onto the ship.
type InstallModuleCommand struct {
	ShipSymbol   string // Required: ship to install onto
	ModuleSymbol string // Required: module symbol, e.g. MODULE_CARGO_HOLD_III
	PlayerID     *int   // Optional: player ID
	AgentSymbol  string // Optional: agent symbol
}

// InstallModuleResponse is the result of an install.
type InstallModuleResponse struct {
	Success       bool
	ShipSymbol    string
	ModuleSymbol  string
	CargoCapacity int // ship's cargo capacity AFTER the install
	Fee           int // shipyard modification fee charged
	Modules       []ports.ModuleInfo
	Message       string
}

func (h *OutfittingHandler) handleInstall(ctx context.Context, cmd *InstallModuleCommand) (*InstallModuleResponse, error) {
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
		"install",
		cmd.ShipSymbol,
		cmd.ModuleSymbol,
		playerID,
		func(ship *navigation.Ship) error {
			// SpaceTraders constraint: the module must be in the ship's cargo.
			if ship.Cargo() == nil || ship.Cargo().GetItemUnits(cmd.ModuleSymbol) < 1 {
				return fmt.Errorf("module %s not in cargo on %s — buy it first", cmd.ModuleSymbol, cmd.ShipSymbol)
			}
			return nil
		},
		func(ctx context.Context, token string) (*ports.ModuleModificationResult, error) {
			return h.apiClient.InstallShipModule(ctx, cmd.ShipSymbol, cmd.ModuleSymbol, token)
		},
	)
	if err != nil {
		return nil, err
	}

	return &InstallModuleResponse{
		Success:       true,
		ShipSymbol:    cmd.ShipSymbol,
		ModuleSymbol:  cmd.ModuleSymbol,
		CargoCapacity: outcome.CargoCapacity,
		Fee:           outcome.Fee,
		Modules:       outcome.Modules,
		Message:       fmt.Sprintf("Installed %s on %s (fee %d, cargo capacity now %d)", cmd.ModuleSymbol, cmd.ShipSymbol, outcome.Fee, outcome.CargoCapacity),
	}, nil
}

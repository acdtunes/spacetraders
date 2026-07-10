package outfitting

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// ListShipModulesQuery lists the modules currently installed on a ship. This is
// a read-only operation — it takes no claim.
//
// CandidateSymbol, when set, requests an offline feasibility check (sp-el60)
// for a not-yet-installed module against the ship's current power/slot/crew
// budget. CandidatePower/Crew/Slots are the candidate module's own install
// requirements — there is no catalog of unowned module specs anywhere in
// this codebase or the SpaceTraders API, so the caller supplies them.
type ListShipModulesQuery struct {
	ShipSymbol  string // Required
	PlayerID    *int   // Optional
	AgentSymbol string // Optional

	CandidateSymbol string // Optional
	CandidatePower  int    // Optional, only meaningful with CandidateSymbol
	CandidateCrew   int    // Optional, only meaningful with CandidateSymbol
	CandidateSlots  int    // Optional, only meaningful with CandidateSymbol
}

// ModuleFeasibility names the candidate a navigation.InstallFeasibility
// verdict was computed for.
type ModuleFeasibility struct {
	CandidateSymbol string
	navigation.InstallFeasibility
}

// ListShipModulesResponse carries the ship's installed modules plus its
// power/slot/crew budget summary (sp-el60), computed offline from the
// DB-cached ship state — reactors, frames, and crew capacity have no swap
// endpoint, so these budgets are permanent per hull. Feasibility is
// populated only when the query carried a CandidateSymbol.
type ListShipModulesResponse struct {
	ShipSymbol string
	Modules    []ports.ModuleInfo

	ReactorPowerOutput int
	PowerUsed          int
	ModuleSlots        int
	ModuleSlotsUsed    int
	MountingPoints     int
	MountingPointsUsed int
	CrewCurrent        int
	CrewRequired       int
	CrewCapacity       int

	Feasibility *ModuleFeasibility
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

	// The power/slot/crew budget summary and feasibility check are computed
	// offline from the DB-cached ship (sp-el60) — no live trial-and-error
	// install required. FindBySymbol reads the ships table directly and only
	// falls back to the API for a ship that has never been synced.
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship %s: %w", cmd.ShipSymbol, err)
	}

	resp := &ListShipModulesResponse{
		ShipSymbol:         cmd.ShipSymbol,
		Modules:            modules,
		ReactorPowerOutput: ship.ReactorPowerOutput(),
		PowerUsed:          navigation.PowerUsed(ship),
		ModuleSlots:        ship.ModuleSlots(),
		ModuleSlotsUsed:    navigation.ModuleSlotsUsed(ship),
		MountingPoints:     ship.MountingPoints(),
		MountingPointsUsed: navigation.MountingPointsUsed(ship),
		CrewCurrent:        ship.CrewCurrent(),
		CrewRequired:       ship.CrewRequired(),
		CrewCapacity:       ship.CrewCapacity(),
	}

	if cmd.CandidateSymbol != "" {
		candidate := navigation.NewShipModule(cmd.CandidateSymbol, 0, 0,
			navigation.NewShipRequirements(cmd.CandidatePower, cmd.CandidateCrew, cmd.CandidateSlots))
		resp.Feasibility = &ModuleFeasibility{
			CandidateSymbol:    cmd.CandidateSymbol,
			InstallFeasibility: navigation.CheckModuleInstallFeasibility(ship, candidate),
		}
	}

	return resp, nil
}

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
// budget. The candidate's own power/crew/slots requirements are resolved via
// ShipRepository.FindModuleRequirements — there is no catalog of unowned
// module specs anywhere in this codebase or the SpaceTraders API, so the
// only real data source is another ship in the fleet that has the symbol
// installed (sp-el60 acceptance fix). Earlier revisions accepted
// caller-supplied CandidatePower/Crew/Slots ints; an unprovided flag
// silently defaulted to 0, which then trivially satisfied every budget
// check and misreported CAN-INSTALL — those fields are gone. When
// FindModuleRequirements finds no match anywhere, the verdict is
// UnknownRequirementsFeasibility, never a zero-filled "fits" verdict.
type ListShipModulesQuery struct {
	ShipSymbol  string // Required
	PlayerID    *int   // Optional
	AgentSymbol string // Optional

	CandidateSymbol string // Optional
}

// ModuleFeasibility names the candidate a navigation.InstallFeasibility
// verdict was computed for, plus the candidate's own resolved requirements
// (sp-el60 acceptance fix) so callers can always print what was checked
// against — even when RequirementsKnown is false, in which case
// RequirementsPower/Crew/Slots stay 0 and must be presented as "unknown",
// not as a real zero-cost spec.
type ModuleFeasibility struct {
	CandidateSymbol string
	navigation.InstallFeasibility

	RequirementsPower int
	RequirementsCrew  int
	RequirementsSlots int
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
		// The candidate's requirements must come from a real data source: a
		// ship elsewhere in the fleet that has this symbol installed
		// (sp-el60 acceptance fix). A DB/infra error here is propagated like
		// any other repository failure in this handler; "no ship has ever
		// carried this symbol" is a clean not-found (found=false, err=nil)
		// and fails closed to UnknownRequirementsFeasibility rather than
		// aborting the whole query.
		reqs, found, err := h.shipRepo.FindModuleRequirements(ctx, cmd.CandidateSymbol)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve requirements for candidate %s: %w", cmd.CandidateSymbol, err)
		}
		if found {
			candidate := navigation.NewShipModule(cmd.CandidateSymbol, 0, 0, reqs)
			resp.Feasibility = &ModuleFeasibility{
				CandidateSymbol:    cmd.CandidateSymbol,
				InstallFeasibility: navigation.CheckModuleInstallFeasibility(ship, candidate),
				RequirementsPower:  reqs.Power(),
				RequirementsCrew:   reqs.Crew(),
				RequirementsSlots:  reqs.Slots(),
			}
		} else {
			resp.Feasibility = &ModuleFeasibility{
				CandidateSymbol:    cmd.CandidateSymbol,
				InstallFeasibility: navigation.UnknownRequirementsFeasibility(),
			}
		}
	}

	return resp, nil
}
